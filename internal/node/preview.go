package node

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/podium-ade/podium/internal/node/docker"
	"github.com/podium-ade/podium/internal/runner"
	"github.com/podium-ade/podium/internal/transport/tailnet"
	"github.com/podium-ade/podium/pkg/spec"
)

// previewDialTimeout bounds a slot device's connection to a gateway on loopback.
const previewDialTimeout = 5 * time.Second

// slotDevice is the part of a tailnet preview device the manager uses, so the allocation
// rules can be tested without a tailnet.
type slotDevice interface {
	Up(ctx context.Context) (string, error)
	Serve(ports map[int]int, splice func(net.Conn, int)) error
	Stop()
	Close() error
}

// previewLease is one task's hold on a preview address, from assignment until release.
type previewLease struct {
	plan docker.Preview
	slot int // a tailnet slot, lanSlot for a LAN address, holdSlot for no address at all
	held bool
}

// The slot of a lease that holds no tailnet device.
const (
	lanSlot  = -1
	holdSlot = -2
)

// previewManager decides where a task's exposed ports are published and keeps what it lent
// until the control plane releases the task. It owns two pools: LAN addresses the operator
// set aside, and tailnet slot devices it brings up on first use and keeps.
type previewManager struct {
	lan       []string
	slots     int
	logger    *slog.Logger
	newDevice func(slot int) (slotDevice, error)

	mu      sync.Mutex
	leases  map[string]*previewLease
	devices map[int]slotDevice
}

func newPreviewManager(cfg Config, logger *slog.Logger) *previewManager {
	host := tailnet.NodeHostname("")
	return &previewManager{
		lan:    append([]string(nil), cfg.PreviewLANAddresses...),
		slots:  cfg.PreviewTailnetSlots,
		logger: logger,
		newDevice: func(slot int) (slotDevice, error) {
			return tailnet.NewPreviewDevice(tailnet.PreviewDeviceOptions{
				Hostname: previewHostname(host, slot),
				StateDir: filepath.Join(cfg.DataDir, tailnet.PreviewStateSubdir, strconv.Itoa(slot)),
				AuthKey:  cfg.PreviewTSAuthKey,
				Logger:   logger,
			})
		},
		leases:  make(map[string]*previewLease),
		devices: make(map[int]slotDevice),
	}
}

// previewHostname turns podium-node-<host> into pv-<host>-<slot>: short, and obviously a
// preview in the admin console.
func previewHostname(nodeHostname string, slot int) string {
	short := nodeHostname[len(tailnet.NodeHostnamePrefix):]
	return "pv-" + short + "-" + strconv.Itoa(slot)
}

// holdOnly is reserve's via for an expose with no ports: the task is kept up after its
// command and nothing is published, so it takes no address and never runs short of one.
const holdOnly = "hold"

// errNoPreview is why an exposed task is turned away from this node. The assignment is
// refused as retryable: another node may have room, or this one once a preview is released.
var errNoPreview = errors.New("no preview address free")

// reserve picks an address for a task's preview without touching the network, so it can
// run on the stream goroutine and refuse the assignment at once. via is the spec's
// preference; empty takes a tailnet slot when one is free, else a LAN address.
func (m *previewManager) reserve(taskID, via string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.leases[taskID]; ok {
		return nil
	}
	if via == holdOnly {
		m.leases[taskID] = &previewLease{slot: holdSlot}
		return nil
	}
	if via != "" {
		return m.reserveVia(taskID, via)
	}
	if m.slots > 0 {
		err := m.reserveVia(taskID, spec.ExposeViaTailnet)
		if err == nil || len(m.lan) == 0 {
			return err
		}
	}
	return m.reserveVia(taskID, spec.ExposeViaLAN)
}

func (m *previewManager) reserveVia(taskID, via string) error {
	switch via {
	case spec.ExposeViaTailnet:
		if m.slots == 0 {
			return fmt.Errorf("%w: this node has no tailnet preview slots (PODIUM_NODE_PREVIEW_TAILNET_SLOTS)", errNoPreview)
		}
		used := map[int]bool{}
		for _, l := range m.leases {
			used[l.slot] = true
		}
		for slot := range m.slots {
			if !used[slot] {
				m.leases[taskID] = &previewLease{
					slot: slot,
					plan: docker.Preview{Via: via, BindIP: "127.0.0.1", Slot: strconv.Itoa(slot)},
				}
				return nil
			}
		}
		return fmt.Errorf("%w: all %d tailnet preview slots are held", errNoPreview, m.slots)
	case spec.ExposeViaLAN:
		if len(m.lan) == 0 {
			return fmt.Errorf("%w: this node has no LAN preview addresses (PODIUM_NODE_PREVIEW_LAN_ADDRESSES)", errNoPreview)
		}
		used := map[string]bool{}
		for _, l := range m.leases {
			if l.slot < 0 {
				used[l.plan.Address] = true
			}
		}
		for _, addr := range m.lan {
			if !used[addr] {
				m.leases[taskID] = &previewLease{
					slot: -1,
					plan: docker.Preview{Via: via, Address: addr, BindIP: addr, SamePorts: true},
				}
				return nil
			}
		}
		return fmt.Errorf("%w: all %d LAN preview addresses are held", errNoPreview, len(m.lan))
	}
	return fmt.Errorf("%w: unknown via %q", errNoPreview, via)
}

// ready finishes a reservation: a tailnet slot's device is brought up, which on its first
// use registers it and can take a while, and its address becomes the preview's.
func (m *previewManager) ready(ctx context.Context, taskID string) (*docker.Preview, error) {
	m.mu.Lock()
	l, ok := m.leases[taskID]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("%w: task %s has no reservation", errNoPreview, taskID)
	}
	if l.slot == holdSlot {
		return nil, nil
	}
	if l.slot < 0 {
		p := l.plan
		return &p, nil
	}
	dev, err := m.device(l.slot)
	if err != nil {
		return nil, err
	}
	addr, err := dev.Up(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	l.plan.Address = addr
	p := l.plan
	m.mu.Unlock()
	return &p, nil
}

func (m *previewManager) device(slot int) (slotDevice, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d, ok := m.devices[slot]; ok {
		return d, nil
	}
	d, err := m.newDevice(slot)
	if err != nil {
		return nil, err
	}
	m.devices[slot] = d
	return d, nil
}

// attach points a tailnet slot at the task's gateway once the engine has published it.
// A LAN preview needs nothing: its ports are published on the address itself.
func (m *previewManager) attach(taskID string, ports map[int]int) error {
	m.mu.Lock()
	l, ok := m.leases[taskID]
	var dev slotDevice
	if ok && l.slot >= 0 {
		dev = m.devices[l.slot]
	}
	m.mu.Unlock()
	if dev == nil {
		return nil
	}
	return dev.Serve(ports, func(c net.Conn, hostPort int) {
		runner.Splice(c, func() (net.Conn, error) {
			return net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(hostPort)), previewDialTimeout)
		})
	})
}

// hold marks a task whose command exited while its preview stays up. It reports false when
// the task has no lease any more — released while its last events were still in flight —
// and the caller tears it down instead.
func (m *previewManager) hold(taskID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.leases[taskID]
	if ok {
		l.held = true
	}
	return ok
}

// release gives a task's address back. The containers are the caller's to tear down.
func (m *previewManager) release(taskID string) {
	m.mu.Lock()
	l, ok := m.leases[taskID]
	delete(m.leases, taskID)
	var dev slotDevice
	if ok && l.slot >= 0 {
		dev = m.devices[l.slot]
	}
	m.mu.Unlock()
	if dev != nil {
		dev.Stop()
	}
}

// restoreHold rebuilds the lease of a hold-only task a previous incarnation left running:
// no gateway to read it from, and no address to take back.
func (m *previewManager) restoreHold(taskID string, held bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.leases[taskID] = &previewLease{slot: holdSlot, held: held}
}

// restore rebuilds a lease from a gateway a previous incarnation left running.
func (m *previewManager) restore(hp docker.HeldPreview, held bool) {
	slot := lanSlot
	if hp.Slot != "" {
		if n, err := strconv.Atoi(hp.Slot); err == nil {
			slot = n
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.leases[hp.TaskID] = &previewLease{
		slot: slot,
		held: held,
		plan: docker.Preview{Via: hp.Via, Address: hp.Address, Slot: hp.Slot, SamePorts: slot < 0},
	}
}

func (m *previewManager) isHeld(taskID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.leases[taskID]
	return ok && l.held
}

// heldIDs is every task this node is keeping up after its command exited, sorted.
func (m *previewManager) heldIDs() []string {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.leases))
	for id, l := range m.leases {
		if l.held {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// free is how many more previews of each kind this node can publish, for the heartbeat.
func (m *previewManager) free() (tailnet, lan int32) {
	if m == nil {
		return 0, 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	usedSlots, usedLAN := 0, 0
	for _, l := range m.leases {
		switch {
		case l.slot >= 0:
			usedSlots++
		case l.slot == lanSlot:
			usedLAN++
		}
	}
	return int32(max(m.slots-usedSlots, 0)), int32(max(len(m.lan)-usedLAN, 0))
}

func (m *previewManager) heldCount() int {
	return len(m.heldIDs())
}

// close takes every slot device off the tailnet. The containers stay: a restarted node
// takes them back.
func (m *previewManager) close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	devs := m.devices
	m.devices = make(map[int]slotDevice)
	m.mu.Unlock()
	for _, d := range devs {
		_ = d.Close()
	}
}

// previewGateways is every preview gateway on the engine, by task. A failed listing only
// costs the previews: their tasks are still found and reconciled like any other.
func (n *Node) previewGateways(ctx context.Context) map[string]docker.HeldPreview {
	list, err := n.exec.ListPreviews(ctx)
	if err != nil {
		n.logger.WarnContext(ctx, "listing preview gateways failed", "error", err)
		return nil
	}
	out := make(map[string]docker.HeldPreview, len(list))
	for _, hp := range list {
		out[hp.TaskID] = hp
	}
	return out
}

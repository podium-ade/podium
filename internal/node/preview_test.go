package node

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/pkg/spec"
)

type fakeSlot struct {
	addr    string
	serving map[int]int
	stopped int
}

func (f *fakeSlot) Up(context.Context) (string, error) { return f.addr, nil }
func (f *fakeSlot) Serve(ports map[int]int, _ func(net.Conn, int)) error {
	f.serving = ports
	return nil
}
func (f *fakeSlot) Stop()        { f.stopped++; f.serving = nil }
func (f *fakeSlot) Close() error { return nil }

func testPreviews(lan []string, slots int) (*previewManager, map[int]*fakeSlot) {
	devs := map[int]*fakeSlot{}
	m := newPreviewManager(Config{PreviewLANAddresses: lan, PreviewTailnetSlots: slots},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.newDevice = func(slot int) (slotDevice, error) {
		d := &fakeSlot{addr: "100.64.0." + string(rune('1'+slot))}
		devs[slot] = d
		return d, nil
	}
	return m, devs
}

func TestPreviewsPreferTailnetAndRunOut(t *testing.T) {
	m, devs := testPreviews([]string{"192.168.1.201"}, 1)

	require.NoError(t, m.reserve("t1", ""))
	p, err := m.ready(context.Background(), "t1")
	require.NoError(t, err)
	assert.Equal(t, spec.ExposeViaTailnet, p.Via)
	assert.Equal(t, "100.64.0.1", p.Address)
	assert.Equal(t, "127.0.0.1", p.BindIP, "a slot forwards to the gateway over loopback")
	assert.False(t, p.SamePorts)

	err = m.reserve("t2", spec.ExposeViaTailnet)
	require.ErrorIs(t, err, errNoPreview, "one slot, one preview")

	require.NoError(t, m.reserve("t2", ""), "with no preference, a full tailnet falls back to the LAN")
	p2, err := m.ready(context.Background(), "t2")
	require.NoError(t, err)
	assert.Equal(t, "192.168.1.201", p2.Address)

	require.NoError(t, m.attach("t1", map[int]int{3000: 49152}))
	assert.Equal(t, map[int]int{3000: 49152}, devs[0].serving)

	m.release("t1")
	assert.Nil(t, devs[0].serving, "a released slot stops serving")
	require.NoError(t, m.reserve("t3", spec.ExposeViaTailnet), "and is free again")
}

func TestPreviewsLANPublishesUnderTheSamePorts(t *testing.T) {
	m, _ := testPreviews([]string{"192.168.1.201", "192.168.1.202"}, 0)
	require.NoError(t, m.reserve("t1", ""))
	require.NoError(t, m.reserve("t2", spec.ExposeViaLAN))
	p1, _ := m.ready(context.Background(), "t1")
	p2, _ := m.ready(context.Background(), "t2")
	assert.True(t, p1.SamePorts)
	assert.Equal(t, p1.Address, p1.BindIP)
	assert.NotEqual(t, p1.Address, p2.Address, "two previews never share an address")

	err := m.reserve("t3", "")
	assert.True(t, errors.Is(err, errNoPreview))
	err = m.reserve("t3", spec.ExposeViaTailnet)
	assert.ErrorContains(t, err, "no tailnet preview slots")
}

func TestHeldPreviewsHoldASlot(t *testing.T) {
	m, _ := testPreviews([]string{"192.168.1.201"}, 0)
	require.NoError(t, m.reserve("t1", ""))
	assert.Empty(t, m.heldIDs(), "reserved is not held: the command still runs")
	assert.True(t, m.hold("t1"))
	assert.Equal(t, []string{"t1"}, m.heldIDs())

	m.release("t1")
	assert.False(t, m.hold("t1"), "a task released before it was held is not held")
	assert.Zero(t, m.heldCount())
}

// A hold-only expose keeps its task up and its slot taken, and takes no address: a node with
// no LAN pool and no tailnet slots still runs it, and its pools stay free for others.
func TestHoldOnlyTakesNoAddress(t *testing.T) {
	m, devs := testPreviews([]string{"192.168.1.201"}, 1)
	require.NoError(t, m.reserve("t1", holdOnly))
	p, err := m.ready(context.Background(), "t1")
	require.NoError(t, err)
	assert.Nil(t, p, "nothing to publish, so no plan")
	assert.Empty(t, devs, "no slot device is brought up")

	tailnet, lan := m.free()
	assert.Equal(t, int32(1), tailnet)
	assert.Equal(t, int32(1), lan)
	assert.True(t, m.hold("t1"))
	assert.Equal(t, []string{"t1"}, m.heldIDs(), "held, so it keeps its node slot")

	bare, _ := testPreviews(nil, 0)
	require.NoError(t, bare.reserve("t2", holdOnly), "a node that publishes nothing can still hold")
}

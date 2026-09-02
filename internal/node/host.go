package node

import (
	"context"
	"log/slog"
	"os"
	"runtime"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
)

// HostFacts is what the node tells the server about the machine it runs on. It is
// gathered once, at startup, and reported at enrollment and in every Hello.
type HostFacts struct {
	Hostname      string
	CPUCores      int32
	MemoryMB      int64
	DockerVersion string
}

// hostFacts collects the static machine facts. Every probe is best-effort: a node that
// cannot read its own memory size is still a perfectly good node.
func hostFacts(ctx context.Context, dockerVersion string) HostFacts {
	f := HostFacts{DockerVersion: dockerVersion, CPUCores: int32(runtime.NumCPU())}
	if h, err := os.Hostname(); err == nil {
		f.Hostname = h
	}
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		f.MemoryMB = int64(vm.Total / (1024 * 1024))
	}
	return f
}

// hostLoad is the sampled half of the heartbeat: how busy the machine is right now.
type hostLoad struct {
	CPUPct        float64
	MemPct        float64
	DiskFreeBytes int64
}

// sampleLoad reads CPU, memory and the free space of the data dir. cpu.Percent with a
// zero interval reports usage since the previous call, so the first heartbeat of a
// process reports whatever the sampler saw since boot.
func sampleLoad(ctx context.Context, dataDir string, logger *slog.Logger) hostLoad {
	var l hostLoad
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	if pcts, err := cpu.PercentWithContext(ctx, 0, false); err == nil && len(pcts) > 0 {
		l.CPUPct = pcts[0]
	} else if err != nil {
		logger.DebugContext(ctx, "sampling cpu failed", "error", err)
	}
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		l.MemPct = vm.UsedPercent
	}
	if u, err := disk.UsageWithContext(ctx, dataDir); err == nil {
		l.DiskFreeBytes = int64(u.Free)
	}
	return l
}

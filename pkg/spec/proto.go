package spec

import (
	"maps"

	"google.golang.org/protobuf/types/known/durationpb"

	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
)

// ToProto converts a TaskSpec to its wire form.
func (s *TaskSpec) ToProto() *podiumv1.TaskSpec {
	if s == nil {
		return nil
	}
	p := &podiumv1.TaskSpec{
		Image:       s.Image,
		Command:     append([]string(nil), s.Command...),
		WorkingDir:  s.WorkingDir,
		Labels:      append([]string(nil), s.Labels...),
		MaxAttempts: int32(s.MaxAttempts),
		Resources:   s.Resources.toProto(),
		Hardening:   s.Hardening.toProto(),
	}
	if len(s.Env) > 0 {
		p.Env = maps.Clone(s.Env)
	}
	if s.Timeout != 0 {
		p.Timeout = durationpb.New(s.Timeout.Std())
	}
	if len(s.Sidecars) > 0 {
		p.Sidecars = make(map[string]*podiumv1.Sidecar, len(s.Sidecars))
		for name, sc := range s.Sidecars {
			p.Sidecars[name] = sc.toProto()
		}
	}
	return p
}

// FromProto converts a wire TaskSpec back to the public type.
func FromProto(p *podiumv1.TaskSpec) *TaskSpec {
	if p == nil {
		return nil
	}
	s := &TaskSpec{
		Image:       p.GetImage(),
		Command:     append([]string(nil), p.GetCommand()...),
		WorkingDir:  p.GetWorkingDir(),
		Labels:      append([]string(nil), p.GetLabels()...),
		MaxAttempts: int(p.GetMaxAttempts()),
		Resources:   resourcesFromProto(p.GetResources()),
		Hardening:   hardeningFromProto(p.GetHardening()),
	}
	if len(p.GetEnv()) > 0 {
		s.Env = maps.Clone(p.GetEnv())
	}
	if t := p.GetTimeout(); t != nil {
		s.Timeout = Duration(t.AsDuration())
	}
	if len(p.GetSidecars()) > 0 {
		s.Sidecars = make(map[string]Sidecar, len(p.GetSidecars()))
		for name, sc := range p.GetSidecars() {
			s.Sidecars[name] = sidecarFromProto(sc)
		}
	}
	return s
}

func (r Resources) toProto() *podiumv1.Resources {
	if r == (Resources{}) {
		return nil
	}
	return &podiumv1.Resources{Cpu: r.CPU, MemoryMb: int64(r.MemoryMB), Pids: int32(r.PIDs)}
}

func resourcesFromProto(p *podiumv1.Resources) Resources {
	if p == nil {
		return Resources{}
	}
	return Resources{CPU: p.GetCpu(), MemoryMB: int(p.GetMemoryMb()), PIDs: int(p.GetPids())}
}

func (h Hardening) toProto() *podiumv1.Hardening {
	if !h.ReadOnlyRootfs && len(h.Capabilities) == 0 {
		return nil
	}
	return &podiumv1.Hardening{
		ReadOnlyRootfs: h.ReadOnlyRootfs,
		Capabilities:   append([]string(nil), h.Capabilities...),
	}
}

func hardeningFromProto(p *podiumv1.Hardening) Hardening {
	if p == nil {
		return Hardening{}
	}
	return Hardening{
		ReadOnlyRootfs: p.GetReadOnlyRootfs(),
		Capabilities:   append([]string(nil), p.GetCapabilities()...),
	}
}

func (s Sidecar) toProto() *podiumv1.Sidecar {
	p := &podiumv1.Sidecar{
		Image:     s.Image,
		Command:   append([]string(nil), s.Command...),
		Readiness: s.Readiness.toProto(),
		Resources: s.Resources.toProto(),
	}
	if len(s.Env) > 0 {
		p.Env = maps.Clone(s.Env)
	}
	return p
}

func sidecarFromProto(p *podiumv1.Sidecar) Sidecar {
	s := Sidecar{
		Image:     p.GetImage(),
		Command:   append([]string(nil), p.GetCommand()...),
		Readiness: readinessFromProto(p.GetReadiness()),
		Resources: resourcesFromProto(p.GetResources()),
	}
	if len(p.GetEnv()) > 0 {
		s.Env = maps.Clone(p.GetEnv())
	}
	return s
}

func (r Readiness) toProto() *podiumv1.Readiness {
	if r.Probes() == 0 && r.Timeout == 0 && r.HTTPPort == 0 {
		return nil
	}
	p := &podiumv1.Readiness{
		TcpPort:  int32(r.TCPPort),
		HttpPath: r.HTTPPath,
		HttpPort: int32(r.HTTPPort),
		Command:  append([]string(nil), r.Command...),
	}
	if r.Timeout != 0 {
		p.Timeout = durationpb.New(r.Timeout.Std())
	}
	return p
}

func readinessFromProto(p *podiumv1.Readiness) Readiness {
	if p == nil {
		return Readiness{}
	}
	r := Readiness{
		TCPPort:  int(p.GetTcpPort()),
		HTTPPath: p.GetHttpPath(),
		HTTPPort: int(p.GetHttpPort()),
		Command:  append([]string(nil), p.GetCommand()...),
	}
	if t := p.GetTimeout(); t != nil {
		r.Timeout = Duration(t.AsDuration())
	}
	return r
}

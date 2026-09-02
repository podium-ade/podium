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
	}
	if len(s.Env) > 0 {
		p.Env = maps.Clone(s.Env)
	}
	if s.Timeout != 0 {
		p.Timeout = durationpb.New(s.Timeout.Std())
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
	}
	if len(p.GetEnv()) > 0 {
		s.Env = maps.Clone(p.GetEnv())
	}
	if t := p.GetTimeout(); t != nil {
		s.Timeout = Duration(t.AsDuration())
	}
	return s
}

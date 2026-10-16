package supervisor

import (
	"context"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
)

// SampleMetrics brackets OS counters with authoritative checks of the captured
// generation. Callers never choose a PID independently of an instance identity.
func (s *Supervisor) SampleMetrics(ctx context.Context, id domain.ProcessIdentity) (launcher.ProcessCounters, error) {
	current, err := s.Identity(ctx, id.InstanceID)
	if err != nil || current != id {
		return launcher.ProcessCounters{}, fmt.Errorf("JVM generation unavailable")
	}
	before, err := s.Inspect(ctx, id)
	if err != nil || !before.ProcessRunning {
		return launcher.ProcessCounters{}, fmt.Errorf("JVM not verifiably running")
	}
	counters, err := launcher.SampleProcessMetrics(ctx, id)
	if err != nil {
		return launcher.ProcessCounters{}, err
	}
	after, err := s.Inspect(ctx, id)
	if err != nil || !after.ProcessRunning {
		return launcher.ProcessCounters{}, fmt.Errorf("JVM changed during sample")
	}
	current, err = s.Identity(ctx, id.InstanceID)
	if err != nil || current != id {
		return launcher.ProcessCounters{}, fmt.Errorf("JVM generation changed")
	}
	return counters, nil
}

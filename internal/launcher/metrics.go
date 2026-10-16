package launcher

import (
	"context"
	"fmt"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

// ProcessCounters contains only exact-JVM counters, never launcher or host data.
// CPU is cumulative process CPU time; the consumer computes elapsed deltas.
type ProcessCounters struct {
	CPUNanos      *int64    `json:"cpu_nanos"`
	ResidentBytes *int64    `json:"resident_bytes"`
	PrivateBytes  *int64    `json:"private_bytes"`
	ObservedAt    time.Time `json:"observed_at"`
	Source        string    `json:"source"`
	GapReason     string    `json:"gap_reason,omitempty"`
}

func SampleProcessMetrics(ctx context.Context, id domain.ProcessIdentity) (ProcessCounters, error) {
	if err := domain.ValidateInstanceID(id.InstanceID); err != nil {
		return ProcessCounters{}, err
	}
	if id.PID <= 0 || id.StartIdentity == "" {
		return ProcessCounters{}, fmt.Errorf("missing JVM identity")
	}
	if err := ctx.Err(); err != nil {
		return ProcessCounters{}, err
	}
	return sampleProcessMetrics(ctx, id)
}

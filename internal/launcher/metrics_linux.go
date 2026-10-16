//go:build linux

package launcher

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

func sampleProcessMetrics(ctx context.Context, id domain.ProcessIdentity) (ProcessCounters, error) {
	// Only the installed fixed-instance broker can inspect isolated Linux JVMs.
	if id.WorkingDir != "/var/lib/minecraft-ui/instances/"+id.InstanceID+"/server" {
		return ProcessCounters{}, fmt.Errorf("metrics require fixed instance broker")
	}
	result, err := readBrokerIdentity(ctx, id)
	if err != nil {
		return ProcessCounters{}, err
	}
	if err = validateBrokerIdentity(id, result); err != nil {
		return ProcessCounters{}, err
	}
	if result.Counters == nil {
		return ProcessCounters{Source: "linux_proc", GapReason: "counters_unavailable"}, nil
	}
	return *result.Counters, nil
}
func readProcessCounters(pid int, start string) ProcessCounters {
	out := ProcessCounters{ObservedAt: time.Now().UTC(), Source: "linux_proc", GapReason: "counters_unavailable"}
	before, _, _, err := processIdentity(pid)
	if err != nil || before != start {
		return out
	}
	f, err := os.Open("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return out
	}
	raw, err := io.ReadAll(io.LimitReader(f, 65537))
	_ = f.Close()
	if err != nil || len(raw) > 65536 {
		return out
	}
	cpu, rss, err := parseProcessStat(raw, os.Getpagesize())
	if err != nil {
		return out
	}
	after, _, _, err := processIdentity(pid)
	if err != nil || after != start {
		return out
	}
	out.CPUNanos = &cpu
	out.ResidentBytes = &rss
	out.GapReason = ""
	return out
}
func parseProcessStat(raw []byte, pageSize int) (int64, int64, error) {
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 {
		return 0, 0, fmt.Errorf("invalid stat")
	}
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 22 || fields[0] == "Z" {
		return 0, 0, fmt.Errorf("incomplete stat")
	}
	user, e := strconv.ParseInt(fields[11], 10, 64)
	if e != nil {
		return 0, 0, e
	}
	kernel, e := strconv.ParseInt(fields[12], 10, 64)
	if e != nil {
		return 0, 0, e
	}
	rss, e := strconv.ParseInt(fields[21], 10, 64)
	if e != nil {
		return 0, 0, e
	}
	// Linux x86-64 exports /proc CPU ticks in USER_HZ=100, independent of CONFIG_HZ.
	const nanosPerTick = int64(time.Second) / 100
	if user < 0 || kernel < 0 || rss < 0 || pageSize <= 0 || user > math.MaxInt64/nanosPerTick-kernel || rss > math.MaxInt64/int64(pageSize) {
		return 0, 0, fmt.Errorf("counter overflow")
	}
	return (user + kernel) * nanosPerTick, rss * int64(pageSize), nil
}

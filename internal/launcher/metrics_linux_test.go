//go:build linux

package launcher

import (
	"context"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"os"
	"strings"
	"testing"
)

func TestLinuxCPUStatUsesUserTicksRSSAndRejectsMalformed(t *testing.T) {
	fields := make([]string, 22)
	for i := range fields {
		fields[i] = "0"
	}
	fields[0] = "S"
	fields[11] = "150"
	fields[12] = "50"
	fields[21] = "10"
	raw := []byte("123 (JVM (custom name)) " + strings.Join(fields, " "))
	cpu, rss, e := parseProcessStat(raw, 4096)
	if e != nil || cpu != 2_000_000_000 || rss != 40960 {
		t.Fatal(cpu, rss, e)
	}
	for _, test := range []string{"", "123 (x) S 1", strings.Replace(string(raw), "150", "-1", 1), strings.Replace(string(raw), "150", "9223372036854775807", 1), strings.Replace(string(raw), " S ", " Z ", 1)} {
		if _, _, e = parseProcessStat([]byte(test), 4096); e == nil {
			t.Fatal("invalid stat accepted", test)
		}
	}
}
func TestCounterIdentityFailureDoesNotBreakIdentityOnlyInspection(t *testing.T) {
	pid := os.Getpid()
	start, _, _, e := processIdentity(pid)
	if e != nil {
		t.Fatal(e)
	}
	good := readProcessCounters(pid, start)
	if good.CPUNanos == nil || good.ResidentBytes == nil || good.PrivateBytes != nil || good.Source != "linux_proc" {
		t.Fatalf("native read: %+v", good)
	}
	changed := readProcessCounters(pid, start+"-recycled")
	if changed.CPUNanos != nil || changed.ResidentBytes != nil || changed.GapReason != "counters_unavailable" {
		t.Fatal("foreign generation counters accepted", changed)
	}
	inspect, e := ReadNativeInspection("alpha", pid, pid+1)
	if e != nil || inspect.StartIdentity != start || inspect.Counters == nil {
		t.Fatal(inspect, e)
	}
	// Public sampler never falls back to arbitrary host PID access.
	if _, e = SampleProcessMetrics(context.Background(), domain.ProcessIdentity{InstanceID: "alpha", PID: pid, StartIdentity: start, WorkingDir: "/tmp/foreign"}); e == nil {
		t.Fatal("bypassed fixed broker")
	}
	if _, e = SampleProcessMetrics(context.Background(), domain.ProcessIdentity{InstanceID: "../other", PID: pid, StartIdentity: start}); e == nil {
		t.Fatal("invalid instance accepted")
	}
}

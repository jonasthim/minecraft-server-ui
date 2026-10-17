//go:build linux

package selfupdate

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestParseLinuxProbeStatus(t *testing.T) {
	status := "Name:\tworker\nUid:\t0\t1000\t1000\t1000\nCapEff:\t0000000000000080\nCapPrm:\t0000000000000080\nCapBnd:\t0000000000000080\nNoNewPrivs:\t1\nSeccomp:\t2\n"
	got := parseLinuxProbeStatus([]byte(status))
	if got.euid != "nonroot" || got.capEffective != "yes" || got.capPermitted != "yes" || got.capBounding != "yes" || got.noNewPrivs != "1" || got.seccomp != "2" {
		t.Fatalf("parsed status = %+v", got)
	}
}

func TestParseLinuxProbeStatusUnknownOnMalformedOrDuplicateFields(t *testing.T) {
	for _, tc := range []struct{ status, field string }{
		{"Uid:\tbad\t0\t0\t0\n", "euid"},
		{"Uid:\t0\tbad\t0\t0\n", "euid"},
		{"Uid:\t0\t0\tbad\t0\n", "euid"},
		{"Uid:\t0\t0\t0\tbad\n", "euid"},
		{"Uid:\t0\t0\t0\t4294967296\n", "euid"},
		{"Uid:\t00\t0\t0\t0\n", "euid"},
		{"Uid:\t0\t0\t0\t0\nUid:\t0\t0\t0\t0\n", "euid"},
		{"CapEff:\t80\nCapEff:\t80\n", "effective"},
		{"CapPrm:\tgg\n", "permitted"},
		{"CapBnd:\t10000000000000000\n", "bounding"},
		{"NoNewPrivs:\t2\n", "nnp"},
		{"Seccomp:\t3\n", "seccomp"},
	} {
		got := parseLinuxProbeStatus([]byte(tc.status))
		values := map[string]string{"euid": got.euid, "effective": got.capEffective, "permitted": got.capPermitted, "bounding": got.capBounding, "nnp": got.noNewPrivs, "seccomp": got.seccomp}
		if values[tc.field] != "unknown" {
			t.Errorf("%s = %q for malformed status", tc.field, values[tc.field])
		}
	}
}

func TestParseLinuxProbeUIDMapRequiresInitialMappingExactly(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"         0          0 4294967295\n", "initial"},
		{"         0       1000 1000\n", "other"},
		{"0 4294967296 1\n", "unknown"},
		{"4000000000 0 1000000000\n", "unknown"},
		{"0 1000 0\n", "unknown"},
		{"00 0 4294967295\n", "unknown"},
		{"0 0 4294967295\n0 1000 1\n", "unknown"},
		{"0 0 4294967295 trailing\n", "unknown"},
		{"", "unknown"},
	} {
		if got := parseLinuxProbeUIDMap([]byte(tc.input)); got != tc.want {
			t.Errorf("parseLinuxProbeUIDMap(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestReadLinuxProbeFileIsBounded(t *testing.T) {
	path := t.TempDir() + "/status"
	if err := os.WriteFile(path, []byte(strings.Repeat("x", linuxProbeProcReadLimit+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, ok := readLinuxProbeFile(path, linuxProbeProcReadLimit); ok {
		t.Fatal("oversized proc fixture was accepted")
	}
}

func TestCaptureLinuxProbeContextAndHarmlessChild(t *testing.T) {
	ctx := captureLinuxProbeContext()
	for _, line := range ctx.markers() {
		if !validLinuxProbeContextMarker(line) {
			t.Fatalf("generated non-allowlisted marker %q", line)
		}
	}
	// Verify a normal harmless child can be started while the context snapshot
	// is taken on the same locked OS thread that launches it.
	command := exec.Command("/usr/bin/id", "-u")
	var stdout bytes.Buffer
	command.Stdout = &stdout
	ctx, err := startLinuxProbeCommand(command)
	if err != nil {
		t.Fatalf("harmless child start: %v", err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("harmless child wait: %v", err)
	}
	if ctx.thread.euid != "unknown" && (strings.TrimSpace(stdout.String()) == "0") != (ctx.thread.euid == "root") {
		t.Fatalf("thread snapshot euid=%q disagrees with harmless child id output %q", ctx.thread.euid, stdout.String())
	}
}

package selfupdate

import (
	"context"
	"testing"
)

type availabilityLegacy struct {
	available                 bool
	reason                    string
	checks, reasons, requests int
}

func (n *availabilityLegacy) Available(context.Context) bool { n.checks++; return n.available }
func (n *availabilityLegacy) UnavailableReason() string      { n.reasons++; return n.reason }
func (n *availabilityLegacy) Request(context.Context, ActivationRequest) error {
	n.requests++
	return nil
}

type availabilityDetailed struct {
	availabilityLegacy
	probes int
	ctx    context.Context
}

func (n *availabilityDetailed) AvailabilityStatus(ctx context.Context) (bool, string) {
	n.probes++
	n.ctx = ctx
	return n.available, n.reason
}

func TestStatusUsesSingleDetailedAvailabilityObservation(t *testing.T) {
	trust, _, _, _ := signedFixture(t)
	ctx := t.Context()
	for _, available := range []bool{false, true} {
		native := &availabilityDetailed{availabilityLegacy: availabilityLegacy{available: available, reason: "fixed native reason"}}
		service := New(Config{Trust: trust, CurrentVersion: "v1.2.2", Schema: 16, StageDir: t.TempDir(), Native: native})
		status := service.Status(ctx)
		if native.probes != 1 || native.checks != 0 || native.reasons != 0 || native.requests != 0 || native.ctx != ctx {
			t.Fatalf("status repeated or bypassed its single observation: %+v", native)
		}
		if status.ActivationAvailable != available || !available && status.DisabledReason != native.reason || available && status.DisabledReason != "" {
			t.Fatalf("availability and reason were inconsistent: %+v", status)
		}
	}
}

func TestStatusKeepsLegacyFallbackAndConfigurationGates(t *testing.T) {
	trust, _, _, _ := signedFixture(t)
	for _, tc := range []struct {
		name                               string
		native                             Native
		configured, development, available bool
		reason                             string
	}{
		{"absent", nil, true, false, false, "The native update and recovery service is not installed or ready."},
		{"legacy reason", &availabilityLegacy{reason: "manual maintenance required"}, true, false, false, "manual maintenance required"},
		{"legacy default", &availabilityLegacy{}, true, false, false, "The native update and recovery service is not installed or ready."},
		{"legacy available", &availabilityLegacy{available: true, reason: "ignored"}, true, false, true, ""},
		{"detailed default", &availabilityDetailed{}, true, false, false, "The native update and recovery service is not installed or ready."},
		{"unconfigured", &availabilityDetailed{}, false, false, false, "Release source and verification keys have not been configured."},
		{"development", &availabilityDetailed{}, true, true, false, "Development builds require a reviewed native installation before manager activation."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{CurrentVersion: "v1.2.2", Schema: 16, StageDir: t.TempDir(), Native: tc.native}
			if tc.configured {
				cfg.Trust = trust
			}
			if tc.development {
				cfg.CurrentVersion = "0.0.0-dev"
			}
			status := New(cfg).Status(t.Context())
			if status.ActivationAvailable != tc.available || status.DisabledReason != tc.reason {
				t.Fatalf("changed fallback/gate: %+v", status)
			}
			if native, ok := tc.native.(*availabilityDetailed); ok && (!tc.configured || tc.development) && (native.probes != 0 || native.checks != 0 || native.reasons != 0 || native.requests != 0) {
				t.Fatal("configuration gate contacted native service")
			}
		})
	}
}

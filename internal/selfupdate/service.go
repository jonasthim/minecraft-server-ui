package selfupdate

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"runtime"
	"sync"
	"time"
)

type Native interface {
	Request(context.Context, ActivationRequest) error
	Available(context.Context) bool
}
type ActivationRequest struct {
	StageID     string    `json:"stage_id"`
	ActorID     int64     `json:"actor_id"`
	RequestedAt time.Time `json:"requested_at"`
}
type Config struct {
	Trust          Trust
	CurrentVersion string
	Schema         int64
	StageDir       string
	DB             *sql.DB
	Native         Native
}
type Service struct {
	cfg    Config
	source *Source
	mu     sync.Mutex
}
type Status struct {
	CurrentVersion      string `json:"current_version"`
	Schema              int64  `json:"schema"`
	TrustConfigured     bool   `json:"trust_configured"`
	ActivationAvailable bool   `json:"activation_available"`
	DisabledReason      string `json:"disabled_reason"`
	SchemaPolicy        string `json:"schema_policy"`
}
type CheckResult struct {
	Version      string    `json:"version"`
	Platform     string    `json:"platform"`
	PublishedAt  time.Time `json:"published_at"`
	ExpiresAt    time.Time `json:"expires_at"`
	BinarySHA256 string    `json:"binary_sha256"`
	Schema       int64     `json:"schema"`
}
type ApplyInput struct {
	StageID string `json:"stage_id"`
	Confirm bool   `json:"confirm"`
}

func New(cfg Config) *Service {
	keys := map[string]ed25519.PublicKey{}
	for id, key := range cfg.Trust.PublicKeys {
		keys[id] = append(ed25519.PublicKey{}, key...)
	}
	cfg.Trust.PublicKeys = keys
	s := &Service{cfg: cfg}
	s.source, _ = NewSource(cfg.Trust)
	return s
}
func (s *Service) Status(ctx context.Context) Status {
	out := Status{CurrentVersion: s.cfg.CurrentVersion, Schema: s.cfg.Schema, TrustConfigured: s.source != nil, SchemaPolicy: "unchanged_schema_only"}
	switch {
	case s.source == nil:
		out.DisabledReason = "Release source and verification keys have not been configured."
	case s.cfg.CurrentVersion == "0.0.0-dev":
		out.DisabledReason = "Development builds require a reviewed native installation before manager activation."
	case s.cfg.Native == nil || !s.cfg.Native.Available(ctx):
		out.DisabledReason = "The native update and recovery service is not installed or ready."
		if reason, ok := s.cfg.Native.(interface{ UnavailableReason() string }); ok && reason.UnavailableReason() != "" {
			out.DisabledReason = reason.UnavailableReason()
		}
	default:
		out.ActivationAvailable = true
	}
	return out
}
func requireAdmin(actor domain.User) error {
	if actor.Role != domain.RoleAdmin {
		return domain.E(domain.CodeForbidden, "Manager updates require an administrator.")
	}
	return nil
}
func (s *Service) metadata(ctx context.Context, tag string) (Manifest, []byte, []byte, error) {
	if s.source == nil {
		return Manifest{}, nil, nil, domain.E(domain.CodeUnavailable, "Release trust is not configured.")
	}
	if _, err := ParseStable(tag); err != nil {
		return Manifest{}, nil, nil, domain.E(domain.CodeValidationFailed, "A stable vMAJOR.MINOR.PATCH release is required.")
	}
	body, sig, err := s.source.Metadata(ctx, tag)
	if err != nil {
		return Manifest{}, nil, nil, domain.Wrap(domain.CodeUpstreamError, "Release metadata could not be downloaded.", err)
	}
	m, err := VerifyManifest(s.cfg.Trust, body, sig, tag, s.cfg.CurrentVersion, s.cfg.Schema, time.Now())
	if err != nil {
		return Manifest{}, nil, nil, domain.Wrap(domain.CodeValidationFailed, "Release verification failed; check trust, version, validity and schema compatibility.", err)
	}
	return m, body, sig, nil
}
func (s *Service) Check(ctx context.Context, tag string, actor domain.User) (CheckResult, error) {
	if err := requireAdmin(actor); err != nil {
		return CheckResult{}, err
	}
	m, _, _, err := s.metadata(ctx, tag)
	if err != nil {
		return CheckResult{}, err
	}
	a, err := m.Artifact(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return CheckResult{}, domain.E(domain.CodeUnavailable, "No release artifact exists for this platform.")
	}
	return CheckResult{Version: m.Version, Platform: a.OS + "/" + a.Arch, PublishedAt: m.PublishedAt, ExpiresAt: m.ExpiresAt, BinarySHA256: a.BinarySHA256, Schema: m.SchemaAfter}, nil
}
func (s *Service) Stage(ctx context.Context, tag string, actor domain.User) (Stage, error) {
	if err := requireAdmin(actor); err != nil {
		return Stage{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, body, sig, err := s.metadata(ctx, tag)
	if err != nil {
		return Stage{}, err
	}
	a, err := m.Artifact(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return Stage{}, err
	}
	fresh, err := s.freshAdmin(ctx, actor)
	if err != nil {
		return Stage{}, err
	}
	actor = fresh
	if err := s.audit(ctx, actor, "manager_update.stage_requested", tag); err != nil {
		return Stage{}, err
	}
	r, err := s.source.open(ctx, tag, a.Name)
	if err != nil {
		return Stage{}, domain.Wrap(domain.CodeUpstreamError, "Release artifact could not be downloaded.", err)
	}
	defer r.Close()
	stager := Stager{BeforePublish: func(ctx context.Context) error { _, err := s.freshAdmin(ctx, actor); return err }, Dir: s.cfg.StageDir, Trust: s.cfg.Trust, CurrentVersion: s.cfg.CurrentVersion, Schema: s.cfg.Schema, OS: runtime.GOOS, Arch: runtime.GOARCH}
	result, err := stager.Stage(ctx, tag, body, sig, r)
	if err != nil {
		return Stage{}, domain.Wrap(domain.CodeUnavailable, "Release staging failed; no activation was requested.", err)
	}
	return result, nil
}
func (s *Service) Apply(ctx context.Context, in ApplyInput, actor domain.User) error {
	if err := requireAdmin(actor); err != nil {
		return err
	}
	if !in.Confirm || !validHash(in.StageID) {
		return domain.E(domain.CodeValidationFailed, "Confirm manager restart and provide a valid stage identifier.")
	}
	status := s.Status(ctx)
	if !status.ActivationAvailable {
		return domain.E(domain.CodeUnavailable, status.DisabledReason)
	}
	fresh, err := s.freshAdmin(ctx, actor)
	if err != nil {
		return err
	}
	actor = fresh
	if err := s.audit(ctx, actor, "manager_update.activation_requested", in.StageID); err != nil {
		return err
	}
	if err := s.cfg.Native.Request(ctx, ActivationRequest{StageID: in.StageID, ActorID: actor.ID, RequestedAt: time.Now().UTC()}); err != nil {
		return domain.Wrap(domain.CodeUnavailable, "Native activation was not accepted.", err)
	}
	return nil
}
func (s *Service) audit(ctx context.Context, actor domain.User, action, target string) error {
	if s.cfg.DB == nil {
		return domain.E(domain.CodeUnavailable, "Manager update auditing is unavailable.")
	}
	id := actor.ID
	err := db.NewAuditRepo(s.cfg.DB).Insert(ctx, domain.AuditEntry{UserID: &id, Username: actor.Username, Action: action, Target: target, Details: map[string]any{}})
	if err != nil {
		return domain.Wrap(domain.CodeUnavailable, "Manager update audit could not be recorded.", err)
	}
	return nil
}

func (s *Service) freshAdmin(ctx context.Context, actor domain.User) (domain.User, error) {
	if s.cfg.DB == nil {
		return domain.User{}, domain.E(domain.CodeUnavailable, "Manager update authorization is unavailable.")
	}
	var out domain.User
	err := s.cfg.DB.QueryRowContext(ctx, `SELECT id,username,role,disabled FROM users WHERE id=?`, actor.ID).Scan(&out.ID, &out.Username, &out.Role, &out.Disabled)
	if err != nil || out.Disabled || out.Role != domain.RoleAdmin {
		return domain.User{}, domain.E(domain.CodeForbidden, "Administrator access was revoked or disabled.")
	}
	return out, nil
}

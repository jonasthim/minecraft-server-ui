package update

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/content"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/lifecycle"
	"github.com/jonasthim/minecraft-server-ui/internal/modpack"
	"github.com/jonasthim/minecraft-server-ui/internal/server/providerutil"
)

type preparedTransaction struct {
	plan           Plan
	request        Request
	before         lifecycle.UpdateState
	after          lifecycle.UpdateState
	lifecycle      *lifecycle.PreparedUpdate
	content        *content.PreparedUpdate
	pack           *modpack.PreparedUpdate
	contentReceipt content.UpdateReceipt
	packReceipt    modpack.UpdateReceipt
}
type previewEnvelope struct {
	PackStaging json.RawMessage `json:"pack_staging,omitempty"`
	Version     int             `json:"version"`
	ActorID     int64           `json:"actor_id"`
	Plan        Plan            `json:"plan"`
	Request     Request         `json:"request"`
}
type jobInput struct {
	Action    string   `json:"action"`
	Request   *Request `json:"request,omitempty"`
	PreviewID string   `json:"preview_id,omitempty"`
	Confirmed bool     `json:"confirmed,omitempty"`
}

func opaqueID() (string, error) {
	var raw [16]byte
	_, err := rand.Read(raw[:])
	return hex.EncodeToString(raw[:]), err
}
func stamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func (s *Service) actor(ctx context.Context, id int64, admin bool) (domain.User, error) {
	var actor domain.User
	actor.ID = id
	if err := s.config.DB.QueryRowContext(ctx, "SELECT username,role,disabled FROM users WHERE id=?", id).Scan(&actor.Username, &actor.Role, &actor.Disabled); err != nil || id <= 0 || actor.Disabled || !actor.Role.AtLeast(domain.RoleOperator) || admin && actor.Role != domain.RoleAdmin {
		return actor, domain.E(domain.CodeForbidden, "current update authority unavailable")
	}
	return actor, nil
}
func (s *Service) execution(ctx context.Context, e *jobs.Execution) (jobs.CurrentExecution, domain.User, error) {
	current, err := e.CheckCurrent(ctx)
	if err != nil {
		return current, domain.User{}, err
	}
	if current.Kind != "instance_update" && current.Kind != "instance_update_prepare" && current.Kind != "instance_rollback" {
		return current, domain.User{}, domain.E(domain.CodeForbidden, "original typed update execution required")
	}
	actor, err := s.actor(ctx, current.RequesterID, current.Kind == "instance_rollback")
	return current, actor, err
}
func (s *Service) audit(ctx context.Context, id string, actor domain.User, action, outcome string) error {
	raw, _ := json.Marshal(map[string]string{"outcome": outcome})
	_, err := s.config.DB.ExecContext(ctx, "INSERT INTO audit_log(ts,user_id,username,action,instance_id,target,details_json) VALUES(?,?,?,?,?,'instance',?)", stamp(), actor.ID, actor.Username, "updates."+action, id, string(raw))
	return err
}
func (s *Service) Register() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.registered {
		return conflict("update jobs already registered")
	}
	if err := s.config.Jobs.Register("instance_update_prepare", s.logged(s.runPrepare)); err != nil {
		return err
	}
	if err := s.config.Jobs.Register("instance_update", s.logged(s.runApply)); err != nil {
		return err
	}
	if err := s.config.Jobs.Register("instance_rollback", s.logged(func(ctx context.Context, e *jobs.Execution) error {
		if s.config.Rollbacks == nil {
			return domain.E(domain.CodeUnavailable, "explicit instance rollback unavailable")
		}
		return s.config.Rollbacks.Run(ctx, e)
	})); err != nil {
		return err
	}
	s.registered = true
	return nil
}

// Preview admits only the typed staging operation. The worker never stops or
// starts a process and never changes selection, launch pointers or world data.
func (s *Service) Preview(ctx context.Context, id string, request Request, actor domain.User) (jobs.Job, error) {
	fresh, err := s.actor(ctx, actor.ID, false)
	if err != nil {
		return jobs.Job{}, err
	}
	detail, err := s.config.Instances.Get(ctx, id)
	if err != nil {
		return jobs.Job{}, err
	}
	if detail.Provisioning != "installed" {
		return jobs.Job{}, domain.E(domain.CodeInstanceNotInstalled, "Install the selected server files before preparing an update.")
	}
	current := domain.VersionSelection{Flavor: detail.Instance.Flavor, MinecraftVersion: detail.Instance.MinecraftVersion, LoaderVersion: detail.Instance.LoaderVersion}
	if _, _, err = request.Resolve(current); err != nil {
		return jobs.Job{}, err
	}
	needsAdmin, err := s.PreviewNeedsAdministrator(ctx, id, request, actor.ID)
	if err != nil {
		return jobs.Job{}, err
	}
	if needsAdmin && fresh.Role != domain.RoleAdmin {
		return jobs.Job{}, domain.E(domain.CodeForbidden, "manual update receipts require current administrator authority")
	}
	if err = s.checkAdmissionReady(ctx, id); err != nil {
		return jobs.Job{}, err
	}
	raw, err := json.Marshal(jobInput{Action: "prepare", Request: &request})
	if err != nil {
		return jobs.Job{}, err
	}
	if err = s.audit(ctx, id, fresh, "preview_request", "requested"); err != nil {
		return jobs.Job{}, err
	}
	return s.config.Jobs.Enqueue(ctx, jobs.Request{InstanceID: id, Kind: "instance_update_prepare", Payload: raw, RequestedBy: fresh.ID, Cancellable: true})
}
func (s *Service) runPrepare(ctx context.Context, e *jobs.Execution) error {
	current, actor, err := s.execution(ctx, e)
	if err != nil {
		return err
	}
	if current.Kind != "instance_update_prepare" {
		return domain.E(domain.CodeForbidden, "original typed staging execution required")
	}
	var persistedInput string
	if err = s.config.DB.QueryRowContext(ctx, "SELECT payload FROM jobs WHERE id=? AND instance_id=? AND requested_by=? AND kind='instance_update_prepare' AND state='running'", current.JobID, current.InstanceID, actor.ID).Scan(&persistedInput); err != nil {
		return err
	}
	var input jobInput
	if json.Unmarshal([]byte(persistedInput), &input) != nil || input.Action != "prepare" || input.Request == nil {
		return invalid("typed update preview request required")
	}
	if err = e.Phase(ctx, "stage_update_preview", 5); err != nil {
		return err
	}
	prepared, err := s.prepare(ctx, e, *input.Request, actor)
	if err != nil {
		return err
	}
	packStaging, err := prepared.pack.PersistedPreview(ctx)
	if err != nil {
		return err
	}
	envelope := previewEnvelope{Version: 1, ActorID: actor.ID, Plan: prepared.plan, Request: prepared.request, PackStaging: packStaging}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	if len(raw) > 16<<20 {
		return invalid("serialized update preview exceeds 16 MiB evidence bound")
	}
	if _, err = s.config.DB.ExecContext(ctx, "INSERT INTO update_previews(id,instance_id,actor_id,private_json,created_at,expires_at,job_id) VALUES(?,?,?,?,?,?,?)", prepared.plan.ID, current.InstanceID, actor.ID, string(raw), stamp(), prepared.plan.ExpiresAt.Format(time.RFC3339Nano), current.JobID); err != nil {
		return err
	}
	if err = s.audit(ctx, current.InstanceID, actor, "preview_result", "prepared"); err != nil {
		return err
	}
	return e.Log(ctx, "Update preview prepared. Open the Updates page to inspect exact changes and blockers; no process or world data was changed.")
}
func (s *Service) GetPreview(ctx context.Context, id, previewID string, actor domain.User) (Plan, error) {
	envelope, err := s.loadPreview(ctx, id, previewID, actor.ID)
	if err != nil {
		return Plan{}, err
	}
	return envelope.Plan, nil
}
func (s *Service) loadPreview(ctx context.Context, id, previewID string, actorID int64) (previewEnvelope, error) {
	var envelope previewEnvelope
	if !validHex(previewID, 32) {
		return envelope, invalid("invalid update preview identity")
	}
	if _, err := s.actor(ctx, actorID, false); err != nil {
		return envelope, err
	}
	var raw string
	var owner int64
	if err := s.config.DB.QueryRowContext(ctx, "SELECT private_json,actor_id FROM update_previews WHERE id=? AND instance_id=?", previewID, id).Scan(&raw, &owner); err != nil {
		if err == sql.ErrNoRows {
			return envelope, domain.NotFound("update preview")
		}
		return envelope, err
	}
	if owner != actorID {
		return envelope, domain.E(domain.CodeForbidden, "update preview belongs to another actor")
	}
	if len(raw) > 16<<20 || json.Unmarshal([]byte(raw), &envelope) != nil || envelope.Version != 1 || envelope.ActorID != owner || envelope.Plan.ID != previewID || envelope.Plan.InstanceID != id {
		return envelope, conflict("update preview is malformed")
	}
	envelope.Plan.ActorID = owner
	if !time.Now().Before(envelope.Plan.ExpiresAt) {
		return envelope, conflict("update preview expired")
	}
	return envelope, nil
}
func (s *Service) ListPreviews(ctx context.Context, id string, actor domain.User) ([]Plan, error) {
	if _, err := s.actor(ctx, actor.ID, false); err != nil {
		return nil, err
	}
	rows, err := s.config.DB.QueryContext(ctx, "SELECT private_json FROM update_previews WHERE instance_id=? AND actor_id=? AND expires_at>? ORDER BY created_at DESC LIMIT 32", id, actor.ID, stamp())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	plans := []Plan{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var envelope previewEnvelope
		if json.Unmarshal([]byte(raw), &envelope) == nil && envelope.Version == 1 && envelope.ActorID == actor.ID && envelope.Plan.ID != "" {
			plans = append(plans, envelope.Plan)
		}
	}
	return plans, rows.Err()
}
func (s *Service) prepare(ctx context.Context, e *jobs.Execution, request Request, actor domain.User) (*preparedTransaction, error) {
	current, _, err := s.execution(ctx, e)
	if err != nil {
		return nil, err
	}
	if err = s.checkAdmissionReady(ctx, current.InstanceID); err != nil {
		return nil, err
	}
	if err = s.checkOrdinaryReady(ctx, current.InstanceID); err != nil {
		return nil, err
	}
	var livePreviews int
	if err = s.config.DB.QueryRowContext(ctx, "SELECT count(*) FROM update_previews WHERE instance_id=? AND expires_at>?", current.InstanceID, stamp()).Scan(&livePreviews); err != nil {
		return nil, err
	}
	if current.Kind == "instance_update_prepare" && livePreviews >= 32 {
		return nil, conflict("too many unexpired update previews; wait for expiry before preparing another")
	}
	before, err := s.config.Lifecycle.UpdateSnapshot(ctx, current.InstanceID)
	if err != nil {
		return nil, err
	}
	observed, err := s.config.Content.Snapshot(ctx, current.InstanceID)
	if err != nil {
		return nil, err
	}
	selected, countdown, err := request.Resolve(before.Plan.Selection)
	if err != nil {
		return nil, err
	}
	if request.Mode == Automatic && selected.Flavor != domain.FlavorVanilla {
		catalog, err := s.config.Lifecycle.Catalog(ctx, selected.Flavor, selected.MinecraftVersion)
		if err != nil {
			return nil, err
		}
		if len(catalog.Loaders) > 0 {
			selected.LoaderVersion = catalog.Loaders[0]
		}
	}
	plan, err := s.config.Lifecycle.ResolveUpdate(ctx, current.InstanceID, selected)
	if err != nil {
		return nil, err
	}
	managed, err := s.config.Lifecycle.PrepareUpdateForJob(ctx, e, before, plan)
	if err != nil {
		return nil, err
	}
	after, err := managed.Snapshot()
	if err != nil {
		return nil, err
	}
	contentPrepared, err := s.config.Content.PrepareUpdateCandidate(ctx, current.InstanceID, observed, managed, request.ContentSelections, request.ContentManual, actor)
	if err != nil {
		return nil, err
	}
	contentReceipt, err := contentPrepared.Receipt(ctx)
	if err != nil {
		return nil, err
	}
	packPrepared, err := s.config.Packs.PrepareUpdatePreview(ctx, current.InstanceID, request.Pack, observed, managed, contentReceipt.Candidate, actor)
	if err != nil {
		return nil, err
	}
	packReceipt, err := packPrepared.Receipt(ctx)
	if err != nil {
		return nil, err
	}
	contentPrepared, err = contentPrepared.WithCandidate(ctx, packReceipt.Candidate)
	if err != nil {
		return nil, err
	}
	contentReceipt, err = contentPrepared.Receipt(ctx)
	if err != nil {
		return nil, err
	}
	previewID, err := opaqueID()
	if err != nil {
		return nil, err
	}
	packRevision := "none"
	if packReceipt.BeforeBaseline != nil {
		packRevision = packReceipt.BeforeBaseline.Revision
	}
	public := Plan{ID: previewID, InstanceID: current.InstanceID, ActorID: actor.ID, Mode: request.Mode, Snapshot: Snapshot{before.Revision, before.Generation, observed.Revision, packRevision}, From: before.Plan.Selection, Target: selected, Requirements: plan.Requirements, Runtime: RuntimeIdentity{ID: after.Runtime.ID, Major: after.Runtime.Major, Version: after.Runtime.Version, OS: after.Runtime.OS, Architecture: after.Runtime.Architecture, SHA256: after.Runtime.Artifact.Checksum.Value}, Artifacts: []Artifact{}, Changes: []Change{}, Blockers: []Blocker{}, CountdownSeconds: countdown, ExpiresAt: time.Now().UTC().Add(30 * time.Minute)}
	public.Pack = packReceipt.Preview
	public.ContentManualRequired = contentReceipt.ManualRequired
	for _, artifact := range plan.Artifacts {
		entry := Artifact{Filename: artifact.Filename, Size: artifact.Size, Checksum: artifact.Checksum}
		if artifact.Checksum.Algorithm == "sha256" {
			entry.SHA256 = artifact.Checksum.Value
		}
		public.Artifacts = append(public.Artifacts, entry)
	}
	if !providerutil.SamePlan(before.Plan, after.Plan) {
		public.Changes = append(public.Changes, Change{Component: "server", Before: selectionLabel(before.Plan.Selection), After: selectionLabel(after.Plan.Selection)})
	}
	if before.Runtime.ID != after.Runtime.ID || before.Runtime.Artifact.Checksum != after.Runtime.Artifact.Checksum {
		public.Changes = append(public.Changes, Change{Component: "runtime", Before: before.Runtime.Version, After: after.Runtime.Version})
	}
	for _, op := range contentReceipt.Operations {
		public.Changes = append(public.Changes, Change{Component: op.Path, Before: op.Before, After: op.After})
	}
	for _, op := range packReceipt.Operations {
		public.Changes = append(public.Changes, Change{Component: op.Path, Before: op.Before.SHA256, After: op.After.SHA256})
	}
	if baselineIdentity(packReceipt.BeforeBaseline) != baselineIdentity(packReceipt.Baseline) {
		public.Changes = append(public.Changes, Change{Component: "pack baseline", Before: baselineLabel(packReceipt.BeforeBaseline), After: baselineLabel(packReceipt.Baseline)})
	}
	blockers := append(append([]string{}, contentReceipt.Blockers...), packReceipt.Blockers...)
	seen := map[string]bool{}
	for _, message := range blockers {
		if !seen[message] {
			public.Blockers = append(public.Blockers, Blocker{Code: "compatibility_or_conflict", Message: message})
			seen[message] = true
		}
	}
	paths := map[string]bool{}
	for _, change := range public.Changes {
		if change.Component == "server" || change.Component == "runtime" || change.Component == "pack baseline" {
			continue
		}
		fold := strings.ToLower(change.Component)
		if paths[fold] {
			public.Blockers = append(public.Blockers, Blocker{Code: "file_conflict", Message: "Content and pack updates claim the same path: " + change.Component})
		}
		paths[fold] = true
	}
	sort.Slice(public.Changes, func(i, j int) bool { return public.Changes[i].Component < public.Changes[j].Component })
	sort.Slice(public.Blockers, func(i, j int) bool { return public.Blockers[i].Message < public.Blockers[j].Message })
	if _, err = Freeze(public); err != nil {
		return nil, err
	}
	return &preparedTransaction{public, request, before, after, managed, contentPrepared, packPrepared, contentReceipt, packReceipt}, nil
}
func selectionLabel(selection domain.VersionSelection) string {
	return fmt.Sprintf("%s %s %s", selection.Flavor, selection.MinecraftVersion, selection.LoaderVersion)
}
func baselineLabel(baseline *modpack.Baseline) string {
	if baseline == nil {
		return "none"
	}
	return baseline.Source.Selection.VersionID
}
func baselineIdentity(baseline *modpack.Baseline) string {
	if baseline == nil {
		return "none"
	}
	raw, _ := json.Marshal(baseline)
	var copy modpack.Baseline
	_ = json.Unmarshal(raw, &copy)
	copy.Revision = ""
	copy.Source.Provenance.FetchedAt = time.Time{}
	raw, _ = json.Marshal(copy)
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

func (s *Service) logged(handler jobs.Handler) jobs.Handler {
	return func(ctx context.Context, e *jobs.Execution) (result error) {
		defer func() {
			if result != nil {
				message := "Update could not complete; inspect current recovery status and the last completed phase before retrying."
				if typed, ok := result.(*domain.Error); ok && typed.Code != domain.CodeInternal {
					message = typed.Message
				}
				cleanup, cancel := e.CleanupContext()
				defer cancel()
				_ = e.Log(cleanup, message)
			}
		}()
		return handler(ctx, e)
	}
}

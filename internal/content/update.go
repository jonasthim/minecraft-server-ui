package content

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"reflect"
	"sort"
	"sync/atomic"

	"github.com/jonasthim/minecraft-server-ui/internal/catalog"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/lifecycle"
)

type UpdateOperation struct {
	Path        string `json:"path"`
	Before      string `json:"before"`
	After       string `json:"after"`
	BeforeStage string `json:"before_stage"`
	AfterStage  string `json:"after_stage"`
}
type UpdateReceipt struct {
	ManualRequired map[string]ManualItem `json:"manual_required,omitempty"`
	Observed       Manifest              `json:"observed"`
	Target         Tuple                 `json:"target"`
	Candidate      []Installed           `json:"candidate"`
	Operations     []UpdateOperation     `json:"operations"`
	Blockers       []string              `json:"blockers"`
	Manual         []ManualItem          `json:"manual"`
	Metadata       map[string]string     `json:"metadata"`
}
type PreparedUpdate struct {
	requiresAdmin  bool
	owner          *PreparedUpdate
	service        *Service
	target         *lifecycle.PreparedUpdate
	id             string
	actorID        int64
	receipt        UpdateReceipt
	sourceBlockers []string
}

func cloneUpdateReceipt(receipt UpdateReceipt) UpdateReceipt {
	raw, _ := json.Marshal(receipt)
	var out UpdateReceipt
	_ = json.Unmarshal(raw, &out)
	return out
}
func (p *PreparedUpdate) check(ctx context.Context) error {
	if p == nil || p.owner != p || p.target == nil {
		return domain.E(domain.CodeForbidden, "content update capability forged or copied")
	}
	current, err := p.target.CheckCurrent(ctx)
	if err != nil {
		return err
	}
	if current.RequesterID != p.actorID {
		return domain.E(domain.CodeForbidden, "content update actor differs")
	}
	return nil
}
func (p *PreparedUpdate) Receipt(ctx context.Context) (UpdateReceipt, error) {
	if err := p.check(ctx); err != nil {
		return UpdateReceipt{}, err
	}
	return cloneUpdateReceipt(p.receipt), nil
}

// WithStage lends only a verified original immutable stage for this job. The
// reader expires when fn returns and no private disk path is ever published.
func (p *PreparedUpdate) WithStage(ctx context.Context, scope *instance.TransactionScope, stageID string, fn func(io.Reader) error) error {
	if err := p.check(ctx); err != nil {
		return err
	}
	if fn == nil {
		return invalid("stage callback required")
	}
	allowed := false
	for _, op := range p.receipt.Operations {
		if stageID != "" && (op.BeforeStage == stageID || op.AfterStage == stageID) {
			allowed = true
		}
	}
	if !allowed {
		return domain.E(domain.CodeForbidden, "stage does not belong to prepared update")
	}
	return p.target.WithCurrentJob(ctx, scope, func(operation context.Context) error {
		_, file, err := p.service.openStage(operation, p.id, stageID)
		if err != nil {
			return err
		}
		defer file.Close()
		return fn(file)
	})
}

func targetCandidate(proof lifecycle.TargetProof, items []Installed) ([]Installed, []string, error) {
	raw, err := json.Marshal(items)
	if err != nil {
		return nil, nil, err
	}
	var candidate []Installed
	if err = json.Unmarshal(raw, &candidate); err != nil {
		return nil, nil, err
	}
	target := Tuple{proof.Selection.MinecraftVersion, string(proof.Selection.Flavor), proof.Selection.LoaderVersion}
	blockers := []string{}
	for i := range candidate {
		item := &candidate[i]
		item.LanguageEvidence = nil
		if item.Unknown || item.Descriptor.Unknown {
			blockers = append(blockers, "Unknown content blocks coordinated updates: "+item.ID)
		}
		if item.Descriptor.LanguageRequirement != "" && item.Descriptor.LanguageRequirement != "*" {
			evidence := proof.Language
			item.LanguageEvidence = &evidence
		}
	}
	blockers = append(blockers, ValidatePackCandidate(target, candidate)...)
	return candidate, blockers, nil
}

// PrepareUpdateCandidate stages publisher-selected replacements and validates
// the COMPLETE candidate against a fresh staged target proof, preserving the
// untouched observed old tuple/revision for later commit CAS.
func (s *Service) PrepareUpdateCandidate(ctx context.Context, id string, expected Manifest, target *lifecycle.PreparedUpdate, requested map[string]catalog.Selection, manual map[string]string, actor domain.User) (*PreparedUpdate, error) {
	p := &PreparedUpdate{service: s, target: target, id: id, actorID: actor.ID, requiresAdmin: len(manual) > 0}
	p.owner = p
	err := target.WithTargetProof(ctx, nil, func(operation context.Context, proof lifecycle.TargetProof) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		current, err := target.CheckCurrent(operation)
		if err != nil {
			return err
		}
		if current.InstanceID != id || current.RequesterID != actor.ID {
			return domain.E(domain.CodeForbidden, "content target job identity differs")
		}
		if _, err = s.actor(operation, actor.ID, p.requiresAdmin); err != nil {
			return err
		}
		manifest, err := s.Snapshot(operation, id)
		if err != nil {
			return err
		}
		if manifest.Revision != expected.Revision || manifest.Tuple != expected.Tuple {
			return conflict("observed content changed before update preparation")
		}
		receipt := UpdateReceipt{Observed: manifest, Target: Tuple{proof.Selection.MinecraftVersion, string(proof.Selection.Flavor), proof.Selection.LoaderVersion}, Candidate: append([]Installed{}, manifest.Items...), Operations: []UpdateOperation{}, Blockers: append([]string{}, manifest.AutomaticBlockers...), Manual: []ManualItem{}, ManualRequired: map[string]ManualItem{}, Metadata: map[string]string{}}
		root, err := s.openServer(operation, id)
		if err != nil {
			return err
		}
		defer root.Close()
		keys := make([]string, 0, len(requested))
		for key := range requested {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			index := -1
			for i := range receipt.Candidate {
				if receipt.Candidate[i].ID == key {
					index = i
					break
				}
			}
			if index < 0 {
				return domain.NotFound("installed content selection")
			}
			previous := receipt.Candidate[index]
			if previous.PackMember != nil {
				return conflict("pack member replacements require a target-aware pack preview")
			}
			selection := requested[key]
			candidatePreview, err := s.previewForTuple(operation, id, Mutation{Action: "update", ContentID: key, Kind: previous.Kind, Selection: &selection, UploadID: manual[key]}, actor, manifest, receipt.Target, false, true, func(items []Installed) ([]Installed, []string, error) { return targetCandidate(proof, items) })
			if err != nil {
				return err
			}
			receipt.Blockers = append(receipt.Blockers, candidatePreview.Preview.Blockers...)
			if candidatePreview.Preview.Manual != nil {
				receipt.Manual = append(receipt.Manual, *candidatePreview.Preview.Manual)
				receipt.ManualRequired[key] = *candidatePreview.Preview.Manual
			}
			receipt.Metadata[key] = candidatePreview.Metadata
			if candidatePreview.Next != nil {
				receipt.Candidate[index] = *candidatePreview.Next
			}
			for _, op := range candidatePreview.Operations {
				receipt.Operations = append(receipt.Operations, UpdateOperation{op.Path, op.Before, op.After, op.BeforeStage, op.AfterStage})
			}
		}
		// All retained items are revalidated too; pinned means retain exact bytes,
		// never waive their compatibility, unknown provenance or dependency gates.
		p.sourceBlockers = append([]string{}, receipt.Blockers...)
		receipt.Candidate, receipt.Blockers, err = func() ([]Installed, []string, error) {
			items, blockers, err := targetCandidate(proof, receipt.Candidate)
			return items, append(receipt.Blockers, blockers...), err
		}()
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, op := range receipt.Operations {
			if seen[op.Path] {
				return conflict("two update items claim the same file")
			}
			seen[op.Path] = true
		}
		p.receipt = cloneUpdateReceipt(receipt)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

type UpdateParticipant struct {
	owner    *UpdateParticipant
	active   atomic.Bool
	scope    *instance.TransactionScope
	prepared *PreparedUpdate
	pack     *PackParticipant
}

// WithUpdateParticipant holds content service admission while sharing one
// existing coordinator scope. Its callback begins after all WithUse calls end.
func (s *Service) WithUpdateParticipant(ctx context.Context, scope *instance.TransactionScope, prepared *PreparedUpdate, actor domain.User, fn func(*UpdateParticipant) error) error {
	if prepared == nil || prepared.service != s {
		return domain.E(domain.CodeForbidden, "prepared update belongs to another service")
	}
	if err := prepared.check(ctx); err != nil {
		return err
	}
	if fn == nil {
		return invalid("content update callback required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := scope.WithUse(ctx, func(operation context.Context, current instance.ScopeSnapshot) error {
		if current.Detail.Instance.ID != prepared.id || tuple(current.Detail.Instance) != prepared.receipt.Observed.Tuple {
			return conflict("old content tuple changed")
		}
		manifest, err := s.Snapshot(operation, prepared.id)
		if err != nil {
			return err
		}
		if manifest.Revision != prepared.receipt.Observed.Revision {
			return conflict("old content revision changed")
		}
		if _, err = s.actor(operation, actor.ID, prepared.requiresAdmin); err != nil {
			return err
		}
		if actor.ID != prepared.actorID {
			return domain.E(domain.CodeForbidden, "prepared content actor differs")
		}
		for _, item := range prepared.receipt.Candidate {
			expected := prepared.receipt.Metadata[item.ID]
			if expected == "" {
				continue
			}
			if item.Selection == nil || s.catalog == nil {
				return conflict("update publisher provenance missing")
			}
			v, err := s.catalog.Version(operation, item.Selection.Provider, item.Selection.ProjectID, item.Selection.VersionID)
			if err != nil {
				return err
			}
			if metadataDigest(v) != expected {
				return conflict("update publisher metadata changed")
			}
		}
		if len(prepared.receipt.Blockers) > 0 || len(prepared.receipt.Manual) > 0 {
			return conflict("complete target content remains blocked")
		}
		return nil
	})
	if err != nil {
		return err
	}
	inner := &PackParticipant{}
	state := &packScope{owner: inner, ctx: ctx, service: s, id: prepared.id, actor: actor, candidate: cloneUpdateReceipt(prepared.receipt).Candidate, expected: prepared.receipt.Observed}
	inner.scope = state
	state.active.Store(true)
	defer func() { state.active.Store(false); state.mu.Lock(); state.mu.Unlock() }()
	p := &UpdateParticipant{scope: scope, prepared: prepared, pack: inner}
	p.owner = p
	p.active.Store(true)
	defer p.active.Store(false)
	return fn(p)
}
func (p *UpdateParticipant) check(ctx context.Context) error {
	if p == nil || p.owner != p || !p.active.Load() {
		return domain.E(domain.CodeForbidden, "content participant copied or expired")
	}
	return p.prepared.check(ctx)
}
func (p *UpdateParticipant) ValidatePostimages(ctx context.Context) error {
	if err := p.check(ctx); err != nil {
		return err
	}
	return p.prepared.target.WithTargetProof(ctx, p.scope, func(operation context.Context, proof lifecycle.TargetProof) error {
		verified, blockers, err := targetCandidate(proof, p.prepared.receipt.Candidate)
		if err != nil {
			return err
		}
		if len(blockers) > 0 || !reflect.DeepEqual(verified, p.prepared.receipt.Candidate) {
			return conflict("target evidence or candidate compatibility changed")
		}
		return p.pack.ValidatePostimages(operation)
	})
}
func (p *UpdateParticipant) Commit(ctx context.Context, tx *sql.Tx) error {
	if err := p.check(ctx); err != nil {
		return err
	}
	return p.scope.WithCommitUse(ctx, tx, func(operation context.Context, _ instance.ScopeSnapshot) error {
		if p.prepared.requiresAdmin {
			var role domain.Role
			var disabled bool
			if err := tx.QueryRowContext(operation, "SELECT role,disabled FROM users WHERE id=?", p.prepared.actorID).Scan(&role, &disabled); err != nil || disabled || role != domain.RoleAdmin {
				return domain.E(domain.CodeForbidden, "manual content administrator authority revoked before commit")
			}
		}
		return p.pack.Commit(operation, tx)
	})
}

// PrepareTargetCandidate is a pure compatibility helper; its output conveys no
// mutation authority. Update participants require an original target capability.
func PrepareTargetCandidate(proof lifecycle.TargetProof, items []Installed) ([]Installed, []string, error) {
	return targetCandidate(proof, items)
}

// WithCandidate merges the complete trusted pack candidate into this job's
// content preparation. Postimage inspection still proves descriptors/hashes;
// retained unknown provenance cannot be cleared for identical bytes.
func (p *PreparedUpdate) WithCandidate(ctx context.Context, candidate []Installed) (*PreparedUpdate, error) {
	if err := p.check(ctx); err != nil {
		return nil, err
	}
	next := &PreparedUpdate{requiresAdmin: p.requiresAdmin, service: p.service, target: p.target, id: p.id, actorID: p.actorID, receipt: cloneUpdateReceipt(p.receipt), sourceBlockers: append([]string{}, p.sourceBlockers...)}
	next.owner = next
	err := p.target.WithTargetProof(ctx, nil, func(_ context.Context, proof lifecycle.TargetProof) error {
		for _, old := range p.receipt.Observed.Items {
			for _, item := range candidate {
				if old.ID == item.ID && old.SHA256 == item.SHA256 && old.Unknown && (!item.Unknown || !reflect.DeepEqual(old.TrialTuple, item.TrialTuple)) {
					return conflict("retained unknown provenance cannot be cleared")
				}
			}
		}
		items, blockers, err := targetCandidate(proof, candidate)
		if err != nil {
			return err
		}
		next.receipt.Candidate = items
		next.receipt.Blockers = append(append([]string{}, p.sourceBlockers...), blockers...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return next, nil
}

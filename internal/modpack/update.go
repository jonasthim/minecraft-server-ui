package modpack

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"reflect"
	"sync"
	"sync/atomic"

	"github.com/jonasthim/minecraft-server-ui/internal/content"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/lifecycle"
)

type UpdateOperation struct {
	Path   string `json:"path"`
	Before stage  `json:"before"`
	After  stage  `json:"after"`
}
type UpdateReceipt struct {
	Observed       content.Manifest    `json:"observed"`
	Target         content.Tuple       `json:"target"`
	Baseline       *Baseline           `json:"baseline,omitempty"`
	BeforeBaseline *Baseline           `json:"before_baseline,omitempty"`
	Candidate      []content.Installed `json:"candidate"`
	Operations     []UpdateOperation   `json:"operations"`
	Preview        *Preview            `json:"preview,omitempty"`
	Blockers       []string            `json:"blockers"`
}
type PreparedUpdate struct {
	owner   *PreparedUpdate
	service *Service
	target  *lifecycle.PreparedUpdate
	id      string
	actor   domain.User
	receipt UpdateReceipt
	request *Request
	private privatePreview
}

func cloneUpdateReceipt(receipt UpdateReceipt) UpdateReceipt {
	raw, _ := json.Marshal(receipt)
	var out UpdateReceipt
	_ = json.Unmarshal(raw, &out)
	return out
}
func (p *PreparedUpdate) check(ctx context.Context) error {
	if p == nil || p.owner != p || p.target == nil {
		return domain.E(domain.CodeForbidden, "pack update capability forged or copied")
	}
	current, err := p.target.CheckCurrent(ctx)
	if err != nil {
		return err
	}
	if current.InstanceID != p.id || current.RequesterID != p.actor.ID {
		return domain.E(domain.CodeForbidden, "pack update origin differs")
	}
	return nil
}
func (p *PreparedUpdate) Receipt(ctx context.Context) (UpdateReceipt, error) {
	if err := p.check(ctx); err != nil {
		return UpdateReceipt{}, err
	}
	return cloneUpdateReceipt(p.receipt), nil
}
func (p *PreparedUpdate) WithStage(ctx context.Context, scope *instance.TransactionScope, id string, fn func(io.Reader) error) error {
	if err := p.check(ctx); err != nil {
		return err
	}
	if fn == nil {
		return invalid("pack stage callback required")
	}
	var chosen stage
	for _, op := range p.receipt.Operations {
		if id != "" && op.Before.ID == id {
			chosen = op.Before
		}
		if id != "" && op.After.ID == id {
			chosen = op.After
		}
	}
	if chosen.ID == "" {
		return domain.E(domain.CodeForbidden, "stage outside pack update")
	}
	return p.target.WithCurrentJob(ctx, scope, func(operation context.Context) error {
		file, err := p.service.openStage(operation, p.id, chosen)
		if err != nil {
			return err
		}
		defer file.Close()
		return fn(file)
	})
}

// PrepareUpdatePreview uses the old manifest/baseline for three-way comparison
// and only the freshly staged target for compatibility. A nil request retains
// exact pack history; it cannot silently qualify a changed Minecraft/loader tuple.
func (s *Service) PrepareUpdatePreview(ctx context.Context, id string, request *Request, expected content.Manifest, target *lifecycle.PreparedUpdate, base []content.Installed, actor domain.User) (*PreparedUpdate, error) {
	p := &PreparedUpdate{service: s, target: target, id: id, actor: actor}
	p.owner = p
	err := target.WithTargetProof(ctx, nil, func(operation context.Context, proof lifecycle.TargetProof) error {
		needsAdmin := false
		if request != nil {
			var err error
			needsAdmin, err = s.RequestNeedsAdministrator(operation, id, *request, actor.ID)
			if err != nil {
				return err
			}
		}
		if _, err := s.actor(operation, actor.ID, needsAdmin); err != nil {
			return err
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if err := s.CheckReady(operation, id); err != nil {
			return err
		}
		manifest, err := s.content.Snapshot(operation, id)
		if err != nil {
			return err
		}
		if manifest.Revision != expected.Revision || manifest.Tuple != expected.Tuple {
			return conflict("observed pack content changed")
		}
		snapshot, err := s.Snapshot(operation, id)
		if err != nil {
			return err
		}
		tuple := content.Tuple{Minecraft: proof.Selection.MinecraftVersion, Loader: string(proof.Selection.Flavor), LoaderVersion: proof.Selection.LoaderVersion}
		receipt := UpdateReceipt{Observed: manifest, Target: tuple, BeforeBaseline: snapshot.Baseline, Baseline: snapshot.Baseline, Candidate: base, Operations: []UpdateOperation{}, Blockers: append([]string{}, snapshot.Blockers...)}
		if request == nil {
			if snapshot.Baseline != nil && tuple != manifest.Tuple {
				receipt.Blockers = append(receipt.Blockers, "Retained pack declares the installed exact tuple; select a verified pack for the target version")
			}
			items, blockers, err := content.PrepareTargetCandidate(proof, base)
			if err != nil {
				return err
			}
			receipt.Candidate = items
			receipt.Blockers = append(receipt.Blockers, blockers...)
			p.receipt = cloneUpdateReceipt(receipt)
			return nil
		}
		// Keep a defensive private request; caller map edits cannot alter receipts.
		raw, err := json.Marshal(request)
		if err != nil {
			return err
		}
		var frozen Request
		if err = json.Unmarshal(raw, &frozen); err != nil {
			return err
		}
		p.request = &frozen
		prepared, err := s.previewForTarget(operation, id, frozen, actor, manifest, base, tuple, false, func(items []content.Installed) ([]content.Installed, []string, error) {
			return content.PrepareTargetCandidate(proof, items)
		})
		if err != nil {
			return err
		}
		p.private = prepared
		receipt.Preview = &prepared.Preview
		receipt.Baseline = &prepared.Baseline
		receipt.Candidate = prepared.Candidate
		receipt.Blockers = append(receipt.Blockers, prepared.Preview.Blockers...)
		if prepared.Preview.ArchiveManual != nil || len(prepared.Preview.Manual) > 0 {
			receipt.Blockers = append(receipt.Blockers, "Required pack archive or member files remain unavailable")
		}
		for _, op := range prepared.Operations {
			receipt.Operations = append(receipt.Operations, UpdateOperation{op.Path, op.Before, op.After})
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
	owner     *UpdateParticipant
	active    atomic.Bool
	mu        sync.Mutex
	scope     *instance.TransactionScope
	prepared  *PreparedUpdate
	validated bool
}

// Factory lock order is original scope -> pack.mu -> content.mu. Its callback
// starts only after every initial WithUse call has returned.
func (s *Service) WithUpdateParticipant(ctx context.Context, scope *instance.TransactionScope, prepared *PreparedUpdate, actor domain.User, fn func(*UpdateParticipant) error) error {
	if prepared == nil || prepared.service != s {
		return domain.E(domain.CodeForbidden, "prepared update belongs to another service")
	}
	if err := prepared.check(ctx); err != nil {
		return err
	}
	if fn == nil {
		return invalid("pack participant callback required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := scope.WithUse(ctx, func(operation context.Context, snapshot instance.ScopeSnapshot) error {
		if snapshot.Detail.Instance.ID != prepared.id || snapshot.Detail.Instance.MinecraftVersion != prepared.receipt.Observed.Tuple.Minecraft {
			return conflict("observed pack instance changed")
		}
		if actor.ID != prepared.actor.ID {
			return domain.E(domain.CodeForbidden, "pack update actor differs")
		}
		needsAdmin := prepared.request != nil && (prepared.request.ArchiveReceiptID != "" || len(prepared.request.Manual) > 0)
		if _, err := s.actor(operation, actor.ID, needsAdmin); err != nil {
			return err
		}
		current, err := s.Snapshot(operation, prepared.id)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(current.Baseline, prepared.receipt.BeforeBaseline) {
			return conflict("pack baseline changed since update preview")
		}
		if len(prepared.receipt.Blockers) > 0 {
			return conflict("target pack remains blocked")
		}
		if prepared.request != nil {
			v, server, _, err := s.resolvePackMetadata(operation, prepared.request.Pack)
			if err != nil {
				return err
			}
			if packMetadataHash(v, server) != prepared.private.Metadata {
				return conflict("target pack publisher metadata changed")
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	p := &UpdateParticipant{scope: scope, prepared: prepared}
	p.owner = p
	p.active.Store(true)
	defer func() { p.active.Store(false); p.mu.Lock(); p.mu.Unlock() }()
	return fn(p)
}
func (p *UpdateParticipant) enter() (func(), error) {
	if p == nil || p.owner != p || !p.active.Load() {
		return nil, domain.E(domain.CodeForbidden, "pack participant copied or expired")
	}
	if !p.mu.TryLock() {
		return nil, domain.E(domain.CodeInstanceBusy, "pack participant overlaps")
	}
	if !p.active.Load() {
		p.mu.Unlock()
		return nil, domain.E(domain.CodeForbidden, "pack participant expired")
	}
	return p.mu.Unlock, nil
}
func (p *UpdateParticipant) ValidatePostimages(ctx context.Context) error {
	release, err := p.enter()
	if err != nil {
		return err
	}
	defer release()
	p.validated = false
	err = p.scope.WithUse(ctx, func(operation context.Context, _ instance.ScopeSnapshot) error {
		root, err := p.prepared.service.openServer(operation, p.prepared.id)
		if err != nil {
			return err
		}
		defer root.Close()
		for _, op := range p.prepared.receipt.Operations {
			hash, _, err := currentHash(operation, root, op.Path, MaxFileBytes)
			if err != nil {
				return err
			}
			if hash != op.After.SHA256 {
				return conflict("pack update postimage changed")
			}
		}
		return p.prepared.service.revalidateServer(operation, p.prepared.id, root)
	})
	if err == nil {
		p.validated = true
	}
	return err
}
func (p *UpdateParticipant) Commit(ctx context.Context, tx *sql.Tx) error {
	release, err := p.enter()
	if err != nil {
		return err
	}
	defer release()
	if !p.validated {
		return conflict("pack postimages not verified")
	}
	return p.scope.WithCommitUse(ctx, tx, func(operation context.Context, _ instance.ScopeSnapshot) error {
		needsAdmin := p.prepared.request != nil && (p.prepared.request.ArchiveReceiptID != "" || len(p.prepared.request.Manual) > 0)
		var role domain.Role
		var disabled bool
		if err := tx.QueryRowContext(operation, "SELECT role,disabled FROM users WHERE id=?", p.prepared.actor.ID).Scan(&role, &disabled); err != nil || disabled || !role.AtLeast(domain.RoleOperator) || needsAdmin && role != domain.RoleAdmin {
			return domain.E(domain.CodeForbidden, "pack update authority changed before commit")
		}
		var raw string
		err := tx.QueryRowContext(operation, "SELECT baseline_json FROM modpack_baselines WHERE instance_id=?", p.prepared.id).Scan(&raw)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		var previous *Baseline
		if err == nil {
			previous = &Baseline{}
			if err = json.Unmarshal([]byte(raw), previous); err != nil {
				return err
			}
		}
		if !reflect.DeepEqual(previous, p.prepared.receipt.BeforeBaseline) {
			return conflict("pack baseline changed before pointer commit")
		}
		if p.prepared.receipt.Baseline == nil {
			return nil
		}
		rawBytes, err := json.Marshal(p.prepared.receipt.Baseline)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(operation, "INSERT INTO modpack_baselines(instance_id,baseline_json) VALUES(?,?) ON CONFLICT(instance_id) DO UPDATE SET baseline_json=excluded.baseline_json", p.prepared.id, string(rawBytes))
		return err
	})
}

// PersistedPreview is inert immutable evidence for a later separately authorized
// member stage. It never transfers the originating job's live capabilities.
func (p *PreparedUpdate) PersistedPreview(ctx context.Context) (json.RawMessage, error) {
	if err := p.check(ctx); err != nil {
		return nil, err
	}
	if p.request == nil {
		return nil, nil
	}
	return json.Marshal(p.private)
}

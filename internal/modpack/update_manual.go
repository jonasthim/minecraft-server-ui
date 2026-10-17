package modpack

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"reflect"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/catalog"
	"github.com/jonasthim/minecraft-server-ui/internal/content"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

type UpdateMemberReceipt struct {
	Path      string `json:"path"`
	ReceiptID string `json:"receipt_id"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
}

// StageUpdateMember loads the durable original-service receipt itself. A caller
// cannot supply expected hashes, another actor's stage, or current-tuple proof.
func (s *Service) StageUpdateMember(ctx context.Context, id, preview, member string, reader io.Reader, actor domain.User) (UpdateMemberReceipt, error) {
	if !validHex(preview, 32) || !allowedPath(member) {
		return UpdateMemberReceipt{}, invalid("exact unresolved pack member required")
	}
	if _, err := s.actor(ctx, actor.ID, true); err != nil {
		return UpdateMemberReceipt{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.CheckReady(ctx, id); err != nil {
		return UpdateMemberReceipt{}, err
	}
	var raw string
	var owner int64
	if err := s.db.QueryRowContext(ctx, "SELECT private_json,actor_id FROM update_previews WHERE id=? AND instance_id=? AND expires_at>?", preview, id, time.Now().UTC().Format(time.RFC3339Nano)).Scan(&raw, &owner); err != nil {
		return UpdateMemberReceipt{}, err
	}
	if owner != actor.ID || len(raw) > 16<<20 {
		return UpdateMemberReceipt{}, domain.E(domain.CodeForbidden, "owned update preview required")
	}
	var envelope struct {
		Version     int             `json:"version"`
		ActorID     int64           `json:"actor_id"`
		PackStaging json.RawMessage `json:"pack_staging"`
		Plan        struct {
			ID         string                  `json:"id"`
			InstanceID string                  `json:"instance_id"`
			Target     domain.VersionSelection `json:"target"`
			Snapshot   struct {
				Revision   string `json:"instance_revision"`
				Generation string `json:"generation"`
				Content    string `json:"content_revision"`
				Pack       string `json:"pack_revision"`
			} `json:"snapshot"`
		} `json:"plan"`
	}
	var p privatePreview
	if json.Unmarshal([]byte(raw), &envelope) != nil || envelope.Version != 1 || envelope.ActorID != owner || envelope.Plan.ID != preview || envelope.Plan.InstanceID != id || json.Unmarshal(envelope.PackStaging, &p) != nil || p.ActorID != owner || p.Preview.InstanceID != id {
		return UpdateMemberReceipt{}, conflict("target pack receipt is malformed")
	}
	target := envelope.Plan.Target
	if p.Preview.Tuple != (content.Tuple{Minecraft: target.MinecraftVersion, Loader: string(target.Flavor), LoaderVersion: target.LoaderVersion}) {
		return UpdateMemberReceipt{}, conflict("target pack receipt tuple differs")
	}
	manifest, err := s.content.Snapshot(ctx, id)
	if err != nil {
		return UpdateMemberReceipt{}, err
	}
	if manifest.Revision != p.Manifest.Revision || manifest.Revision != envelope.Plan.Snapshot.Content || manifest.Tuple != p.Manifest.Tuple {
		return UpdateMemberReceipt{}, conflict("observed old content changed")
	}
	baseline, err := s.Snapshot(ctx, id)
	if err != nil {
		return UpdateMemberReceipt{}, err
	}
	revision := "none"
	if baseline.Baseline != nil {
		revision = baseline.Baseline.Revision
	}
	if revision != envelope.Plan.Snapshot.Pack {
		return UpdateMemberReceipt{}, conflict("observed old pack changed")
	}
	pending := false
	for _, item := range p.Preview.Manual {
		if item.Path == member {
			pending = true
		}
	}
	file, found := p.Members[member]
	if !found || !pending || file.ArchiveEntry != "" {
		return UpdateMemberReceipt{}, invalid("pack member is not unresolved in this target preview")
	}
	original, server, actual, err := s.resolvePackMetadata(ctx, p.Request.Pack)
	if err != nil {
		return UpdateMemberReceipt{}, err
	}
	if p.Metadata != packMetadataHash(original, server) {
		return UpdateMemberReceipt{}, conflict("publisher pack metadata changed; prepare again")
	}
	var artifact catalog.VerifiedArtifact
	if p.Request.ArchiveReceiptID != "" {
		artifact, err = s.archiveArtifact(ctx, id, p.Request.ArchiveReceiptID, actor.ID, p.Request.Pack, actual, original, server)
	} else {
		artifact, err = s.catalog.Download(ctx, actual)
	}
	if err != nil {
		return UpdateMemberReceipt{}, err
	}
	if artifact.SHA256 != p.Preview.Source.SHA256 || artifact.Selection != p.Preview.Source.ArtifactSelection {
		return UpdateMemberReceipt{}, conflict("target archive changed; prepare again")
	}
	archive, err := s.openPackArtifact(ctx, id, artifact, p.Request.ArchiveReceiptID)
	if err != nil {
		return UpdateMemberReceipt{}, err
	}
	defer archive.Close()
	info, err := archive.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != artifact.File.Size {
		return UpdateMemberReceipt{}, invalid("target archive differs")
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, contextReader{ctx, archive}); err != nil {
		return UpdateMemberReceipt{}, err
	}
	if hex.EncodeToString(hash.Sum(nil)) != artifact.SHA256 {
		return UpdateMemberReceipt{}, conflict("target archive checksum changed")
	}
	format := "modrinth"
	if p.Request.Pack.Provider == catalog.CurseForge {
		format = "curseforge-server"
	}
	parsed, err := ParseArchive(ctx, archive, info.Size(), format)
	if err != nil {
		return UpdateMemberReceipt{}, err
	}
	fresh, exists := parsed.Files[member]
	if !exists || !reflect.DeepEqual(fresh, file) {
		return UpdateMemberReceipt{}, conflict("target member changed in publisher archive")
	}
	check := func(ctx context.Context, tx *sql.Tx, published stage) error {
		var role domain.Role
		var disabled bool
		if err := tx.QueryRowContext(ctx, "SELECT role,disabled FROM users WHERE id=?", actor.ID).Scan(&role, &disabled); err != nil || disabled || role != domain.RoleAdmin {
			return domain.E(domain.CodeForbidden, "administrator authority revoked before stage publication")
		}
		var current, instanceRevision, generation string
		if err := tx.QueryRowContext(ctx, "SELECT private_json FROM update_previews WHERE id=? AND instance_id=? AND actor_id=? AND expires_at>?", preview, id, actor.ID, time.Now().UTC().Format(time.RFC3339Nano)).Scan(&current); err != nil {
			return err
		}
		if current != raw {
			return conflict("update preview changed during upload")
		}
		if err := tx.QueryRowContext(ctx, "SELECT i.updated_at,l.generation FROM instances i JOIN lifecycle_installations l ON l.instance_id=i.id WHERE i.id=?", id).Scan(&instanceRevision, &generation); err != nil {
			return err
		}
		if instanceRevision != envelope.Plan.Snapshot.Revision || generation != envelope.Plan.Snapshot.Generation {
			return conflict("old selection changed during member upload")
		}
		var tuple content.Tuple
		if err := tx.QueryRowContext(ctx, "SELECT minecraft_version,flavor,loader_version FROM instances WHERE id=?", id).Scan(&tuple.Minecraft, &tuple.Loader, &tuple.LoaderVersion); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT manifest_json FROM content_items WHERE instance_id=? ORDER BY id", id)
		if err != nil {
			return err
		}
		items := []content.Installed{}
		for rows.Next() {
			var item content.Installed
			var encoded string
			if err = rows.Scan(&encoded); err != nil {
				break
			}
			if err = json.Unmarshal([]byte(encoded), &item); err != nil {
				break
			}
			items = append(items, item)
			if len(items) > 4096 {
				err = invalid("current manifest exceeds stage verification bound")
				break
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(struct {
			Tuple content.Tuple
			Items []content.Installed
		}{tuple, items})
		if err != nil {
			return err
		}
		if digest(encoded) != envelope.Plan.Snapshot.Content {
			return conflict("old content changed during member upload")
		}
		var baselineRaw string
		currentPack := "none"
		err = tx.QueryRowContext(ctx, "SELECT baseline_json FROM modpack_baselines WHERE instance_id=?", id).Scan(&baselineRaw)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == nil {
			var baseline Baseline
			if json.Unmarshal([]byte(baselineRaw), &baseline) != nil {
				return conflict("old baseline became malformed")
			}
			currentPack = baseline.Revision
		}
		if currentPack != envelope.Plan.Snapshot.Pack {
			return conflict("old pack changed during member upload")
		}
		var active bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM update_transactions WHERE instance_id=? AND state NOT IN ('complete','rolled_back','superseded')) OR EXISTS(SELECT 1 FROM modpack_journals WHERE instance_id=?) OR EXISTS(SELECT 1 FROM content_journals WHERE instance_id=?) OR EXISTS(SELECT 1 FROM backup_restores WHERE instance_id=? AND state!='complete')`, id, id, id, id).Scan(&active); err != nil {
			return err
		}
		if active {
			return conflict("another transaction began during member upload")
		}
		// Acquisition metadata is appended atomically, while confirmed Plan,
		// Request and original pack proof remain untouched.
		var document map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &document); err != nil {
			return err
		}
		receipts := map[string]string{}
		if encoded := document["pack_member_receipts"]; len(encoded) > 0 {
			if err := json.Unmarshal(encoded, &receipts); err != nil {
				return conflict("member acquisition references malformed")
			}
		}
		receipts[member] = published.ID
		if len(receipts) > 8192 {
			return invalid("member receipt bound exceeded")
		}
		references, err := json.Marshal(receipts)
		if err != nil {
			return err
		}
		document["pack_member_receipts"] = references
		next, err := json.Marshal(document)
		if err != nil {
			return err
		}
		if len(next) > 16<<20 {
			return invalid("private update envelope exceeds 16 MiB")
		}
		result, err := tx.ExecContext(ctx, "UPDATE update_previews SET private_json=? WHERE id=? AND instance_id=? AND actor_id=? AND private_json=?", string(next), preview, id, actor.ID, raw)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return conflict("member acquisition preview changed")
		}
		return nil
	}
	staged, err := s.stageChecked(ctx, id, actor.ID, reader, file.Size, check)
	if err != nil {
		return UpdateMemberReceipt{}, err
	}
	opened, err := s.openStage(ctx, id, staged)
	if err != nil {
		return UpdateMemberReceipt{}, err
	}
	defer opened.Close()
	if err = verifyPackMember(ctx, opened, staged.Size, file); err != nil {
		return UpdateMemberReceipt{}, err
	}
	if _, err = s.actor(ctx, actor.ID, true); err != nil {
		return UpdateMemberReceipt{}, err
	}
	return UpdateMemberReceipt{Path: member, ReceiptID: staged.ID, SHA256: staged.SHA256, Size: staged.Size}, nil
}

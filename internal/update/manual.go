package update

import (
	"context"
	"io"
	"reflect"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/modpack"
)

// StagePackMember returns inert exact publisher bytes, never a live capability.
// Applying them always requires another original typed staging job and preview.
func (s *Service) StagePackMember(ctx context.Context, id, preview, member string, r io.Reader, actor domain.User) (modpack.UpdateMemberReceipt, error) {
	fresh, err := s.actor(ctx, actor.ID, true)
	if err != nil {
		return modpack.UpdateMemberReceipt{}, err
	}
	envelope, err := s.loadPreview(ctx, id, preview, fresh.ID)
	if err != nil {
		return modpack.UpdateMemberReceipt{}, err
	}
	if len(envelope.PackStaging) == 0 || envelope.Plan.Pack == nil {
		return modpack.UpdateMemberReceipt{}, invalid("target preview has no pending pack receipt")
	}
	if err = s.checkAdmissionReady(ctx, id); err != nil {
		return modpack.UpdateMemberReceipt{}, err
	}
	snapshot, err := s.currentSnapshot(ctx, id)
	if err != nil {
		return modpack.UpdateMemberReceipt{}, err
	}
	if snapshot != envelope.Plan.Snapshot {
		return modpack.UpdateMemberReceipt{}, conflict("old instance or pack revision changed")
	}
	target, err := s.config.Lifecycle.ResolveUpdate(ctx, id, envelope.Plan.Target)
	if err != nil {
		return modpack.UpdateMemberReceipt{}, err
	}
	if !reflect.DeepEqual(target.Requirements, envelope.Plan.Requirements) || len(target.Artifacts) != len(envelope.Plan.Artifacts) {
		return modpack.UpdateMemberReceipt{}, conflict("target provider requirements changed")
	}
	for i, artifact := range target.Artifacts {
		expected := envelope.Plan.Artifacts[i]
		if artifact.Filename != expected.Filename || artifact.Size != expected.Size || artifact.Checksum != expected.Checksum {
			return modpack.UpdateMemberReceipt{}, conflict("target provider artifact changed")
		}
	}
	receipt, err := s.config.Packs.StageUpdateMember(ctx, id, preview, member, r, fresh)
	if err != nil {
		return receipt, err
	}
	if err = s.audit(ctx, id, fresh, "pack_member_stage", "verified"); err != nil {
		return modpack.UpdateMemberReceipt{}, err
	}
	return receipt, nil
}

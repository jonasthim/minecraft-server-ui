package modpack

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/catalog"
	"github.com/jonasthim/minecraft-server-ui/internal/content"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"io"
	"os"
	"time"
)

type archiveBinding struct {
	Original catalog.Selection `json:"original"`
	Actual   catalog.Selection `json:"actual"`
	File     catalog.File      `json:"file"`
	Metadata string            `json:"metadata"`
}

func exactArchiveFile(v catalog.Version, selection catalog.Selection) (catalog.File, error) {
	for _, f := range v.Files {
		if f.ID == selection.FileID {
			if f.Size <= 0 || f.Size > MaxArchiveBytes {
				return f, invalid("archive size outside bound")
			}
			return f, nil
		}
	}
	return catalog.File{}, invalid("exact official archive file unavailable")
}

// StageArchive is an exact publisher-artifact acquisition path. It is not a ZIP
// import API: official original/server selections, size and hashes own the bytes.
func (s *Service) StageArchive(ctx context.Context, id string, original catalog.Selection, r io.Reader, actor domain.User) (out ArchiveReceipt, result error) {
	current, e := s.actor(ctx, actor.ID, true)
	if e != nil {
		return out, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		outcome := "verified"
		if result != nil {
			outcome = "rejected"
		}
		result = errors.Join(result, s.audit(cleanup, id, current, "archive_upload", outcome))
	}()
	if e = s.CheckReady(ctx, id); e != nil {
		return out, e
	}
	v, server, actual, e := s.resolvePackMetadata(ctx, original)
	if e != nil {
		return out, e
	}
	file, e := exactArchiveFile(server, actual)
	if e != nil {
		return out, e
	}
	if _, e = publishedHashes(file.Hashes); e != nil {
		return out, e
	}
	st, e := s.stage(ctx, id, actor.ID, r, file.Size)
	if e != nil {
		return out, e
	}
	f, e := s.openStage(ctx, id, st)
	if e != nil {
		return out, e
	}
	e = verifyMember(ctx, f, st.Size, PackFile{Size: file.Size, Hashes: file.Hashes})
	f.Close()
	if e != nil {
		return out, e
	}
	binding := archiveBinding{Original: original, Actual: actual, File: file, Metadata: packMetadataHash(v, server)}
	raw, _ := json.Marshal(binding)
	_, e = s.db.ExecContext(ctx, "INSERT INTO modpack_archives(id,instance_id,actor_id,binding_json,created_at) VALUES(?,?,?,?,?)", st.ID, id, actor.ID, string(raw), time.Now().UTC().Format(time.RFC3339Nano))
	if e != nil {
		return out, e
	}
	return ArchiveReceipt{ID: st.ID, SHA256: st.SHA256, Size: st.Size}, nil
}
func (s *Service) archiveArtifact(ctx context.Context, id, receipt string, actor int64, original, actual catalog.Selection, v, server catalog.Version) (catalog.VerifiedArtifact, error) {
	var out catalog.VerifiedArtifact
	var raw, created string
	var owner int64
	e := s.db.QueryRowContext(ctx, "SELECT actor_id,binding_json,created_at FROM modpack_archives WHERE instance_id=? AND id=?", id, receipt).Scan(&owner, &raw, &created)
	if e != nil {
		return out, domain.E(domain.CodeForbidden, "archive receipt unavailable")
	}
	if owner != actor || created < time.Now().UTC().Add(-24*time.Hour).Format(time.RFC3339Nano) {
		return out, domain.E(domain.CodeForbidden, "archive receipt foreign or expired")
	}
	var binding archiveBinding
	file, e := exactArchiveFile(server, actual)
	if e != nil {
		return out, e
	}
	if json.Unmarshal([]byte(raw), &binding) != nil || binding.Original != original || binding.Actual != actual || binding.Metadata != packMetadataHash(v, server) || metadataHash(binding.File) != metadataHash(file) {
		return out, conflict("official archive selection or metadata changed")
	}
	var st stage
	if e = s.db.QueryRowContext(ctx, "SELECT id,sha256,size FROM modpack_stages WHERE id=? AND instance_id=? AND actor_id=?", receipt, id, actor).Scan(&st.ID, &st.SHA256, &st.Size); e != nil {
		return out, e
	}
	f, e := s.openStage(ctx, id, st)
	if e != nil {
		return out, e
	}
	e = verifyMember(ctx, f, st.Size, PackFile{Size: file.Size, Hashes: file.Hashes})
	f.Close()
	if e != nil {
		return out, e
	}
	return catalog.VerifiedArtifact{Selection: actual, Provenance: server.Provenance, File: file, SHA256: st.SHA256}, nil
}
func (s *Service) openPackArtifact(ctx context.Context, id string, artifact catalog.VerifiedArtifact, receipt string) (*os.File, error) {
	if receipt != "" {
		return s.openStage(ctx, id, stage{ID: receipt, SHA256: artifact.SHA256, Size: artifact.File.Size})
	}
	return os.Open(artifact.Path)
}
func (s *Service) archivePreview(ctx context.Context, id string, request Request, actor domain.User, manifest content.Manifest, baseline Baseline, v, server catalog.Version, actual catalog.Selection) (Preview, error) {
	if request.PreviousPreviewID != "" {
		parent, e := s.loadPreview(ctx, id, request.PreviousPreviewID, actor.ID)
		if e != nil {
			return Preview{}, e
		}
		if parent.Request.Pack != request.Pack || parent.Preview.Source.ArtifactSelection != actual || parent.Metadata != packMetadataHash(v, server) {
			return Preview{}, conflict("parent archive metadata differs")
		}
		if job, e := s.liveJob(ctx, id, parent.Preview.ID); e != nil || job != "" {
			return Preview{}, conflict("parent preview owned by live job")
		}
		if request.Manual == nil {
			request.Manual = map[string]string{}
		}
		for name, receipt := range parent.Request.Manual {
			if request.Manual[name] == "" {
				request.Manual[name] = receipt
			}
		}
	}
	file, e := exactArchiveFile(server, actual)
	if e != nil {
		return Preview{}, e
	}
	p := privatePreview{Preview: Preview{ID: newID(), InstanceID: id, Tuple: manifest.Tuple, Revision: manifest.Revision, BaselineRevision: baseline.Revision, Source: Source{Selection: request.Pack, ArtifactSelection: actual, Name: v.Name, OfficialURL: v.OfficialURL, Provenance: v.Provenance}, Changes: []Change{}, Selections: map[string]Selection{}, Manual: []ManualFile{}, Blockers: []string{"Exact pack archive is unavailable; metadata only, no file plan has been parsed"}, Excluded: []string{}, Summary: "Pack archive acquisition required; file compatibility and baseline have not been evaluated", ArchiveManual: &ArchiveManual{Selection: actual, Filename: file.Filename, Size: file.Size, Hashes: file.Hashes, OfficialURL: server.OfficialURL, Reason: "Supply this exact official pack archive; bytes must match publisher size and supported digests"}}, Request: request, ActorID: actor.ID, Metadata: packMetadataHash(v, server), Manifest: manifest}
	return s.storePreview(ctx, p)
}
func (s *Service) storePreview(ctx context.Context, p privatePreview) (Preview, error) {
	raw, e := json.Marshal(p)
	if e != nil {
		return p.Preview, e
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return p.Preview, e
	}
	defer tx.Rollback()
	_, e = tx.ExecContext(ctx, "INSERT INTO modpack_previews(id,instance_id,actor_id,private_json,state,created_at) VALUES(?,?,?,?,'preview',?)", p.Preview.ID, p.Preview.InstanceID, p.ActorID, string(raw), time.Now().UTC().Format(time.RFC3339Nano))
	if e == nil && p.Request.PreviousPreviewID != "" {
		_, e = tx.ExecContext(ctx, "UPDATE modpack_previews SET state='superseded' WHERE id=? AND instance_id=? AND actor_id=? AND state IN ('preview','queued')", p.Request.PreviousPreviewID, p.Preview.InstanceID, p.ActorID)
	}
	if e != nil {
		return p.Preview, e
	}
	return p.Preview, tx.Commit()
}

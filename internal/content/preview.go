package content

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"github.com/jonasthim/minecraft-server-ui/internal/catalog"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"io"
	"os"
	"path"
	"strings"
	"time"
)

func contains(values []string, s string) bool {
	for _, v := range values {
		if s == v {
			return true
		}
	}
	return false
}
func needsAdmin(m Mutation) bool { return m.UploadID != "" || m.Trial }
func (s *Service) Preview(ctx context.Context, id string, m Mutation, actor domain.User) (Preview, error) {
	if _, e := s.actor(ctx, actor.ID, needsAdmin(m)); e != nil {
		return Preview{}, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.expire(ctx, id); e != nil {
		return Preview{}, e
	}
	manifest, e := s.Snapshot(ctx, id)
	if e != nil {
		return Preview{}, e
	}
	if m.Action == "install" || m.Action == "update" || m.Action == "enable" {
		detail, e := s.instances.Get(ctx, id)
		if e != nil {
			return Preview{}, e
		}
		if detail.Provisioning != "installed" {
			return Preview{}, conflict("install the selected server and loader before activating content")
		}
	}
	var count int
	if e = s.db.QueryRowContext(ctx, "SELECT count(*) FROM content_previews WHERE instance_id=?", id).Scan(&count); e != nil {
		return Preview{}, e
	}
	if count >= 32 {
		return Preview{}, conflict("preview limit reached; discard unused previews")
	}
	p := privatePreview{Preview: Preview{ID: newID(), InstanceID: id, Tuple: manifest.Tuple, Revision: manifest.Revision, Action: m.Action, Changes: []OwnedFile{}, Blockers: []string{}, Dependencies: []catalog.Dependency{}}, Mutation: m, ActorID: actor.ID, Operations: []fileOperation{}}
	root, e := s.openServer(ctx, id)
	if e != nil {
		return p.Preview, e
	}
	defer root.Close()
	var previous *Installed
	for i := range manifest.Items {
		if manifest.Items[i].ID == m.ContentID {
			v := manifest.Items[i]
			previous = &v
		}
	}
	switch m.Action {
	case "install", "update":
		if m.Action == "update" && previous == nil {
			return p.Preview, domain.NotFound("installed content")
		}
		if m.Action == "install" && len(manifest.Items) >= 2048 {
			return p.Preview, conflict("installed content limit reached")
		}
		if m.Kind != catalog.Mod && m.Kind != catalog.Plugin {
			return p.Preview, invalid("choose mod or plugin")
		}
		if previous != nil && previous.Kind != m.Kind {
			return p.Preview, invalid("updates must retain content kind")
		}
		if m.Kind == catalog.Plugin && manifest.Tuple.Loader != "paper" || m.Kind == catalog.Mod && !contains([]string{"fabric", "forge", "neoforge"}, manifest.Tuple.Loader) {
			return p.Preview, invalid("content kind is incompatible with the installed server type")
		}
		var upload Upload
		var descriptor Descriptor
		if m.Selection != nil {
			if s.catalog == nil {
				return p.Preview, domain.E(domain.CodeUnavailable, "catalog unavailable")
			}
			project, err := s.catalog.Project(ctx, m.Selection.Provider, m.Selection.ProjectID)
			if err != nil {
				return p.Preview, domain.E(domain.CodeUpstreamError, "publisher project lookup failed")
			}
			if project.Kind != m.Kind {
				return p.Preview, invalid("publisher project kind differs from selection")
			}
			if previous != nil && previous.Selection != nil && (previous.Selection.Provider != m.Selection.Provider || previous.Selection.ProjectID != m.Selection.ProjectID) {
				return p.Preview, invalid("update cannot replace the recorded publisher project")
			}
			var v catalog.Version
			var f catalog.File
			if m.UploadID == "" {
				v, f, upload, e = s.catalogStage(ctx, id, actor.ID, *m.Selection)
			} else {
				v, e = s.catalog.Version(ctx, m.Selection.Provider, m.Selection.ProjectID, m.Selection.VersionID)
				for _, file := range v.Files {
					if file.ID == m.Selection.FileID {
						f = file
					}
				}
				if f.ID == "" {
					return p.Preview, domain.NotFound("publisher file")
				}
				upload, _, e = s.uploadMetadata(ctx, id, m.UploadID, actor.ID)
				if e == nil {
					e = s.verifyPublished(ctx, id, upload.ID, f.Hashes)
				}
			}
			if e != nil {
				return p.Preview, e
			}
			p.Metadata = metadataDigest(v)
			p.Preview.Dependencies = v.Dependencies
			if p.Preview.Dependencies == nil {
				p.Preview.Dependencies = []catalog.Dependency{}
			}
			if !contains(v.Compatibility.Minecraft, manifest.Tuple.Minecraft) || !contains(v.Compatibility.Loaders, manifest.Tuple.Loader) || v.Compatibility.ServerSide == "unsupported" || v.Compatibility.Environment == "client" || v.Compatibility.Environment == "client_only" {
				p.Preview.Blockers = append(p.Preview.Blockers, "Publisher compatibility does not establish this exact server tuple.")
			}
			if upload.ID == "" {
				p.Preview.Manual = &ManualItem{*m.Selection, f.Filename, f.OfficialURL, f.Hashes, f.ManualReason}
				p.Preview.Blockers = append(p.Preview.Blockers, "Publisher file requires administrator verified upload from its official page.")
				break
			}
			if upload.ID != "" && f.Size > 0 && upload.Size != f.Size {
				return p.Preview, conflict("manual file size differs from publisher metadata")
			}
			p.Filename = f.Filename
		} else {
			if m.UploadID == "" {
				return p.Preview, invalid("choose publisher selection or administrator upload")
			}
			upload, _, e = s.uploadMetadata(ctx, id, m.UploadID, actor.ID)
			if e != nil {
				return p.Preview, e
			}
			p.Filename = upload.Filename
			p.Preview.Unknown = true
			if !m.Trial {
				p.Preview.Blockers = append(p.Preview.Blockers, "Unknown upload requires an explicit administrator trial against this exact tuple.")
			}
		}
		_, f, e := s.openStage(ctx, id, upload.ID)
		if e != nil {
			return p.Preview, e
		}
		descriptor, e = inspectArchive(f, upload.Size)
		f.Close()
		if e != nil {
			return p.Preview, e
		}
		if descriptor.ClientOnly {
			return p.Preview, invalid("client-only content is refused")
		}
		if descriptor.Loader != "" && descriptor.Loader != manifest.Tuple.Loader {
			return p.Preview, invalid("descriptor loader mismatch")
		}
		if descriptor.Unknown && !p.Preview.Unknown {
			p.Preview.Blockers = append(p.Preview.Blockers, "Publisher artifact lacks a supported descriptor; use explicit unknown upload trial.")
		}
		if !portable(p.Filename) || !strings.HasSuffix(strings.ToLower(p.Filename), ".jar") {
			return p.Preview, invalid("publisher filename is not a portable JAR filename")
		}
		p.StageID = upload.ID
		next := Installed{ID: newID(), Kind: m.Kind, Name: p.Filename, Version: descriptor.Version, Selection: m.Selection, SHA256: upload.SHA256, Enabled: true, Pinned: true, Unknown: p.Preview.Unknown, Descriptor: descriptor, Dependencies: p.Preview.Dependencies}
		if next.Unknown {
			t := manifest.Tuple
			next.TrialTuple = &t
		}
		if previous != nil {
			next.ID = previous.ID
			next.Enabled = previous.Enabled
			next.Pinned = previous.Pinned
			if previous.Descriptor.ID != "" && descriptor.ID != previous.Descriptor.ID {
				return p.Preview, invalid("update descriptor identity differs from installed content")
			}
		}
		destination := "mods/" + p.Filename
		if m.Kind == catalog.Plugin {
			destination = "plugins/" + p.Filename
		}
		if !next.Enabled {
			destination += ".disabled"
		}
		next.Files = []OwnedFile{{destination, upload.SHA256}}
		p.Next = &next
		if previous != nil {
			for _, owned := range previous.Files {
				if e = s.addOperation(ctx, root, id, actor.ID, &p, owned.Path, owned.SHA256, "", ""); e != nil {
					return p.Preview, e
				}
			}
		}
		if e = s.checkCollision(ctx, root, destination, previous); e != nil {
			return p.Preview, e
		}
		if e = s.addOperation(ctx, root, id, actor.ID, &p, destination, "", upload.SHA256, upload.ID); e != nil {
			return p.Preview, e
		}
	case "disable", "enable", "uninstall":
		if previous == nil {
			return p.Preview, domain.NotFound("installed content")
		}
		if m.Action != "enable" {
			for _, item := range manifest.Items {
				if !item.Enabled || item.ID == previous.ID {
					continue
				}
				if _, ok := item.Descriptor.Required[previous.Descriptor.ID]; ok {
					p.Preview.Blockers = append(p.Preview.Blockers, "Enabled content depends on this item: "+item.ID)
				}
				for _, d := range item.Dependencies {
					if d.Required && previous.Selection != nil && dependencyMatches(d, *previous.Selection) {
						p.Preview.Blockers = append(p.Preview.Blockers, "Publisher dependency prevents removal: "+item.ID)
					}
				}
			}
		}
		next := *previous
		if m.Action == "enable" {
			next.Enabled = true
			p.Preview.Dependencies = next.Dependencies
			if p.Preview.Dependencies == nil {
				p.Preview.Dependencies = []catalog.Dependency{}
			}
		}
		if m.Action == "disable" {
			next.Enabled = false
		}
		next.Files = []OwnedFile{}
		for _, owned := range previous.Files {
			if e = s.addOperation(ctx, root, id, actor.ID, &p, owned.Path, owned.SHA256, "", ""); e != nil {
				return p.Preview, e
			}
			if m.Action == "uninstall" {
				continue
			}
			destination := strings.TrimSuffix(owned.Path, ".disabled")
			if !next.Enabled {
				destination += ".disabled"
			}
			if destination == owned.Path {
				p.Operations = p.Operations[:len(p.Operations)-1]
				next.Files = append(next.Files, owned)
				continue
			}
			if e = s.checkCollision(ctx, root, destination, previous); e != nil {
				return p.Preview, e
			}
			stage := p.Operations[len(p.Operations)-1].BeforeStage
			if e = s.addOperation(ctx, root, id, actor.ID, &p, destination, "", owned.SHA256, stage); e != nil {
				return p.Preview, e
			}
			next.Files = append(next.Files, OwnedFile{destination, owned.SHA256})
		}
		if m.Action != "uninstall" {
			p.Next = &next
		}
	case "config_write":
		if !configPath(m.ConfigPath) || !validText([]byte(m.Text)) || len(m.ExpectedSHA256) != 64 {
			return p.Preview, invalid("config edit requires an allowlisted UTF-8 file and expected SHA-256")
		}
		existing, e := readFile(ctx, root, m.ConfigPath, MaxConfigBytes)
		if e != nil {
			return p.Preview, e
		}
		if !validText(existing) {
			return p.Preview, invalid("existing configuration is not bounded UTF-8 text")
		}
		current, _, e := currentHash(ctx, root, m.ConfigPath, MaxConfigBytes)
		if e != nil {
			return p.Preview, e
		}
		if current != m.ExpectedSHA256 {
			return p.Preview, conflict("configuration changed; reload before editing")
		}
		u, e := s.stage(ctx, id, actor.ID, path.Base(m.ConfigPath), strings.NewReader(m.Text), int64(len(m.Text)))
		if e != nil {
			return p.Preview, e
		}
		if e = s.addOperation(ctx, root, id, actor.ID, &p, m.ConfigPath, m.ExpectedSHA256, u.SHA256, u.ID); e != nil {
			return p.Preview, e
		}
		p.Mutation.Text = ""
	default:
		return p.Preview, invalid("unknown content action")
	}
	if p.Next != nil {
		next, _, err := s.PreparePackCandidate(ctx, id, manifest.Tuple, []Installed{*p.Next})
		if err != nil {
			return p.Preview, err
		}
		p.Next = &next[0]
	}
	p.Preview.Blockers = append(p.Preview.Blockers, proposedEnabledBlockers(manifest.Tuple, manifest.Items, p)...)
	for _, operation := range p.Operations {
		p.Preview.Changes = append(p.Preview.Changes, OwnedFile{operation.Path, operation.After})
	}
	if len(p.Preview.Dependencies) > 128 {
		p.Preview.Blockers = append(p.Preview.Blockers, "Publisher dependency node limit exceeded.")
	}
	raw, e := json.Marshal(p)
	if e != nil {
		return p.Preview, e
	}
	_, e = s.db.ExecContext(ctx, "INSERT INTO content_previews(id,instance_id,actor_id,private_json,created_at) VALUES(?,?,?,?,?)", p.Preview.ID, id, actor.ID, string(raw), time.Now().UTC().Format(time.RFC3339Nano))
	return p.Preview, e
}
func dependencyLabel(d catalog.Dependency) string {
	if d.ProjectID != "" {
		return d.ProjectID
	}
	if d.Name != "" {
		return d.Name
	}
	return "unresolved publisher item"
}
func (s *Service) uploadMetadata(ctx context.Context, id, stage string, actor int64) (Upload, int64, error) {
	var owner int64
	var created string
	e := s.db.QueryRowContext(ctx, "SELECT actor_id,created_at FROM content_stages WHERE id=? AND instance_id=?", stage, id).Scan(&owner, &created)
	if e != nil || owner != actor || created < cutoff() {
		return Upload{}, owner, domain.E(domain.CodeForbidden, "upload belongs to another actor, is expired or unavailable")
	}
	u, f, e := s.openStage(ctx, id, stage)
	if f != nil {
		f.Close()
	}
	return u, owner, e
}
func (s *Service) verifyPublished(ctx context.Context, id, stage string, hashes map[string]string) error {
	u, f, e := s.openStage(ctx, id, stage)
	if e != nil {
		return e
	}
	defer f.Close()
	writers := []io.Writer{}
	known := 0
	h1, h256, h512 := sha1.New(), sha256.New(), sha512.New()
	for alg, hash := range hashes {
		var n int
		switch alg {
		case "sha1":
			n = 40
			writers = append(writers, h1)
		case "sha256":
			n = 64
			writers = append(writers, h256)
		case "sha512":
			n = 128
			writers = append(writers, h512)
		default:
			continue
		}
		known++
		if b, e := hex.DecodeString(hash); e != nil || len(b)*2 != n {
			return invalid("publisher digest is malformed")
		}
	}
	if known == 0 {
		return invalid("no trustworthy published digest; use a separate unknown trial upload")
	}
	if _, e = io.Copy(io.MultiWriter(writers...), contextReader{ctx, f}); e != nil {
		return e
	}
	for alg, h := range map[string]interface{ Sum([]byte) []byte }{"sha1": h1, "sha256": h256, "sha512": h512} {
		if expected, ok := hashes[alg]; ok && !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), expected) {
			return conflict("manual upload differs from published artifact digest")
		}
	}
	if u.Size == 0 {
		return invalid("empty publisher artifact")
	}
	return nil
}
func (s *Service) addOperation(ctx context.Context, root *os.Root, id string, actor int64, p *privatePreview, name, before, after, afterStage string) error {
	for i := range p.Operations {
		if p.Operations[i].Path == name {
			if before != "" {
				return conflict("duplicate content mutation")
			}
			p.Operations[i].After = after
			p.Operations[i].AfterStage = afterStage
			return nil
		}
	}
	hash, _, e := currentHash(ctx, root, name, MaxArtifactBytes)
	if e != nil {
		return e
	}
	if hash != before {
		return conflict("managed file has local changes or destination exists")
	}
	operation := fileOperation{Path: name, Before: before, After: after, AfterStage: afterStage}
	if before != "" {
		f, e := openChecked(root, name, MaxArtifactBytes)
		if e != nil {
			return e
		}
		fi, e := f.Stat()
		if e != nil {
			f.Close()
			return e
		}
		u, e := s.stage(ctx, id, actor, path.Base(name), f, fi.Size())
		f.Close()
		if e != nil {
			return e
		}
		if u.SHA256 != before {
			return conflict("content file changed while capturing preimage")
		}
		operation.BeforeStage = u.ID
	}
	p.Operations = append(p.Operations, operation)
	return nil
}
func (s *Service) checkCollision(ctx context.Context, r *os.Root, destination string, previous *Installed) error {
	p, base, e := parent(r, destination, false)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	defer p.Close()
	dir, e := p.Open(".")
	if e != nil {
		return e
	}
	defer dir.Close()
	visited := 0
	for {
		if e := ctx.Err(); e != nil {
			return e
		}
		entries, readErr := dir.ReadDir(64)
		for _, entry := range entries {
			visited++
			if visited > 4096 {
				return conflict("content directory exceeds collision verification entry limit")
			}
			if !strings.EqualFold(entry.Name(), base) {
				continue
			}
			owned := false
			if previous != nil {
				for _, file := range previous.Files {
					if file.Path == destination && entry.Name() == base {
						owned = true
					}
				}
			}
			if !owned {
				return conflict("content filename collides with an existing local file")
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}

	return ctx.Err()
}

func metadataDigest(v catalog.Version) string {
	v.FetchedAt = time.Time{}
	raw, _ := json.Marshal(v)
	return digest(raw)
}

func compatibilityBlockers(d Descriptor, t Tuple, kind catalog.Kind, items []Installed, unknownTrial bool) []string {
	blockers := descriptorBlockers(d, t, kind, items)
	if !unknownTrial {
		return blockers
	}
	filtered := []string{}
	for _, blocker := range blockers {
		if blocker == "Descriptor compatibility is unknown; administrator trial required." {
			continue
		}
		if d.Minecraft == "" && blocker == "Descriptor Minecraft/API requirement is unresolved or incompatible." {
			continue
		}
		filtered = append(filtered, blocker)
	}
	return filtered
}

func dependencyMatches(d catalog.Dependency, s catalog.Selection) bool {
	if d.Provider != s.Provider || d.ProjectID == "" && d.VersionID == "" {
		return false
	}
	if d.ProjectID != "" && d.ProjectID != s.ProjectID {
		return false
	}
	return d.VersionID == "" || d.VersionID == s.VersionID
}

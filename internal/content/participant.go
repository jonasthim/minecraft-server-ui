package content

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
)

// InspectPackReader uses the same inert descriptor reader as ordinary content.
func InspectPackReader(ctx context.Context, r io.ReaderAt, size int64) (Descriptor, error) {
	if e := ctx.Err(); e != nil {
		return Descriptor{}, e
	}
	return inspectArchive(r, size)
}
func ValidatePackCandidate(t Tuple, items []Installed) []string {
	if len(items) > 2048 {
		return []string{"Content candidate exceeds 2048 items"}
	}
	return enabledManifestBlockers(t, items)
}

// WithVerifiedPackUpload consumes only a Task16-owned, actor-bound upload and
// verifies every published supported digest. The reader expires with fn.
func (s *Service) WithVerifiedPackUpload(ctx context.Context, id, upload string, actor domain.User, hashes map[string]string, fn func(io.ReaderAt, int64) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, e := s.actor(ctx, actor.ID, true); e != nil {
		return e
	}
	u, _, e := s.uploadMetadata(ctx, id, upload, actor.ID)
	if e != nil {
		return e
	}
	if e = s.verifyPublished(ctx, id, upload, hashes); e != nil {
		return e
	}
	_, f, e := s.openStage(ctx, id, u.ID)
	if e != nil {
		return e
	}
	defer f.Close()
	return fn(f, u.Size)
}

type packScope struct {
	owner     *PackParticipant
	active    atomic.Bool
	mu        sync.Mutex
	ctx       context.Context
	service   *Service
	id        string
	actor     domain.User
	candidate []Installed
	validated bool
	expected  Manifest
}
type PackParticipant struct{ scope *packScope }

// WithPackParticipant enters and retains the original stopped session once. It
// holds content admission until the coordinator has committed and cleaned its
// journal; callback must not call other content/session methods.
func (s *Service) WithPackParticipant(ctx context.Context, session *instance.StoppedSession, expected Manifest, candidate []Installed, actor domain.User, fn func(context.Context, instance.Detail, instance.Directories, *PackParticipant) error) error {
	if fn == nil {
		return invalid("pack participant callback required")
	}
	return session.WithTransactionScope(ctx, func(ctx context.Context, detail instance.Detail, dirs instance.Directories) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		id := detail.Instance.ID
		if _, e := s.actor(ctx, actor.ID, false); e != nil {
			return e
		}
		if detail.Provisioning != "installed" || tuple(detail.Instance) != expected.Tuple {
			return conflict("installed server tuple changed")
		}
		if e := s.CheckReady(ctx, id); e != nil {
			return e
		}
		current, e := s.Snapshot(ctx, id)
		if e != nil {
			return e
		}
		if current.Revision != expected.Revision {
			return conflict("content revision changed")
		}
		for _, previous := range current.Items {
			for _, next := range candidate {
				if next.ID == previous.ID && next.SHA256 == previous.SHA256 && previous.Unknown && (!next.Unknown || !reflect.DeepEqual(next.TrialTuple, previous.TrialTuple)) {
					return conflict("preserved unknown trial provenance cannot be cleared")
				}
			}
		}
		verified, b, e := s.PreparePackCandidate(ctx, id, expected.Tuple, candidate)
		if e != nil {
			return e
		}
		if !reflect.DeepEqual(verified, candidate) {
			return conflict("loader language evidence changed since preview")
		}
		if len(b) > 0 {
			return conflict("complete content candidate is incompatible or unknown")
		}
		encoded, e := json.Marshal(candidate)
		if e != nil {
			return e
		}
		var immutable []Installed
		if e = json.Unmarshal(encoded, &immutable); e != nil {
			return e
		}
		p := &PackParticipant{}
		scope := &packScope{owner: p, ctx: ctx, service: s, id: id, actor: actor, candidate: immutable, expected: expected}
		p.scope = scope
		scope.active.Store(true)
		defer func() { scope.active.Store(false); scope.mu.Lock(); scope.mu.Unlock() }()
		return fn(ctx, detail, dirs, p)
	})
}
func (p *PackParticipant) enter(ctx context.Context) (*packScope, func(), error) {
	if p == nil || p.scope == nil || p.scope.owner != p || !p.scope.active.Load() {
		return nil, nil, domain.E(domain.CodeForbidden, "pack participant expired or forged")
	}
	s := p.scope
	if !s.mu.TryLock() {
		return nil, nil, domain.E(domain.CodeInstanceBusy, "pack participant method overlaps")
	}
	if !s.active.Load() {
		s.mu.Unlock()
		return nil, nil, domain.E(domain.CodeForbidden, "pack participant expired")
	}
	if e := s.ctx.Err(); e != nil {
		s.mu.Unlock()
		return nil, nil, e
	}
	if e := ctx.Err(); e != nil {
		s.mu.Unlock()
		return nil, nil, e
	}
	return s, s.mu.Unlock, nil
}

// ValidatePostimages must run after publication and before opening the SQL tx.
// Every descriptor and hash is re-read; callers cannot bless unknown bytes by
// submitting a fabricated Installed descriptor or Unknown=false.
func (p *PackParticipant) ValidatePostimages(ctx context.Context) error {
	s, release, e := p.enter(ctx)
	if e != nil {
		return e
	}
	defer release()
	s.validated = false
	if _, e = s.service.actor(ctx, s.actor.ID, false); e != nil {
		return e
	}
	root, e := s.service.openServer(ctx, s.id)
	if e != nil {
		return e
	}
	defer root.Close()
	seen := map[string]bool{}
	for _, item := range s.candidate {
		if len(item.Files) != 1 {
			return invalid("pack candidate item must own exactly one JAR")
		}
		file := item.Files[0]
		if seen[strings.ToLower(file.Path)] || !validCandidateFile(item, file.Path) {
			return invalid("duplicate or unsafe pack content ownership")
		}
		seen[strings.ToLower(file.Path)] = true
		f, e := openChecked(root, file.Path, MaxArtifactBytes)
		if e != nil {
			return e
		}
		hasher := sha256.New()
		sha1Hasher := sha1.New()
		sha512Hasher := sha512.New()
		n, e := io.Copy(io.MultiWriter(hasher, sha1Hasher, sha512Hasher), io.LimitReader(contextReader{ctx, f}, MaxArtifactBytes+1))
		hash := hex.EncodeToString(hasher.Sum(nil))
		if e != nil || n > MaxArtifactBytes || hash != file.SHA256 || hash != item.SHA256 {
			f.Close()
			return conflict("candidate JAR postimage differs")
		}
		if _, e = f.Seek(0, io.SeekStart); e != nil {
			f.Close()
			return e
		}
		info, e := f.Stat()
		if e != nil {
			f.Close()
			return e
		}
		descriptor, e := inspectArchive(f, info.Size())
		f.Close()
		if e != nil {
			return e
		}
		if !sameDescriptor(descriptor, item.Descriptor) || descriptor.Unknown && !item.Unknown {
			return conflict("candidate descriptor differs from verified bytes")
		}
		if item.PackMember != nil {
			provenance := item.PackMember
			for name, expected := range provenance.Hashes {
				actual := ""
				switch name {
				case "sha1":
					actual = hex.EncodeToString(sha1Hasher.Sum(nil))
				case "sha256":
					actual = hash
				case "sha512":
					actual = hex.EncodeToString(sha512Hasher.Sum(nil))
				default:
					continue
				}
				if !strings.EqualFold(actual, expected) {
					return conflict("pack member postimage differs from declared publisher digest")
				}
			}
			if provenance.Path == "" || len(provenance.PackSHA256) != 64 || (provenance.MetadataSource != "modrinth" && provenance.MetadataSource != "curseforge") {
				return invalid("pack member provenance incomplete")
			}
		}
	}
	for _, area := range []string{"mods", "plugins"} {
		directory, _, err := parent(root, area+"/unused", false)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		f, err := directory.Open(".")
		if err != nil {
			directory.Close()
			return err
		}
		entries, err := f.ReadDir(4097)
		f.Close()
		directory.Close()
		if err != nil && err != io.EOF {
			return err
		}
		if len(entries) > 4096 {
			return conflict("content directory exceeds bounded complete-candidate verification")
		}
		for _, entry := range entries {
			if err = ctx.Err(); err != nil {
				return err
			}
			name := strings.ToLower(area + "/" + entry.Name())
			if (strings.HasSuffix(name, ".jar") || strings.HasSuffix(name, ".jar.disabled")) && !seen[name] {
				return conflict("unmanaged JAR is outside proposed complete candidate")
			}
		}
	}
	if e = s.service.revalidateServer(ctx, s.id, root); e != nil {
		return e
	}
	s.validated = true
	return nil
}

// Commit performs supplied-tx queries only. Its capability and validated bytes
// must still be live. The coordinator closes the tx before any service calls.
func (p *PackParticipant) Commit(ctx context.Context, tx *sql.Tx) error {
	s, release, e := p.enter(ctx)
	if e != nil {
		return e
	}
	defer release()
	if !s.validated || tx == nil {
		return conflict("verified postimages and SQL transaction are required")
	}
	var synchronous int
	if e = tx.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); e != nil {
		return e
	}
	if synchronous < 2 {
		return conflict("pack commit requires SQLite FULL synchronous durability")
	}
	var role domain.Role
	var disabled bool
	if e = tx.QueryRowContext(ctx, "SELECT role,disabled FROM users WHERE id=?", s.actor.ID).Scan(&role, &disabled); e != nil || disabled || !role.AtLeast(domain.RoleOperator) {
		return domain.E(domain.CodeForbidden, "pack authority changed before commit")
	}
	var currentTuple Tuple
	var provisioning string
	if e = tx.QueryRowContext(ctx, "SELECT minecraft_version,flavor,loader_version,provisioning FROM instances JOIN instance_settings ON instance_settings.instance_id=instances.id WHERE instances.id=?", s.id).Scan(&currentTuple.Minecraft, &currentTuple.Loader, &currentTuple.LoaderVersion, &provisioning); e != nil {
		return e
	}
	if currentTuple != s.expected.Tuple || provisioning != "installed" {
		return conflict("server tuple changed before commit")
	}
	rows, e := tx.QueryContext(ctx, "SELECT manifest_json FROM content_items WHERE instance_id=? ORDER BY id", s.id)
	if e != nil {
		return e
	}
	old := []Installed{}
	for rows.Next() {
		var raw string
		if e = rows.Scan(&raw); e != nil {
			break
		}
		var item Installed
		if e = json.Unmarshal([]byte(raw), &item); e != nil {
			break
		}
		old = append(old, item)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return e
	}
	raw, _ := json.Marshal(struct {
		Tuple Tuple
		Items []Installed
	}{currentTuple, old})
	if digest(raw) != s.expected.Revision {
		return conflict("content manifest changed before commit")
	}
	if _, e = tx.ExecContext(ctx, "DELETE FROM content_items WHERE instance_id=?", s.id); e != nil {
		return e
	}
	for _, item := range s.candidate {
		raw, e := json.Marshal(item)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO content_items(instance_id,id,manifest_json) VALUES(?,?,?)", s.id, item.ID, string(raw)); e != nil {
			return e
		}
	}
	s.validated = false
	return nil
}

// Coordinator file primitives retain Task16's checked-parent, link, preimage,
// file-sync and parent-sync behavior. The caller owns stopped admission and a
// durable multi-file journal; these primitives do not acquire queue/native locks.
func PackReadFile(ctx context.Context, r *os.Root, name string, max int64) ([]byte, error) {
	return readFile(ctx, r, name, max)
}
func PackCurrentHash(ctx context.Context, r *os.Root, name string, max int64) (string, error) {
	h, _, e := currentHash(ctx, r, name, max)
	return h, e
}
func PackOpenChecked(r *os.Root, name string, max int64) (*os.File, error) {
	return openChecked(r, name, max)
}
func PackAtomicStream(ctx context.Context, r *os.Root, name, expected string, reader io.Reader, remove bool) error {
	return atomicStream(ctx, r, name, expected, reader, remove)
}
func PackSyncDirectory(r *os.Root) error { return syncDir(r) }

// PreparePackCandidate adds compatibility evidence from the trusted committed
// loader adapter; it never accepts an API-provided language version assertion.
func (s *Service) PreparePackCandidate(ctx context.Context, id string, t Tuple, items []Installed) ([]Installed, []string, error) {
	raw, e := json.Marshal(items)
	if e != nil {
		return nil, nil, e
	}
	var candidate []Installed
	if e = json.Unmarshal(raw, &candidate); e != nil {
		return nil, nil, e
	}
	needs := false
	for _, item := range candidate {
		if item.Enabled && item.Descriptor.LanguageRequirement != "" && item.Descriptor.LanguageRequirement != "*" {
			needs = true
		}
	}
	if needs && s.languageEvidence != nil {
		evidence, err := s.languageEvidence(ctx, id, domain.VersionSelection{Flavor: domain.Flavor(t.Loader), MinecraftVersion: t.Minecraft, LoaderVersion: t.LoaderVersion})
		if err == nil {
			for i := range candidate {
				if candidate[i].Descriptor.LanguageRequirement != "" {
					candidate[i].LanguageEvidence = &evidence
				}
			}
		}
	}
	return candidate, ValidatePackCandidate(t, candidate), nil
}

func sameDescriptor(a, b Descriptor) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func validCandidateFile(item Installed, name string) bool {
	if !relative(name) {
		return false
	}
	parts := strings.Split(name, "/")
	if len(parts) != 2 {
		return false
	}
	prefix := "mods"
	if item.Kind == "plugin" {
		prefix = "plugins"
	}
	if parts[0] != prefix {
		return false
	}
	if item.Enabled {
		return strings.HasSuffix(strings.ToLower(name), ".jar")
	}
	return strings.HasSuffix(strings.ToLower(name), ".jar.disabled")
}

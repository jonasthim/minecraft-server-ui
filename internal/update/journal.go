package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jonasthim/minecraft-server-ui/internal/content"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/lifecycle"
	"github.com/jonasthim/minecraft-server-ui/internal/modpack"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
)

// Journal version 1 is private recovery history. No world move is representable.
// Rollback consumes before/after history only after separately persisted consent.
type Journal struct {
	StartedIdentity     *domain.ProcessIdentity        `json:"started_identity,omitempty"`
	RecoveryObservation string                         `json:"recovery_observation,omitempty"`
	Autostart           *lifecycle.AutostartSuspension `json:"autostart,omitempty"`
	Version             int                            `json:"version"`
	Plan                Plan                           `json:"plan"`
	Request             Request                        `json:"request"`
	Before              lifecycle.UpdateState          `json:"before"`
	After               lifecycle.UpdateState          `json:"after"`
	ContentBefore       content.Manifest               `json:"content_before"`
	ContentAfter        []content.Installed            `json:"content_after"`
	PackBefore          *modpack.Baseline              `json:"pack_before"`
	PackAfter           *modpack.Baseline              `json:"pack_after"`
	BootAttempted       bool                           `json:"boot_attempted"`
	Operations          []JournalOperation             `json:"operations"`
	StopEpoch           int64                          `json:"stop_epoch"`
	WasRunning          bool                           `json:"was_running"`
}
type JournalOperation struct {
	Path       string `json:"path"`
	Before     string `json:"before"`
	After      string `json:"after"`
	BeforeBlob string `json:"before_blob"`
	AfterBlob  string `json:"after_blob"`
}

func hashBytes(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func (s *Service) dataDir(id string) (string, error) {
	d, err := s.config.Paths.For(id)
	if err != nil {
		return "", err
	}
	return filepath.Dir(filepath.Dir(d.Root)), nil
}
func (s *Service) journalRoot(ctx context.Context, id, transaction string, create bool) (*os.Root, error) {
	if !validHex(transaction, 32) {
		return nil, invalid("invalid update transaction identity")
	}
	d, err := s.config.Paths.For(id)
	if err != nil {
		return nil, err
	}
	root, err := safepath.OpenRoot(ctx, d.State)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if create {
		if err = root.Mkdir("updates", 0700); err != nil && !os.IsExist(err) {
			return nil, err
		}
		if err = content.PackSyncDirectory(root); err != nil {
			return nil, err
		}
	}
	info, err := root.Lstat("updates")
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, invalid("unsafe update journal directory")
	}
	updates, err := root.OpenRoot("updates")
	if err != nil {
		return nil, err
	}
	defer updates.Close()
	if create {
		if err = updates.Mkdir(transaction, 0700); err != nil && !os.IsExist(err) {
			return nil, err
		}
		if err = content.PackSyncDirectory(updates); err != nil {
			return nil, err
		}
	}
	info, err = updates.Lstat(transaction)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, invalid("unsafe update transaction directory")
	}
	opened, err := updates.OpenRoot(transaction)
	if err != nil {
		return nil, err
	}
	after, err := opened.Stat(".")
	if err != nil || !os.SameFile(info, after) {
		opened.Close()
		return nil, conflict("update journal directory changed")
	}
	return opened, nil
}
func validBlob(name string) bool {
	return len(name) > 0 && len(name) < 100 && !strings.ContainsAny(name, "/\\:. \r\n\x00")
}
func (s *Service) stageJournalBlob(ctx context.Context, id, transaction, name, expected string, reader io.Reader) error {
	if !validBlob(name) || !validHex(expected, 64) {
		return invalid("invalid update blob identity")
	}
	root, err := s.journalRoot(ctx, id, transaction, true)
	if err != nil {
		return err
	}
	defer root.Close()
	existing, err := content.PackCurrentHash(ctx, root, name, content.MaxArtifactBytes)
	if err != nil {
		return err
	}
	if existing != "" {
		if existing == expected {
			return nil
		}
		return conflict("update stage already differs")
	}
	if err = content.PackAtomicStream(ctx, root, name, "", reader, false); err != nil {
		return err
	}
	actual, err := content.PackCurrentHash(ctx, root, name, content.MaxArtifactBytes)
	if err != nil {
		return err
	}
	if actual != expected {
		return conflict("update staged bytes failed checksum")
	}
	return nil
}
func journalPath(name string) bool {
	parts := strings.Split(name, "/")
	if len(parts) < 2 || len(parts) > 12 {
		return false
	}
	for _, part := range parts {
		p := strings.ToLower(part)
		if part == "" || part == "." || part == ".." || strings.HasPrefix(p, ".") || p == "world" || p == "worlds" || strings.HasPrefix(p, "world_") || p == "server.properties" || p == "eula.txt" || p == "manager.db" || strings.Contains(p, "secret") || strings.Contains(p, "credential") || strings.Contains(p, "management") || strings.Contains(p, "rcon") {
			return false
		}
	}
	switch parts[0] {
	case "mods", "plugins", "config", "defaultconfigs", "scripts", "kubejs":
		return true
	}
	return false
}
func (s *Service) applyJournalFiles(ctx context.Context, id, transaction string, j Journal, forward bool) error {
	if len(j.Operations) == 0 {
		return ctx.Err()
	}
	if len(j.Operations) > 16384 {
		return invalid("update operation bound exceeded")
	}
	d, err := s.config.Paths.For(id)
	if err != nil {
		return err
	}
	root, err := safepath.OpenRoot(ctx, d.Server)
	if err != nil {
		return err
	}
	defer root.Close()
	blobs, err := s.journalRoot(ctx, id, transaction, false)
	if err != nil {
		return err
	}
	defer blobs.Close()
	seen := map[string]bool{}
	for _, op := range j.Operations {
		if !journalPath(op.Path) || seen[strings.ToLower(op.Path)] {
			return invalid("unsafe or repeated update path")
		}
		seen[strings.ToLower(op.Path)] = true
		if _, err = s.config.Paths.Resolve(id, "server", op.Path); err != nil {
			return err
		}
		from, to, blob := op.Before, op.After, op.AfterBlob
		if !forward {
			from, to, blob = op.After, op.Before, op.BeforeBlob
		}
		if from != "" && !validHex(from, 64) || to != "" && !validHex(to, 64) {
			return invalid("invalid update file checksum")
		}
		current, err := content.PackCurrentHash(ctx, root, op.Path, content.MaxArtifactBytes)
		if err != nil {
			return err
		}
		if current == to {
			continue
		}
		if current != from {
			return conflict("update file changed outside its journal")
		}
		var reader io.Reader = strings.NewReader("")
		var file *os.File
		if to != "" {
			if !validBlob(blob) {
				return invalid("missing update recovery blob")
			}
			actual, err := content.PackCurrentHash(ctx, blobs, blob, content.MaxArtifactBytes)
			if err != nil {
				return err
			}
			if actual != to {
				return conflict("update recovery blob checksum changed")
			}
			file, err = content.PackOpenChecked(blobs, blob, content.MaxArtifactBytes)
			if err != nil {
				return err
			}
			reader = file
		}
		err = content.PackAtomicStream(ctx, root, op.Path, from, reader, to == "")
		if file != nil {
			file.Close()
		}
		if err != nil {
			return err
		}
		actual, err := content.PackCurrentHash(ctx, root, op.Path, content.MaxArtifactBytes)
		if err != nil {
			return err
		}
		if actual != to {
			return conflict("published update bytes failed checksum")
		}
	}
	canonical, err := safepath.OpenRoot(ctx, d.Server)
	if err != nil {
		return err
	}
	defer canonical.Close()
	before, err := root.Stat(".")
	if err != nil {
		return err
	}
	after, err := canonical.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		return conflict("canonical update server directory changed")
	}
	return nil
}
func (s *Service) stageJournal(ctx context.Context, id, transaction string, p *preparedTransaction) (Journal, error) {
	j := Journal{Version: 1, Plan: p.plan, Request: p.request, Before: p.before, After: p.after, ContentBefore: p.contentReceipt.Observed, ContentAfter: p.contentReceipt.Candidate, PackBefore: p.packReceipt.BeforeBaseline, PackAfter: p.packReceipt.Baseline, Operations: []JournalOperation{}}
	add := func(name, before, after, beforeStage, afterStage string, open func(context.Context, string, func(io.Reader) error) error) error {
		op := JournalOperation{Path: name, Before: before, After: after}
		index := len(j.Operations)
		if before != "" {
			op.BeforeBlob = "before-" + itoa(index)
			if err := open(ctx, beforeStage, func(reader io.Reader) error {
				return s.stageJournalBlob(ctx, id, transaction, op.BeforeBlob, before, reader)
			}); err != nil {
				return err
			}
		}
		if after != "" {
			op.AfterBlob = "after-" + itoa(index)
			if err := open(ctx, afterStage, func(reader io.Reader) error {
				return s.stageJournalBlob(ctx, id, transaction, op.AfterBlob, after, reader)
			}); err != nil {
				return err
			}
		}
		if !journalPath(name) {
			return invalid("unsupported update file path")
		}
		j.Operations = append(j.Operations, op)
		return nil
	}
	for _, op := range p.contentReceipt.Operations {
		if err := add(op.Path, op.Before, op.After, op.BeforeStage, op.AfterStage, func(ctx context.Context, stage string, fn func(io.Reader) error) error {
			return p.content.WithStage(ctx, nil, stage, fn)
		}); err != nil {
			return Journal{}, err
		}
	}
	for _, op := range p.packReceipt.Operations {
		if err := add(op.Path, op.Before.SHA256, op.After.SHA256, op.Before.ID, op.After.ID, func(ctx context.Context, stage string, fn func(io.Reader) error) error {
			return p.pack.WithStage(ctx, nil, stage, fn)
		}); err != nil {
			return Journal{}, err
		}
	}
	sort.Slice(j.Operations, func(i, k int) bool { return j.Operations[i].Path < j.Operations[k].Path })
	return j, nil
}
func itoa(value int) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	out := ""
	for value > 0 {
		out = string(digits[value%10]) + out
		value /= 10
	}
	return out
}
func journalJSON(j Journal) (string, error) { raw, err := json.Marshal(j); return string(raw), err }

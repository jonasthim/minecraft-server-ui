package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
)

func stageID(body []byte, platform string) string {
	h := sha256.Sum256(append(append(append([]byte{}, body...), 0), []byte(platform)...))
	return hex.EncodeToString(h[:])
}
func (s *Stager) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// List revalidates bounded immutable stages. Receipts are derived from signed
// metadata and actual archive/executable bytes, never trusted from ready.json.
// Missing staging directories are ordinary first-use state; no files are made.
func (s *Stager) List(ctx context.Context) ([]Stage, error) {
	out := []Stage{}
	root, err := safepath.OpenRoot(ctx, s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	entries, e := directory.ReadDir(6)
	closeErr := directory.Close()
	if e != nil && !errors.Is(e, io.EOF) {
		return nil, e
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(entries) > 5 {
		return nil, errors.New("staging directory exceeds discovery bound")
	}
	for _, entry := range entries {
		if entry.Name() == ".staging.lock" || strings.HasPrefix(entry.Name(), ".pending-") {
			continue
		}
		if !validHash(entry.Name()) || !entry.IsDir() || len(out) == 2 {
			return nil, errors.New("staging directory requires reconciliation")
		}
		stage, err := s.verifyStage(ctx, root, entry.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, stage)
	}
	if err := sameRoot(ctx, s.Dir, root); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (s *Stager) verifyStage(ctx context.Context, parent *os.Root, id string) (Stage, error) {
	var receipt Stage
	info, err := parent.Lstat(id)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return receipt, errors.New("unsafe staged directory")
	}
	root, err := parent.OpenRoot(id)
	if err != nil {
		return receipt, err
	}
	defer root.Close()
	ready, err := readChecked(ctx, root, "ready.json", 2048)
	if err != nil {
		return receipt, err
	}
	if strictJSON(ready, &receipt) != nil || !validHash(receipt.ID) {
		return Stage{}, errors.New("invalid staged receipt")
	}
	body, err := readChecked(ctx, root, "release.json", MaxManifestBytes)
	if err != nil {
		return Stage{}, err
	}
	sig, err := readChecked(ctx, root, "release.sig", 1024)
	if err != nil {
		return Stage{}, err
	}
	m, err := VerifyManifest(s.Trust, body, sig, receipt.Version, s.CurrentVersion, s.Schema, s.now())
	if err != nil {
		return Stage{}, err
	}
	a, err := m.Artifact(s.OS, s.Arch)
	if err != nil {
		return Stage{}, err
	}
	expected := Stage{ID: stageID(body, s.OS+"/"+s.Arch), Version: m.Version, Platform: s.OS + "/" + s.Arch, BinarySHA256: a.BinarySHA256, Schema: m.SchemaAfter}
	if receipt != expected || expected.ID != id {
		return Stage{}, errors.New("staged receipt identity mismatch")
	}
	directory, err := root.Open(".")
	if err != nil {
		return Stage{}, err
	}
	entries, e := directory.ReadDir(8)
	directory.Close()
	if (e != nil && !errors.Is(e, io.EOF)) || len(entries) != 7 {
		return Stage{}, errors.New("unexpected staged file set")
	}
	allowed := map[string]bool{"ready.json": true, "release.json": true, "release.sig": true, a.Name: true, binaryName(s.OS): true, "LICENSE": true, "THIRD_PARTY_NOTICES.txt": true}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return Stage{}, errors.New("unexpected staged member")
		}
	}
	for _, file := range []struct {
		name, hash string
		size       int64
	}{{a.Name, a.SHA256, a.Size}, {binaryName(s.OS), a.BinarySHA256, a.BinarySize}} {
		h, e := hashFile(ctx, root, file.name)
		if e != nil || h != file.hash {
			return Stage{}, errors.New("staged file verification failed")
		}
		info, e := root.Lstat(file.name)
		if e != nil || info.Size() != file.size {
			return Stage{}, errors.New("staged file size mismatch")
		}
	}
	for _, name := range []string{"LICENSE", "THIRD_PARTY_NOTICES.txt"} {
		if _, err := readChecked(ctx, root, name, 1<<20); err != nil {
			return Stage{}, err
		}
	}
	after, err := parent.Lstat(id)
	opened, e := root.Stat(".")
	if err != nil || e != nil || !os.SameFile(info, after) || !os.SameFile(info, opened) {
		return Stage{}, errors.New("staged directory binding changed")
	}
	return expected, nil
}

//go:build !windows

package installinputs

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/release/candidate"
)

type importFixture struct {
	prepared    string
	transaction *os.Root
	options     Options
	files       map[string][]byte
}

func newImportFixture(t *testing.T, targetOS string) importFixture {
	return newImportFixtureTrust(t, targetOS, "OWNER SIGNED")
}

func newImportFixtureTrust(t *testing.T, targetOS, trustLabel string) importFixture {
	return newImportFixtureTrustWithUpdaterSize(t, targetOS, trustLabel, 0)
}

func newImportFixtureTrustWithUpdaterSize(t *testing.T, targetOS, trustLabel string, updaterSize int) importFixture {
	t.Helper()
	if targetOS == "" {
		targetOS = runtime.GOOS
	}
	base := t.TempDir()
	prepared := filepath.Join(base, "prepared")
	transactionPath := filepath.Join(base, "transaction")
	if err := os.Mkdir(prepared, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(transactionPath, 0700); err != nil {
		t.Fatal(err)
	}
	transaction, err := os.OpenRoot(transactionPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transaction.Close() })
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, name := range memberNames(targetOS) {
		files[name] = []byte("authenticated member: " + name)
	}
	if updaterSize > 0 {
		updaterName := "selfupdate"
		if targetOS == "windows" {
			updaterName = "selfupdate.exe"
		}
		files[updaterName] = bytes.Repeat([]byte("u"), updaterSize)
	}
	bootstrap := candidate.Bootstrap{Format: 1, Purpose: "minecraft-ui-bootstrap", Repository: "owner/repository", Version: "v1.2.3", PublishedAt: time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC), ExpiresAt: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC), Schema: 18, Trust: trustLabel}
	for _, platform := range []string{"linux", "windows"} {
		members := make([]candidate.Member, 0)
		for _, name := range memberNames(platform) {
			body := files[name]
			if body == nil {
				body = []byte("fixture member: " + name)
			}
			mode := uint32(0644)
			if name == "minecraft-ui" || name == "selfupdate" || name == "minecraft-ui.exe" || name == "selfupdate.exe" || name == "install.sh" || name == "unitctl" {
				mode = 0755
			}
			if name == "sudoers" {
				mode = 0440
			}
			members = append(members, candidate.Member{Name: name, Size: int64(len(body)), SHA256: hashText(body), Mode: mode})
		}
		archiveExt := ".tar.gz"
		if platform == "windows" {
			archiveExt = ".zip"
		}
		bootstrap.Artifacts = append(bootstrap.Artifacts, candidate.Bundle{OS: platform, Arch: "amd64", Name: "minecraft-ui-bootstrap-" + platform + "-amd64" + archiveExt, Size: 1, SHA256: hashText([]byte("archive")), Members: members})
	}
	metadata, err := json.Marshal(bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	metadata = append(metadata, '\n')
	sig := ed25519.Sign(private, metadata)
	envelope, err := json.Marshal(candidate.Signature{KeyID: "fixture", Signature: base64.StdEncoding.EncodeToString(sig)})
	if err != nil {
		t.Fatal(err)
	}
	envelope = append(envelope, '\n')
	var artifact candidate.Bundle
	for _, a := range bootstrap.Artifacts {
		if a.OS == targetOS {
			artifact = a
		}
	}
	receipt := completionReceipt{Format: 1, Repository: bootstrap.Repository, Version: bootstrap.Version, Schema: bootstrap.Schema, OS: artifact.OS, Arch: artifact.Arch, Trust: bootstrap.Trust, ManifestSHA256: hashText(metadata), Members: artifact.Members}
	receiptBytes, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	receiptBytes = append(receiptBytes, '\n')
	if err := os.WriteFile(filepath.Join(prepared, metadataName), metadata, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared, signatureName), envelope, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared, receiptName), receiptBytes, 0600); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if contains(memberNames(targetOS), name) {
			if err := os.WriteFile(filepath.Join(prepared, name), body, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	options := Options{Prepared: prepared, Version: "v1.2.3", Trust: candidate.Trust{Repository: "owner/repository", PublicKeys: map[string]ed25519.PublicKey{"fixture": public}}}
	if _, verifyErr := candidate.VerifyBootstrap(options.Trust, metadata, envelope, options.Version, time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)); verifyErr != nil {
		t.Fatalf("fixture candidate invalid: %v", verifyErr)
	}
	return importFixture{prepared: prepared, transaction: transaction, options: options, files: files}
}

func TestOpenRecordedRoundTripsLargeSignedUpdater(t *testing.T) {
	const updaterSize = int(candidate.MaxAssetBytes + 1)
	f := newImportFixtureTrustWithUpdaterSize(t, runtime.GOOS, "OWNER SIGNED", updaterSize)
	config := importConfig{targetOS: runtime.GOOS, targetArch: "amd64", now: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), testRoot: true}
	imported, err := importWith(context.Background(), f.transaction, f.options, config)
	if err != nil {
		t.Fatalf("import signed updater above asset limit: %v", err)
	}
	if err := imported.Close(); err != nil {
		t.Fatal(err)
	}
	metadata, err := os.ReadFile(filepath.Join(f.transaction.Name(), inputsName, metadataName))
	if err != nil {
		t.Fatal(err)
	}
	options := RecordedOptions{Version: f.options.Version, Trust: f.options.Trust, ManifestSHA256: digest(metadata)}
	reopened, err := openRecordedWith(context.Background(), f.transaction, options, recordedConfig{targetOS: runtime.GOOS, targetArch: "amd64", now: config.now, testRoot: true})
	if err != nil {
		t.Fatalf("reopen signed updater above asset limit: %v", err)
	}
	defer reopened.Close()
	updaterName := "selfupdate"
	if runtime.GOOS == "windows" {
		updaterName = "selfupdate.exe"
	}
	member, err := reopened.OpenMember(context.Background(), updaterName)
	if err != nil {
		t.Fatal(err)
	}
	defer member.Close()
	body, err := io.ReadAll(member)
	if err != nil || len(body) != updaterSize || !bytes.Equal(body, f.files[updaterName]) {
		t.Fatalf("large updater differs: got %d bytes, err=%v", len(body), err)
	}
}

func memberNames(platform string) []string {
	if platform == "windows" {
		return []string{"minecraft-ui.exe", "selfupdate.exe", "LICENSE", "THIRD_PARTY_NOTICES.txt", "install.ps1"}
	}
	return []string{"minecraft-ui", "selfupdate", "LICENSE", "THIRD_PARTY_NOTICES.txt", "install.sh", "unitctl", "minecraft@.service", "minecraft-ui.service", "sudoers", "minecraft-ui-update@.service", "minecraft-ui-update-recovery.service", "minecraft-ui-update.conf", "update-trust.example.json", "NOTICE"}
}

func TestImportCopiesExactAuthenticatedBytesAndOpenMember(t *testing.T) {
	targetOS := runtime.GOOS
	f := newImportFixture(t, targetOS)
	inputs, err := importWith(context.Background(), f.transaction, f.options, importConfig{targetOS: runtime.GOOS, targetArch: "amd64", now: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), testRoot: true})
	if err != nil {
		t.Fatal(err)
	}
	defer inputs.Close()
	if inputs.Summary().Version != "v1.2.3" || inputs.Summary().MemberCount != len(memberNames(runtime.GOOS)) {
		t.Fatalf("unexpected summary: %+v", inputs.Summary())
	}
	member := memberNames(runtime.GOOS)[0]
	opened, err := inputs.OpenMember(context.Background(), member)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(f.transaction.Name(), inputsName, member))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != string(f.files[member]) {
		t.Fatalf("imported bytes changed: %q", body)
	}
	_ = opened.Close()
	if _, err := inputs.OpenMember(context.Background(), "../bootstrap.json"); err == nil {
		t.Fatal("accepted unauthenticated member name")
	}
	for _, name := range append([]string{metadataName, signatureName, receiptName}, memberNames(targetOS)...) {
		original, err := os.ReadFile(filepath.Join(f.prepared, name))
		if err != nil {
			t.Fatal(err)
		}
		copied, err := os.ReadFile(filepath.Join(f.transaction.Name(), inputsName, name))
		if err != nil || !bytes.Equal(original, copied) {
			t.Fatalf("retained bytes differ for %s: %v", name, err)
		}
	}
}

func TestOpenRecordedReauthenticatesImportedInputsAndRetainsMemberPins(t *testing.T) {
	f, options := importRecordedFixture(t)
	inputs, err := openRecordedWith(context.Background(), f.transaction, options, recordedConfig{targetOS: runtime.GOOS, targetArch: "amd64", now: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), testRoot: true})
	if err != nil {
		t.Fatal(err)
	}
	defer inputs.Close()
	if got := inputs.Summary(); got.Version != options.Version || got.ManifestSHA256 != options.ManifestSHA256 || got.MemberCount != len(memberNames(runtime.GOOS)) {
		t.Fatalf("recorded summary=%+v", got)
	}
	member := memberNames(runtime.GOOS)[0]
	file, err := inputs.OpenMember(context.Background(), member)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(file)
	closeErr := file.Close()
	if err != nil || closeErr != nil || !bytes.Equal(body, f.files[member]) {
		t.Fatalf("recorded member body differs: read=%v close=%v", err, closeErr)
	}
}

func TestOpenRecordedRejectsTamperingWithoutChangingTree(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*testing.T, importFixture)
	}{
		{"metadata", func(t *testing.T, f importFixture) {
			path := filepath.Join(f.transaction.Name(), inputsName, metadataName)
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			body[len(body)-2] ^= 1
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"member", func(t *testing.T, f importFixture) {
			member := memberNames(runtime.GOOS)[0]
			if err := os.WriteFile(filepath.Join(f.transaction.Name(), inputsName, member), bytes.Repeat([]byte{'x'}, len(f.files[member])), 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"foreign extra", func(t *testing.T, f importFixture) {
			if err := os.WriteFile(filepath.Join(f.transaction.Name(), inputsName, "foreign"), []byte("foreign"), 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"unsafe mode", func(t *testing.T, f importFixture) {
			if err := os.Chmod(filepath.Join(f.transaction.Name(), inputsName, "bootstrap.json"), 0644); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, options := importRecordedFixture(t)
			tc.mutate(t, f)
			before := snapshotRecordedInputTree(t, f.transaction.Name())
			if inputs, err := openRecordedWith(context.Background(), f.transaction, options, recordedConfig{targetOS: runtime.GOOS, targetArch: "amd64", now: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), testRoot: true}); err == nil {
				_ = inputs.Close()
				t.Fatal("accepted modified recorded inputs")
			}
			after := snapshotRecordedInputTree(t, f.transaction.Name())
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("failed recorded inspection changed tree:\nbefore=%v\nafter=%v", before, after)
			}
		})
	}
}

func TestOpenRecordedRejectsPinnedMetadataReplacementDuringRead(t *testing.T) {
	f, options := importRecordedFixture(t)
	var afterMutation map[string]string
	config := recordedConfig{targetOS: runtime.GOOS, targetArch: "amd64", now: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), testRoot: true, afterRead: func(name string, transaction, _ *os.Root) error {
		if name != metadataName {
			return nil
		}
		path := filepath.Join(transaction.Name(), inputsName, metadataName)
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		if err := os.WriteFile(path, body, 0600); err != nil {
			return err
		}
		afterMutation = snapshotRecordedInputTree(t, transaction.Name())
		return nil
	}}
	if inputs, err := openRecordedWith(context.Background(), f.transaction, options, config); err == nil {
		_ = inputs.Close()
		t.Fatal("accepted a metadata pathname replaced after its file was pinned")
	}
	if afterMutation == nil || !reflect.DeepEqual(afterMutation, snapshotRecordedInputTree(t, f.transaction.Name())) {
		t.Fatal("failed inspection changed the tree after the injected replacement")
	}
}

func TestOpenRecordedRejectsTransactionRootReplacement(t *testing.T) {
	f, options := importRecordedFixture(t)
	base := filepath.Dir(f.transaction.Name())
	original := f.transaction.Name()
	moved := original + ".held"
	if err := f.transaction.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(original, 0700); err != nil {
		t.Fatal(err)
	}
	newRoot, err := os.OpenRoot(original)
	if err != nil {
		t.Fatal(err)
	}
	defer newRoot.Close()
	before := snapshotRecordedInputTree(t, base)
	if inputs, err := openRecordedWith(context.Background(), newRoot, options, recordedConfig{targetOS: runtime.GOOS, targetArch: "amd64", now: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), testRoot: true}); err == nil {
		_ = inputs.Close()
		t.Fatal("accepted a replacement transaction root")
	}
	if after := snapshotRecordedInputTree(t, base); !reflect.DeepEqual(before, after) {
		t.Fatalf("root replacement refusal changed tree:\nbefore=%v\nafter=%v", before, after)
	}
}

func importRecordedFixture(t *testing.T) (importFixture, RecordedOptions) {
	t.Helper()
	f := newImportFixture(t, runtime.GOOS)
	config := importConfig{targetOS: runtime.GOOS, targetArch: "amd64", now: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), testRoot: true}
	inputs, err := importWith(context.Background(), f.transaction, f.options, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := inputs.Close(); err != nil {
		t.Fatal(err)
	}
	metadata, err := os.ReadFile(filepath.Join(f.prepared, metadataName))
	if err != nil {
		t.Fatal(err)
	}
	return f, RecordedOptions{Version: f.options.Version, Trust: f.options.Trust, ManifestSHA256: digest(metadata)}
}

func snapshotRecordedInputTree(t *testing.T, root string) map[string]string {
	t.Helper()
	entries := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		value := info.Mode().String()
		if !entry.IsDir() {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += ":" + string(body)
		}
		entries[rel] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestCloseReleasesRetainedPins(t *testing.T) {
	f := newImportFixture(t, runtime.GOOS)
	inputs, err := importWith(context.Background(), f.transaction, f.options, importConfig{targetOS: runtime.GOOS, targetArch: "amd64", now: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), testRoot: true})
	if err != nil {
		t.Fatal(err)
	}
	pins := make([]*os.File, 0, len(inputs.files))
	for _, owned := range inputs.files {
		pins = append(pins, owned.pin)
	}
	if err := inputs.Close(); err != nil {
		t.Fatal(err)
	}
	for _, pin := range pins {
		if _, err := pin.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("Close did not release retained creation pin: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(f.transaction.Name(), inputsName)); err != nil {
		t.Fatalf("Close removed the successfully imported private files: %v", err)
	}
}

func TestImportWindowsLayoutThroughPlatformSeam(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows native DACL qualification is a separate gate")
	}
	f := newImportFixture(t, "windows")
	inputs, err := importWith(context.Background(), f.transaction, f.options, importConfig{targetOS: "windows", targetArch: "amd64", now: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), testRoot: true})
	if err != nil {
		t.Fatal(err)
	}
	defer inputs.Close()
	if got := len(inputs.Members()); got != 5 {
		t.Fatalf("Windows signed member count = %d", got)
	}
}

func TestImportRejectsSignatureBypassAndCleansCreatedInputs(t *testing.T) {
	f := newImportFixture(t, runtime.GOOS)
	name := memberNames(runtime.GOOS)[0]
	spoof := bytes.Repeat([]byte{'x'}, len(f.files[name]))
	if err := os.WriteFile(filepath.Join(f.prepared, name), spoof, 0600); err != nil {
		t.Fatal(err)
	}
	_, err := importWith(context.Background(), f.transaction, f.options, importConfig{targetOS: runtime.GOOS, targetArch: "amd64", now: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), testRoot: true})
	if err == nil {
		t.Fatal("accepted bytes that do not match signed digest")
	}
	if _, statErr := f.transaction.Lstat(inputsName); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed import left private inputs: %v", statErr)
	}
}

func TestImportRejectsWrongTrustAndExistingInputs(t *testing.T) {
	f := newImportFixture(t, runtime.GOOS)
	originalTrust := f.options.Trust
	_, wrongKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrongPublic := wrongKey.Public().(ed25519.PublicKey)
	f.options.Trust.PublicKeys = map[string]ed25519.PublicKey{"fixture": wrongPublic}
	config := importConfig{targetOS: runtime.GOOS, targetArch: "amd64", now: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), testRoot: true}
	if _, err := importWith(context.Background(), f.transaction, f.options, config); err == nil {
		t.Fatal("accepted bootstrap signed by a different key")
	}
	f.options.Trust = originalTrust
	for _, mutate := range []struct {
		name    string
		options Options
		now     time.Time
	}{
		{name: "repository", options: Options{Prepared: f.options.Prepared, Version: f.options.Version, Trust: candidate.Trust{Repository: "other/repository", PublicKeys: f.options.Trust.PublicKeys}}},
		{name: "version", options: Options{Prepared: f.options.Prepared, Version: "v1.2.4", Trust: f.options.Trust}},
		{name: "expiry", options: f.options, now: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)},
	} {
		badConfig := config
		if !mutate.now.IsZero() {
			badConfig.now = mutate.now
		}
		if _, err := importWith(context.Background(), f.transaction, mutate.options, badConfig); err == nil {
			t.Fatalf("accepted invalid %s", mutate.name)
		}
	}
	if err := f.transaction.Mkdir(inputsName, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := importWith(context.Background(), f.transaction, f.options, config); err == nil {
		t.Fatal("reused existing transaction inputs directory")
	}
	if _, err := f.transaction.Lstat(inputsName); err != nil {
		t.Fatalf("existing inputs directory was changed: %v", err)
	}
}

func TestImportRejectsInconsistentReceipt(t *testing.T) {
	f := newImportFixture(t, runtime.GOOS)
	path := filepath.Join(f.prepared, receiptName)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body = bytes.Replace(body, []byte(`"version":"v1.2.3"`), []byte(`"version":"v1.2.4"`), 1)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = importWith(context.Background(), f.transaction, f.options, importConfig{targetOS: runtime.GOOS, targetArch: "amd64", now: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), testRoot: true})
	if err == nil {
		t.Fatal("accepted receipt inconsistent with signed bootstrap")
	}
}

func TestImportRejectsPreparedSourcePathReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("source replacement behavior is covered by the native Windows gate")
	}
	f := newImportFixture(t, runtime.GOOS)
	member := memberNames(runtime.GOOS)[0]
	config := importConfig{targetOS: runtime.GOOS, targetArch: "amd64", now: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), testRoot: true, afterCopy: func(name string, prepared, _ *os.Root) error {
		if name != metadataName {
			return nil
		}
		path := filepath.Join(prepared.Name(), member)
		if err := os.Rename(path, path+".saved"); err != nil {
			return err
		}
		if err := os.WriteFile(path, f.files[member], 0600); err != nil {
			return err
		}
		return os.Remove(path + ".saved")
	}}
	if _, err := importWith(context.Background(), f.transaction, f.options, config); err == nil {
		t.Fatal("accepted replaced prepared source path")
	}
	if _, err := f.transaction.Lstat(inputsName); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed source recheck left imported inputs: %v", err)
	}
}

func TestImportRejectsPreparedLinks(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			f := newImportFixture(t, runtime.GOOS)
			member := memberNames(runtime.GOOS)[0]
			path := filepath.Join(f.prepared, member)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(f.prepared, memberNames(runtime.GOOS)[1])
			var err error
			if kind == "symlink" {
				err = os.Symlink(target, path)
			} else {
				err = os.Link(target, path)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = importWith(context.Background(), f.transaction, f.options, importConfig{targetOS: runtime.GOOS, targetArch: "amd64", now: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), testRoot: true})
			if err == nil || !strings.Contains(err.Error(), "authenticated input import failed: prepared file is unsafe") {
				t.Fatalf("link was not rejected at the source boundary: %v", err)
			}
		})
	}
}

func TestImportRejectsMissingAndExtraPreparedEntries(t *testing.T) {
	for _, change := range []string{"missing", "extra"} {
		t.Run(change, func(t *testing.T) {
			f := newImportFixture(t, runtime.GOOS)
			if change == "missing" {
				if err := os.Remove(filepath.Join(f.prepared, memberNames(runtime.GOOS)[0])); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(filepath.Join(f.prepared, "unlisted-extra"), []byte("foreign"), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := importWith(context.Background(), f.transaction, f.options, importConfig{targetOS: runtime.GOOS, targetArch: "amd64", now: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), testRoot: true})
			want := "authenticated input import failed: prepared file set does not match authenticated members"
			if change == "extra" {
				want = "authenticated input import failed: prepared file set could not be read"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("prepared set mismatch was not rejected at its fixed stage: %v", err)
			}
		})
	}
}

func TestProductionTransactionRootRejectsNonPrivateMode(t *testing.T) {
	f := newImportFixture(t, runtime.GOOS)
	if err := os.Chmod(f.transaction.Name(), 0770); err != nil {
		t.Fatal(err)
	}
	_, err := importWith(context.Background(), f.transaction, f.options, importConfig{targetOS: runtime.GOOS, targetArch: "amd64", now: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)})
	if err == nil || !strings.Contains(err.Error(), "authenticated input import failed: transaction root is not private") {
		t.Fatalf("production policy accepted a non-private transaction root: %v", err)
	}
	if _, statErr := f.transaction.Lstat(inputsName); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("rejected transaction root was modified: %v", statErr)
	}
}

func TestProductionTransactionRootRejectsNonRootOwner(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a non-root-owned directory cannot be constructed by a root test process without privilege changes")
	}
	f := newImportFixture(t, runtime.GOOS)
	_, err := importWith(context.Background(), f.transaction, f.options, importConfig{targetOS: runtime.GOOS, targetArch: "amd64", now: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)})
	if err == nil || !strings.Contains(err.Error(), "authenticated input import failed: transaction root is not private") {
		t.Fatalf("production policy accepted a non-root-owned transaction root: %v", err)
	}
	if _, statErr := f.transaction.Lstat(inputsName); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("rejected transaction root was modified: %v", statErr)
	}
}

func TestReadEntriesAcceptsMaximumAndRejectsOverflow(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for i := 0; i < maxPreparedEntries; i++ {
		name := filepath.Join(dir, fmt.Sprintf("entry-%02d", i))
		if err := os.WriteFile(name, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := readEntries(root)
	if err != nil || len(entries) != maxPreparedEntries {
		t.Fatalf("maximum valid directory rejected: entries=%d err=%v", len(entries), err)
	}
	if err := os.WriteFile(filepath.Join(dir, "overflow"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = readEntries(root)
	if err == nil || !strings.Contains(err.Error(), "directory entry count exceeds bound") {
		t.Fatalf("entry overflow was not rejected: %v", err)
	}
}

func TestHashHandleCancellationStopsBoundedRecheck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large-member")
	body := bytes.Repeat([]byte("x"), 8*copyBufferSize)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	// Negative control: a live context hashes the whole bounded fixture.
	gotHash, gotSize, err := hashHandle(context.Background(), file, int64(len(body)))
	if err != nil || gotSize != int64(len(body)) || gotHash != hashText(body) {
		t.Fatalf("uncancelled hash changed: size=%d hash=%q err=%v", gotSize, gotHash, err)
	}

	ctx := &cancelOnSecondErr{Context: context.Background()}
	_, consumed, err := hashHandle(ctx, file, int64(len(body)))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation identity was lost: consumed=%d err=%v", consumed, err)
	}
	if consumed <= 0 || consumed > copyBufferSize {
		t.Fatalf("cancelled recheck consumed outside one chunk: %d", consumed)
	}
	position, err := file.Seek(0, io.SeekCurrent)
	if err != nil || position != 0 {
		t.Fatalf("cancelled hash did not rewind handle: position=%d err=%v", position, err)
	}
}

type cancelOnSecondErr struct {
	context.Context
	checks int
}

func (ctx *cancelOnSecondErr) Err() error {
	ctx.checks++
	if ctx.checks >= 2 {
		return context.Canceled
	}
	return nil
}

func TestDevelopmentBootstrapRequiresExplicitOptIn(t *testing.T) {
	f := newImportFixtureTrust(t, runtime.GOOS, "DEVELOPMENT ONLY")
	config := importConfig{targetOS: runtime.GOOS, targetArch: "amd64", now: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), testRoot: true}
	if _, err := importWith(context.Background(), f.transaction, f.options, config); err == nil {
		t.Fatal("accepted development bootstrap without opt-in")
	}
	f.options.AllowDevelopment = true
	inputs, err := importWith(context.Background(), f.transaction, f.options, config)
	if err != nil {
		t.Fatal(err)
	}
	_ = inputs.Close()
}

func TestReceiptRejectsDuplicateAndUnknownFields(t *testing.T) {
	if _, err := decodeReceipt([]byte(`{"format":1,"format":1}`)); err == nil {
		t.Fatal("accepted duplicate receipt key")
	}
	if _, err := decodeReceipt([]byte(`{"format":1,"unknown":true}`)); err == nil {
		t.Fatal("accepted unknown receipt field")
	}
}

func TestImportCancellationCleansPartialOutputs(t *testing.T) {
	f := newImportFixture(t, runtime.GOOS)
	ctx, cancel := context.WithCancel(context.Background())
	_, err := importWith(ctx, f.transaction, f.options, importConfig{targetOS: runtime.GOOS, targetArch: "amd64", now: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), testRoot: true, afterCopy: func(name string, _, _ *os.Root) error {
		if name == metadataName {
			cancel()
		}
		return nil
	}})
	if err == nil {
		t.Fatal("accepted cancellation during copy")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation identity was lost: %v", err)
	}
	if _, statErr := f.transaction.Lstat(inputsName); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("cancelled import left private inputs: %v", statErr)
	}
}

func TestInputsDirectoryCreationFailureCleansOnlyRetainedIdentity(t *testing.T) {
	for _, replace := range []bool{false, true} {
		name := "same_identity_cleanup"
		if replace {
			name = "replacement_preserved"
		}
		t.Run(name, func(t *testing.T) {
			f := newImportFixture(t, runtime.GOOS)
			cause := errors.New("injected post-create validation failure")
			config := importConfig{targetOS: runtime.GOOS, targetArch: "amd64", now: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), testRoot: true, createInputsDirectory: func(parent *os.Root, _ bool) (*os.Root, os.FileInfo, error) {
				if err := parent.Mkdir(inputsName, 0700); err != nil {
					return nil, nil, err
				}
				info, err := parent.Lstat(inputsName)
				if err != nil {
					return nil, nil, errors.Join(errInputsDirectoryCreated, err)
				}
				root, err := parent.OpenRoot(inputsName)
				if err != nil {
					return nil, info, errors.Join(errInputsDirectoryCreated, err)
				}
				if replace {
					if err := parent.Rename(inputsName, "inputs-created"); err != nil {
						_ = root.Close()
						return nil, info, err
					}
					if err := parent.Mkdir(inputsName, 0700); err != nil {
						_ = root.Close()
						return nil, info, err
					}
					if err := os.WriteFile(filepath.Join(parent.Name(), inputsName, "foreign"), []byte("replacement"), 0600); err != nil {
						_ = root.Close()
						return nil, info, err
					}
				}
				return root, info, errors.Join(errInputsDirectoryCreated, cause)
			}}
			_, err := importWith(context.Background(), f.transaction, f.options, config)
			if err == nil || !strings.Contains(err.Error(), "authenticated input import failed: ") {
				t.Fatalf("post-create failure was not reported: %v", err)
			}
			if replace {
				if !strings.Contains(err.Error(), "partial private input cleanup was incomplete") {
					t.Fatalf("replacement race was not reported as incomplete cleanup: %v", err)
				}
				body, readErr := os.ReadFile(filepath.Join(f.transaction.Name(), inputsName, "foreign"))
				if readErr != nil || string(body) != "replacement" {
					t.Fatalf("replacement directory was not preserved: %q %v", body, readErr)
				}
				if err := os.Remove(filepath.Join(f.transaction.Name(), inputsName, "foreign")); err != nil {
					t.Fatal(err)
				}
				if err := f.transaction.Remove(inputsName); err != nil {
					t.Fatal(err)
				}
				if err := f.transaction.Remove("inputs-created"); err != nil {
					t.Fatal(err)
				}
			} else {
				if !strings.Contains(err.Error(), "private inputs directory could not be created") {
					t.Fatalf("cleaned failed creation reported the wrong stage: %v", err)
				}
				if _, statErr := f.transaction.Lstat(inputsName); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("same-identity failed creation was not cleaned: %v", statErr)
				}
			}
		})
	}
}

func TestOpenMemberReauthenticatesPrivateCopy(t *testing.T) {
	f := newImportFixture(t, runtime.GOOS)
	inputs, err := importWith(context.Background(), f.transaction, f.options, importConfig{targetOS: runtime.GOOS, targetArch: "amd64", now: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), testRoot: true})
	if err != nil {
		t.Fatal(err)
	}
	member := memberNames(runtime.GOOS)[0]
	if err := os.WriteFile(filepath.Join(f.transaction.Name(), inputsName, member), []byte("altered imported bytes"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := inputs.OpenMember(context.Background(), member); err == nil {
		t.Fatal("accepted changed private member")
	}
	_ = inputs.Close()
}

func TestOpenMemberRejectsPathReplacement(t *testing.T) {
	f := newImportFixture(t, runtime.GOOS)
	inputs, err := importWith(context.Background(), f.transaction, f.options, importConfig{targetOS: runtime.GOOS, targetArch: "amd64", now: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), testRoot: true})
	if err != nil {
		t.Fatal(err)
	}
	member := memberNames(runtime.GOOS)[0]
	base := filepath.Join(f.transaction.Name(), inputsName)
	path := filepath.Join(base, member)
	moved := path + ".saved"
	if err := os.Rename(path, moved); err != nil {
		_ = inputs.Close()
		t.Fatal(err)
	}
	if err := os.WriteFile(path, f.files[member], 0600); err != nil {
		_ = inputs.Close()
		t.Fatal(err)
	}
	if _, err := inputs.OpenMember(context.Background(), member); err == nil || !strings.Contains(err.Error(), "authenticated input import failed: member identity changed") {
		_ = inputs.Close()
		t.Fatalf("replacement path was not rejected at its fixed stage: %v", err)
	}
	if err := inputs.Close(); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(path); err != nil || !bytes.Equal(body, f.files[member]) {
		t.Fatalf("replacement was not preserved: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(moved); err != nil {
		t.Fatal(err)
	}
}

func TestFailedCleanupPreservesReplacement(t *testing.T) {
	f := newImportFixture(t, runtime.GOOS)
	config := importConfig{targetOS: runtime.GOOS, targetArch: "amd64", now: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), testRoot: true, afterCopy: func(name string, _, destination *os.Root) error {
		if name != metadataName {
			return nil
		}
		base := destination.Name()
		if err := os.Rename(filepath.Join(base, metadataName), filepath.Join(base, "foreign-original")); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(base, metadataName), []byte("foreign replacement"), 0600)
	}}
	if _, err := importWith(context.Background(), f.transaction, f.options, config); err == nil {
		t.Fatal("accepted replaced output")
	} else if !strings.Contains(err.Error(), "authenticated input import failed: partial private input cleanup was incomplete") {
		t.Fatalf("cleanup failure was not propagated: %v", err)
	}
	replacement, err := os.ReadFile(filepath.Join(f.transaction.Name(), inputsName, metadataName))
	if err != nil || string(replacement) != "foreign replacement" {
		t.Fatalf("replacement was not preserved: %q %v", replacement, err)
	}
	original, err := os.ReadFile(filepath.Join(f.transaction.Name(), inputsName, "foreign-original"))
	if err != nil || len(original) == 0 {
		t.Fatalf("renamed foreign entry was not preserved: %v", err)
	}
}

func TestFailedCleanupReportsMissingOrReplacedInputsRoot(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		name := "missing"
		if replacement {
			name = "replaced"
		}
		t.Run(name, func(t *testing.T) {
			f := newImportFixture(t, runtime.GOOS)
			cause := errors.New("injected import stop")
			config := importConfig{targetOS: runtime.GOOS, targetArch: "amd64", now: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), testRoot: true, afterCopy: func(copied string, _, _ *os.Root) error {
				if copied != metadataName {
					return nil
				}
				if err := f.transaction.Rename(inputsName, "inputs-moved"); err != nil {
					return err
				}
				if replacement {
					if err := f.transaction.Mkdir(inputsName, 0700); err != nil {
						return err
					}
					if err := os.WriteFile(filepath.Join(f.transaction.Name(), inputsName, "foreign"), []byte("replacement"), 0600); err != nil {
						return err
					}
				}
				return cause
			}}
			_, err := importWith(context.Background(), f.transaction, f.options, config)
			if !errors.Is(err, cause) {
				t.Fatalf("original import cause was lost: %v", err)
			}
			if err == nil || !strings.Contains(err.Error(), "authenticated input import failed: partial private input cleanup was incomplete") {
				t.Fatalf("incomplete cleanup was not reported: %v", err)
			}
			moved, openErr := f.transaction.OpenRoot("inputs-moved")
			if openErr != nil {
				t.Fatal(openErr)
			}
			entries, readErr := readEntries(moved)
			_ = moved.Close()
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("owned files were not cleaned through the held directory root: entries=%d err=%v", len(entries), readErr)
			}
			if replacement {
				body, readErr := os.ReadFile(filepath.Join(f.transaction.Name(), inputsName, "foreign"))
				if readErr != nil || string(body) != "replacement" {
					t.Fatalf("replacement directory contents were not preserved: %q %v", body, readErr)
				}
				if err := f.transaction.Remove(inputsName); err == nil {
					t.Fatal("removed non-empty replacement directory")
				}
				if err := os.Remove(filepath.Join(f.transaction.Name(), inputsName, "foreign")); err != nil {
					t.Fatal(err)
				}
				if err := f.transaction.Remove(inputsName); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.transaction.Remove("inputs-moved"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func hashText(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }
func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

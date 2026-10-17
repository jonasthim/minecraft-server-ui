package installinputs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/jonasthim/minecraft-server-ui/release/candidate"
)

type RecordedOptions struct {
	Version          string
	Trust            candidate.Trust
	AllowDevelopment bool
	ManifestSHA256   string
}

type recordedConfig struct {
	targetOS, targetArch string
	now                  time.Time
	testRoot             bool
	afterRead            func(name string, transaction, inputs *os.Root) error
}

// OpenRecorded reopens the exact authenticated input set previously imported
// into transaction/inputs. It is read-only and retains file pins for member
// reads until the returned Inputs is closed.
func OpenRecorded(ctx context.Context, transaction *os.Root, options RecordedOptions) (*Inputs, error) {
	return openRecordedWith(ctx, transaction, options, recordedConfig{targetOS: runtime.GOOS, targetArch: runtime.GOARCH, now: time.Now().UTC()})
}

func openRecordedWith(ctx context.Context, transaction *os.Root, options RecordedOptions, config recordedConfig) (_ *Inputs, retErr error) {
	options.Version = strings.Clone(options.Version)
	trust, err := cloneTrust(options.Trust)
	if err != nil {
		return nil, fail("recorded trust is invalid", err)
	}
	options.Trust = trust
	if ctx == nil || config.now.IsZero() || (config.targetOS != "linux" && config.targetOS != "windows") || config.targetArch != "amd64" {
		return nil, fail("unsupported host or transaction", nil)
	}
	if options.Version == "" || options.Version == "latest" || !validManifestDigest(options.ManifestSHA256) {
		return nil, fail("recorded version or manifest identity is invalid", nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, fail("cancelled", err)
	}
	if err := verifyTransactionBinding(transaction, config.testRoot); err != nil {
		return nil, fail("transaction root is not private", err)
	}
	directoryInfo, err := transaction.Lstat(inputsName)
	if err != nil || !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fail("recorded inputs directory is unsafe", err)
	}
	root, err := transaction.OpenRoot(inputsName)
	if err != nil {
		return nil, fail("recorded inputs directory could not be opened", err)
	}
	openedDirectoryInfo, err := root.Stat(".")
	if err != nil || !os.SameFile(directoryInfo, openedDirectoryInfo) || validateRecordedInputsDirectory(root, openedDirectoryInfo, config.testRoot) != nil {
		_ = root.Close()
		return nil, fail("recorded inputs directory identity or permissions are unsafe", err)
	}
	rootOpen := true
	files := make(map[string]ownedFile, maxPreparedEntries)
	closeOwned := func() error {
		var closeErr error
		for name, file := range files {
			if file.pin != nil {
				closeErr = errors.Join(closeErr, file.pin.Close())
				file.pin = nil
				files[name] = file
			}
		}
		if rootOpen {
			rootOpen = false
			closeErr = errors.Join(closeErr, root.Close())
		}
		return closeErr
	}
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, closeOwned())
		}
	}()
	if err := verifyRecordedInputsRoot(transaction, root, directoryInfo, config.testRoot); err != nil {
		return nil, fail("recorded inputs root changed", err)
	}
	entries, err := readEntries(root)
	if err != nil {
		return nil, fail("recorded inputs file set could not be read", err)
	}
	initial := make(map[string]os.FileInfo, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !validRecordedName(name) || entry.IsDir() {
			return nil, fail("recorded inputs file set is unsafe", nil)
		}
		info, err := root.Lstat(name)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fail("recorded inputs member is unsafe", err)
		}
		initial[name] = info
	}
	read := func(name string, limit int64, mode os.FileMode) ([]byte, error) {
		body, owned, err := readRecordedFile(ctx, root, name, limit, mode, initial[name], config.testRoot)
		if err != nil {
			return nil, err
		}
		files[name] = owned
		if config.afterRead != nil {
			if err := config.afterRead(name, transaction, root); err != nil {
				return nil, err
			}
		}
		return body, nil
	}
	metadata, err := read(metadataName, manifestLimit, 0600)
	if err != nil {
		return nil, fail("recorded bootstrap metadata is unsafe", err)
	}
	signature, err := read(signatureName, signatureLimit, 0600)
	if err != nil {
		return nil, fail("recorded bootstrap signature is unsafe", err)
	}
	receiptBytes, err := read(receiptName, receiptLimit, 0600)
	if err != nil {
		return nil, fail("recorded completion receipt is unsafe", err)
	}
	manifestDigest := sha256.Sum256(metadata)
	if hex.EncodeToString(manifestDigest[:]) != options.ManifestSHA256 {
		return nil, fail("recorded bootstrap manifest digest differs from intent", nil)
	}
	manifest, err := candidate.VerifyBootstrap(options.Trust, metadata, signature, options.Version, config.now)
	if err != nil || manifest.Trust == "DEVELOPMENT ONLY" && !options.AllowDevelopment {
		return nil, fail("recorded bootstrap signature or policy is invalid", err)
	}
	artifact := artifactForOS(manifest, config.targetOS)
	if artifact.OS != config.targetOS || artifact.Arch != config.targetArch {
		return nil, fail("recorded bootstrap platform does not match host", nil)
	}
	receipt, err := decodeReceipt(receiptBytes)
	if err != nil || !receiptMatches(receipt, manifest, artifact, metadata) {
		return nil, fail("recorded completion receipt does not match signed manifest", err)
	}
	expectedNames := []string{metadataName, signatureName, receiptName}
	for _, member := range artifact.Members {
		expectedNames = append(expectedNames, member.Name)
	}
	if !sameNameSlice(mapKeys(initial), expectedNames) {
		return nil, fail("recorded inputs set differs from signed manifest", nil)
	}
	for index, member := range artifact.Members {
		limit := candidate.MaxAssetBytes
		// The bootstrap contract treats the first two members (manager and
		// updater) as binaries on both platforms. Keep this positional rule in
		// sync with candidate.validateBundle and the producer.
		if index < 2 {
			limit = candidate.MaxBinaryBytes
		}
		owned, err := pinRecordedMember(ctx, root, member, limit, config.testRoot)
		if err != nil {
			return nil, fail("recorded member differs from signed manifest", err)
		}
		files[member.Name] = owned
		if config.afterRead != nil {
			if err := config.afterRead(member.Name, transaction, root); err != nil {
				return nil, fail("recorded input changed during inspection", err)
			}
		}
	}
	if err := verifyRecordedSet(ctx, transaction, root, directoryInfo, files, expectedNames, config.testRoot); err != nil {
		return nil, fail("recorded inputs changed during inspection", err)
	}
	manifestHash := sha256.Sum256(metadata)
	result := &Inputs{root: root, files: files, members: cloneMembers(artifact.Members), summary: Summary{Version: manifest.Version, Schema: manifest.Schema, OS: artifact.OS, Arch: artifact.Arch, ManifestSHA256: hex.EncodeToString(manifestHash[:]), MemberCount: len(artifact.Members)}}
	rootOpen = false
	return result, nil
}

func validManifestDigest(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validRecordedName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\\`) && len(name) <= 255
}

func mapKeys(values map[string]os.FileInfo) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func readRecordedFile(ctx context.Context, root *os.Root, name string, limit int64, mode os.FileMode, expected os.FileInfo, testRoot bool) ([]byte, ownedFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, ownedFile{}, err
	}
	if expected == nil || expected.Size() < 1 || expected.Size() > limit {
		return nil, ownedFile{}, errors.New("recorded file size is invalid")
	}
	if err := validateRecordedInputMetadata(nil, expected, mode, testRoot); err != nil {
		return nil, ownedFile{}, err
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, ownedFile{}, err
	}
	info, err := f.Stat()
	if err != nil || !os.SameFile(expected, info) || validateRecordedInputMetadata(f, info, mode, testRoot) != nil {
		_ = f.Close()
		return nil, ownedFile{}, errors.Join(errors.New("recorded file handle is unsafe"), err)
	}
	body, readErr := io.ReadAll(io.LimitReader(f, limit+1))
	after, statErr := f.Stat()
	pathInfo, pathErr := root.Lstat(name)
	if readErr != nil || statErr != nil || pathErr != nil || int64(len(body)) != info.Size() || int64(len(body)) > limit || !sameRecordedFileMetadata(info, after) || !os.SameFile(after, pathInfo) || validateRecordedInputMetadata(f, after, mode, testRoot) != nil {
		_ = f.Close()
		return nil, ownedFile{}, errors.Join(errors.New("recorded file changed while reading"), readErr, statErr, pathErr)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, ownedFile{}, err
	}
	return bytes.Clone(body), ownedFile{name: name, info: after, pin: f, hash: digest(body), mode: mode}, nil
}

func pinRecordedMember(ctx context.Context, root *os.Root, member candidate.Member, limit int64, testRoot bool) (ownedFile, error) {
	if err := ctx.Err(); err != nil {
		return ownedFile{}, err
	}
	name := member.Name
	before, err := root.Lstat(name)
	if err != nil || before.Size() != member.Size || before.Size() < 1 || before.Size() > limit || validateRecordedInputMetadata(nil, before, os.FileMode(member.Mode), testRoot) != nil {
		return ownedFile{}, errors.Join(errors.New("recorded member metadata is unsafe"), err)
	}
	f, err := root.Open(name)
	if err != nil {
		return ownedFile{}, err
	}
	info, err := f.Stat()
	if err != nil || !os.SameFile(before, info) || validateRecordedInputMetadata(f, info, os.FileMode(member.Mode), testRoot) != nil {
		_ = f.Close()
		return ownedFile{}, errors.Join(errors.New("recorded member handle is unsafe"), err)
	}
	hash, size, err := hashHandle(ctx, f, limit)
	if err != nil || size != member.Size || hash != member.SHA256 {
		_ = f.Close()
		return ownedFile{}, errors.Join(errors.New("recorded member digest or size differs from signed manifest"), err)
	}
	after, statErr := f.Stat()
	pathInfo, pathErr := root.Lstat(name)
	if statErr != nil || pathErr != nil || !sameRecordedFileMetadata(info, after) || !os.SameFile(after, pathInfo) || validateRecordedInputMetadata(f, after, os.FileMode(member.Mode), testRoot) != nil {
		_ = f.Close()
		return ownedFile{}, errors.Join(errors.New("recorded member changed while hashing"), statErr, pathErr)
	}
	return ownedFile{name: name, info: after, pin: f, hash: hash, mode: os.FileMode(member.Mode)}, nil
}

func verifyRecordedInputsRoot(transaction, root *os.Root, directoryInfo os.FileInfo, testRoot bool) error {
	if err := verifyTransactionBinding(transaction, testRoot); err != nil {
		return err
	}
	opened, err := root.Stat(".")
	pathInfo, pathErr := transaction.Lstat(inputsName)
	if err != nil || pathErr != nil || !os.SameFile(directoryInfo, opened) || !os.SameFile(opened, pathInfo) {
		return errors.Join(errors.New("recorded inputs directory identity changed"), err, pathErr)
	}
	return validateRecordedInputsDirectory(root, opened, testRoot)
}

func verifyRecordedSet(ctx context.Context, transaction, root *os.Root, directoryInfo os.FileInfo, files map[string]ownedFile, expected []string, testRoot bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := verifyRecordedInputsRoot(transaction, root, directoryInfo, testRoot); err != nil {
		return err
	}
	entries, err := readEntries(root)
	if err != nil {
		return err
	}
	actual := make([]string, 0, len(entries))
	for _, entry := range entries {
		actual = append(actual, entry.Name())
	}
	if !sameNameSlice(actual, expected) || len(files) != len(expected) {
		return errors.New("recorded file set changed")
	}
	for _, name := range expected {
		owned, ok := files[name]
		if !ok || owned.pin == nil || owned.name != name {
			return errors.New("recorded file pin is missing")
		}
		info, err := owned.pin.Stat()
		pathInfo, pathErr := root.Lstat(name)
		if err != nil || pathErr != nil || !sameRecordedFileMetadata(owned.info, info) || !os.SameFile(info, pathInfo) || validateRecordedInputMetadata(owned.pin, info, owned.mode, testRoot) != nil {
			return errors.Join(errors.New("recorded file identity changed"), err, pathErr)
		}
		hash, size, err := hashHandle(ctx, owned.pin, info.Size())
		if err != nil || size != info.Size() || hash != owned.hash {
			return errors.Join(errors.New("recorded file digest changed after capture"), err)
		}
		after, statErr := owned.pin.Stat()
		pathAfter, pathAfterErr := root.Lstat(name)
		if statErr != nil || pathAfterErr != nil || !sameRecordedFileMetadata(info, after) || !os.SameFile(after, pathAfter) {
			return errors.Join(errors.New("recorded file changed during final digest check"), statErr, pathAfterErr)
		}
	}
	return verifyRecordedInputsRoot(transaction, root, directoryInfo, testRoot)
}

func sameRecordedFileMetadata(a, b os.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b) && a.Size() == b.Size() && a.Mode() == b.Mode() && a.ModTime().Equal(b.ModTime())
}

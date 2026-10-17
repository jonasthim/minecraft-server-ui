// Package installinputs reauthenticates prepared bootstrap inputs and imports
// them into a protected, transaction-local directory. It never executes them.
package installinputs

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
	"github.com/jonasthim/minecraft-server-ui/release/candidate"
)

const (
	metadataName   = "bootstrap.json"
	signatureName  = "bootstrap.sig"
	receiptName    = "completion.json"
	inputsName     = "inputs"
	manifestLimit  = candidate.MaxBootstrapBytes
	signatureLimit = 1024
	receiptLimit   = 64 << 10
	copyBufferSize = 128 << 10
	// The frozen bootstrap layout permits at most 14 Linux members plus the
	// three authenticated metadata files. Use that maximum for both source and
	// destination enumeration, including one extra entry as an overflow sentinel.
	maxPreparedEntries = 17
)

var errInputsDirectoryCreated = errors.New("private inputs directory was created before validation failed")

type Options struct {
	Prepared         string
	Version          string
	Trust            candidate.Trust
	AllowDevelopment bool
}

type Summary struct {
	Version        string `json:"version"`
	Schema         int64  `json:"schema"`
	OS             string `json:"os"`
	Arch           string `json:"arch"`
	ManifestSHA256 string `json:"manifest_sha256"`
	MemberCount    int    `json:"member_count"`
}

type completionReceipt struct {
	Format         int                `json:"format"`
	Repository     string             `json:"repository"`
	Version        string             `json:"version"`
	Schema         int64              `json:"schema"`
	OS             string             `json:"os"`
	Arch           string             `json:"arch"`
	Trust          string             `json:"trust"`
	ManifestSHA256 string             `json:"manifest_sha256"`
	Members        []candidate.Member `json:"members"`
}

type stageError struct {
	stage string
	cause error
}

func (e *stageError) Error() string { return "authenticated input import failed: " + e.stage }
func (e *stageError) Unwrap() error { return e.cause }

func fail(stage string, cause error) error { return &stageError{stage: stage, cause: cause} }

type ownedFile struct {
	name string
	info os.FileInfo
	pin  *os.File
}

type sourceFile struct {
	name string
	info os.FileInfo
	file *os.File
	hash string
}

type importConfig struct {
	targetOS              string
	targetArch            string
	now                   time.Time
	testRoot              bool
	afterCopy             func(name string, prepared, destination *os.Root) error
	createInputsDirectory func(parent *os.Root, testRoot bool) (*os.Root, os.FileInfo, error)
}

// Inputs is an authenticated, private copy of one prepared native artifact.
// Its handles remain open until Close; callers cannot choose output paths.
type Inputs struct {
	mu      sync.Mutex
	root    *os.Root
	files   map[string]ownedFile
	members []candidate.Member
	summary Summary
	closed  bool
}

// Import authenticates a prepared input directory with independently supplied
// trust and exclusively creates transaction/inputs on a production-private
// transaction root.
func Import(ctx context.Context, transaction *os.Root, options Options) (*Inputs, error) {
	return importWith(ctx, transaction, options, importConfig{targetOS: runtime.GOOS, targetArch: runtime.GOARCH, now: time.Now().UTC()})
}

func importWith(ctx context.Context, transaction *os.Root, options Options, config importConfig) (_ *Inputs, retErr error) {
	options.Prepared = strings.Clone(options.Prepared)
	options.Version = strings.Clone(options.Version)
	if err := ctx.Err(); err != nil {
		return nil, fail("cancelled", err)
	}
	if transaction == nil || config.now.IsZero() || (config.targetOS != "linux" && config.targetOS != "windows") || config.targetArch != "amd64" {
		return nil, fail("unsupported host or transaction", nil)
	}
	if err := validateTransactionRoot(transaction, config.testRoot); err != nil {
		return nil, fail("transaction root is not private", err)
	}
	trust, err := cloneTrust(options.Trust)
	if err != nil {
		return nil, fail("trust is invalid", err)
	}
	if !filepath.IsAbs(options.Prepared) || filepath.Clean(options.Prepared) != options.Prepared {
		return nil, fail("prepared directory is invalid", nil)
	}
	if options.Version == "" || options.Version == "latest" {
		return nil, fail("exact version is required", nil)
	}
	prepared, err := safepath.OpenRoot(ctx, options.Prepared)
	if err != nil {
		return nil, fail("prepared directory is unsafe", err)
	}
	preparedOpen := true
	defer func() {
		if !preparedOpen {
			return
		}
		if err := prepared.Close(); err != nil {
			retErr = errors.Join(retErr, fail("prepared directory close was incomplete", err))
		}
	}()
	if err := verifyPreparedBinding(ctx, options.Prepared, prepared); err != nil {
		return nil, fail("prepared directory binding changed", err)
	}
	entries, err := readEntries(prepared)
	if err != nil {
		return nil, fail("prepared file set could not be read", err)
	}
	initial := make(map[string]os.FileInfo, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !filepath.IsLocal(name) || strings.ContainsAny(name, `/\\`) || entry.IsDir() {
			return nil, fail("prepared file set is unsafe", nil)
		}
		info, err := prepared.Lstat(name)
		if err != nil || !info.Mode().IsRegular() || checkSourcePath(prepared, name, info) != nil {
			return nil, fail("prepared file is unsafe", err)
		}
		initial[name] = info
	}
	metadata, metadataFile, err := readSourceFile(ctx, prepared, metadataName, manifestLimit, initial[metadataName])
	if err != nil {
		return nil, fail("bootstrap metadata could not be read", err)
	}
	signature, signatureFile, err := readSourceFile(ctx, prepared, signatureName, signatureLimit, initial[signatureName])
	if err != nil {
		_ = metadataFile.file.Close()
		return nil, fail("bootstrap signature could not be read", err)
	}
	receiptBytes, receiptFile, err := readSourceFile(ctx, prepared, receiptName, receiptLimit, initial[receiptName])
	if err != nil {
		_ = metadataFile.file.Close()
		_ = signatureFile.file.Close()
		return nil, fail("completion receipt could not be read", err)
	}
	sourceFiles := map[string]sourceFile{metadataName: metadataFile, signatureName: signatureFile, receiptName: receiptFile}
	sourceFilesOpen := true
	defer func() {
		if !sourceFilesOpen {
			return
		}
		for _, file := range sourceFiles {
			if err := file.file.Close(); err != nil {
				retErr = errors.Join(retErr, fail("prepared file close was incomplete", err))
			}
		}
	}()
	manifest, err := candidate.VerifyBootstrap(trust, metadata, signature, options.Version, config.now)
	if err != nil || manifest.Trust == "DEVELOPMENT ONLY" && !options.AllowDevelopment {
		return nil, fail("bootstrap signature or policy is invalid", err)
	}
	artifact := artifactForOS(manifest, config.targetOS)
	if artifact.OS != config.targetOS || artifact.Arch != config.targetArch {
		return nil, fail("authenticated platform does not match host", nil)
	}
	receipt, err := decodeReceipt(receiptBytes)
	if err != nil || !receiptMatches(receipt, manifest, artifact, metadata) {
		return nil, fail("completion receipt does not match authenticated metadata", err)
	}
	expectedNames := []string{metadataName, signatureName, receiptName}
	for _, member := range artifact.Members {
		expectedNames = append(expectedNames, member.Name)
	}
	if !sameNames(initial, expectedNames) {
		return nil, fail("prepared file set does not match authenticated members", nil)
	}
	for _, member := range artifact.Members {
		info := initial[member.Name]
		if info == nil || !info.Mode().IsRegular() || info.Size() != member.Size || checkSourcePath(prepared, member.Name, info) != nil {
			return nil, fail("prepared member identity or size is invalid", nil)
		}
		file, err := openSourceFile(prepared, member.Name, info)
		if err != nil {
			return nil, fail("prepared member could not be opened", err)
		}
		file.hash = member.SHA256
		sourceFiles[member.Name] = file
	}
	if err := verifyPreparedBinding(ctx, options.Prepared, prepared); err != nil {
		return nil, fail("prepared directory binding changed", err)
	}
	createInputs := config.createInputsDirectory
	if createInputs == nil {
		createInputs = createInputsDirectory
	}
	inputsRoot, inputsInfo, err := createInputs(transaction, config.testRoot)
	if err != nil {
		if errors.Is(err, errInputsDirectoryCreated) {
			if inputsRoot != nil && inputsInfo != nil {
				cleanupErr := cleanupInputs(transaction, inputsRoot, inputsInfo, nil)
				if cleanupErr != nil {
					return nil, fail("partial private input cleanup was incomplete", errors.Join(err, cleanupErr))
				}
				return nil, fail("private inputs directory could not be created", err)
			}
			return nil, fail("partial private input cleanup was incomplete", err)
		}
		return nil, fail("private inputs directory could not be created", err)
	}
	owned := make(map[string]ownedFile, len(expectedNames))
	published := false
	var installed *Inputs
	defer func() {
		if published {
			return
		}
		cleanupErr := cleanupInputs(transaction, inputsRoot, inputsInfo, owned)
		if cleanupErr != nil {
			retErr = errors.Join(retErr, fail("partial private input cleanup was incomplete", cleanupErr))
		}
		if installed != nil {
			installed.root = nil
			installed.files = nil
		}
	}()
	if err := verifyTransactionBinding(transaction, config.testRoot); err != nil {
		return nil, fail("transaction root binding changed", err)
	}
	if err := syncDirectory(transaction); err != nil {
		return nil, fail("transaction directory sync failed", err)
	}
	manifestHash := sha256.Sum256(metadata)
	for _, item := range []struct {
		name string
		body []byte
		mode os.FileMode
	}{{metadataName, metadata, 0600}, {signatureName, signature, 0600}, {receiptName, receiptBytes, 0600}} {
		if err := ctx.Err(); err != nil {
			return nil, fail("cancelled", err)
		}
		file, err := createOutput(inputsRoot, item.name, item.mode, &owned)
		if err != nil {
			return nil, fail("metadata copy could not be created", err)
		}
		if err := writeOutput(file, item.body, item.mode); err != nil {
			return nil, fail("metadata copy failed", err)
		}
		if err := verifyOwnedFile(ctx, inputsRoot, owned[item.name], int64(len(item.body)), digest(item.body)); err != nil {
			return nil, fail("metadata copy verification failed", err)
		}
		if config.afterCopy != nil {
			if err := config.afterCopy(item.name, prepared, inputsRoot); err != nil {
				return nil, fail("cancelled or changed during import", err)
			}
		}
	}
	for _, member := range artifact.Members {
		if err := ctx.Err(); err != nil {
			return nil, fail("cancelled", err)
		}
		source := sourceFiles[member.Name]
		output, err := createOutput(inputsRoot, member.Name, os.FileMode(member.Mode), &owned)
		if err != nil {
			return nil, fail("member copy could not be created", err)
		}
		if err := copyMember(ctx, source, output, member); err != nil {
			return nil, fail("member copy or authentication failed", err)
		}
		if err := verifyOwnedFile(ctx, inputsRoot, owned[member.Name], member.Size, member.SHA256); err != nil {
			return nil, fail("member copy verification failed", err)
		}
		if config.afterCopy != nil {
			if err := config.afterCopy(member.Name, prepared, inputsRoot); err != nil {
				return nil, fail("cancelled or changed during import", err)
			}
		}
	}
	if err := verifySourceSet(ctx, options.Prepared, prepared, sourceFiles, initial, expectedNames); err != nil {
		return nil, fail("prepared source changed during import", err)
	}
	if err := verifyDestinationSet(inputsRoot, owned, expectedNames); err != nil {
		return nil, fail("private input set is incomplete", err)
	}
	if err := verifyTransactionBinding(transaction, config.testRoot); err != nil {
		return nil, fail("transaction root binding changed", err)
	}
	for _, source := range sourceFiles {
		if err := source.file.Close(); err != nil {
			return nil, fail("prepared file close was incomplete", err)
		}
	}
	sourceFilesOpen = false
	if err := prepared.Close(); err != nil {
		return nil, fail("prepared directory close was incomplete", err)
	}
	preparedOpen = false
	summary := Summary{Version: manifest.Version, Schema: manifest.Schema, OS: artifact.OS, Arch: artifact.Arch, ManifestSHA256: hex.EncodeToString(manifestHash[:]), MemberCount: len(artifact.Members)}
	installed = &Inputs{root: inputsRoot, files: owned, members: cloneMembers(artifact.Members), summary: summary}
	published = true
	return installed, nil
}

func (inputs *Inputs) Summary() Summary {
	if inputs == nil {
		return Summary{}
	}
	inputs.mu.Lock()
	defer inputs.mu.Unlock()
	return inputs.summary
}

func (inputs *Inputs) Members() []candidate.Member {
	if inputs == nil {
		return nil
	}
	inputs.mu.Lock()
	defer inputs.mu.Unlock()
	return cloneMembers(inputs.members)
}

func (inputs *Inputs) OpenMember(ctx context.Context, name string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, fail("cancelled", err)
	}
	if inputs == nil {
		return nil, fail("input snapshot is unavailable", nil)
	}
	inputs.mu.Lock()
	defer inputs.mu.Unlock()
	if inputs.closed || inputs.root == nil {
		return nil, fail("input snapshot is closed", nil)
	}
	var member *candidate.Member
	for i := range inputs.members {
		if inputs.members[i].Name == name {
			member = &inputs.members[i]
			break
		}
	}
	if member == nil {
		return nil, fail("member name is not authenticated", nil)
	}
	owned, ok := inputs.files[name]
	if !ok || owned.pin == nil {
		return nil, fail("member identity is unavailable", nil)
	}
	f, err := inputs.root.Open(name)
	if err != nil {
		return nil, fail("member could not be opened", err)
	}
	info, err := f.Stat()
	current, currentErr := inputs.root.Lstat(name)
	if err != nil || currentErr != nil || !info.Mode().IsRegular() || checkOpenFile(f, info) != nil || !os.SameFile(info, current) || !sameFileIdentity(owned, info) {
		_ = f.Close()
		return nil, fail("member identity changed", err)
	}
	if err := verifyHandleDigest(ctx, f, *member); err != nil {
		_ = f.Close()
		return nil, fail("member content changed", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, fail("member could not be rewound", err)
	}
	return f, nil
}

func (inputs *Inputs) Close() error {
	if inputs == nil {
		return nil
	}
	inputs.mu.Lock()
	defer inputs.mu.Unlock()
	if inputs.closed {
		return nil
	}
	inputs.closed = true
	var closeErr error
	for name, file := range inputs.files {
		if file.pin != nil {
			if err := file.pin.Close(); err != nil {
				closeErr = errors.Join(closeErr, err)
			}
			file.pin = nil
			inputs.files[name] = file
		}
	}
	if inputs.root != nil {
		closeErr = errors.Join(closeErr, inputs.root.Close())
		inputs.root = nil
	}
	if closeErr != nil {
		return fail("input handle close was incomplete", closeErr)
	}
	return nil
}

func cloneMembers(members []candidate.Member) []candidate.Member {
	return append([]candidate.Member(nil), members...)
}

func cloneTrust(trust candidate.Trust) (candidate.Trust, error) {
	if len(trust.PublicKeys) < 1 || len(trust.PublicKeys) > 16 {
		return candidate.Trust{}, errors.New("trust key count is out of bounds")
	}
	cloned := candidate.Trust{Repository: strings.Clone(trust.Repository), PublicKeys: make(map[string]ed25519.PublicKey, len(trust.PublicKeys))}
	for id, key := range trust.PublicKeys {
		if len(key) != ed25519.PublicKeySize {
			return candidate.Trust{}, errors.New("trust key size is invalid")
		}
		cloned.PublicKeys[strings.Clone(id)] = append(ed25519.PublicKey(nil), key...)
	}
	return cloned, nil
}

func artifactForOS(manifest candidate.Bootstrap, targetOS string) candidate.Bundle {
	for _, artifact := range manifest.Artifacts {
		if artifact.OS == targetOS {
			return artifact
		}
	}
	return candidate.Bundle{}
}

func receiptMatches(receipt completionReceipt, manifest candidate.Bootstrap, artifact candidate.Bundle, metadata []byte) bool {
	digest := sha256.Sum256(metadata)
	return receipt.Format == 1 && receipt.Repository == manifest.Repository && receipt.Version == manifest.Version && receipt.Schema == manifest.Schema && receipt.OS == artifact.OS && receipt.Arch == artifact.Arch && receipt.Trust == manifest.Trust && receipt.ManifestSHA256 == hex.EncodeToString(digest[:]) && reflect.DeepEqual(receipt.Members, artifact.Members)
}

func decodeReceipt(body []byte) (completionReceipt, error) {
	var receipt completionReceipt
	if len(body) == 0 || len(body) > receiptLimit {
		return receipt, errors.New("receipt size is invalid")
	}
	if err := rejectDuplicateJSONKeys(body); err != nil {
		return receipt, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return receipt, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return receipt, errors.New("receipt has trailing data")
	}
	return receipt, nil
}

func rejectDuplicateJSONKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return errors.New("receipt has duplicate object keys")
				}
				seen[name] = true
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return errors.New("receipt JSON delimiter is invalid")
		}
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("receipt has trailing data")
	}
	return nil
}

func readSourceFile(ctx context.Context, root *os.Root, name string, limit int64, expected os.FileInfo) ([]byte, sourceFile, error) {
	if expected == nil || expected.Size() < 1 || expected.Size() > limit {
		return nil, sourceFile{}, errors.New("source file size is invalid")
	}
	file, err := openSourceFile(root, name, expected)
	if err != nil {
		return nil, sourceFile{}, err
	}
	body, err := io.ReadAll(io.LimitReader(file.file, limit+1))
	if err != nil || int64(len(body)) != expected.Size() || int64(len(body)) > limit {
		_ = file.file.Close()
		return nil, sourceFile{}, errors.Join(err, errors.New("source file read exceeded bounds"))
	}
	if _, err := file.file.Seek(0, io.SeekStart); err != nil {
		_ = file.file.Close()
		return nil, sourceFile{}, err
	}
	h := sha256.Sum256(body)
	file.hash = hex.EncodeToString(h[:])
	if err := verifySourceFile(ctx, root, file, file.hash); err != nil {
		_ = file.file.Close()
		return nil, sourceFile{}, err
	}
	return bytes.Clone(body), file, nil
}

func openSourceFile(root *os.Root, name string, expected os.FileInfo) (sourceFile, error) {
	if expected == nil {
		return sourceFile{}, errors.New("source file is missing")
	}
	f, err := root.Open(name)
	if err != nil {
		return sourceFile{}, err
	}
	info, err := f.Stat()
	pathInfo, pathErr := root.Lstat(name)
	if err != nil || pathErr != nil || !info.Mode().IsRegular() || !os.SameFile(expected, info) || !os.SameFile(info, pathInfo) || checkSourceOpen(f, info) != nil {
		_ = f.Close()
		return sourceFile{}, errors.New("source file binding is unsafe")
	}
	return sourceFile{name: name, info: info, file: f}, nil
}

func copyMember(ctx context.Context, source sourceFile, destination *os.File, member candidate.Member) error {
	if source.info.Size() != member.Size {
		return errors.New("signed member size mismatch")
	}
	if _, err := source.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	hash := sha256.New()
	buf := make([]byte, copyBufferSize)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := source.file.Read(buf)
		if n > 0 {
			if size > member.Size-int64(n) {
				return errors.New("signed member grew during copy")
			}
			written, writeErr := destination.Write(buf[:n])
			if writeErr != nil || written != n {
				return errors.Join(writeErr, io.ErrShortWrite)
			}
			_, _ = hash.Write(buf[:n])
			size += int64(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if size != member.Size || hex.EncodeToString(hash.Sum(nil)) != member.SHA256 {
		return errors.New("signed member digest or size mismatch")
	}
	if err := setOutputMode(destination, os.FileMode(member.Mode)); err != nil {
		return err
	}
	if err := destination.Sync(); err != nil {
		return err
	}
	return nil
}

func writeOutput(file *os.File, body []byte, mode os.FileMode) error {
	n, err := file.Write(body)
	if err != nil || n != len(body) {
		return errors.Join(err, io.ErrShortWrite)
	}
	if err := setOutputMode(file, mode); err != nil {
		return err
	}
	return file.Sync()
}

func verifyOwnedFile(ctx context.Context, root *os.Root, owned ownedFile, size int64, expectedHash string) error {
	if owned.pin == nil {
		return errors.New("owned file pin is missing")
	}
	info, err := owned.pin.Stat()
	if err != nil || !os.SameFile(owned.info, info) || info.Size() != size || !info.Mode().IsRegular() || checkOpenFile(owned.pin, info) != nil {
		return errors.New("owned file identity or size changed")
	}
	current, err := root.Lstat(owned.name)
	if err != nil || !os.SameFile(info, current) || checkSourcePath(root, owned.name, current) != nil {
		return errors.New("owned file path changed")
	}
	hash, gotSize, err := hashHandle(ctx, owned.pin, size)
	if err != nil {
		return errors.Join(errors.New("owned file digest changed"), err)
	}
	if gotSize != size || hash != expectedHash {
		return errors.New("owned file digest changed")
	}
	return nil
}

func verifyHandleDigest(ctx context.Context, file *os.File, member candidate.Member) error {
	info, err := file.Stat()
	if err != nil || info.Size() != member.Size || !info.Mode().IsRegular() || checkOpenFile(file, info) != nil {
		return errors.New("member handle metadata changed")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	hash := sha256.New()
	buf := make([]byte, copyBufferSize)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := file.Read(buf)
		if n > 0 {
			size += int64(n)
			_, _ = hash.Write(buf[:n])
			if size > member.Size {
				return errors.New("member handle grew")
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if size != member.Size || hex.EncodeToString(hash.Sum(nil)) != member.SHA256 {
		return errors.New("member handle digest mismatch")
	}
	return nil
}

func hashHandle(ctx context.Context, file *os.File, limit int64) (sum string, size int64, retErr error) {
	if limit < 0 {
		return "", 0, errors.New("file hash limit is invalid")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", 0, err
	}
	defer func() {
		_, seekErr := file.Seek(0, io.SeekStart)
		retErr = errors.Join(retErr, seekErr)
		if retErr != nil {
			sum = ""
		}
	}()
	hash := sha256.New()
	buf := make([]byte, copyBufferSize)
	for {
		if err := ctx.Err(); err != nil {
			return "", size, err
		}
		n, readErr := file.Read(buf)
		if n > 0 {
			if int64(n) > limit-size {
				return "", size, errors.New("file exceeds hash limit")
			}
			_, _ = hash.Write(buf[:n])
			size += int64(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", size, readErr
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func verifySourceSet(ctx context.Context, preparedPath string, root *os.Root, files map[string]sourceFile, initial map[string]os.FileInfo, expected []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := verifyPreparedBinding(ctx, preparedPath, root); err != nil {
		return err
	}
	entries, err := readEntries(root)
	if err != nil || !sameNames(entriesToInfos(entries), expected) {
		return errors.New("prepared source set changed")
	}
	for _, name := range expected {
		file, ok := files[name]
		if !ok {
			return errors.New("prepared source identity is missing")
		}
		if err := verifySourceFile(ctx, root, file, file.hash); err != nil {
			return err
		}
		current, err := root.Lstat(name)
		if err != nil || !os.SameFile(initial[name], current) {
			return errors.New("prepared source path identity changed")
		}
	}
	return nil
}

func verifySourceFile(ctx context.Context, root *os.Root, file sourceFile, expectedHash string) error {
	before, err := file.file.Stat()
	if err != nil || !os.SameFile(file.info, before) || before.Size() != file.info.Size() || !before.ModTime().Equal(file.info.ModTime()) || checkSourceOpen(file.file, before) != nil {
		return errors.New("prepared source metadata changed")
	}
	current, err := root.Lstat(file.name)
	if err != nil || !os.SameFile(before, current) {
		return errors.New("prepared source path changed")
	}
	hash, size, err := hashHandle(ctx, file.file, before.Size())
	if err != nil {
		return errors.Join(errors.New("prepared source content changed"), err)
	}
	if size != before.Size() || hash != expectedHash {
		return errors.New("prepared source content changed")
	}
	after, err := file.file.Stat()
	current, pathErr := root.Lstat(file.name)
	if err != nil || pathErr != nil || !os.SameFile(before, after) || !os.SameFile(after, current) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return errors.New("prepared source changed during recheck")
	}
	return nil
}

func sameNames(actual map[string]os.FileInfo, expected []string) bool {
	names := make([]string, 0, len(actual))
	for name := range actual {
		names = append(names, name)
	}
	return sameNameSlice(names, expected)
}

func sameNameSlice(actual, expected []string) bool {
	a := append([]string(nil), actual...)
	e := append([]string(nil), expected...)
	sort.Strings(a)
	sort.Strings(e)
	if len(a) != len(e) {
		return false
	}
	for i := range a {
		if a[i] != e[i] {
			return false
		}
	}
	return true
}

func entriesToInfos(entries []os.DirEntry) map[string]os.FileInfo {
	out := make(map[string]os.FileInfo, len(entries))
	for _, entry := range entries {
		out[entry.Name()] = nil
	}
	return out
}

func readEntries(root *os.Root) ([]os.DirEntry, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	entries, readErr := directory.ReadDir(maxPreparedEntries + 1)
	if len(entries) > maxPreparedEntries {
		readErr = errors.Join(readErr, errors.New("directory entry count exceeds bound"))
	}
	if errors.Is(readErr, io.EOF) && len(entries) <= maxPreparedEntries {
		readErr = nil
	}
	return entries, errors.Join(readErr, directory.Close())
}

func digest(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }

func verifyPreparedBinding(ctx context.Context, path string, root *os.Root) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return verifyRootBinding(path, root)
}

func verifyTransactionBinding(root *os.Root, testRoot bool) error {
	if err := validateTransactionRoot(root, testRoot); err != nil {
		return err
	}
	return verifyRootBinding(root.Name(), root)
}

func verifyRootBinding(path string, root *os.Root) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("directory path is not canonical")
	}
	current, err := safepath.OpenRoot(context.Background(), path)
	if err != nil {
		return err
	}
	defer current.Close()
	opened, err := current.Stat(".")
	if err != nil {
		return err
	}
	held, err := root.Stat(".")
	if err != nil || !os.SameFile(held, opened) {
		return errors.New("directory identity changed")
	}
	return nil
}

func verifyDestinationSet(root *os.Root, owned map[string]ownedFile, expected []string) error {
	entries, err := readEntries(root)
	if err != nil {
		return err
	}
	actual := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !filepath.IsLocal(name) || strings.ContainsAny(name, `/\\`) {
			return errors.New("destination name is unsafe")
		}
		actual = append(actual, name)
	}
	if !sameNameSlice(actual, expected) {
		return errors.New("destination set changed")
	}
	for _, name := range expected {
		file, ok := owned[name]
		if !ok || file.name != name {
			return errors.New("destination ownership is incomplete")
		}
		info, err := file.pin.Stat()
		if err != nil || !os.SameFile(file.info, info) || checkOpenFile(file.pin, info) != nil {
			return errors.New("destination pin changed")
		}
		pathInfo, err := root.Lstat(name)
		if err != nil || !os.SameFile(info, pathInfo) {
			return errors.New("destination path changed")
		}
	}
	return syncDirectory(root)
}

func cleanupInputs(parent, root *os.Root, directoryInfo os.FileInfo, owned map[string]ownedFile) error {
	var cleanupErr error
	for name, file := range owned {
		current, err := root.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr, errors.New("owned destination path is missing; cleanup is incomplete"))
		} else if err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		} else if file.pin == nil {
			cleanupErr = errors.Join(cleanupErr, errors.New("owned destination pin is missing; cleanup is incomplete"))
		} else {
			pinned, statErr := file.pin.Stat()
			if statErr != nil {
				cleanupErr = errors.Join(cleanupErr, statErr)
			} else if os.SameFile(file.info, pinned) && os.SameFile(pinned, current) {
				if removeErr := root.Remove(name); removeErr != nil {
					cleanupErr = errors.Join(cleanupErr, removeErr)
				}
			} else {
				cleanupErr = errors.Join(cleanupErr, errors.New("owned destination identity changed; replacement preserved"))
			}
		}
	}
	for name, file := range owned {
		if file.pin != nil {
			if err := file.pin.Close(); err != nil {
				cleanupErr = errors.Join(cleanupErr, err)
			}
			file.pin = nil
			owned[name] = file
		}
	}
	if err := syncDirectory(root); err != nil {
		cleanupErr = errors.Join(cleanupErr, err)
	}
	if err := root.Close(); err != nil {
		cleanupErr = errors.Join(cleanupErr, err)
	}
	current, err := parent.Lstat(inputsName)
	if err == nil && directoryInfo != nil && os.SameFile(directoryInfo, current) && current.IsDir() {
		child, openErr := parent.OpenRoot(inputsName)
		var entries []os.DirEntry
		var readErr error
		if openErr == nil {
			entries, readErr = readEntries(child)
			readErr = errors.Join(readErr, child.Close())
		} else {
			readErr = openErr
		}
		if readErr != nil {
			cleanupErr = errors.Join(cleanupErr, readErr)
		} else if len(entries) == 0 {
			if removeErr := parent.Remove(inputsName); removeErr != nil {
				cleanupErr = errors.Join(cleanupErr, removeErr)
			}
		} else {
			cleanupErr = errors.Join(cleanupErr, errors.New("unowned destination entries preserved"))
		}
	} else if errors.Is(err, os.ErrNotExist) {
		cleanupErr = errors.Join(cleanupErr, errors.New("created private inputs directory is missing; replacement preserved"))
	} else if err != nil {
		cleanupErr = errors.Join(cleanupErr, err)
	} else {
		cleanupErr = errors.Join(cleanupErr, errors.New("created private inputs directory identity changed; replacement preserved"))
	}
	if err := syncDirectory(parent); err != nil {
		cleanupErr = errors.Join(cleanupErr, err)
	}
	return cleanupErr
}

func sameFileIdentity(owned ownedFile, info os.FileInfo) bool {
	return owned.info != nil && info != nil && owned.pin != nil && os.SameFile(owned.info, info)
}

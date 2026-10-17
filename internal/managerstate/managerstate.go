// Package managerstate captures and independently verifies stopped manager
// database snapshots. It never opens the live database through SQLite.
package managerstate

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
)

const (
	formatVersion  = 1
	manifestName   = "snapshot.json"
	manifestLimit  = 1 << 20
	copyBufferSize = 128 << 10
)

var allowedFiles = map[string]bool{
	"manager.db": true, "manager.db-wal": true, "manager.db-shm": true, "manager.db-journal": true,
}

// Summary contains only verified snapshot metadata. It excludes source paths,
// database rows, and SQLite diagnostics.
type Summary struct {
	Format         int    `json:"format"`
	ID             string `json:"id"`
	Schema         int64  `json:"schema"`
	BinaryVersion  string `json:"binary_version"`
	FileCount      int    `json:"file_count"`
	TotalBytes     int64  `json:"total_bytes"`
	ManifestSHA256 string `json:"manifest_sha256"`
}

type manifest struct {
	Format        int          `json:"format"`
	ID            string       `json:"id"`
	CapturedAt    string       `json:"captured_at"`
	Schema        int64        `json:"schema"`
	BinaryVersion string       `json:"binary_version"`
	Files         []fileRecord `json:"files"`
}

type fileRecord struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type sourceFile struct {
	name string
	info os.FileInfo
}

// ownedFile keeps the original creation handle open until cleanup or successful
// publication so its filesystem identity cannot be recycled underneath cleanup.
type ownedFile struct {
	file *os.File
	info os.FileInfo
}

type ownedFiles map[string]ownedFile

// Snapshot copies the existing database and any present SQLite sidecars under
// the manager lock, validates a separate working copy, then publishes the
// manifest last. It refuses to create the data directory or manager database.
func Snapshot(ctx context.Context, dataDir, binaryVersion string) (Summary, error) {
	return snapshot(ctx, dataDir, binaryVersion, nil)
}

// snapshot's optional callback is an unexported deterministic race seam for
// package tests; production callers cannot bypass validation or publication.
func snapshot(ctx context.Context, dataDir, binaryVersion string, afterCopy func(*os.Root) error) (summary Summary, retErr error) {
	return snapshotWithFileHook(ctx, dataDir, binaryVersion, afterCopy, nil)
}

func snapshotWithFileHook(ctx context.Context, dataDir, binaryVersion string, afterCopy func(*os.Root) error, afterFile func(string)) (summary Summary, retErr error) {
	if err := ctx.Err(); err != nil {
		return Summary{}, err
	}
	if err := validateVersion(binaryVersion); err != nil {
		return Summary{}, err
	}
	data, err := openDataRoot(ctx, dataDir)
	if err != nil {
		return Summary{}, err
	}
	defer func() {
		if data.Close() != nil {
			retErr = errors.Join(retErr, errors.New("manager data handle close was incomplete"))
			summary = Summary{}
		}
	}()
	if _, err := sourceFiles(data); err != nil {
		return Summary{}, err
	}
	if err := verifyDataBinding(ctx, dataDir, data); err != nil {
		return Summary{}, err
	}
	release, err := launcher.AcquireManager(dataDir)
	if err != nil {
		return Summary{}, errors.New("manager state is busy")
	}
	defer release()
	if err := verifyDataBinding(ctx, dataDir, data); err != nil {
		return Summary{}, err
	}
	files, err := sourceFiles(data)
	if err != nil {
		return Summary{}, err
	}
	managerSID, err := platformManagerSID(data)
	if err != nil {
		return Summary{}, errors.New("manager account identity could not be verified")
	}
	maintenance, err := ensurePrivateDirectory(data, "maintenance", managerSID)
	if err != nil {
		return Summary{}, errors.New("private maintenance storage is unavailable")
	}
	defer func() {
		if maintenance.Close() != nil {
			retErr = errors.Join(retErr, errors.New("private maintenance handle close was incomplete"))
			summary = Summary{}
		}
	}()

	id, err := newID()
	if err != nil {
		return Summary{}, errors.New("snapshot identifier could not be generated")
	}
	snapshot, snapshotInfo, err := createPrivateDirectory(maintenance, id, managerSID)
	if err != nil {
		return Summary{}, errors.New("snapshot directory could not be created")
	}
	defer snapshot.Close()
	published := false
	snapshotFiles := ownedFiles{}
	cleanupSnapshot := func() error {
		if err := removeOwnedTree(maintenance, id, snapshotInfo, snapshot, snapshotFiles); err != nil {
			return errors.New("partial snapshot cleanup was incomplete")
		}
		return nil
	}
	defer func() {
		if !published {
			if err := cleanupSnapshot(); err != nil {
				retErr = errors.Join(retErr, err)
				summary = Summary{}
			}
		}
	}()

	var records []fileRecord
	records, snapshotFiles, err = copyCapturedWithHook(ctx, data, snapshot, files, afterFile)
	if err != nil {
		return Summary{}, err
	}
	if afterCopy != nil {
		if err := afterCopy(data); err != nil {
			return Summary{}, err
		}
	}
	if err := verifySourceSet(ctx, data, files, records); err != nil {
		return Summary{}, err
	}
	validation, validationName, validationInfo, err := createScratch(maintenance, managerSID)
	if err != nil {
		return Summary{}, errors.New("snapshot validation workspace could not be created")
	}
	validationFiles, err := copySnapshotForValidation(ctx, snapshot, validation, records)
	validationCleaned := false
	cleanupValidation := func() error {
		if validationCleaned {
			return nil
		}
		if err := removeOwnedTree(maintenance, validationName, validationInfo, validation, validationFiles); err != nil {
			return errors.New("private validation cleanup was incomplete")
		}
		validationCleaned = true
		return nil
	}
	defer func() {
		if err := cleanupValidation(); err != nil {
			retErr = errors.Join(retErr, err)
			summary = Summary{}
		}
	}()
	if err != nil {
		return Summary{}, err
	}
	validationBinding := func() error {
		return verifyValidationBinding(ctx, dataDir, data, maintenance, validationName, validation, validationInfo, validationFiles)
	}
	schema, err := validateSQLiteBound(ctx, filepath.Join(dataDir, "maintenance", validationName, "manager.db"), validationBinding, nil)
	if bindingErr := validationBinding(); bindingErr != nil {
		return Summary{}, fixedContextError("snapshot validation directory binding changed", bindingErr)
	}
	if err != nil {
		return Summary{}, fixedContextError("manager database snapshot failed integrity or schema validation", err)
	}
	if err := captureSQLiteSidecars(validation, validationFiles); err != nil {
		return Summary{}, errors.New("snapshot validation workspace contains unexpected files")
	}
	if err := cleanupValidation(); err != nil {
		return Summary{}, err
	}
	if err := verifySourceSet(ctx, data, files, records); err != nil {
		return Summary{}, err
	}
	meta := manifest{Format: formatVersion, ID: id, CapturedAt: time.Now().UTC().Format(time.RFC3339Nano), Schema: schema, BinaryVersion: binaryVersion, Files: records}
	body, err := json.Marshal(meta)
	if err != nil || len(body) > manifestLimit {
		return Summary{}, errors.New("snapshot manifest could not be encoded")
	}
	manifestFile, err := writeSyncedExclusive(snapshot, manifestName, body, 0600)
	if manifestFile.file != nil || manifestFile.info != nil {
		snapshotFiles[manifestName] = manifestFile
	}
	if err != nil {
		return Summary{}, errors.New("snapshot manifest could not be published")
	}
	if err := syncDirectory(snapshot); err != nil {
		return Summary{}, errors.New("snapshot directory could not be synchronized")
	}
	summary, err = verifySnapshot(ctx, dataDir, data, maintenance, id, filepath.Join(dataDir, "maintenance"), managerSID)
	if err != nil {
		return Summary{}, fixedContextError("published snapshot did not pass independent verification", err)
	}
	if err := syncDirectory(maintenance); err != nil {
		return Summary{}, errors.New("maintenance directory could not be synchronized")
	}
	if err := syncDirectory(data); err != nil {
		return Summary{}, errors.New("manager data directory could not be synchronized")
	}
	if err := verifySourceSet(ctx, data, files, records); err != nil {
		return Summary{}, err
	}
	if summary.Schema != schema || summary.BinaryVersion != binaryVersion {
		return Summary{}, errors.New("published snapshot metadata changed during verification")
	}
	if err := closeOwnedFiles(snapshotFiles); err != nil {
		published = true
		return Summary{}, errors.New("snapshot file handles could not be closed")
	}
	published = true
	return summary, nil
}

// Verify independently checks a snapshot under the same exclusive manager
// lock. Validation always runs on a fresh working copy, never on raw snapshot
// bytes.
func Verify(ctx context.Context, dataDir, snapshotID string) (summary Summary, retErr error) {
	if err := ctx.Err(); err != nil {
		return Summary{}, err
	}
	if !validID(snapshotID) {
		return Summary{}, errors.New("invalid snapshot identifier")
	}
	data, err := openDataRoot(ctx, dataDir)
	if err != nil {
		return Summary{}, err
	}
	defer func() {
		if data.Close() != nil {
			retErr = errors.Join(retErr, errors.New("manager data handle close was incomplete"))
			summary = Summary{}
		}
	}()
	if _, err := sourceFiles(data); err != nil {
		return Summary{}, err
	}
	if err := verifyDataBinding(ctx, dataDir, data); err != nil {
		return Summary{}, err
	}
	release, err := launcher.AcquireManager(dataDir)
	if err != nil {
		return Summary{}, errors.New("manager state is busy")
	}
	defer release()
	if err := verifyDataBinding(ctx, dataDir, data); err != nil {
		return Summary{}, err
	}
	managerSID, err := platformManagerSID(data)
	if err != nil {
		return Summary{}, errors.New("manager account identity could not be verified")
	}
	maintenance, err := openPrivateDirectory(data, "maintenance", managerSID)
	if err != nil {
		return Summary{}, errors.New("private maintenance storage is unavailable")
	}
	defer func() {
		if maintenance.Close() != nil {
			retErr = errors.Join(retErr, errors.New("private maintenance handle close was incomplete"))
			summary = Summary{}
		}
	}()
	return verifySnapshot(ctx, dataDir, data, maintenance, snapshotID, filepath.Join(dataDir, "maintenance"), managerSID)
}

func verifySnapshot(ctx context.Context, dataDir string, data, maintenance *os.Root, id, maintenancePath, managerSID string) (summary Summary, retErr error) {
	snapshot, err := openCheckedDirectory(maintenance, id, managerSID)
	if err != nil {
		return Summary{}, errors.New("snapshot directory is missing or unsafe")
	}
	defer snapshot.Close()
	meta, body, err := readManifest(snapshot)
	if err != nil || meta.ID != id || !validManifest(meta) {
		return Summary{}, errors.New("snapshot manifest is invalid")
	}
	actual, err := snapshotEntries(snapshot)
	if err != nil || len(actual) != len(meta.Files)+1 {
		return Summary{}, errors.New("snapshot file set is invalid")
	}
	records := append([]fileRecord(nil), meta.Files...)
	for i, expected := range records {
		if i >= len(actual) || actual[i].name != expected.Name || !allowedFiles[expected.Name] {
			return Summary{}, errors.New("snapshot file set is invalid")
		}
		if _, err := verifyFile(ctx, snapshot, expected); err != nil {
			return Summary{}, fixedContextError("snapshot file verification failed", err)
		}
	}
	if actual[len(actual)-1].name != manifestName {
		return Summary{}, errors.New("snapshot manifest is missing")
	}
	if err := checkOrdinaryFile(snapshot, actual[len(actual)-1].name, actual[len(actual)-1].info); err != nil {
		return Summary{}, errors.New("snapshot manifest file is unsafe")
	}
	validation, validationName, validationInfo, err := createScratch(maintenance, managerSID)
	if err != nil {
		return Summary{}, errors.New("snapshot validation workspace could not be created")
	}
	validationFiles, err := copySnapshotForValidation(ctx, snapshot, validation, records)
	validationCleaned := false
	cleanupValidation := func() error {
		if validationCleaned {
			return nil
		}
		if err := removeOwnedTree(maintenance, validationName, validationInfo, validation, validationFiles); err != nil {
			return errors.New("private validation cleanup was incomplete")
		}
		validationCleaned = true
		return nil
	}
	defer func() {
		if err := cleanupValidation(); err != nil {
			retErr = errors.Join(retErr, err)
			summary = Summary{}
		}
	}()
	if err != nil {
		return Summary{}, fixedContextError("snapshot validation copy failed", err)
	}
	validationPath := filepath.Join(maintenancePath, validationName, "manager.db")
	validationBinding := func() error {
		return verifyValidationBinding(ctx, dataDir, data, maintenance, validationName, validation, validationInfo, validationFiles)
	}
	schema, err := validateSQLiteBound(ctx, validationPath, validationBinding, nil)
	if bindingErr := validationBinding(); bindingErr != nil {
		return Summary{}, fixedContextError("snapshot validation directory binding changed", bindingErr)
	}
	if err != nil {
		return Summary{}, fixedContextError("snapshot database integrity or schema does not match its manifest", err)
	}
	if schema != meta.Schema {
		return Summary{}, errors.New("snapshot database integrity or schema does not match its manifest")
	}
	if err := captureSQLiteSidecars(validation, validationFiles); err != nil {
		return Summary{}, errors.New("snapshot validation workspace contains unexpected files")
	}
	if err := cleanupValidation(); err != nil {
		return Summary{}, err
	}
	var total int64
	for _, item := range records {
		if item.Size > int64(^uint64(0)>>1)-total {
			return Summary{}, errors.New("snapshot total size is invalid")
		}
		total += item.Size
	}
	digest := sha256.Sum256(body)
	return Summary{Format: meta.Format, ID: meta.ID, Schema: meta.Schema, BinaryVersion: meta.BinaryVersion, FileCount: len(records), TotalBytes: total, ManifestSHA256: hex.EncodeToString(digest[:])}, nil
}

func validManifest(m manifest) bool {
	if m.Format != formatVersion || !validID(m.ID) || m.Schema < 1 || m.Schema > db.LatestVersion() || validateVersion(m.BinaryVersion) != nil || len(m.Files) == 0 || m.Files[0].Name != "manager.db" {
		return false
	}
	capturedAt, err := time.Parse(time.RFC3339Nano, m.CapturedAt)
	if err != nil || capturedAt.UTC().Format(time.RFC3339Nano) != m.CapturedAt {
		return false
	}
	seen := map[string]bool{}
	last := ""
	for _, item := range m.Files {
		if !allowedFiles[item.Name] || seen[item.Name] || item.Size < 0 || (item.Name == "manager.db" && item.Size == 0) || !validDigest(item.SHA256) || (last != "" && item.Name <= last) {
			return false
		}
		seen[item.Name] = true
		last = item.Name
	}
	return true
}

func readManifest(root *os.Root) (manifest, []byte, error) {
	f, err := root.Open(manifestName)
	if err != nil {
		return manifest{}, nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > manifestLimit {
		return manifest{}, nil, errors.New("manifest size or type is invalid")
	}
	if err := checkOpenFile(f, info); err != nil {
		return manifest{}, nil, err
	}
	body, err := io.ReadAll(io.LimitReader(f, manifestLimit+1))
	if err != nil || len(body) > manifestLimit {
		return manifest{}, nil, errors.New("manifest read exceeded its bound")
	}
	if err := rejectDuplicateJSONKeys(body); err != nil {
		return manifest{}, nil, err
	}
	var m manifest
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		return manifest{}, nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return manifest{}, nil, errors.New("manifest has trailing data")
	}
	return m, body, nil
}

func rejectDuplicateJSONKeys(body []byte) error {
	d := json.NewDecoder(strings.NewReader(string(body)))
	d.UseNumber()
	var walk func() error
	walk = func() error {
		token, err := d.Token()
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
			for d.More() {
				keyToken, err := d.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok || seen[key] {
					return errors.New("manifest contains duplicate or invalid object keys")
				}
				seen[key] = true
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		case '[':
			for d.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		default:
			return errors.New("manifest JSON has an unexpected delimiter")
		}
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("manifest has trailing JSON data")
	}
	return nil
}

func sourceFiles(root *os.Root) ([]sourceFile, error) {
	entries, err := rootReadDir(root)
	if err != nil {
		return nil, errors.New("manager state directory could not be inspected")
	}
	var files []sourceFile
	for _, entry := range entries {
		name := entry.Name()
		if !allowedFiles[name] {
			continue
		}
		info, err := root.Lstat(name)
		if err != nil || !info.Mode().IsRegular() {
			return nil, errors.New("manager state contains an unsafe file")
		}
		if err := checkOrdinaryFile(root, name, info); err != nil {
			return nil, errors.New("manager state contains a linked or unsafe file")
		}
		files = append(files, sourceFile{name: name, info: info})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	if len(files) == 0 || files[0].name != "manager.db" {
		return nil, errors.New("existing manager database is required")
	}
	return files, nil
}

func snapshotEntries(root *os.Root) ([]sourceFile, error) {
	entries, err := rootReadDir(root)
	if err != nil {
		return nil, err
	}
	files := make([]sourceFile, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !allowedFiles[name] && name != manifestName {
			return nil, errors.New("snapshot contains an unknown file")
		}
		info, err := root.Lstat(name)
		if err != nil || !info.Mode().IsRegular() || checkOrdinaryFile(root, name, info) != nil {
			return nil, errors.New("snapshot contains an unsafe file")
		}
		files = append(files, sourceFile{name: name, info: info})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	return files, nil
}

func copyCaptured(ctx context.Context, source, destination *os.Root, files []sourceFile) ([]fileRecord, ownedFiles, error) {
	return copyCapturedWithHook(ctx, source, destination, files, nil)
}

func copyCapturedWithHook(ctx context.Context, source, destination *os.Root, files []sourceFile, afterFile func(string)) ([]fileRecord, ownedFiles, error) {
	records := make([]fileRecord, 0, len(files))
	identities := make(ownedFiles, len(files))
	for _, sourceFile := range files {
		if err := ctx.Err(); err != nil {
			return records, identities, fixedContextError("manager database snapshot copy failed", err)
		}
		record, destFile, err := copyNamed(ctx, source, destination, sourceFile.name, sourceFile.info.Size())
		if err != nil {
			if destFile.file != nil || destFile.info != nil {
				identities[sourceFile.name] = destFile
			}
			return records, identities, fixedContextError("manager database snapshot copy failed", err)
		}
		if record.Size != sourceFile.info.Size() {
			identities[sourceFile.name] = destFile
			return records, identities, errors.New("manager database changed during snapshot")
		}
		records = append(records, record)
		identities[sourceFile.name] = destFile
		if afterFile != nil {
			afterFile(sourceFile.name)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Name < records[j].Name })
	return records, identities, nil
}

func copyNamed(ctx context.Context, source, destination *os.Root, name string, expectedSize int64) (record fileRecord, destFile ownedFile, retErr error) {
	in, err := source.Open(name)
	if err != nil {
		return fileRecord{}, ownedFile{}, err
	}
	defer in.Close()
	before, err := in.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() != expectedSize {
		return fileRecord{}, ownedFile{}, errors.New("copy source changed")
	}
	if err := checkOpenFile(in, before); err != nil {
		return fileRecord{}, ownedFile{}, err
	}
	if current, err := source.Lstat(name); err != nil || !os.SameFile(before, current) {
		return fileRecord{}, ownedFile{}, errors.New("copy source path identity changed")
	}
	out, err := destination.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fileRecord{}, ownedFile{}, err
	}
	destFile.file = out
	destFile.info, err = out.Stat()
	if err != nil {
		return fileRecord{}, destFile, err
	}
	hash := sha256.New()
	buf := make([]byte, copyBufferSize)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return fileRecord{}, destFile, err
		}
		n, readErr := in.Read(buf)
		if n > 0 {
			if size > expectedSize-int64(n) {
				return fileRecord{}, destFile, errors.New("copy source grew")
			}
			written, writeErr := out.Write(buf[:n])
			if writeErr != nil || written != n {
				return fileRecord{}, destFile, errors.Join(writeErr, io.ErrShortWrite)
			}
			_, _ = hash.Write(buf[:n])
			size += int64(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fileRecord{}, destFile, readErr
		}
	}
	if size != expectedSize {
		return fileRecord{}, destFile, errors.New("copy source shrank")
	}
	if err := out.Sync(); err != nil {
		return fileRecord{}, destFile, err
	}
	after, err := in.Stat()
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return fileRecord{}, destFile, errors.New("copy source identity changed")
	}
	if !destFile.info.Mode().IsRegular() || checkOpenFile(out, destFile.info) != nil {
		return fileRecord{}, destFile, errors.New("copied file identity is unsafe")
	}
	if current, err := destination.Lstat(name); err != nil || !os.SameFile(destFile.info, current) {
		return fileRecord{}, destFile, errors.New("copied file path identity changed")
	}
	return fileRecord{Name: name, Size: size, SHA256: hex.EncodeToString(hash.Sum(nil))}, destFile, nil
}

func verifyFile(ctx context.Context, root *os.Root, want fileRecord) (os.FileInfo, error) {
	f, err := root.Open(want.Name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() != want.Size || checkOpenFile(f, before) != nil {
		return nil, errors.New("snapshot member metadata is invalid")
	}
	if current, err := root.Lstat(want.Name); err != nil || !os.SameFile(before, current) {
		return nil, errors.New("snapshot member path identity changed")
	}
	hash := sha256.New()
	buf := make([]byte, copyBufferSize)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, readErr := f.Read(buf)
		if n > 0 {
			size += int64(n)
			_, _ = hash.Write(buf[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) || size != want.Size || hex.EncodeToString(hash.Sum(nil)) != want.SHA256 {
		return nil, errors.New("snapshot member digest mismatch")
	}
	return before, nil
}

func verifySourceSet(ctx context.Context, root *os.Root, expected []sourceFile, records []fileRecord) error {
	actual, err := sourceFiles(root)
	if err != nil || len(actual) != len(expected) {
		return errors.New("manager database sidecar set changed during snapshot")
	}
	for i := range expected {
		if actual[i].name != expected[i].name || !os.SameFile(expected[i].info, actual[i].info) || actual[i].info.Size() != expected[i].info.Size() || !actual[i].info.ModTime().Equal(expected[i].info.ModTime()) {
			return errors.New("manager database identity changed during snapshot")
		}
		record, err := copyToHash(ctx, root, expected[i].name, expected[i].info.Size())
		if err != nil {
			return fixedContextError("manager database content changed during snapshot", err)
		}
		if record.SHA256 != fileDigest(records, expected[i].name) {
			return errors.New("manager database content changed during snapshot")
		}
	}
	return nil
}

func fileDigest(records []fileRecord, name string) string {
	for _, record := range records {
		if record.Name == name {
			return record.SHA256
		}
	}
	return ""
}

func copyToHash(ctx context.Context, root *os.Root, name string, size int64) (fileRecord, error) {
	// Reuse the streaming copier with a discard destination is unnecessary; hash
	// in bounded chunks so verification never materializes database bytes.
	f, err := root.Open(name)
	if err != nil {
		return fileRecord{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() != size || checkOpenFile(f, info) != nil {
		return fileRecord{}, errors.New("source metadata changed")
	}
	if current, err := root.Lstat(name); err != nil || !os.SameFile(info, current) {
		return fileRecord{}, errors.New("source path identity changed")
	}
	h := sha256.New()
	buf := make([]byte, copyBufferSize)
	var nbytes int64
	for {
		if err := ctx.Err(); err != nil {
			return fileRecord{}, err
		}
		n, readErr := f.Read(buf)
		if n > 0 {
			nbytes += int64(n)
			_, _ = h.Write(buf[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fileRecord{}, readErr
		}
	}
	after, err := f.Stat()
	if err != nil || !os.SameFile(info, after) || info.Size() != after.Size() || nbytes != size {
		return fileRecord{}, errors.New("source changed while hashing")
	}
	return fileRecord{Name: name, Size: nbytes, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

func copySnapshotForValidation(ctx context.Context, source, destination *os.Root, records []fileRecord) (ownedFiles, error) {
	owned := make(ownedFiles, len(records))
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return owned, fixedContextError("snapshot validation copy failed", err)
		}
		copied, created, err := copyNamed(ctx, source, destination, record.Name, record.Size)
		if err != nil || copied.SHA256 != record.SHA256 {
			if created.file != nil || created.info != nil {
				owned[record.Name] = created
			}
			if err != nil {
				return owned, fixedContextError("snapshot validation copy digest mismatch", err)
			}
			return owned, errors.New("snapshot validation copy digest mismatch")
		}
		owned[record.Name] = created
	}
	return owned, nil
}

func fixedContextError(message string, err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("%s: %w", message, context.Canceled)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%s: %w", message, context.DeadlineExceeded)
	default:
		return errors.New(message)
	}
}

func validateSQLite(ctx context.Context, path string) (version int64, retErr error) {
	return validateSQLiteBound(ctx, path, nil, nil)
}

func validateSQLiteBound(ctx context.Context, path string, validateBinding func() error, beforeOpen func() error) (version int64, retErr error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if validateBinding != nil {
		if err := validateBinding(); err != nil {
			return 0, errors.New("validation database binding changed")
		}
	}
	if beforeOpen != nil {
		if err := beforeOpen(); err != nil {
			return 0, fixedContextError("validation database could not be opened", err)
		}
	}
	database, err := sql.Open("sqlite", db.FileURI(path)+"?mode=rw&_pragma=busy_timeout(5000)")
	if err != nil {
		return 0, err
	}
	database.SetMaxOpenConns(1)
	defer func() {
		if err := database.Close(); err != nil {
			retErr = errors.Join(retErr, errors.New("validation database close was incomplete"))
			version = 0
		}
	}()
	pingErr := database.PingContext(ctx)
	if validateBinding != nil {
		if err := validateBinding(); err != nil {
			return 0, errors.New("validation database binding changed")
		}
	}
	if pingErr != nil {
		return 0, pingErr
	}
	return validateSQLiteHandle(ctx, database)
}

// validateSQLiteHandle validates an already-open single-connection database.
// The caller owns its connection lifetime and may use this under an existing
// manager lock without opening a second database or reacquiring that lock.
func validateSQLiteHandle(ctx context.Context, database *sql.DB) (version int64, retErr error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if database == nil {
		return 0, errors.New("validation database is unavailable")
	}
	var integrity string
	rows, err := database.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return 0, err
	}
	count := 0
	for rows.Next() {
		count++
		if err := rows.Scan(&integrity); err != nil || integrity != "ok" || count > 1 {
			_ = rows.Close()
			return 0, errors.New("integrity_check failed")
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil || count != 1 {
		return 0, errors.New("integrity_check did not return exactly ok")
	}
	foreign, err := database.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return 0, err
	}
	if foreign.Next() {
		_ = foreign.Close()
		return 0, errors.New("foreign key check failed")
	}
	if err := errors.Join(foreign.Err(), foreign.Close()); err != nil {
		return 0, err
	}
	version, err = db.CurrentVersion(ctx, database)
	if err != nil || version > db.LatestVersion() {
		return 0, errors.New("database schema is not supported")
	}
	if err := validateCoreSchema(ctx, database, version); err != nil {
		return 0, err
	}
	if err := validateGooseLedger(ctx, database, version); err != nil {
		return 0, err
	}
	return version, nil
}

func validateCoreSchema(ctx context.Context, database *sql.DB, version int64) error {
	required := map[string][]string{
		"instances":          {"id", "name", "flavor", "minecraft_version", "java_major", "game_port", "config_json"},
		"process_identities": {"instance_id", "identity_json"},
		"artifacts":          {"id", "instance_id", "artifact_json"},
		"jobs":               {"id", "instance_id", "kind", "state", "phase", "error", "created_at"},
	}
	if version >= 2 {
		required["users"] = []string{"id", "username", "role", "disabled"}
		required["settings"] = []string{"id", "doc", "updated_at"}
		required["auth_bootstrap"] = []string{"id"}
		required["api_tokens"] = []string{"id", "user_id", "hash", "scopes"}
	}
	if version >= 3 {
		required["jobs"] = append(required["jobs"], "requested_by", "payload", "progress", "cancellable", "cancel_requested")
	}
	if version >= 4 {
		required["instance_settings"] = []string{"instance_id", "eula_accepted", "provisioning"}
		required["instance_mutations"] = []string{"instance_id", "kind", "staged_name", "ownership_token"}
	}
	if version >= 5 {
		required["lifecycle_installations"] = []string{"instance_id", "generation", "installation_json"}
		required["lifecycle_generations"] = []string{"instance_id", "generation", "installation_json"}
		required["lifecycle_activations"] = []string{"instance_id", "generation", "installation_json"}
	}
	for table, columns := range required {
		if err := requireColumns(ctx, database, table, columns); err != nil {
			return errors.New("database application schema is incomplete")
		}
	}
	return nil
}

func captureSQLiteSidecars(root *os.Root, owned ownedFiles) error {
	entries, err := rootReadDir(root)
	if err != nil {
		return errors.New("owned cleanup could not inspect its directory")
	}
	for _, entry := range entries {
		name := entry.Name()
		if !allowedFiles[name] {
			return errors.New("unexpected SQLite validation output")
		}
		info, err := root.Lstat(name)
		if err != nil || !info.Mode().IsRegular() || checkOrdinaryFile(root, name, info) != nil {
			return errors.New("unsafe SQLite validation output")
		}
		if previous, exists := owned[name]; exists && !ownedFileMatches(previous, info) {
			if name == "manager.db" {
				return errors.New("SQLite replaced the validation database")
			}
			if previous.file != nil {
				if err := previous.file.Close(); err != nil {
					return errors.New("validation sidecar handle could not be released")
				}
			}
			delete(owned, name)
		}
		if _, exists := owned[name]; !exists {
			file, err := root.Open(name)
			if err != nil {
				return errors.New("validation sidecar handle could not be retained")
			}
			opened, err := file.Stat()
			if err != nil || !os.SameFile(info, opened) {
				_ = file.Close()
				return errors.New("validation sidecar identity changed")
			}
			owned[name] = ownedFile{file: file, info: info}
		}
	}
	if _, ok := owned["manager.db"]; !ok {
		return errors.New("validation database is missing")
	}
	return nil
}

func requireColumns(ctx context.Context, database *sql.DB, table string, columns []string) error {
	if strings.ContainsAny(table, "'\";-") {
		return errors.New("invalid schema table")
	}
	rows, err := database.QueryContext(ctx, "PRAGMA table_info('"+table+"')")
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notnull, &defaultValue, &pk); err != nil {
			_ = rows.Close()
			return err
		}
		seen[name] = true
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, column := range columns {
		if !seen[column] {
			return fmt.Errorf("missing schema column %s.%s", table, column)
		}
	}
	return nil
}

var migrationVersions = []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 18, 19, 20}

func validateGooseLedger(ctx context.Context, database *sql.DB, version int64) error {
	rows, err := database.QueryContext(ctx, "SELECT id, version_id, is_applied FROM goose_db_version ORDER BY id")
	if err != nil {
		return err
	}
	var stack []int64
	var expectedID int64 = 1
	count := 0
	for rows.Next() {
		var id, migration int64
		var applied int
		if err := rows.Scan(&id, &migration, &applied); err != nil || id != expectedID || (applied != 0 && applied != 1) {
			_ = rows.Close()
			return errors.New("incoherent Goose migration ledger")
		}
		expectedID++
		count++
		if count == 1 && migration == 0 && applied == 1 {
			continue
		}
		if count == 1 {
			_ = rows.Close()
			return errors.New("Goose migration ledger has no baseline")
		}
		if applied == 1 {
			index := len(stack)
			if index >= len(migrationVersions) || migration != migrationVersions[index] {
				_ = rows.Close()
				return errors.New("Goose migration ledger skips or repeats a migration")
			}
			stack = append(stack, migration)
		} else {
			if len(stack) == 0 || stack[len(stack)-1] != migration {
				_ = rows.Close()
				return errors.New("Goose migration ledger reverts out of order")
			}
			stack = stack[:len(stack)-1]
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil || len(stack) == 0 || stack[len(stack)-1] != version {
		return errors.New("Goose migration ledger does not match database schema")
	}
	return nil
}

func validateVersion(version string) error {
	if version == "" || len(version) > 128 || strings.TrimSpace(version) != version || strings.ContainsAny(version, "\r\n\x00") {
		return errors.New("manager binary version is invalid")
	}
	return nil
}

func newID() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func validID(id string) bool {
	if len(id) != 64 || strings.ToLower(id) != id {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func validDigest(digest string) bool { return validID(digest) }

func openDataRoot(ctx context.Context, dataDir string) (*os.Root, error) {
	if !filepath.IsAbs(dataDir) || filepath.Clean(dataDir) != dataDir {
		return nil, errors.New("existing manager data directory is required")
	}
	root, err := safepath.OpenRoot(ctx, dataDir)
	if err != nil {
		return nil, errors.New("existing manager data directory is unsafe")
	}
	if _, err := sourceFiles(root); err != nil {
		_ = root.Close()
		return nil, err
	}
	return root, nil
}

func verifyDataBinding(ctx context.Context, dataDir string, data *os.Root) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	checked, err := safepath.OpenRoot(ctx, dataDir)
	if err != nil {
		return errors.New("manager data directory identity changed")
	}
	defer checked.Close()
	opened, err := data.Stat(".")
	if err != nil {
		return errors.New("manager data directory identity changed")
	}
	current, err := checked.Stat(".")
	if err != nil || !os.SameFile(opened, current) {
		return errors.New("manager data directory identity changed")
	}
	return nil
}

func verifyValidationBinding(ctx context.Context, dataDir string, data, maintenance *os.Root, validationName string, validation *os.Root, validationInfo os.FileInfo, owned ownedFiles) error {
	if validation == nil || validationInfo == nil || maintenance == nil {
		return errors.New("validation directory binding is unavailable")
	}
	if data != nil {
		if err := verifyDataBinding(ctx, dataDir, data); err != nil {
			return err
		}
		maintenanceInfo, err := data.Lstat("maintenance")
		maintenanceOpened, openErr := maintenance.Stat(".")
		maintenancePathInfo, pathErr := os.Lstat(filepath.Join(dataDir, "maintenance"))
		if err != nil || openErr != nil || pathErr != nil || !os.SameFile(maintenanceInfo, maintenanceOpened) || !os.SameFile(maintenanceInfo, maintenancePathInfo) {
			return errors.New("maintenance directory binding changed")
		}
	}
	validationOpened, err := validation.Stat(".")
	validationInfoAtRoot, rootErr := maintenance.Lstat(validationName)
	validationPathInfo, pathErr := os.Lstat(filepath.Join(maintenance.Name(), validationName))
	if err != nil || rootErr != nil || pathErr != nil || !os.SameFile(validationInfo, validationOpened) || !os.SameFile(validationInfo, validationInfoAtRoot) || !os.SameFile(validationInfo, validationPathInfo) {
		return errors.New("validation directory binding changed")
	}
	databaseFile, ok := owned["manager.db"]
	if !ok || databaseFile.file == nil || databaseFile.info == nil {
		return errors.New("validation database identity is unavailable")
	}
	currentDatabase, err := validation.Lstat("manager.db")
	if err != nil || !ownedFileMatches(databaseFile, currentDatabase) || checkOpenFile(databaseFile.file, databaseFile.info) != nil {
		return errors.New("validation database binding changed")
	}
	return nil
}

func createScratch(parent *os.Root, managerSID string) (*os.Root, string, os.FileInfo, error) {
	for i := 0; i < 4; i++ {
		id, err := newID()
		if err != nil {
			return nil, "", nil, err
		}
		name := ".validation-" + id
		root, info, err := createPrivateDirectory(parent, name, managerSID)
		if err == nil {
			return root, name, info, nil
		}
	}
	return nil, "", nil, errors.New("could not allocate private validation workspace")
}

func writeSyncedExclusive(root *os.Root, name string, body []byte, mode os.FileMode) (ownedFile, error) {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return ownedFile{}, err
	}
	created := ownedFile{file: f}
	n, writeErr := f.Write(body)
	syncErr := f.Sync()
	info, statErr := f.Stat()
	created.info = info
	if n != len(body) {
		writeErr = errors.Join(writeErr, io.ErrShortWrite)
	}
	if err := errors.Join(writeErr, syncErr, statErr); err != nil {
		return created, err
	}
	if !info.Mode().IsRegular() || checkOrdinaryFile(root, name, info) != nil {
		return created, errors.New("published file identity is unsafe")
	}
	if err := checkOpenFile(f, info); err != nil {
		return created, errors.New("published file identity is unsafe")
	}
	return created, nil
}

func removeOwnedTree(parent *os.Root, name string, identity os.FileInfo, root *os.Root, owned ownedFiles) (retErr error) {
	if root == nil || identity == nil {
		_ = closeOwnedFiles(owned)
		return nil
	}
	rootClosed := false
	defer func() {
		retErr = errors.Join(retErr, closeOwnedFiles(owned))
		if !rootClosed {
			retErr = errors.Join(retErr, root.Close())
		}
	}()
	current, err := parent.Lstat(name)
	if err != nil || !os.SameFile(identity, current) {
		return errors.New("owned cleanup refused a replaced directory")
	}
	entries, err := rootReadDir(root)
	if err != nil {
		return err
	}
	if len(entries) > len(owned) {
		return errors.New("owned cleanup preserved unknown directory contents")
	}
	for _, entry := range entries {
		info, err := root.Lstat(entry.Name())
		ownedIdentity, exists := owned[entry.Name()]
		if err != nil || !exists || !ownedFileMatches(ownedIdentity, info) || !info.Mode().IsRegular() || checkOrdinaryFile(root, entry.Name(), info) != nil {
			return errors.New("owned cleanup preserved unknown directory contents")
		}
		if err := root.Remove(entry.Name()); err != nil {
			return errors.New("owned cleanup could not remove a captured file")
		}
	}
	if err := closeOwnedFiles(owned); err != nil {
		return errors.New("owned cleanup could not close captured file handles")
	}
	if err := root.Close(); err != nil {
		rootClosed = true
		return errors.New("owned cleanup could not close its directory")
	}
	rootClosed = true
	current, err = parent.Lstat(name)
	if err != nil || !os.SameFile(identity, current) {
		return errors.New("owned cleanup refused a replaced directory")
	}
	if err := parent.Remove(name); err != nil {
		return errors.New("owned cleanup could not remove its directory")
	}
	return nil
}

func ownedFileMatches(owned ownedFile, current os.FileInfo) bool {
	if owned.info == nil || current == nil || !os.SameFile(owned.info, current) {
		return false
	}
	if owned.file == nil {
		return false
	}
	opened, err := owned.file.Stat()
	return err == nil && os.SameFile(owned.info, opened) && os.SameFile(opened, current)
}

func closeOwnedFiles(owned ownedFiles) error {
	var closeErr error
	for name, item := range owned {
		if item.file == nil {
			continue
		}
		if err := item.file.Close(); err != nil {
			closeErr = errors.Join(closeErr, err)
		}
		item.file = nil
		owned[name] = item
	}
	return closeErr
}

func rootReadDir(root *os.Root) ([]os.DirEntry, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	return directory.ReadDir(-1)
}

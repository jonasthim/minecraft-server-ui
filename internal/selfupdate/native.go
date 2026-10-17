package selfupdate

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
)

type nativePaths struct{ Data, Trust, State, Bin, Binary, Updater string }
type nativeTrust struct {
	Format     int               `json:"format"`
	Repository string            `json:"repository"`
	PublicKeys map[string]string `json:"public_keys"`
}
type Installed struct {
	Format  int    `json:"format"`
	Version string `json:"version"`
	Schema  int64  `json:"schema"`
	SHA256  string `json:"sha256"`
}

func LoadNativeTrust(ctx context.Context) (Trust, error) {
	paths := platformPaths()
	root, err := openProtectedRoot(ctx, filepath.Dir(paths.Trust))
	if err != nil {
		return Trust{}, err
	}
	defer root.Close()
	if err := protectedFile(root, filepath.Base(paths.Trust)); err != nil {
		return Trust{}, err
	}
	body, err := readChecked(ctx, root, filepath.Base(paths.Trust), 16<<10)
	if err != nil {
		return Trust{}, err
	}
	var file nativeTrust
	if strictJSON(body, &file) != nil || file.Format != 1 {
		return Trust{}, errors.New("native update trust configuration is invalid")
	}
	trust := Trust{Repository: file.Repository, PublicKeys: map[string]ed25519.PublicKey{}}
	for id, encoded := range file.PublicKeys {
		key, err := base64.StdEncoding.Strict().DecodeString(encoded)
		if err != nil {
			return Trust{}, errors.New("native update public key is invalid")
		}
		trust.PublicKeys[id] = key
	}
	if err := trust.Validate(); err != nil {
		return Trust{}, err
	}
	return trust, nil
}

type nativeBroker struct {
	paths   nativePaths
	matches bool
}

// NewNative binds only the reviewed canonical installation. It cannot target a
// different service, executable, data directory, URL, port or helper command.
func NewNative(dataDir, address string) Native {
	paths := platformPaths()
	return &nativeBroker{paths: paths, matches: filepath.Clean(dataDir) == paths.Data && dataDir == filepath.Clean(dataDir) && address == "127.0.0.1:8080"}
}
func (b *nativeBroker) Available(ctx context.Context) bool {
	if !b.matches {
		return false
	}
	if _, err := LoadNativeTrust(ctx); err != nil {
		return false
	}
	return brokerAvailable(ctx, b.paths)
}
func (b *nativeBroker) Request(ctx context.Context, request ActivationRequest) (err error) {
	if !b.matches {
		return errors.New("self-update requires the canonical native installation and loopback port8080")
	}
	if !validHash(request.StageID) || request.ActorID < 1 || request.RequestedAt.After(time.Now()) || time.Since(request.RequestedAt) > time.Minute {
		return errors.New("invalid native activation request")
	}
	root, err := safepath.OpenRoot(ctx, controlDirectory(b.paths.Data))
	if err != nil {
		return err
	}
	defer root.Close()
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if err := writeFile(ctx, root, "request.json", bytesReader(body), int64(len(body)), ""); err != nil {
		return fmt.Errorf("native update request conflicts with pending work: %w", err)
	}
	if err := syncRoot(root); err != nil {
		return err
	}
	if err := brokerRequest(ctx, b.paths, request.StageID); err != nil {
		// Keep a durable request when native acceptance is ambiguous. An administrator
		// must reconcile it instead of issuing a conflicting activation.
		return fmt.Errorf("native update request requires reconciliation: %w", err)
	}
	return nil
}
func bytesReader(body []byte) io.Reader { return bytes.NewReader(body) }

// NativeDriver contains only fixed service operations and fixed-account probes.
// The operating system adapters never accept caller commands or paths.
type nativeDriver interface {
	Fence(context.Context, *os.Root) error
	Stop(context.Context) error
	Start(context.Context) error
	ProbeSchema(context.Context, bool) (Capability, error)         // false=current,true=previous
	ProbeCandidate(context.Context, Installed) (Capability, error) // fixed private .next, manager account, no DB
	WaitHealthy(context.Context, Installed, string) error
	WriteChallenge(context.Context, Challenge) error
	ReadDrain(context.Context) (DrainReceipt, error)
	ClearControl(context.Context) error
	LockManager(context.Context) (func(), error)
}
type activationJournal struct {
	Format            int               `json:"format"`
	Request           ActivationRequest `json:"request"`
	Nonce             string            `json:"nonce"`
	Prior             Installed         `json:"prior"`
	Candidate         Installed         `json:"candidate"`
	Phase             string            `json:"phase"`
	RollbackAttempted bool              `json:"rollback_attempted"`
}
type activationWorker struct {
	state, bin *os.Root
	binary     string
	driver     nativeDriver
}

func (w *activationWorker) save(ctx context.Context, j activationJournal) error {
	body, err := json.Marshal(j)
	if err != nil {
		return err
	}
	_ = w.state.Remove("activation.pending")
	if err := writeFile(ctx, w.state, "activation.pending", bytesReader(body), int64(len(body)), ""); err != nil {
		return err
	}
	if err := w.state.Rename("activation.pending", "activation.json"); err != nil {
		return err
	}
	return w.fence(ctx, w.state)
}
func (w *activationWorker) load(ctx context.Context) (activationJournal, error) {
	var j activationJournal
	body, err := readChecked(ctx, w.state, "activation.json", 8192)
	if err != nil {
		return j, err
	}
	if strictJSON(body, &j) != nil || j.Format != 1 || !validHash(j.Request.StageID) || !validHash(j.Nonce) || !validHash(j.Prior.SHA256) || !validHash(j.Candidate.SHA256) || j.Prior.Schema < 1 || j.Prior.Schema != j.Candidate.Schema || j.Request.ActorID < 1 {
		return j, errors.New("native activation journal requires manual repair")
	}
	return j, nil
}
func hashFile(ctx context.Context, root *os.Root, name string) (string, error) {
	before, err := root.Lstat(name)
	if err != nil {
		return "", err
	}
	if !before.Mode().IsRegular() || before.Size() < 1 || before.Size() > MaxArtifactBytes {
		return "", errors.New("unsafe executable file")
	}
	file, err := root.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return "", errors.New("executable changed while opening")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(contextReader{ctx, file}, MaxArtifactBytes+1))
	if err != nil {
		return "", err
	}
	after, err := root.Lstat(name)
	if err != nil || !os.SameFile(opened, after) || n != opened.Size() || after.Size() != opened.Size() {
		return "", errors.New("executable changed while hashing")
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}
func copyVerified(ctx context.Context, source *os.Root, from string, destination *os.Root, to string, expected Installed) error {
	if !validHash(expected.SHA256) {
		return errors.New("invalid installed executable receipt")
	}
	info, err := source.Lstat(from)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("unsafe source executable")
	}
	f, err := source.Open(from)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("source executable changed")
	}
	if err := writeFile(ctx, destination, to, f, info.Size(), expected.SHA256); err != nil {
		return err
	}
	file, err := destination.Open(to)
	if err != nil {
		return err
	}
	err = errors.Join(file.Chmod(0755), file.Sync(), file.Close())
	return err
}

// activate publishes only a previously reverified privileged snapshot. The old
// executable is copied, never renamed away, before atomic replacement.
func (w *activationWorker) activate(ctx context.Context, j activationJournal, candidateRoot *os.Root) (retErr error) {
	if err := w.fence(ctx, candidateRoot); err != nil {
		return err
	}
	if _, err := w.state.Lstat("activation.json"); err == nil {
		return errors.New("native activation journal already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	currentHash, err := hashFile(ctx, w.bin, w.binary)
	if err != nil || currentHash != j.Prior.SHA256 {
		return errors.New("current manager does not match bootstrap receipt")
	}
	j.Format = 1
	j.Phase = "prepared"
	if err := w.save(ctx, j); err != nil {
		return err
	}
	challenge := Challenge{Format: 1, Nonce: j.Nonce, FromVersion: j.Prior.Version, ToVersion: j.Candidate.Version, ActorID: j.Request.ActorID, ExpiresAt: time.Now().UTC().Add(9 * time.Minute)}
	if err := w.driver.WriteChallenge(ctx, challenge); err != nil {
		return err
	}
	if err := w.driver.Stop(ctx); err != nil {
		cause := fmt.Errorf("stop manager before activation: %w", err)
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		release, e := w.driver.LockManager(cleanup)
		if e != nil {
			return errors.Join(cause, e)
		}
		current, e := hashFile(cleanup, w.bin, w.binary)
		release()
		if e != nil || current != j.Prior.SHA256 {
			return errors.Join(cause, errors.New("manager stop outcome requires recovery"))
		}
		next := j
		next.Phase = "aborted"
		if e := w.save(cleanup, next); e != nil {
			return errors.Join(cause, e)
		}
		return errors.Join(cause, w.driver.ClearControl(cleanup), w.driver.Start(cleanup))
	}
	var release func()
	locked, completed, cleanupAttempted := false, false, false
	// One owner performs cleanup, with a budget independent of caller cancellation.
	// Memory-only phase changes cannot suppress this owner.
	defer func() {
		if locked {
			release()
			locked = false
		}
		if retErr == nil || completed || cleanupAttempted {
			return
		}
		cleanupAttempted = true
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		current, e := hashFile(cleanup, w.bin, w.binary)
		if e == nil && current == j.Prior.SHA256 && (j.Phase == "prepared" || j.Phase == "stopped") {
			next := j
			next.Phase = "aborted"
			if e := w.save(cleanup, next); e != nil {
				retErr = errors.Join(retErr, e)
				return
			}
			j = next
			retErr = errors.Join(retErr, w.driver.ClearControl(cleanup), w.driver.Start(cleanup))
			return
		}
		retErr = errors.Join(retErr, w.rollback(cleanup, &j))
	}()
	release, err = w.driver.LockManager(ctx)
	if err != nil {
		return err
	}
	locked = true
	receipt, err := w.driver.ReadDrain(ctx)
	if err != nil || !receipt.Drained || receipt.Nonce != j.Nonce || receipt.Version != j.Prior.Version || receipt.Schema != j.Prior.Schema || receipt.ExecutableSHA256 != j.Prior.SHA256 {
		return errors.New("manager did not prove a complete safe drain")
	}
	capability, err := w.driver.ProbeSchema(ctx, false)
	if err != nil || capability.Format != 1 || capability.Version != j.Prior.Version || capability.Schema != j.Prior.Schema || capability.ActorAuthorized == nil || !*capability.ActorAuthorized {
		return errors.New("live schema or current administrator authorization does not permit activation")
	}
	j.Phase = "stopped"
	if err := w.save(ctx, j); err != nil {
		return err
	}
	// No pre-existing executable scratch file is reused or overwritten.
	for _, name := range []string{w.binary + ".next", w.binary + ".previous"} {
		if _, e := w.bin.Lstat(name); e == nil {
			return errors.New("native executable scratch requires administrator reconciliation")
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	if err := copyVerified(ctx, w.bin, w.binary, w.bin, w.binary+".previous", j.Prior); err != nil {
		return err
	}
	if err := copyVerified(ctx, candidateRoot, w.binary, w.bin, w.binary+".next", j.Candidate); err != nil {
		return err
	}
	if err := w.fence(ctx, w.bin); err != nil {
		return err
	}
	// A signature authenticates bytes, not packaging consistency. Refuse a
	// candidate whose own no-DB capability differs before any normal startup.
	capability, err = w.driver.ProbeCandidate(ctx, j.Candidate)
	if err != nil || capability.Format != 1 || capability.Version != j.Candidate.Version || capability.Schema != j.Candidate.Schema || capability.Schema != j.Prior.Schema {
		return errors.New("candidate embedded version or schema does not match verified release; manual maintenance required")
	}
	candidateHash, err := hashFile(ctx, w.bin, w.binary+".next")
	if err != nil || candidateHash != j.Candidate.SHA256 {
		return errors.New("candidate changed during capability probe")
	}
	j.Phase = "replacing"
	if err := w.save(ctx, j); err != nil {
		return err
	}
	if err := w.bin.Rename(w.binary+".next", w.binary); err != nil {
		return err
	}
	if err := w.fence(ctx, w.bin); err != nil {
		return err
	}
	j.Phase = "candidate"
	if err := w.save(ctx, j); err != nil {
		return err
	}
	release()
	locked = false
	if err := w.driver.Start(ctx); err == nil {
		if err := w.driver.WaitHealthy(ctx, j.Candidate, j.Nonce); err == nil {
			j.Phase = "healthy"
			if err := w.recordInstalled(ctx, j.Candidate); err != nil {
				return err
			}
			if err := w.save(ctx, j); err != nil {
				return err
			}
			completed = true
			return w.driver.ClearControl(ctx)
		}
	}
	cleanupAttempted = true
	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return w.rollback(cleanup, &j)
}
func (w *activationWorker) recordInstalled(ctx context.Context, installed Installed) error {
	body, err := json.Marshal(installed)
	if err != nil {
		return err
	}
	_ = w.state.Remove("installed.pending")
	if err := writeFile(ctx, w.state, "installed.pending", bytesReader(body), int64(len(body)), ""); err != nil {
		return err
	}
	if err := w.state.Rename("installed.pending", "installed.json"); err != nil {
		return err
	}
	return w.fence(ctx, w.state)
}
func (w *activationWorker) rollback(ctx context.Context, j *activationJournal) error {
	return w.restorePrior(ctx, j, true)
}
func (w *activationWorker) restorePrior(ctx context.Context, j *activationJournal, start bool) error {
	if j.RollbackAttempted {
		return errors.New("automatic manager rollback already attempted; manual repair required")
	}
	next := *j
	next.RollbackAttempted = true
	next.Phase = "rollback"
	if err := w.save(ctx, next); err != nil {
		return err
	}
	*j = next
	if start {
		if err := w.driver.Stop(ctx); err != nil {
			return err
		}
	}
	release, err := w.driver.LockManager(ctx)
	if err != nil {
		return err
	}
	locked := true
	defer func() {
		if locked {
			release()
		}
	}()
	previous, err := hashFile(ctx, w.bin, w.binary+".previous")
	if err != nil || previous != j.Prior.SHA256 {
		return errors.New("previous manager executable does not match captured receipt")
	}
	capability, err := w.driver.ProbeSchema(ctx, true)
	if err != nil || capability.Format != 1 || capability.Schema != j.Prior.Schema || capability.Version != j.Prior.Version {
		return errors.New("previous manager cannot read current schema; manual maintenance required")
	}
	if err := w.bin.Rename(w.binary+".previous", w.binary); err != nil {
		return err
	}
	if err := w.fence(ctx, w.bin); err != nil {
		return err
	}
	if err := w.recordInstalled(ctx, j.Prior); err != nil {
		return err
	}
	release()
	locked = false
	// Boot recovery runs Before=manager and must never wait on starting that unit.
	if start {
		challenge := Challenge{Format: 1, Nonce: j.Nonce, FromVersion: j.Prior.Version, ToVersion: j.Prior.Version, ActorID: j.Request.ActorID, ExpiresAt: time.Now().UTC().Add(3 * time.Minute)}
		if err := w.driver.WriteChallenge(ctx, challenge); err != nil {
			return err
		}
		if err := w.driver.Start(ctx); err != nil {
			return err
		}
		if err := w.driver.WaitHealthy(ctx, j.Prior, j.Nonce); err != nil {
			return errors.New("previous manager executable restored but readiness failed; manual repair required")
		}
	}
	if err := w.driver.ClearControl(ctx); err != nil {
		return err
	}
	next = *j
	next.Phase = "rolled_back"
	if err := w.save(ctx, next); err != nil {
		return err
	}
	*j = next
	return nil
}
func (w *activationWorker) recover(ctx context.Context) error {
	j, err := w.load(ctx)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	switch j.Phase {
	case "healthy", "rolled_back", "aborted":
		return nil
	case "prepared", "stopped":
		hash, err := hashFile(ctx, w.bin, w.binary)
		if err != nil || hash != j.Prior.SHA256 {
			return errors.New("unpublished manager recovery is ambiguous")
		}
		if err := w.driver.ClearControl(ctx); err != nil {
			return err
		}
		j.Phase = "aborted"
		return w.save(ctx, j)
	case "replacing", "candidate":
		if err := w.driver.WriteChallenge(ctx, Challenge{Format: 1, Nonce: j.Nonce, FromVersion: j.Prior.Version, ToVersion: j.Candidate.Version, ActorID: j.Request.ActorID, ExpiresAt: time.Now().UTC().Add(3 * time.Minute)}); err != nil {
			return err
		}
		return w.restorePrior(ctx, &j, false)
	default:
		return errors.New("native update recovery requires manual repair")
	}
}

func (w *activationWorker) cleanupTerminal(ctx context.Context, installed Installed) error {
	journal, err := w.load(ctx)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if journal.Phase != "healthy" && journal.Phase != "rolled_back" && journal.Phase != "aborted" {
		return errors.New("pending manager update requires recovery")
	}
	hash, err := hashFile(ctx, w.bin, w.binary)
	if err != nil || hash != installed.SHA256 {
		return errors.New("installed manager does not match completed update receipt")
	}
	for _, name := range []string{w.binary + ".previous", w.binary + ".next"} {
		if err := w.bin.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := w.state.Remove("request.json"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := w.state.Remove("activation.json"); err != nil {
		return err
	}
	return errors.Join(w.fence(ctx, w.bin), w.fence(ctx, w.state))
}

func (w *activationWorker) fence(ctx context.Context, root *os.Root) error {
	if w.driver != nil {
		return w.driver.Fence(ctx, root)
	}
	return syncPrivilegedRoot(ctx, root)
}

func (b *nativeBroker) UnavailableReason() string {
	if !b.matches {
		return "Manager activation requires the canonical native data directory and 127.0.0.1:8080; custom deployments require manual maintenance."
	}
	return ""
}

// checkPendingNative reads only protected state; caller-side brokers must never
// create a lock inside the manager's read-only filesystem namespace.
func checkPendingNative(ctx context.Context, state *os.Root) error {
	body, err := readChecked(ctx, state, "activation.json", 8192)
	if errors.Is(err, os.ErrNotExist) {
		if _, e := state.Lstat("request.json"); e == nil {
			return errors.New("native update request is pending or requires reconciliation")
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
		return nil
	}
	if err != nil {
		return err
	}
	var journal activationJournal
	if strictJSON(body, &journal) != nil {
		return errors.New("native update recovery journal is invalid")
	}
	if journal.Phase != "healthy" && journal.Phase != "rolled_back" && journal.Phase != "aborted" {
		return errors.New("native update recovery is pending")
	}
	return nil
}

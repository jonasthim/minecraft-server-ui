package selfupdate

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
	instanceupdate "github.com/jonasthim/minecraft-server-ui/internal/update"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Capability struct {
	Format          int    `json:"format"`
	Version         string `json:"version"`
	Schema          int64  `json:"schema"`
	ActorAuthorized *bool  `json:"actor_authorized,omitempty"`
}
type Challenge struct {
	Format      int       `json:"format"`
	Nonce       string    `json:"nonce"`
	FromVersion string    `json:"from_version"`
	ToVersion   string    `json:"to_version"`
	ExpiresAt   time.Time `json:"expires_at"`
	ActorID     int64     `json:"actor_id"`
}
type Readiness struct {
	Format           int    `json:"format"`
	Status           string `json:"status"`
	Version          string `json:"version"`
	Schema           int64  `json:"schema"`
	ExecutableSHA256 string `json:"executable_sha256"`
}
type DrainReceipt struct {
	Format           int       `json:"format"`
	Nonce            string    `json:"nonce"`
	Version          string    `json:"version"`
	Schema           int64     `json:"schema"`
	ExecutableSHA256 string    `json:"executable_sha256"`
	Drained          bool      `json:"drained"`
	CreatedAt        time.Time `json:"created_at"`
}

func controlDirectory(dataDir string) string { return filepath.Join(dataDir, "selfupdate", "control") }
func readChecked(ctx context.Context, root *os.Root, name string, max int64) ([]byte, error) {
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() < 1 || before.Size() > max {
		return nil, errors.New("unsafe or oversized update state file")
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, errors.New("update state changed while opening")
	}
	b, err := io.ReadAll(io.LimitReader(contextReader{ctx, f}, max+1))
	if err != nil {
		return nil, err
	}
	after, err := root.Lstat(name)
	if err != nil || !os.SameFile(opened, after) || int64(len(b)) > max {
		return nil, errors.New("update state changed while reading")
	}
	return b, nil
}
func ReadChallenge(ctx context.Context, dataDir string) (Challenge, error) {
	var c Challenge
	root, err := safepath.OpenRoot(ctx, controlDirectory(dataDir))
	if err != nil {
		return c, err
	}
	defer root.Close()
	ordinary, err := fixedControlFilePresent(root, "challenge.json")
	if err != nil {
		return c, err
	}
	if !ordinary {
		return c, os.ErrNotExist
	}
	if err := rejectMaintenanceChallenge(root); err != nil {
		return c, err
	}
	body, err := readChecked(ctx, root, "challenge.json", 2048)
	if err != nil {
		return c, err
	}
	if err := strictJSON(body, &c); err != nil {
		return c, errors.New("invalid update challenge")
	}
	now := time.Now()
	if c.Format != 1 || !validHash(c.Nonce) || !c.ExpiresAt.After(now) || c.ExpiresAt.Sub(now) > 10*time.Minute {
		return c, errors.New("update challenge is expired or invalid")
	}
	if _, err := ParseStable(c.ToVersion); err != nil {
		return c, err
	}
	if _, err := ParseStable(c.FromVersion); err != nil {
		return c, err
	}
	return c, nil
}

// ExecutableSHA256 hashes the running image, including the original inode on
// Linux after a path rename. It never runs another executable.
func ExecutableSHA256(ctx context.Context) (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "linux" {
		path = "/proc/self/exe"
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > MaxArtifactBytes {
		return "", errors.New("running executable size is invalid")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(contextReader{ctx, f}, MaxArtifactBytes+1))
	if err != nil || n != info.Size() {
		return "", errors.New("running executable hash could not be established")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// NewReadiness returns a private finite readiness endpoint. No browser session
// or PAT is needed; the native worker owns a fresh one-use challenge. Readiness
// must be revoked by the integrator before shutdown admission closes.
func NewReadiness(dataDir, version string, schema int64, ready func() bool) http.Handler {
	challenge, challengeErr := ReadChallenge(context.Background(), dataDir)
	var mu sync.Mutex
	used := false
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		ip := net.ParseIP(host)
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		defer mu.Unlock()
		if r.Method != "GET" || r.URL.RawQuery != "" || err != nil || ip == nil || !ip.IsLoopback() || challengeErr != nil || challenge.ToVersion != version || !challenge.ExpiresAt.After(time.Now()) || used || subtle.ConstantTimeCompare([]byte(token), []byte(challenge.Nonce)) != 1 {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if ready == nil || !ready() {
			http.Error(w, "Manager is not ready", http.StatusServiceUnavailable)
			return
		}
		hash, err := ExecutableSHA256(r.Context())
		if err != nil {
			http.Error(w, "Executable identity unavailable", http.StatusServiceUnavailable)
			return
		}
		used = true
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Readiness{Format: 1, Status: "ready", Version: version, Schema: schema, ExecutableSHA256: hash})
	})
}

// ReadOnlySchema never calls db.Open/Migrate and never creates a missing DB.
// The native worker owns the manager lock before invoking the manager-UID probe.
func ReadOnlySchema(ctx context.Context, path string) (int64, error) {
	root, err := safepath.OpenRoot(ctx, filepath.Dir(path))
	if err != nil {
		return 0, err
	}
	defer root.Close()
	info, err := root.Lstat(filepath.Base(path))
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, errors.New("unsafe manager database")
	}
	database, err := sql.Open("sqlite", db.FileURI(path)+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(1000)")
	if err != nil {
		return 0, err
	}
	defer database.Close()
	database.SetMaxOpenConns(1)
	schema, err := db.CurrentVersion(ctx, database)
	if err != nil {
		return 0, err
	}
	if err := sameRoot(ctx, filepath.Dir(path), root); err != nil {
		return 0, err
	}
	after, err := root.Lstat(filepath.Base(path))
	if err != nil || !os.SameFile(info, after) {
		return 0, errors.New("manager database binding changed during probe")
	}
	return schema, nil
}

// PrepareDrainReceipt runs only after HTTP, schedule, jobs, observations and
// native shutdown have drained. It does not attempt startup-only lease recovery.
// Nil means there is no pending native update and normal shutdown can continue.
func PrepareDrainReceipt(ctx context.Context, database *sql.DB, dataDir, version string) (*DrainReceipt, error) {
	challenge, err := ReadChallenge(ctx, dataDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if challenge.FromVersion != version {
		return nil, errors.New("drain challenge does not match running manager")
	}
	schema, err := verifyDrainedState(ctx, database, dataDir)
	if err != nil {
		return nil, err
	}
	hash, err := ExecutableSHA256(ctx)
	if err != nil {
		return nil, err
	}
	return &DrainReceipt{Format: 1, Nonce: challenge.Nonce, Version: version, Schema: schema, ExecutableSHA256: hash, Drained: true, CreatedAt: time.Now().UTC()}, nil
}

// verifyDrainedState is shared by actor-bound web updates and local OS-admin
// maintenance. Callers bind its schema result to their own authorization.
func verifyDrainedState(ctx context.Context, database *sql.DB, dataDir string) (int64, error) {
	if database == nil {
		return 0, errors.New("manager database is unavailable for drain verification")
	}
	schema, err := db.CurrentVersion(ctx, database)
	if err != nil {
		return 0, err
	}
	tx, err := database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	queries := []string{`SELECT COUNT(*) FROM jobs WHERE state='running'`, `SELECT COUNT(*) FROM backup_leases WHERE state IN ('armed','unresolved')`, `SELECT COUNT(*) FROM backup_restores WHERE state!='complete'`, `SELECT COUNT(*) FROM content_journals`, `SELECT COUNT(*) FROM modpack_journals`, `SELECT COUNT(*) FROM lifecycle_activations`, `SELECT COUNT(*) FROM instance_mutations`, `SELECT COUNT(*) FROM update_transactions WHERE state NOT IN ('complete','rolled_back','superseded')`, `SELECT COUNT(*) FROM backup_update_rollbacks WHERE state!='complete'`}
	for _, query := range queries {
		var n int64
		if err := tx.QueryRowContext(ctx, query).Scan(&n); err != nil {
			return 0, fmt.Errorf("verify drained manager transaction state: %w", err)
		}
		if n != 0 {
			return 0, errors.New("manager has unfinished jobs, saving leases or file transactions; normal recovery is required")
		}
	}
	if err := instanceupdate.ValidateSupersession(ctx, tx); err != nil {
		return 0, fmt.Errorf("verify drained update ownership: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM instances LIMIT 10001`)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return 0, err
	}
	if len(ids) > 10000 {
		return 0, errors.New("instance drain verification limit exceeded")
	}
	paths, err := instance.NewPaths(dataDir)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		dirs, err := paths.For(id)
		if err != nil {
			return 0, err
		}
		for _, item := range []struct {
			dir   string
			names []string
		}{{dirs.State, []string{"config.pending.json", "backup-restore.json", "content-transaction.json", "modpack-transaction.json", "update-transaction.json", "update-world-risk.json"}}, {filepath.Join(dirs.Server, ".manager"), []string{"activation.pending.json"}}} {
			root, e := safepath.OpenRoot(ctx, item.dir)
			if errors.Is(e, os.ErrNotExist) {
				continue
			}
			if e != nil {
				return 0, e
			}
			for _, name := range item.names {
				_, e := root.Lstat(name)
				if !errors.Is(e, os.ErrNotExist) {
					root.Close()
					return 0, errors.New("instance file transaction blocker requires recovery")
				}
			}
			if e := root.Close(); e != nil {
				return 0, e
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return schema, nil
}

// PublishDrainReceipt must be called only after successful database.Close and
// before releasing the manager lock. The updater checks nonce/hash/schema again.
func PublishDrainReceipt(ctx context.Context, dataDir string, receipt *DrainReceipt) (err error) {
	if receipt == nil {
		return nil
	}
	challenge, err := ReadChallenge(ctx, dataDir)
	if err != nil {
		return err
	}
	if receipt.Nonce != challenge.Nonce || !receipt.Drained {
		return errors.New("drain receipt no longer matches native update")
	}
	root, err := safepath.OpenRoot(ctx, controlDirectory(dataDir))
	if err != nil {
		return err
	}
	defer root.Close()
	body, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	if err := writeFile(ctx, root, "drained.pending", strings.NewReader(string(body)), int64(len(body)), ""); err != nil {
		return err
	}
	if err := root.Rename("drained.pending", "drained.json"); err != nil {
		return err
	}
	return syncRoot(root)
}

// ReadOnlyActorAuthorized is used only by the fixed manager-UID schema probe;
// the actor ID comes from the native worker's bound challenge, never CLI args.
func ReadOnlyActorAuthorized(ctx context.Context, path string, id int64) (bool, error) {
	if id < 1 {
		return false, errors.New("native update actor is missing")
	}
	database, err := sql.Open("sqlite", db.FileURI(path)+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(1000)")
	if err != nil {
		return false, err
	}
	defer database.Close()
	var authorized bool
	if err := database.QueryRowContext(ctx, `SELECT role='admin' AND disabled=0 FROM users WHERE id=?`, id).Scan(&authorized); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("read native update actor authorization: %w", err)
	}
	return authorized, nil
}

type ProbeResult struct {
	Format           int    `json:"format"`
	Nonce            string `json:"nonce"`
	Version          string `json:"version"`
	Schema           int64  `json:"schema"`
	ExecutableSHA256 string `json:"executable_sha256"`
	ActorAuthorized  bool   `json:"actor_authorized"`
}

// WriteSchemaProbeResult is called only by the fixed Windows manager-account
// probe service. The caller never opens/migrates the application for serving.
func WriteSchemaProbeResult(ctx context.Context, dataDir, version string, embeddedSchema int64) error {
	challenge, err := ReadChallenge(ctx, dataDir)
	if err != nil {
		return err
	}
	root, err := safepath.OpenRoot(ctx, controlDirectory(dataDir))
	if err != nil {
		return err
	}
	defer root.Close()
	body, requestErr := readChecked(ctx, root, "capability.json", 2048)
	schema := embeddedSchema
	authorized := false
	if requestErr == nil {
		var request CandidateProbeRequest
		if strictJSON(body, &request) != nil || request.Format != 1 || request.Nonce != challenge.Nonce || request.Version != version || challenge.ToVersion != version || request.Schema != embeddedSchema || !validHash(request.SHA256) {
			return errors.New("candidate capability request mismatch")
		}
		hash, err := ExecutableSHA256(ctx)
		if err != nil || hash != request.SHA256 {
			return errors.New("candidate capability image mismatch")
		}
	} else {
		if !errors.Is(requestErr, os.ErrNotExist) {
			return requestErr
		}
		if challenge.FromVersion != version {
			return errors.New("probe challenge version mismatch")
		}
		schema, err = ReadOnlySchema(ctx, filepath.Join(dataDir, "manager.db"))
		if err != nil {
			return err
		}
		authorized, err = ReadOnlyActorAuthorized(ctx, filepath.Join(dataDir, "manager.db"), challenge.ActorID)
		if err != nil {
			return err
		}
	}
	hash, err := ExecutableSHA256(ctx)
	if err != nil {
		return err
	}
	result := ProbeResult{Format: 1, Nonce: challenge.Nonce, Version: version, Schema: schema, ExecutableSHA256: hash, ActorAuthorized: authorized}
	body, err = json.Marshal(result)
	if err != nil {
		return err
	}
	if err := writeFile(ctx, root, "probe.pending", bytesReader(body), int64(len(body)), ""); err != nil {
		return err
	}
	if err := root.Rename("probe.pending", "probe.json"); err != nil {
		return err
	}
	return syncRoot(root)
}

// CandidateProbeRequest is written only by the fixed native importer for the
// demand-start manager-account probe. It never selects a path or command.
type CandidateProbeRequest struct {
	Format  int    `json:"format"`
	Nonce   string `json:"nonce"`
	Version string `json:"version"`
	Schema  int64  `json:"schema"`
	SHA256  string `json:"sha256"`
}

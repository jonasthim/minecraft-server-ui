package selfupdate

import (
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
)

const maintenanceChallengeLimit = 4096
const maintenanceReceiptAgeLimit = 3 * time.Minute

// MaintenanceChallenge is separate from the actor-bound web update challenge.
// It authorizes only manager-side drain proof and one-use local readiness.
type MaintenanceChallenge struct {
	Format      int        `json:"format"`
	OperationID string     `json:"operation_id"`
	Nonce       string     `json:"nonce"`
	Mode        string     `json:"mode"`
	From        *Installed `json:"from,omitempty"`
	To          *Installed `json:"to,omitempty"`
	ExpiresAt   time.Time  `json:"expires_at"`
}

type MaintenanceDrainReceipt struct {
	Format      int       `json:"format"`
	OperationID string    `json:"operation_id"`
	Nonce       string    `json:"nonce"`
	Image       Installed `json:"image"`
	Drained     bool      `json:"drained"`
	CreatedAt   time.Time `json:"created_at"`
}

type MaintenanceReadiness struct {
	Format      int       `json:"format"`
	OperationID string    `json:"operation_id"`
	Image       Installed `json:"image"`
	Status      string    `json:"status"`
}

func ReadMaintenanceChallenge(ctx context.Context, dataDir string) (MaintenanceChallenge, error) {
	var challenge MaintenanceChallenge
	root, err := safepath.OpenRoot(ctx, controlDirectory(dataDir))
	if err != nil {
		return challenge, err
	}
	defer root.Close()
	return readMaintenanceChallengeRoot(ctx, root)
}

func readMaintenanceChallengeRoot(ctx context.Context, root *os.Root) (MaintenanceChallenge, error) {
	var challenge MaintenanceChallenge
	present, err := fixedControlFilePresent(root, "maintenance-challenge.json")
	if err != nil {
		return challenge, err
	}
	if !present {
		return challenge, os.ErrNotExist
	}
	ordinary, err := fixedControlFilePresent(root, "challenge.json")
	if err != nil {
		return challenge, err
	}
	if ordinary {
		return challenge, errors.New("conflicting native update challenges")
	}
	body, err := readChecked(ctx, root, "maintenance-challenge.json", maintenanceChallengeLimit)
	if err != nil {
		return challenge, err
	}
	if err := strictJSON(body, &challenge); err != nil {
		return challenge, errors.New("invalid maintenance challenge")
	}
	if err := validateMaintenanceChallenge(challenge, time.Now()); err != nil {
		return MaintenanceChallenge{}, err
	}
	return challenge, nil
}

func fixedControlFilePresent(root *os.Root, name string) (bool, error) {
	_, err := root.Lstat(name)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func maintenanceLinkCount(root *os.Root, name string) (uint32, error) {
	file, err := root.Open(name)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, errors.New("maintenance receipt link is not a regular file")
	}
	return fileLinkCount(file)
}

func rejectMaintenanceChallenge(root *os.Root) error {
	present, err := fixedControlFilePresent(root, "maintenance-challenge.json")
	if err != nil {
		return err
	}
	if present {
		return errors.New("conflicting native update challenges")
	}
	return nil
}

func validateMaintenanceChallenge(challenge MaintenanceChallenge, now time.Time) error {
	if challenge.Format != 1 || !validHash(challenge.OperationID) || !validHash(challenge.Nonce) ||
		!challenge.ExpiresAt.After(now) || challenge.ExpiresAt.Sub(now) > 10*time.Minute {
		return errors.New("maintenance challenge is expired or invalid")
	}
	switch challenge.Mode {
	case "apply":
		if challenge.To == nil || !validInstalledReceipt(*challenge.To) {
			return errors.New("maintenance apply challenge image is invalid")
		}
		if challenge.From != nil && (!validInstalledReceipt(*challenge.From) || challenge.From.Schema != challenge.To.Schema) {
			return errors.New("maintenance apply source image is invalid")
		}
	case "uninstall":
		if challenge.From == nil || challenge.To != nil || !validInstalledReceipt(*challenge.From) {
			return errors.New("maintenance uninstall challenge image is invalid")
		}
	default:
		return errors.New("maintenance challenge mode is invalid")
	}
	return nil
}

// PrepareMaintenanceDrainReceipt runs only after HTTP admission, jobs,
// observations, save leases and native shutdown have already drained. It does
// not perform startup recovery or consult web-user authorization.
func PrepareMaintenanceDrainReceipt(ctx context.Context, database *sql.DB, dataDir, version string) (*MaintenanceDrainReceipt, error) {
	challenge, err := ReadMaintenanceChallenge(ctx, dataDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if challenge.From == nil {
		return nil, errors.New("running manager cannot prove a fresh-install drain")
	}
	if version != challenge.From.Version {
		return nil, errors.New("maintenance source version does not match running manager")
	}
	if database == nil {
		return nil, errors.New("manager database is unavailable for maintenance drain")
	}
	schema, err := verifyDrainedState(ctx, database, dataDir)
	if err != nil {
		return nil, err
	}
	if schema != challenge.From.Schema {
		return nil, errors.New("maintenance source schema does not match manager database")
	}
	hash, err := ExecutableSHA256(ctx)
	if err != nil {
		return nil, err
	}
	if hash != challenge.From.SHA256 {
		return nil, errors.New("maintenance source image does not match running executable")
	}
	return &MaintenanceDrainReceipt{
		Format:      1,
		OperationID: challenge.OperationID,
		Nonce:       challenge.Nonce,
		Image:       *challenge.From,
		Drained:     true,
		CreatedAt:   time.Now().UTC(),
	}, nil
}

// PublishMaintenanceDrainReceipt is called only after the manager DB has
// closed successfully and while the caller still owns the manager data lock.
// Nil means that no local maintenance challenge was present.
// syncRoot provides directory durability on Unix. Windows intentionally uses
// a no-op here: this receipt is ephemeral evidence for the current live
// operation, not durable quiescence evidence. The privileged consumer must
// revalidate the fresh challenge and receipt, pending absence, process exit and
// data lock, then fence the volume before durable journal or asset changes.
func PublishMaintenanceDrainReceipt(ctx context.Context, dataDir string, receipt *MaintenanceDrainReceipt) error {
	if receipt == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := safepath.OpenRoot(ctx, controlDirectory(dataDir))
	if err != nil {
		return err
	}
	defer root.Close()
	challenge, err := readMaintenanceChallengeRoot(ctx, root)
	if err != nil {
		return err
	}
	if err := validateMaintenanceReceipt(*receipt, challenge, time.Now()); err != nil {
		return err
	}
	for _, name := range []string{"maintenance-drained.pending", "maintenance-drained.json"} {
		present, err := fixedControlFilePresent(root, name)
		if err != nil {
			return err
		}
		if present {
			return errors.New("maintenance receipt publication already exists")
		}
	}
	return publishMaintenanceDrainReceipt(ctx, root, challenge, receipt, nil)
}

// publishMaintenanceDrainReceipt publishes only through the two fixed names.
// beforeLink is a test seam for a competing output creation at the exact
// no-replace boundary; production always passes nil.
func publishMaintenanceDrainReceipt(ctx context.Context, root *os.Root, challenge MaintenanceChallenge, receipt *MaintenanceDrainReceipt, beforeLink func() error) (retErr error) {
	body, err := json.Marshal(receipt)
	if err != nil {
		return errors.New("maintenance receipt encoding failed")
	}
	if len(body) == 0 || len(body) > maintenanceChallengeLimit {
		return errors.New("maintenance receipt exceeds limit")
	}
	file, err := root.OpenFile("maintenance-drained.pending", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("maintenance receipt pending file could not be created")
	}
	info, statErr := file.Stat()
	if statErr != nil || !info.Mode().IsRegular() || info.Size() != 0 {
		closeErr := file.Close()
		return errors.Join(errors.New("maintenance receipt pending file is unsafe"), statErr, closeErr)
	}
	pendingOwned := true
	published := false
	defer func() {
		if !pendingOwned || published {
			return
		}
		current, statErr := root.Lstat("maintenance-drained.pending")
		if errors.Is(statErr, os.ErrNotExist) {
			return
		}
		if statErr != nil {
			retErr = errors.Join(retErr, errors.New("maintenance receipt pending cleanup identity is unavailable"), statErr)
			return
		}
		if !os.SameFile(info, current) {
			retErr = errors.Join(retErr, errors.New("maintenance receipt pending cleanup identity changed"))
			return
		}
		if removeErr := root.Remove("maintenance-drained.pending"); removeErr != nil {
			retErr = errors.Join(retErr, errors.New("maintenance receipt pending cleanup failed"), removeErr)
		}
	}()
	fileClosed := false
	closeFile := func() error {
		if fileClosed {
			return nil
		}
		fileClosed = true
		return file.Close()
	}
	defer func() {
		if closeErr := closeFile(); closeErr != nil {
			retErr = errors.Join(retErr, errors.New("maintenance receipt pending close failed"), closeErr)
		}
	}()
	written, writeErr := io.Copy(file, contextReader{ctx, bytes.NewReader(body)})
	if writeErr != nil || written != int64(len(body)) {
		return errors.Join(errors.New("maintenance receipt write failed"), writeErr, ctx.Err())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(errors.New("maintenance receipt sync failed"), err)
	}
	if err := closeFile(); err != nil {
		return errors.Join(errors.New("maintenance receipt close failed"), err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := readMaintenanceChallengeRoot(ctx, root)
	if err != nil || !sameMaintenanceChallenge(challenge, current) {
		if err == nil {
			err = errors.New("maintenance challenge changed before publication")
		}
		return err
	}
	pending, err := root.Lstat("maintenance-drained.pending")
	if err != nil || !pending.Mode().IsRegular() || !os.SameFile(info, pending) {
		return errors.New("maintenance receipt pending identity changed")
	}
	links, err := maintenanceLinkCount(root, "maintenance-drained.pending")
	if err != nil || links != 1 {
		return errors.New("maintenance receipt has an unexpected hard link")
	}
	if beforeLink != nil {
		if err := beforeLink(); err != nil {
			return err
		}
	}
	// Link is an atomic exclusive publication: unlike Root.Rename it cannot
	// replace an output that appeared after the preceding fixed-name check.
	if err := root.Link("maintenance-drained.pending", "maintenance-drained.json"); err != nil {
		return errors.New("maintenance receipt output already exists or cannot be published")
	}
	published = true
	linkedPending, pendingErr := root.Lstat("maintenance-drained.pending")
	linkedOutput, outputErr := root.Lstat("maintenance-drained.json")
	if pendingErr != nil || outputErr != nil || !linkedPending.Mode().IsRegular() || !linkedOutput.Mode().IsRegular() ||
		!os.SameFile(info, linkedPending) || !os.SameFile(info, linkedOutput) {
		return errors.New("maintenance receipt link identities could not be verified")
	}
	links, err = maintenanceLinkCount(root, "maintenance-drained.pending")
	if err != nil || links != 2 {
		return errors.New("maintenance receipt has unexpected hard links after publication")
	}
	if err := syncRoot(root); err != nil {
		return errors.Join(errors.New("maintenance receipt directory sync failed"), err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err = readMaintenanceChallengeRoot(ctx, root)
	if err != nil || !sameMaintenanceChallenge(challenge, current) {
		if err == nil {
			err = errors.New("maintenance challenge changed before publication")
		}
		return err
	}
	linkedPending, pendingErr = root.Lstat("maintenance-drained.pending")
	linkedOutput, outputErr = root.Lstat("maintenance-drained.json")
	if pendingErr != nil || outputErr != nil || !linkedPending.Mode().IsRegular() || !linkedOutput.Mode().IsRegular() ||
		!os.SameFile(info, linkedPending) || !os.SameFile(info, linkedOutput) {
		return errors.New("maintenance receipt link identities changed before cleanup")
	}
	if err := root.Remove("maintenance-drained.pending"); err != nil {
		return errors.Join(errors.New("maintenance receipt pending cleanup failed"), err)
	}
	pendingOwned = false
	pending, pendingErr = root.Lstat("maintenance-drained.pending")
	output, outputErr := root.Lstat("maintenance-drained.json")
	if !errors.Is(pendingErr, os.ErrNotExist) || outputErr != nil || !output.Mode().IsRegular() || !os.SameFile(info, output) {
		return errors.New("maintenance receipt final identity fence failed")
	}
	links, err = maintenanceLinkCount(root, "maintenance-drained.json")
	if err != nil || links != 1 {
		return errors.New("maintenance receipt has unexpected final hard links")
	}
	if err := syncRoot(root); err != nil {
		return errors.Join(errors.New("maintenance receipt final directory sync failed"), err)
	}
	return nil
}

func validateMaintenanceReceipt(receipt MaintenanceDrainReceipt, challenge MaintenanceChallenge, now time.Time) error {
	if challenge.From == nil || receipt.Format != 1 || !receipt.Drained || receipt.OperationID != challenge.OperationID ||
		receipt.Nonce != challenge.Nonce || receipt.Image != *challenge.From || receipt.CreatedAt.IsZero() ||
		receipt.CreatedAt.After(now) || now.Sub(receipt.CreatedAt) > maintenanceReceiptAgeLimit {
		return errors.New("maintenance drain receipt does not match current challenge")
	}
	return nil
}

func sameMaintenanceChallenge(a, b MaintenanceChallenge) bool {
	if a.Format != b.Format || a.OperationID != b.OperationID || a.Nonce != b.Nonce || a.Mode != b.Mode || !a.ExpiresAt.Equal(b.ExpiresAt) {
		return false
	}
	if (a.From == nil) != (b.From == nil) || (a.To == nil) != (b.To == nil) {
		return false
	}
	if a.From != nil && *a.From != *b.From {
		return false
	}
	return a.To == nil || *a.To == *b.To
}

// NewMaintenanceReadiness exposes one loopback-only readiness proof bound to
// the exact apply challenge and the running executable image.
func NewMaintenanceReadiness(dataDir, version string, schema int64, ready func() bool) http.Handler {
	challenge, challengeErr := ReadMaintenanceChallenge(context.Background(), dataDir)
	want := Installed{Format: 1, Version: version, Schema: schema}
	if challengeErr == nil {
		if challenge.Mode != "apply" || challenge.To == nil || challenge.To.Version != version || challenge.To.Schema != schema {
			challengeErr = errors.New("maintenance readiness image does not match challenge")
		} else if hash, err := ExecutableSHA256(context.Background()); err != nil {
			challengeErr = err
		} else {
			want.SHA256 = hash
			if *challenge.To != want {
				challengeErr = errors.New("maintenance readiness image does not match challenge")
			}
		}
	}
	var mu sync.Mutex
	used := false
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		mu.Lock()
		defer mu.Unlock()
		if r.Method != http.MethodGet || r.URL.Path != "/internal/maintenance-ready" || r.URL.RawQuery != "" || challengeErr != nil {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		host, _, splitErr := net.SplitHostPort(r.RemoteAddr)
		ip := net.ParseIP(host)
		authorization := r.Header.Get("Authorization")
		if splitErr != nil || ip == nil || !ip.IsLoopback() || !strings.HasPrefix(authorization, "Bearer ") || used ||
			subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(authorization, "Bearer ")), []byte(challenge.Nonce)) != 1 {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		current, err := ReadMaintenanceChallenge(r.Context(), dataDir)
		if err != nil || !sameMaintenanceChallenge(challenge, current) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if ready == nil || !ready() {
			http.Error(w, "Manager is not ready", http.StatusServiceUnavailable)
			return
		}
		actualHash, err := ExecutableSHA256(r.Context())
		if err != nil || actualHash != want.SHA256 {
			http.Error(w, "Executable identity unavailable", http.StatusServiceUnavailable)
			return
		}
		used = true
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(MaintenanceReadiness{Format: 1, OperationID: challenge.OperationID, Image: want, Status: "ready"})
	})
}

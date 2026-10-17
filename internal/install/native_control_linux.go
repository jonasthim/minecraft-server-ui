//go:build linux

package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
	"github.com/jonasthim/minecraft-server-ui/internal/selfupdate"
	"syscall"
)

type linuxControlFile struct {
	info os.FileInfo
	body []byte
}

func (d *linuxInstallDriver) openControl(ctx context.Context, principal linuxPrincipal) (*os.Root, error) {
	path := filepath.Join(d.hooks.dataRoot, linuxControlDirectory)
	root, err := safepath.OpenRoot(ctx, path)
	if err != nil {
		return nil, errors.New("manager maintenance control directory is unavailable")
	}
	if err := checkManagerControlRoot(root, principal); err != nil {
		_ = root.Close()
		return nil, err
	}
	if err := checkLinuxControlEntries(root); err != nil {
		_ = root.Close()
		return nil, err
	}
	return root, nil
}

func checkLinuxControlEntries(root *os.Root) error {
	entries, err := readLinuxRootDir(root)
	if err != nil {
		return err
	}
	allowed := map[string]bool{"maintenance-challenge.json": true, "maintenance-drained.json": true, "maintenance-drained.pending": true, "challenge.json": true, "drained.json": true, "drained.pending": true, "request.json": true}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return errors.New("unknown maintenance control entry requires review")
		}
	}
	return nil
}

func checkManagerControlRoot(root *os.Root, principal linuxPrincipal) error {
	info, err := root.Stat(".")
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || st.Nlink < 2 || st.Uid != principal.UID || st.Gid != principal.GID || info.Mode().Perm() != 0700 {
		return errors.New("manager maintenance control directory is unsafe")
	}
	return nil
}

func readLinuxControlFile(ctx context.Context, root *os.Root, name string, principal linuxPrincipal, max int64) (linuxControlFile, error) {
	var result linuxControlFile
	if err := ctx.Err(); err != nil {
		return result, err
	}
	info, err := root.Lstat(name)
	if err != nil {
		return result, err
	}
	if err := checkControlFileIdentity(info, principal, max); err != nil {
		return result, err
	}
	file, err := root.Open(name)
	if err != nil {
		return result, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = file.Close()
		return result, errors.New("maintenance control file identity changed")
	}
	body, readErr := io.ReadAll(io.LimitReader(file, max+1))
	closeErr := file.Close()
	after, statErr := root.Lstat(name)
	if readErr != nil || closeErr != nil || statErr != nil || !os.SameFile(info, after) || len(body) == 0 || int64(len(body)) > max {
		return result, errors.Join(errors.New("maintenance control file read failed"), readErr, closeErr, statErr)
	}
	return linuxControlFile{info: info, body: body}, nil
}

func checkControlFileIdentity(info os.FileInfo, principal linuxPrincipal, max int64) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || info.Size() < 1 || info.Size() > max || st.Nlink != 1 || st.Uid != principal.UID || st.Gid != principal.GID {
		return errors.New("maintenance control file is unsafe")
	}
	return nil
}

func (d *linuxInstallDriver) writeMaintenanceChallenge(ctx context.Context, tx transaction, intent linuxIntent, principal linuxPrincipal) error {
	root, err := d.openControl(ctx, principal)
	if err != nil {
		return err
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, readDirErr := directory.ReadDir(-1)
	closeDirErr := directory.Close()
	if readDirErr != nil || closeDirErr != nil {
		return errors.Join(readDirErr, closeDirErr)
	}
	allowed := map[string]bool{"maintenance-challenge.json": true, "maintenance-drained.json": true, "maintenance-drained.pending": true, "challenge.json": true, "drained.json": true, "drained.pending": true, "request.json": true}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return errors.New("unknown maintenance control entry requires review")
		}
	}
	for _, name := range []string{"challenge.json", "drained.json", "drained.pending", "request.json"} {
		if _, err := root.Lstat(name); err == nil {
			return errors.New("ordinary updater control state conflicts with local maintenance")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for _, name := range []string{"maintenance-challenge.json", "maintenance-drained.json", "maintenance-drained.pending"} {
		if _, err := root.Lstat(name); err == nil {
			return errors.New("maintenance control state already exists")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	challenge := selfupdate.MaintenanceChallenge{Format: 1, OperationID: tx.ID, Nonce: intent.Nonce, Mode: string(intent.Mode), ExpiresAt: d.hooks.now().UTC().Add(10 * time.Minute)}
	if intent.Prior != nil {
		from := selfupdate.Installed{Format: 1, Version: intent.Prior.Version, Schema: intent.Prior.Schema, SHA256: intent.Prior.SHA256}
		challenge.From = &from
	}
	if intent.Target != nil {
		to := selfupdate.Installed{Format: 1, Version: intent.Target.Version, Schema: intent.Target.Schema, SHA256: intent.Target.SHA256}
		challenge.To = &to
	}
	body, err := json.Marshal(challenge)
	if err != nil || len(body) > 4096 {
		return errors.New("maintenance challenge encoding is invalid")
	}
	file, err := root.OpenFile("maintenance-challenge.json", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	info, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return errors.Join(errors.New("new maintenance challenge is unsafe"), statErr)
	}
	writeErr := writeAll(file, body)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if writeErr == nil {
		writeErr = file.Chown(int(principal.UID), int(principal.GID))
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return errors.Join(writeErr, closeErr)
	}
	if err := syncDirectory(root); err != nil {
		return err
	}
	current, err := root.Lstat("maintenance-challenge.json")
	if err != nil || !os.SameFile(info, current) || checkControlFileIdentity(current, principal, 4096) != nil {
		return errors.Join(errors.New("maintenance challenge publication is uncertain"), err)
	}
	return nil
}

func (d *linuxInstallDriver) readMaintenanceProof(ctx context.Context, intent linuxIntent, principal linuxPrincipal) (selfupdate.MaintenanceChallenge, selfupdate.MaintenanceDrainReceipt, error) {
	var challenge selfupdate.MaintenanceChallenge
	var receipt selfupdate.MaintenanceDrainReceipt
	root, err := d.openControl(ctx, principal)
	if err != nil {
		return challenge, receipt, err
	}
	defer root.Close()
	challengeFile, err := readLinuxControlFile(ctx, root, "maintenance-challenge.json", principal, 4096)
	if err != nil {
		return challenge, receipt, err
	}
	if err := decodeLinuxStrictJSON(challengeFile.body, 4096, &challenge); err != nil || !linuxChallengeMatches(challenge, intent, d.hooks.now()) {
		return challenge, receipt, errors.New("maintenance challenge does not match installer intent")
	}
	receiptFile, err := readLinuxControlFile(ctx, root, "maintenance-drained.json", principal, 4096)
	if err != nil {
		return challenge, receipt, errors.New("manager drain receipt is unavailable")
	}
	if err := decodeLinuxStrictJSON(receiptFile.body, 4096, &receipt); err != nil {
		return challenge, receipt, errors.New("manager drain receipt is malformed")
	}
	if challenge.From == nil || receipt.Format != 1 || !receipt.Drained || receipt.OperationID != intent.ID || receipt.Nonce != intent.Nonce || receipt.Image != *challenge.From || receipt.CreatedAt.IsZero() || receipt.CreatedAt.After(d.hooks.now()) || d.hooks.now().Sub(receipt.CreatedAt) > 3*time.Minute {
		return challenge, receipt, errors.New("manager drain receipt does not match current challenge")
	}
	for _, name := range []string{"maintenance-drained.pending", "drained.json", "drained.pending", "challenge.json"} {
		if _, err := root.Lstat(name); err == nil {
			return challenge, receipt, errors.New("pending or conflicting manager control state remains")
		} else if !errors.Is(err, os.ErrNotExist) {
			return challenge, receipt, err
		}
	}
	return challenge, receipt, nil
}

func linuxChallengeMatches(ch selfupdate.MaintenanceChallenge, intent linuxIntent, now time.Time) bool {
	if ch.Format != 1 || ch.OperationID != intent.ID || ch.Nonce != intent.Nonce || ch.Mode != string(intent.Mode) || !ch.ExpiresAt.After(now) || ch.ExpiresAt.Sub(now) > 10*time.Minute {
		return false
	}
	if (ch.From == nil) != (intent.Prior == nil) || (ch.To == nil) != (intent.Target == nil) {
		return false
	}
	if ch.From != nil && *ch.From != (selfupdate.Installed{Format: 1, Version: intent.Prior.Version, Schema: intent.Prior.Schema, SHA256: intent.Prior.SHA256}) {
		return false
	}
	if ch.To != nil && *ch.To != (selfupdate.Installed{Format: 1, Version: intent.Target.Version, Schema: intent.Target.Schema, SHA256: intent.Target.SHA256}) {
		return false
	}
	return true
}

func (d *linuxInstallDriver) clearOwnMaintenanceControl(ctx context.Context, intent linuxIntent, principal linuxPrincipal) error {
	return d.clearOwnMaintenanceControlMode(ctx, intent, principal, false)
}

// clearOwnMaintenanceControlPreservingUpdater is used only during rollback
// after the manager is stopped and both installer locks are held. It removes
// exact native-owned challenge files while leaving ordinary updater controls
// untouched for the subsequent strict SnapshotUpdater/ReconcileUpdater path.
func (d *linuxInstallDriver) clearOwnMaintenanceControlPreservingUpdater(ctx context.Context, intent linuxIntent, principal linuxPrincipal) error {
	return d.clearOwnMaintenanceControlMode(ctx, intent, principal, true)
}

func (d *linuxInstallDriver) clearOwnMaintenanceControlMode(ctx context.Context, intent linuxIntent, principal linuxPrincipal, preserveUpdater bool) error {
	root, err := d.openControl(ctx, principal)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	entries, err := readLinuxRootDir(root)
	if err != nil {
		return err
	}
	allowed := map[string]bool{"maintenance-challenge.json": true, "maintenance-drained.json": true}
	if preserveUpdater {
		allowed["challenge.json"] = true
		allowed["drained.json"] = true
		allowed["drained.pending"] = true
		allowed["request.json"] = true
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return errors.New("foreign or pending maintenance control state requires review")
		}
	}
	challengeIntents := map[string]linuxIntent{intent.Nonce: intent}
	if evidence, _, _, evidenceErr := d.readEvidence(ctx); evidenceErr == nil && lowerHex(evidence.RollbackNonce, 64) {
		rollback := rollbackIntent(intent)
		rollback.Nonce = evidence.RollbackNonce
		challengeIntents[evidence.RollbackNonce] = rollback
	}
	files := make(map[string]linuxControlFile, 2)
	for _, name := range []string{"maintenance-challenge.json", "maintenance-drained.json"} {
		file, readErr := readLinuxControlFile(ctx, root, name, principal, 4096)
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			return readErr
		}
		if name == "maintenance-challenge.json" {
			var challenge selfupdate.MaintenanceChallenge
			if decodeLinuxStrictJSON(file.body, 4096, &challenge) != nil {
				return errors.New("maintenance challenge is not owned by this operation")
			}
			candidate, owned := challengeIntents[challenge.Nonce]
			if !owned || !linuxChallengeOwned(challenge, candidate) {
				return errors.New("maintenance challenge is not owned by this operation")
			}
			files[name] = file
		}
		if name == "maintenance-drained.json" {
			var receipt selfupdate.MaintenanceDrainReceipt
			var challenge selfupdate.MaintenanceChallenge
			challengeFile, haveChallenge := files["maintenance-challenge.json"]
			if !haveChallenge || decodeLinuxStrictJSON(challengeFile.body, 4096, &challenge) != nil || decodeLinuxStrictJSON(file.body, 4096, &receipt) != nil || receipt.OperationID != intent.ID || receipt.Nonce != challenge.Nonce || challenge.From == nil || receipt.Image != *challenge.From || !receipt.Drained || receipt.Format != 1 || receipt.CreatedAt.IsZero() || receipt.CreatedAt.Before(intent.CreatedAt) || receipt.CreatedAt.After(d.hooks.now()) {
				return errors.New("maintenance drain receipt is not owned by this operation")
			}
			candidate, owned := challengeIntents[challenge.Nonce]
			if !owned || !linuxChallengeOwned(challenge, candidate) {
				return errors.New("maintenance drain challenge is not owned by this operation")
			}
			files[name] = file
		}
	}
	if _, drained := files["maintenance-drained.json"]; drained {
		if _, challenge := files["maintenance-challenge.json"]; !challenge {
			return errors.New("drain receipt without its challenge requires review")
		}
	}
	// Validate every proof and identity before deleting either member of the pair.
	// Remove the drain receipt first so the manager never sees an orphan challenge
	// paired with a receipt that we have already removed.
	for _, name := range []string{"maintenance-drained.json", "maintenance-challenge.json"} {
		file, exists := files[name]
		if !exists {
			continue
		}
		current, statErr := root.Lstat(name)
		if statErr != nil || !os.SameFile(file.info, current) {
			return errors.Join(errors.New("maintenance control identity changed before cleanup"), statErr)
		}
	}
	for _, name := range []string{"maintenance-drained.json", "maintenance-challenge.json"} {
		if _, exists := files[name]; !exists {
			continue
		}
		if err := root.Remove(name); err != nil {
			return err
		}
	}
	return syncDirectory(root)
}

func linuxChallengeOwned(challenge selfupdate.MaintenanceChallenge, intent linuxIntent) bool {
	if !linuxChallengeMatchesFields(challenge, intent) || challenge.ExpiresAt.IsZero() || challenge.ExpiresAt.Before(intent.CreatedAt) {
		return false
	}
	return true
}

func linuxChallengeMatchesFields(ch selfupdate.MaintenanceChallenge, intent linuxIntent) bool {
	if ch.Format != 1 || ch.OperationID != intent.ID || ch.Nonce != intent.Nonce || ch.Mode != string(intent.Mode) {
		return false
	}
	if (ch.From == nil) != (intent.Prior == nil) || (ch.To == nil) != (intent.Target == nil) {
		return false
	}
	if ch.From != nil && *ch.From != (selfupdate.Installed{Format: 1, Version: intent.Prior.Version, Schema: intent.Prior.Schema, SHA256: intent.Prior.SHA256}) {
		return false
	}
	if ch.To != nil && *ch.To != (selfupdate.Installed{Format: 1, Version: intent.Target.Version, Schema: intent.Target.Schema, SHA256: intent.Target.SHA256}) {
		return false
	}
	return true
}

func (d *linuxInstallDriver) probeSchema(ctx context.Context, binary string, principal linuxPrincipal, wantSchema int64) error {
	if d.hooks.probeSchema != nil {
		return d.hooks.probeSchema(ctx, binary, principal, wantSchema)
	}
	command := exec.CommandContext(ctx, "/usr/sbin/runuser", "-u", linuxInstallerAccount, "--", binary, "update-schema")
	command.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "MINECRAFT_UI_DATA_DIR=" + d.hooks.dataRoot, "MINECRAFT_UI_ADDR=127.0.0.1:8080"}
	var stdout limitedServiceOutput
	stdout.limit = 8192
	command.Stdout, command.Stderr = &stdout, io.Discard
	if err := command.Run(); err != nil || stdout.exceeded {
		return errors.New("manager-account read-only schema probe failed")
	}
	var capability selfupdate.Capability
	if decodeLinuxStrictJSON(stdout.buf, 8192, &capability) != nil || capability.Format != 1 || capability.Version == "" || capability.Schema != wantSchema || capability.ActorAuthorized != nil && !*capability.ActorAuthorized {
		return errors.New("manager-account schema probe does not match installed layout")
	}
	return nil
}

func (d *linuxInstallDriver) probeCandidate(ctx context.Context, principal linuxPrincipal, image Image) error {
	path := linuxManagerExecutablePath
	if d.hooks.probeSchema != nil {
		return d.hooks.probeSchema(ctx, path, principal, image.Schema)
	}
	command := exec.CommandContext(ctx, "/usr/sbin/runuser", "-u", linuxInstallerAccount, "--", path, "update-capability")
	command.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "MINECRAFT_UI_DATA_DIR=" + d.hooks.dataRoot}
	var stdout limitedServiceOutput
	stdout.limit = 8192
	command.Stdout, command.Stderr = &stdout, io.Discard
	if err := command.Run(); err != nil || stdout.exceeded {
		return errors.New("manager-account candidate capability probe failed")
	}
	var capability selfupdate.Capability
	if decodeLinuxStrictJSON(stdout.buf, 8192, &capability) != nil || capability.Format != 1 || capability.Version != image.Version || capability.Schema != image.Schema {
		return errors.New("candidate capability differs from immutable target")
	}
	return nil
}

func (d *linuxInstallDriver) waitMaintenanceReady(ctx context.Context, tx transaction, intent linuxIntent) error {
	if d.hooks.ready != nil {
		return d.hooks.ready(ctx, tx, intent)
	}
	if intent.Target == nil {
		return errors.New("maintenance readiness target is unavailable")
	}
	deadline, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("maintenance readiness redirect is forbidden")
	}}
	defer client.CloseIdleConnections()
	for {
		status, err := d.inspectService(deadline)
		if err == nil && linuxStatusActive(status) && requireLinuxManagerImage(deadline, *intent.Target) == nil {
			request, reqErr := http.NewRequestWithContext(deadline, http.MethodGet, "http://127.0.0.1:8080/internal/maintenance-ready", nil)
			if reqErr == nil {
				request.Header.Set("Authorization", "Bearer "+intent.Nonce)
				response, reqErr := client.Do(request)
				if reqErr == nil {
					body, readErr := io.ReadAll(io.LimitReader(response.Body, 4097))
					closeErr := response.Body.Close()
					var ready selfupdate.MaintenanceReadiness
					if readErr == nil && closeErr == nil && len(body) <= 4096 && response.StatusCode == http.StatusOK && strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") && decodeLinuxStrictJSON(body, 4096, &ready) == nil && ready.Format == 1 && ready.OperationID == tx.ID && ready.Status == "ready" && ready.Image.Version == intent.Target.Version && ready.Image.Schema == intent.Target.Schema && ready.Image.SHA256 == intent.Target.SHA256 {
						return nil
					}
				}
			}
		}
		select {
		case <-deadline.Done():
			return errors.New("manager maintenance readiness gate failed")
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (d *linuxInstallDriver) provisionAccountAndData(ctx context.Context, intent linuxIntent) (linuxPrincipal, error) {
	principal, present, err := d.inspectAccount(ctx)
	if err != nil {
		return linuxPrincipal{}, err
	}
	created := false
	if !present {
		if intent.Principal != nil {
			return linuxPrincipal{}, errors.New("captured manager account disappeared")
		}
		if d.hooks.createUser != nil {
			principal, err = d.hooks.createUser(ctx)
		} else {
			cmd := linuxUseraddCommand(ctx)
			err = cmd.Run()
			if err == nil {
				principal, present, err = d.inspectAccount(ctx)
			}
		}
		if err != nil || (!present && d.hooks.createUser == nil) {
			return linuxPrincipal{}, errors.Join(errors.New("fixed manager service account could not be created"), err)
		}
		created = true
		if err := d.setCreatedPrincipal(ctx, principal); err != nil {
			return linuxPrincipal{}, fmt.Errorf("persist created manager account identity: %w", err)
		}
	} else if intent.Principal != nil && !sameLinuxPrincipal(principal, *intent.Principal) {
		return linuxPrincipal{}, errors.New("manager service account identity changed")
	}
	if err := d.setExistingPrincipal(ctx, principal); err != nil {
		return linuxPrincipal{}, err
	}
	if err := ensureLinuxDataRoot(ctx, d.hooks.dataRoot, principal, created); err != nil {
		return linuxPrincipal{}, err
	}
	return principal, nil
}

func linuxUseraddCommand(ctx context.Context) *exec.Cmd {
	command := exec.CommandContext(ctx, "/usr/sbin/useradd", "--system", "--user-group", "--no-create-home", "--home-dir", linuxInstallerData, "--shell", "/usr/sbin/nologin", linuxInstallerAccount)
	command.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8"}
	return command
}

func ensureLinuxDataDirectories(ctx context.Context, rootPath string, principal linuxPrincipal, createdRoot bool) error {
	if err := ensureLinuxDataRoot(ctx, rootPath, principal, createdRoot); err != nil {
		return err
	}
	root, err := safepath.OpenRoot(ctx, rootPath)
	if err != nil {
		return errors.New("manager data root changed before directory provisioning")
	}
	defer root.Close()
	if err := validateLinuxDataRootHandle(root, principal); err != nil {
		return err
	}
	for _, relative := range []string{"selfupdate", "selfupdate/control", "selfupdate/stages", "backups"} {
		parent := root
		var ownedParent *os.Root
		parts := strings.Split(relative, "/")
		for i, part := range parts {
			created, child, err := ensureLinuxPrivateDirectory(ctx, parent, part, principal)
			if err != nil {
				if ownedParent != nil {
					_ = ownedParent.Close()
				}
				return fmt.Errorf("manager data directory %s is unsafe: %w", relative, err)
			}
			if ownedParent != nil {
				_ = ownedParent.Close()
			}
			ownedParent = child
			parent = child
			if i == len(parts)-1 {
				if err := syncDirectory(child); err != nil {
					_ = child.Close()
					return err
				}
			}
			_ = created // creation is reflected by the descriptor-bound chown path.
		}
		if ownedParent != nil {
			if err := ownedParent.Close(); err != nil {
				return err
			}
		}
	}
	return nil
}

func ensureLinuxDataRoot(ctx context.Context, rootPath string, principal linuxPrincipal, create bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	parentPath, leaf := filepath.Dir(rootPath), filepath.Base(rootPath)
	if filepath.Clean(rootPath) != rootPath || !filepath.IsAbs(rootPath) || !filepath.IsLocal(leaf) || leaf == "." || leaf == ".." {
		return errors.New("manager data root path is not canonical")
	}
	parent, err := safepath.OpenRoot(ctx, parentPath)
	if err != nil {
		return errors.New("manager data parent is unsafe")
	}
	defer parent.Close()
	info, err := parent.Lstat(leaf)
	created := false
	if errors.Is(err, os.ErrNotExist) && create {
		if err := parent.Mkdir(leaf, 0700); err != nil {
			return errors.New("new manager data directory could not be created")
		}
		created = true
		if err := syncDirectory(parent); err != nil {
			return err
		}
		info, err = parent.Lstat(leaf)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("manager data directory is unavailable or unsafe")
	}
	root, err := parent.OpenRoot(leaf)
	if err != nil {
		return errors.New("manager data directory could not be pinned")
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("manager data directory changed while opening")
	}
	if created {
		file, err := root.Open(".")
		if err != nil {
			return err
		}
		before, statErr := file.Stat()
		chownErr := file.Chown(int(principal.UID), int(principal.GID))
		after, afterErr := file.Stat()
		closeErr := file.Close()
		if statErr != nil || chownErr != nil || afterErr != nil || closeErr != nil || !os.SameFile(opened, before) || !os.SameFile(opened, after) {
			return errors.Join(errors.New("new manager data directory ownership could not be set safely"), statErr, chownErr, afterErr, closeErr)
		}
		if err := syncDirectory(root); err != nil {
			return err
		}
	}
	if err := validateLinuxDataRootHandle(root, principal); err != nil {
		return err
	}
	return nil
}

func validateLinuxDataRootHandle(root *os.Root, principal linuxPrincipal) error {
	info, err := root.Stat(".")
	st, ok := statLinux(info)
	if err != nil || !ok || !info.IsDir() || info.Mode().Perm() != 0700 || st.Uid != principal.UID || st.Gid != principal.GID {
		return errors.New("manager data root owner or mode is unsafe")
	}
	return nil
}

func ensureLinuxPrivateDirectory(ctx context.Context, parent *os.Root, name string, principal linuxPrincipal) (bool, *os.Root, error) {
	if err := ctx.Err(); err != nil {
		return false, nil, err
	}
	info, err := parent.Lstat(name)
	created := false
	if errors.Is(err, os.ErrNotExist) {
		if err := parent.Mkdir(name, 0700); err != nil {
			return false, nil, err
		}
		created = true
		if err := syncDirectory(parent); err != nil {
			return false, nil, err
		}
		info, err = parent.Lstat(name)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, nil, errors.New("directory path is not an ordinary directory")
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return false, nil, err
	}
	opened, err := child.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = child.Close()
		return false, nil, errors.New("directory identity changed while opening")
	}
	if created {
		file, err := child.Open(".")
		if err != nil {
			_ = child.Close()
			return false, nil, err
		}
		before, statErr := file.Stat()
		chownErr := file.Chown(int(principal.UID), int(principal.GID))
		after, afterErr := file.Stat()
		closeErr := file.Close()
		if statErr != nil || chownErr != nil || afterErr != nil || closeErr != nil || !os.SameFile(opened, before) || !os.SameFile(opened, after) {
			_ = child.Close()
			return false, nil, errors.Join(errors.New("directory ownership could not be set on pinned handle"), statErr, chownErr, afterErr, closeErr)
		}
		if err := syncDirectory(child); err != nil {
			_ = child.Close()
			return false, nil, err
		}
	} else {
		st, ok := statLinux(opened)
		if !ok || st.Uid != principal.UID || st.Gid != principal.GID || opened.Mode().Perm() != 0700 {
			_ = child.Close()
			return false, nil, errors.New("existing directory owner or mode is unsafe")
		}
	}
	return created, child, nil
}

func statLinux(info os.FileInfo) (*syscall.Stat_t, bool) {
	if info == nil {
		return nil, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return st, ok
}

// verifyLinuxDataDirectoriesReadOnly validates the fixed account-owned data
// layout without creating paths, changing permissions, or acquiring the
// updater lock. It is used by the retained-state check and Prepare.
func verifyLinuxDataDirectoriesReadOnly(ctx context.Context, rootPath string, principal linuxPrincipal) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, relative := range []string{".", "selfupdate", "selfupdate/control", "selfupdate/stages", "backups"} {
		path := rootPath
		if relative != "." {
			path = filepath.Join(rootPath, relative)
		}
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
			return errors.Join(errors.New("manager data directory is missing or unsafe"), err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != principal.UID || stat.Gid != principal.GID {
			return errors.New("manager data directory owner differs from retained principal")
		}
	}
	return ctx.Err()
}

func syncLinuxParentDirectory(path string) error {
	parent, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}

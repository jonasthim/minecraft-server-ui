//go:build linux

package install

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/installinputs"
	"github.com/jonasthim/minecraft-server-ui/internal/selfupdate"
	"github.com/jonasthim/minecraft-server-ui/release/candidate"
)

func (d *linuxInstallDriver) Prepare(ctx context.Context, operation mode, options ApplyOptions) (prepared *transaction, unchanged *Result, retErr error) {
	if d == nil || d.store == nil || d.intent != nil || d.tx != nil {
		return nil, nil, errors.New("Linux installer preparation is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	loaded, err := d.store.Load(ctx)
	if err != nil {
		return nil, nil, err
	}
	if loaded != nil {
		return nil, nil, errors.New("installer recovery is required before a new operation")
	}
	if operation != modeApply && operation != modeUninstall {
		return nil, nil, errors.New("installer operation is invalid")
	}
	if operation == modeApply {
		if err := validateApplyOptions(options); err != nil {
			return nil, nil, err
		}
	} else if options != (ApplyOptions{}) {
		return nil, nil, errors.New("uninstall does not accept apply options")
	}
	if operation == modeUninstall {
		gamesStopped := d.hooks.gamesStopped
		if gamesStopped == nil {
			gamesStopped = requireLinuxGamesStopped
		}
		if err := gamesStopped(ctx); err != nil {
			return nil, nil, fmt.Errorf("managed game services must be conclusively stopped before uninstall: %w", err)
		}
	}

	service, err := d.inspectService(ctx)
	if err != nil {
		return nil, nil, errors.New("manager service state is unavailable")
	}
	account, accountPresent, err := d.inspectAccount(ctx)
	if err != nil {
		return nil, nil, errors.New("manager service account is invalid")
	}
	priorInventory, inventoryPresent, err := d.readInventory(ctx)
	if err != nil {
		return nil, nil, err
	}
	d.updater, err = d.session.SnapshotUpdater(ctx)
	if err != nil {
		return nil, nil, errors.New("existing updater state requires reconciliation")
	}
	installed, receiptPresent := d.updater.Installed()
	if inventoryPresent && priorInventory.Status != "retained" && !receiptPresent || !inventoryPresent && receiptPresent {
		return nil, nil, errors.New("manager receipt and Linux inventory disagree")
	}
	updaterSuperseded := false
	var retainedOrigin *linuxIntent
	if inventoryPresent {
		if err := validateLinuxInventory(priorInventory); err != nil {
			return nil, nil, errors.New("manager receipt and Linux inventory are inconsistent")
		}
		if priorInventory.Status == "retained" {
			if receiptPresent || !accountPresent || !sameLinuxPrincipal(account, priorInventory.Principal) || serviceActive(service) || service.UnitFileState == "enabled" {
				return nil, nil, errors.New("retained failed-install host state differs from protected evidence")
			}
			if err := verifyLinuxDataDirectoriesReadOnly(ctx, d.hooks.dataRoot, priorInventory.Principal); err != nil {
				return nil, nil, errors.New("retained failed-install data directories differ from protected evidence")
			}
			origin, err := d.retainedInventoryOrigin(ctx, priorInventory)
			if err != nil {
				return nil, nil, err
			}
			retainedOrigin = &origin
		}
		if priorInventory.Status != "retained" && !sameImage(priorInventory.Image, fromInstalled(installed)) {
			updaterSuperseded = priorInventory.Status == "installed" && priorInventory.Image.Schema == installed.Schema && d.updater.HasTerminal()
			if !updaterSuperseded {
				return nil, nil, errors.New("manager receipt and Linux inventory are inconsistent")
			}
		}
		if !accountPresent || !sameLinuxPrincipal(account, priorInventory.Principal) {
			return nil, nil, errors.New("manager account differs from retained installation")
		}
	}
	if !inventoryPresent && (accountPresent || service.LoadState == "loaded") {
		return nil, nil, errors.New("unmanaged manager installation requires review")
	}
	if !inventoryPresent {
		if _, err := os.Lstat(d.hooks.dataRoot); err == nil {
			return nil, nil, errors.New("manager data exists without a matching protected inventory")
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, nil, errors.New("manager data path cannot be safely inspected")
		}
	}
	if operation == modeUninstall && (!inventoryPresent || priorInventory.Status == "uninstalled" || priorInventory.Status == "retained") {
		if err := d.inspectAllAssets(ctx); err != nil {
			return nil, nil, err
		}
		if inventoryPresent && priorInventory.Status == "retained" {
			if !d.assetsMatchInventory(priorInventory) {
				return nil, nil, errors.New("retained failed-install deployment assets differ from protected evidence")
			}
			return nil, &Result{Outcome: OutcomeUnchanged}, nil
		}
		if inventoryPresent && priorInventory.Status == "uninstalled" {
			if !d.assetsMatchInventory(priorInventory) || d.anyDeploymentAssetPresent() || service.LoadState == "loaded" || serviceActive(service) {
				return nil, nil, errors.New("uninstalled manager layout differs from protected inventory")
			}
			if trust := d.assets[linuxAssetUpdateTrust]; trust != nil && trust.State().Present {
				if err := d.validateUpdateTrust(ctx); err != nil {
					return nil, nil, errors.New("operator-controlled update trust is invalid")
				}
			}
			return nil, &Result{Outcome: OutcomeUnchanged}, nil
		}
		if d.anyDeploymentAssetPresent() || service.LoadState == "loaded" || serviceActive(service) {
			return nil, nil, errors.New("unmanaged or inconsistent Linux deployment paths require review")
		}
		return nil, &Result{Outcome: OutcomeUnchanged}, nil
	}

	var inputs linuxInstallerInputs
	var summary installinputs.Summary
	var trustBytes []byte
	if operation == modeApply {
		trustBytes, err = readIndependentTrust(ctx, options.TrustFile)
		if err != nil {
			return nil, nil, errors.New("independent bootstrap trust is invalid")
		}
		if _, err := candidate.ParsePublicTrust(trustBytes, "jonasthim/minecraft-server-ui"); err != nil {
			return nil, nil, errors.New("independent bootstrap trust is invalid")
		}
		if retainedOrigin != nil && !priorInventory.Assets[12].State.Present && !bytes.Equal(trustBytes, retainedOrigin.BootstrapTrust) {
			return nil, nil, errors.New("retry trust differs from original retained failed-install trust")
		}
	}

	id, err := randomLinuxHex(32)
	if err != nil {
		return nil, nil, errors.New("installer operation identity could not be created")
	}
	operationRoot, err := d.createOperation(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	d.operation = operationRoot
	intentRecorded := false
	defer func() {
		if retErr == nil || intentRecorded || d.operation == nil {
			return
		}
		var cleanupErr error
		if d.inputs != nil {
			members := d.inputs.Members()
			cleanupErr = errors.Join(d.inputs.Close())
			d.inputs = nil
			if cleanupErr == nil {
				cleanupErr = d.discardUnjournaledOperation(ctx, id, members)
			}
		} else {
			cleanupErr = d.discardEmptyUnjournaledOperation(id)
		}
		if cleanupErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("clean failed uncommitted installer preparation: %w", cleanupErr))
		}
	}()
	if operation == modeApply {
		trust, parseErr := candidate.ParsePublicTrust(trustBytes, "jonasthim/minecraft-server-ui")
		if parseErr != nil {
			return nil, nil, errors.New("independent bootstrap trust is invalid")
		}
		inputOptions := installinputs.Options{Prepared: options.Prepared, Version: options.Version, Trust: trust, AllowDevelopment: options.AllowDevelopment}
		if d.hooks.importInputs != nil {
			inputs, err = d.hooks.importInputs(ctx, operationRoot, inputOptions)
		} else {
			inputs, err = installinputs.Import(ctx, operationRoot, inputOptions)
		}
		if err != nil {
			return nil, nil, fmt.Errorf("authenticate prepared installer input: %w", err)
		}
		d.inputs = inputs
		summary = inputs.Summary()
	}

	prior := (*Image)(nil)
	if receiptPresent {
		p := fromInstalled(installed)
		prior = &p
	}
	target := (*Image)(nil)
	if operation == modeApply {
		image := Image{Version: summary.Version, Schema: summary.Schema}
		members := inputs.Members()
		for _, member := range members {
			if member.Name == "minecraft-ui" {
				image.SHA256 = member.SHA256
				break
			}
		}
		if !validImage(image) || image.Schema != func() int64 {
			if prior != nil {
				return prior.Schema
			}
			return image.Schema
		}() {
			return nil, nil, errors.New("authenticated target image is invalid or schema-incompatible")
		}
		target = &image
	}
	if operation == modeUninstall && (!inventoryPresent || priorInventory.Status != "installed" || !receiptPresent) {
		return nil, nil, errors.New("Linux uninstall target is inconsistent")
	}

	if err := d.checkConfiguration(ctx); err != nil {
		return nil, nil, err
	}
	if err := d.inspectAllAssets(ctx); err != nil {
		return nil, nil, err
	}
	if inventoryPresent && priorInventory.Status == "retained" && !d.assetsMatchInventory(priorInventory) {
		return nil, nil, errors.New("retained failed-install deployment assets differ from protected evidence")
	}
	if !inventoryPresent && d.anyDeploymentAssetPresent() {
		return nil, nil, errors.New("fixed Linux deployment paths contain unmanaged assets")
	}
	if operation == modeApply && priorInventory.Status == "uninstalled" && !accountPresent {
		return nil, nil, errors.New("retained service account is missing")
	}
	principal := (*linuxPrincipal)(nil)
	if accountPresent {
		value := account
		principal = &value
	}
	serviceWasActive := serviceActive(service)
	serviceWasEnabled := service.UnitFileState == "enabled"
	if priorInventory.Status == "uninstalled" {
		serviceWasActive = false
		serviceWasEnabled = false
	}
	currentReceipt := (*selfupdate.Installed)(nil)
	if receiptPresent {
		currentReceipt = &installed
	}
	plans, err := d.planAssets(ctx, operation, inputs, trustBytes, priorInventory, inventoryPresent, options.Repair, currentReceipt, updaterSuperseded)
	if err != nil {
		return nil, nil, err
	}
	if operation == modeApply && receiptPresent && priorInventory.Status == "installed" && !options.Repair && target != nil && sameImage(*target, priorInventory.Image) && d.assetsMatchInventory(priorInventory) && linuxPlanContentUnchanged(plans) && serviceActive(service) {
		if err := d.probeCandidate(ctx, priorInventory.Principal, *target); err != nil {
			return nil, nil, errors.New("authenticated target capability is inconsistent with the installed service")
		}
		if err := d.requireHealthyInstalledImage(ctx, priorInventory.Image); err != nil {
			return nil, nil, errors.New("installed manager health could not be verified")
		}
		if d.inputs != nil {
			members := d.inputs.Members()
			if err := d.inputs.Close(); err != nil {
				return nil, nil, err
			}
			d.inputs = nil
			if err := d.discardUnjournaledOperation(ctx, id, members); err != nil {
				return nil, nil, err
			}
		}
		return nil, &Result{Outcome: OutcomeUnchanged, Image: imagePointer(priorInventory.Image)}, nil
	}
	nonce, err := randomLinuxHex(32)
	if err != nil {
		return nil, nil, errors.New("installer nonce could not be created")
	}
	intent := linuxIntent{
		Format: 1, ID: id, Mode: operation, CreatedAt: d.hooks.now().UTC(), Nonce: nonce,
		Prior: prior, Target: target, Principal: principal, ServiceWasActive: serviceWasActive,
		ServiceWasEnabled: serviceWasEnabled, AllowDevelopment: options.AllowDevelopment,
		Assets: plans,
	}
	if inventoryPresent {
		inventoryCopy := priorInventory
		intent.PriorInventory = &inventoryCopy
	}
	if operation == modeApply {
		intent.ManifestSHA256 = summary.ManifestSHA256
		intent.BootstrapTrust = trustBytes
		intent.ExecutorSHA256 = plans[1].After.SHA256
	} else {
		intent.ExecutorSHA256 = plans[1].Before.SHA256
	}
	if err := d.captureAssetPreimages(ctx, plans); err != nil {
		return nil, nil, err
	}
	// Fresh account creation happens later, after the immutable prepared journal
	// has been recorded. The effect document will bind its allocated UID/GID.
	// The intent digest is computed from the serialized intent itself. A valid
	// placeholder permits pure model validation before the exact digest exists;
	// only the computed digest is attached to the durable transaction record.
	tx := transaction{Format: 1, ID: id, Mode: operation, Phase: phasePrepared, IntentSHA256: strings.Repeat("0", 64), Prior: prior, Target: target}
	body, err := marshalLinuxIntent(intent, tx)
	if err != nil {
		return nil, nil, fmt.Errorf("validate immutable Linux installer plan: %w", err)
	}
	digest, err := d.store.CreateIntent(ctx, id, body)
	if err != nil {
		return nil, nil, fmt.Errorf("persist immutable Linux installer intent: %w", err)
	}
	intentRecorded = true
	tx.IntentSHA256 = digest
	d.intent, d.tx = &intent, cloneTransaction(&tx)
	if err := d.writeInitialEffects(ctx); err != nil {
		return nil, nil, fmt.Errorf("persist Linux installer native evidence: %w", err)
	}
	return &tx, nil, nil
}

func linuxPlanContentUnchanged(plans []linuxAssetPlan) bool {
	for _, plan := range plans {
		if !sameLinuxAssetContent(plan.Before, plan.After) {
			return false
		}
	}
	return true
}

func (d *linuxInstallDriver) requireHealthyInstalledImage(ctx context.Context, expected Image) error {
	if d.hooks.health != nil {
		return d.hooks.health(ctx, expected)
	}
	if err := requireLinuxManagerImage(ctx, expected); err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, "http://127.0.0.1:8080/healthz", nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("manager health redirect is forbidden")
	}}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		return errors.New("installed manager health endpoint is unavailable")
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 1025))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || len(body) > 1024 || response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
		return errors.New("installed manager health endpoint reported failure")
	}
	var health struct {
		Status string `json:"status"`
	}
	if decodeLinuxStrictJSON(body, 1024, &health) != nil || health.Status != "ok" {
		return errors.New("installed manager health response is invalid")
	}
	return nil
}

func (d *linuxInstallDriver) discardEmptyUnjournaledOperation(id string) error {
	if d.operation == nil || !lowerHex(id, 64) {
		return errors.New("uncommitted installer operation is unavailable")
	}
	entries, err := readLinuxRootDir(d.operation)
	if err != nil {
		return err
	}
	if len(entries) == 1 && entries[0].Name() == "inputs" {
		inputs, openErr := d.operation.OpenRoot("inputs")
		if openErr != nil {
			return openErr
		}
		children, readErr := readLinuxRootDir(inputs)
		closeErr := inputs.Close()
		if readErr != nil || closeErr != nil || len(children) != 0 {
			return errors.Join(errors.New("uncommitted installer inputs contain unowned or partial files"), readErr, closeErr)
		}
		if err := d.operation.Remove("inputs"); err != nil {
			return err
		}
		if err := syncDirectory(d.operation); err != nil {
			return err
		}
		entries = nil
	}
	if len(entries) != 0 {
		return errors.New("uncommitted installer operation contains unrecognized files")
	}
	if err := d.operation.Close(); err != nil {
		return err
	}
	d.operation = nil
	install, err := d.state.OpenRoot(installDirectory)
	if err != nil {
		return err
	}
	defer install.Close()
	if err := install.Remove(id); err != nil {
		return err
	}
	return syncDirectory(install)
}

func (d *linuxInstallDriver) discardUnjournaledOperation(ctx context.Context, id string, members []candidate.Member) error {
	if d.operation == nil || !lowerHex(id, 64) {
		return errors.New("uncommitted installer operation is unavailable")
	}
	inputs, err := d.operation.OpenRoot("inputs")
	if err != nil {
		return err
	}
	entries, err := readLinuxRootDir(inputs)
	if err != nil {
		_ = inputs.Close()
		return err
	}
	expected := map[string]bool{"bootstrap.json": true, "bootstrap.sig": true, "completion.json": true}
	for _, member := range members {
		expected[member.Name] = true
	}
	if len(entries) != len(expected) {
		_ = inputs.Close()
		return errors.New("uncommitted authenticated input set changed")
	}
	for _, entry := range entries {
		if !expected[entry.Name()] {
			_ = inputs.Close()
			return errors.New("unrecognized file in uncommitted installer operation")
		}
		info, err := inputs.Lstat(entry.Name())
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			_ = inputs.Close()
			return errors.Join(errors.New("uncommitted input identity is unsafe"), err)
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 0 || st.Nlink != 1 {
			_ = inputs.Close()
			return errors.New("uncommitted input owner or link count is unsafe")
		}
	}
	for _, entry := range entries {
		if err := inputs.Remove(entry.Name()); err != nil {
			_ = inputs.Close()
			return err
		}
	}
	if err := inputs.Close(); err != nil {
		return err
	}
	if err := syncDirectory(d.operation); err != nil {
		return err
	}
	if err := d.operation.Remove("inputs"); err != nil {
		return err
	}
	if err := syncDirectory(d.operation); err != nil {
		return err
	}
	if err := d.operation.Close(); err != nil {
		return err
	}
	d.operation = nil
	install, err := d.state.OpenRoot(installDirectory)
	if err != nil {
		return err
	}
	defer install.Close()
	if err := install.Remove(id); err != nil {
		return err
	}
	return syncDirectory(install)
}

func readLinuxRootDir(root *os.Root) ([]os.DirEntry, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	return entries, errors.Join(readErr, closeErr)
}

func (d *linuxInstallDriver) inspectService(ctx context.Context) (linuxServiceStatus, error) {
	if d.hooks.service != nil {
		return d.hooks.service(ctx)
	}
	return inspectLinuxManager(ctx)
}

func (d *linuxInstallDriver) inspectAccount(ctx context.Context) (linuxPrincipal, bool, error) {
	if d.hooks.account != nil {
		return d.hooks.account(ctx)
	}
	return lookupLinuxInstallerAccount(ctx)
}

func lookupLinuxInstallerAccount(ctx context.Context) (linuxPrincipal, bool, error) {
	if err := ctx.Err(); err != nil {
		return linuxPrincipal{}, false, err
	}
	entry, err := user.Lookup(linuxInstallerAccount)
	if errors.Is(err, user.UnknownUserError(linuxInstallerAccount)) {
		return linuxPrincipal{}, false, nil
	}
	if err != nil {
		return linuxPrincipal{}, false, err
	}
	uid, uerr := strconv.ParseUint(entry.Uid, 10, 32)
	gid, gerr := strconv.ParseUint(entry.Gid, 10, 32)
	if uerr != nil || gerr != nil || uid == 0 || gIDIsRoot(uint32(gid)) || entry.HomeDir != linuxInstallerData || !validLoginShell(linuxInstallerAccount) {
		return linuxPrincipal{}, false, errors.New("existing manager account does not match fixed nonlogin policy")
	}
	return linuxPrincipal{UID: uint32(uid), GID: uint32(gid)}, true, nil
}

func validLoginShell(name string) bool {
	body, err := os.ReadFile("/etc/passwd")
	if err != nil || len(body) > 1<<20 {
		return false
	}
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) == 7 && fields[0] == name {
			return fields[6] == "/usr/sbin/nologin" || fields[6] == "/sbin/nologin"
		}
	}
	return false
}

func gIDIsRoot(gid uint32) bool { return gid == 0 }

func serviceActive(status linuxServiceStatus) bool { return linuxStatusActive(status) }

func fromInstalled(value selfupdate.Installed) Image {
	return Image{Version: value.Version, Schema: value.Schema, SHA256: value.SHA256}
}

func randomLinuxHex(size int) (string, error) {
	if size < 16 || size > 64 {
		return "", errors.New("random installer value size is invalid")
	}
	body := make([]byte, size)
	if _, err := rand.Read(body); err != nil {
		return "", err
	}
	return hex.EncodeToString(body), nil
}

func (d *linuxInstallDriver) createOperation(ctx context.Context, id string) (*os.Root, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	install, err := d.state.OpenRoot(installDirectory)
	if errors.Is(err, os.ErrNotExist) {
		if err = d.state.Mkdir(installDirectory, 0700); err != nil {
			return nil, err
		}
		if err = syncDirectory(d.state); err != nil {
			return nil, err
		}
		install, err = d.state.OpenRoot(installDirectory)
	}
	if err != nil {
		return nil, fmt.Errorf("open private installer directory: %w", err)
	}
	defer install.Close()
	if err := checkPrivateOperationDir(install); err != nil {
		return nil, err
	}
	if _, err := install.Lstat(id); err == nil {
		return nil, errors.New("installer operation identity already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := install.Mkdir(id, 0700); err != nil {
		return nil, fmt.Errorf("create private installer operation: %w", err)
	}
	if err := syncDirectory(install); err != nil {
		return nil, fmt.Errorf("sync private installer operation parent: %w", err)
	}
	return openLinuxOperation(ctx, d.state, id)
}

func readIndependentTrust(ctx context.Context, path string) ([]byte, error) {
	if ctx == nil || ctx.Err() != nil || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("trust file path is invalid")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > maxLinuxBootstrapTrustBytes {
		return nil, errors.New("trust file metadata is invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("trust file changed while opening")
	}
	body, err := io.ReadAll(io.LimitReader(file, maxLinuxBootstrapTrustBytes+1))
	if err != nil || len(body) != int(info.Size()) || len(body) > maxLinuxBootstrapTrustBytes {
		return nil, errors.Join(errors.New("trust file read is outside its bound"), err)
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, after) {
		return nil, errors.New("trust file changed while reading")
	}
	if _, err := candidate.ParsePublicTrust(body, "jonasthim/minecraft-server-ui"); err != nil {
		return nil, err
	}
	return body, nil
}

func (d *linuxInstallDriver) readInventory(ctx context.Context) (linuxInventory, bool, error) {
	info, err := d.state.Lstat("installation.json")
	if errors.Is(err, os.ErrNotExist) {
		return linuxInventory{}, false, nil
	}
	if err != nil {
		return linuxInventory{}, false, err
	}
	expectedUID := uint32(0)
	if d.hooks.testRoot {
		expectedUID = uint32(os.Geteuid())
	}
	if checkProtectedFile(info, expectedUID) != nil || info.Size() <= 0 || info.Size() > maxLinuxInventoryBytes {
		return linuxInventory{}, false, errors.New("protected Linux inventory is unsafe")
	}
	file, err := d.state.Open("installation.json")
	if err != nil {
		return linuxInventory{}, false, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = file.Close()
		return linuxInventory{}, false, errors.New("Linux inventory identity changed")
	}
	body, readErr := io.ReadAll(io.LimitReader(file, maxLinuxInventoryBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(body) != int(info.Size()) {
		return linuxInventory{}, false, errors.Join(errors.New("Linux inventory read failed"), readErr, closeErr)
	}
	decoded, err := decodeLinuxInventory(body)
	if err != nil {
		return linuxInventory{}, false, err
	}
	return decoded, true, nil
}

func (d *linuxInstallDriver) checkConfiguration(ctx context.Context) error {
	if d.hooks.configuration != nil {
		return d.hooks.configuration(ctx)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(linuxInstallerEnv)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 64<<10 {
		return errors.New("manager environment configuration is unsafe")
	}
	file, err := os.Open(linuxInstallerEnv)
	if err != nil {
		return errors.New("manager environment configuration cannot be read")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("manager environment configuration changed")
	}
	body, err := io.ReadAll(io.LimitReader(file, 64<<10+1))
	if err != nil || len(body) > 64<<10 {
		return errors.New("manager environment configuration exceeded its bound")
	}
	dataDir, address := linuxInstallerData, "127.0.0.1:8080"
	seen := map[string]bool{}
	for _, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || seen[key] {
			return errors.New("manager environment configuration is not canonical")
		}
		seen[key] = true
		switch key {
		case "MINECRAFT_UI_DATA_DIR":
			dataDir = value
		case "MINECRAFT_UI_ADDR":
			address = value
		case "MINECRAFT_UI_BASE_URL":
		default:
			return errors.New("manager environment configuration contains an unsupported setting")
		}
	}
	if dataDir != linuxInstallerData || address != "127.0.0.1:8080" {
		return errors.New("manager environment overrides the fixed installer layout")
	}
	return nil
}

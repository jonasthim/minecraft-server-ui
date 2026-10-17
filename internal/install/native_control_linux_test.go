//go:build linux

package install

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
	"github.com/jonasthim/minecraft-server-ui/internal/selfupdate"
)

func linuxControlFixture(t *testing.T) (*linuxInstallDriver, linuxIntent, transaction, linuxPrincipal) {
	t.Helper()
	base := t.TempDir()
	data := filepath.Join(base, "data")
	control := filepath.Join(data, "selfupdate", "control")
	if err := os.MkdirAll(control, 0700); err != nil {
		t.Fatal(err)
	}
	principal := linuxPrincipal{UID: uint32(os.Getuid()), GID: uint32(os.Getgid())}
	image := Image{Version: "v1.2.3", Schema: 18, SHA256: strings.Repeat("a", 64)}
	target := Image{Version: "v1.2.4", Schema: 18, SHA256: strings.Repeat("b", 64)}
	intent := linuxIntent{Format: 1, ID: strings.Repeat("1", 64), Mode: modeApply, CreatedAt: time.Now().UTC(), Nonce: strings.Repeat("2", 64), Prior: &image, Target: &target, Principal: &principal}
	tx := transaction{Format: 1, ID: intent.ID, Mode: modeApply, Phase: phaseStopped, IntentSHA256: strings.Repeat("3", 64), Prior: imagePointer(image), Target: imagePointer(target)}
	d := &linuxInstallDriver{hooks: linuxNativeHooks{dataRoot: data, now: func() time.Time { return time.Now().UTC() }}, intent: &intent, tx: &tx}
	return d, intent, tx, principal
}

func writeLinuxControlFixture(t *testing.T, dir, name string, value any, principal linuxPrincipal) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, int(principal.UID), int(principal.GID)); err != nil {
		t.Fatal(err)
	}
}

func TestReadMaintenanceProofRequiresExactIntentAndFreshManagerDrain(t *testing.T) {
	d, intent, _, principal := linuxControlFixture(t)
	control := filepath.Join(d.hooks.dataRoot, linuxControlDirectory)
	from := toInstalled(*intent.Prior)
	to := toInstalled(*intent.Target)
	challenge := selfupdate.MaintenanceChallenge{Format: 1, OperationID: intent.ID, Nonce: intent.Nonce, Mode: string(modeApply), From: &from, To: &to, ExpiresAt: time.Now().UTC().Add(2 * time.Minute)}
	receipt := selfupdate.MaintenanceDrainReceipt{Format: 1, OperationID: intent.ID, Nonce: intent.Nonce, Image: from, Drained: true, CreatedAt: time.Now().UTC()}
	writeLinuxControlFixture(t, control, "maintenance-challenge.json", challenge, principal)
	writeLinuxControlFixture(t, control, "maintenance-drained.json", receipt, principal)
	got, drained, err := d.readMaintenanceProof(context.Background(), intent, principal)
	if err != nil || got.Nonce != intent.Nonce || drained.Image != from {
		t.Fatalf("readMaintenanceProof = (%+v,%+v,%v)", got, drained, err)
	}

	receipt.Nonce = strings.Repeat("9", 64)
	if err := os.Remove(filepath.Join(control, "maintenance-drained.json")); err != nil {
		t.Fatal(err)
	}
	writeLinuxControlFixture(t, control, "maintenance-drained.json", receipt, principal)
	if _, _, err := d.readMaintenanceProof(context.Background(), intent, principal); err == nil {
		t.Fatal("accepted drain receipt for a different operation nonce")
	}
}

func TestMaintenanceControlRejectsUnrecognizedFilesAndUnsafeIdentity(t *testing.T) {
	d, _, _, principal := linuxControlFixture(t)
	control, err := d.openControl(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.hooks.dataRoot, linuxControlDirectory, "mystery.json"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := d.openControl(context.Background(), principal); err == nil {
		t.Fatal("accepted unrecognized maintenance control file")
	}

	if err := os.Remove(filepath.Join(d.hooks.dataRoot, linuxControlDirectory, "mystery.json")); err != nil {
		t.Fatal(err)
	}
	writeLinuxControlFixture(t, filepath.Join(d.hooks.dataRoot, linuxControlDirectory), "maintenance-challenge.json", map[string]any{"format": 1}, principal)
	if err := os.Chmod(filepath.Join(d.hooks.dataRoot, linuxControlDirectory, "maintenance-challenge.json"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readLinuxControlFile(context.Background(), mustOpenControl(t, d, principal), "maintenance-challenge.json", principal, 4096); err == nil {
		t.Fatal("accepted control file with wrong owner")
	}
}

func mustOpenControl(t *testing.T, d *linuxInstallDriver, principal linuxPrincipal) *os.Root {
	t.Helper()
	root, err := safepath.OpenRoot(context.Background(), filepath.Join(d.hooks.dataRoot, linuxControlDirectory))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

func TestClearMaintenanceControlRemovesOnlyMatchingIntentFiles(t *testing.T) {
	d, intent, _, principal := linuxControlFixture(t)
	control := filepath.Join(d.hooks.dataRoot, linuxControlDirectory)
	from, to := toInstalled(*intent.Prior), toInstalled(*intent.Target)
	challenge := selfupdate.MaintenanceChallenge{Format: 1, OperationID: intent.ID, Nonce: intent.Nonce, Mode: string(intent.Mode), From: &from, To: &to, ExpiresAt: time.Now().UTC().Add(time.Minute)}
	receipt := selfupdate.MaintenanceDrainReceipt{Format: 1, OperationID: intent.ID, Nonce: intent.Nonce, Image: from, Drained: true, CreatedAt: time.Now().UTC()}
	writeLinuxControlFixture(t, control, "maintenance-challenge.json", challenge, principal)
	writeLinuxControlFixture(t, control, "maintenance-drained.json", receipt, principal)
	if err := d.clearOwnMaintenanceControl(context.Background(), intent, principal); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"maintenance-challenge.json", "maintenance-drained.json"} {
		if _, err := os.Lstat(filepath.Join(control, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s still exists: %v", name, err)
		}
	}
}

func TestClearMaintenanceControlPreservesProofsWhenDrainOrForeignStateRefuses(t *testing.T) {
	for _, fixture := range []struct {
		name   string
		mutate func(t *testing.T, control string, intent linuxIntent, principal linuxPrincipal)
	}{
		{name: "foreign pending", mutate: func(t *testing.T, control string, _ linuxIntent, principal linuxPrincipal) {
			writeLinuxControlFixture(t, control, "maintenance-drained.pending", map[string]any{"format": 1}, principal)
		}},
		{name: "wrong drain nonce", mutate: func(t *testing.T, control string, intent linuxIntent, principal linuxPrincipal) {
			from, to := toInstalled(*intent.Prior), toInstalled(*intent.Target)
			challenge := selfupdate.MaintenanceChallenge{Format: 1, OperationID: intent.ID, Nonce: intent.Nonce, Mode: string(intent.Mode), From: &from, To: &to, ExpiresAt: time.Now().UTC().Add(time.Minute)}
			receipt := selfupdate.MaintenanceDrainReceipt{Format: 1, OperationID: intent.ID, Nonce: strings.Repeat("9", 64), Image: from, Drained: true, CreatedAt: time.Now().UTC()}
			writeLinuxControlFixture(t, control, "maintenance-challenge.json", challenge, principal)
			writeLinuxControlFixture(t, control, "maintenance-drained.json", receipt, principal)
		}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			d, intent, _, principal := linuxControlFixture(t)
			control := filepath.Join(d.hooks.dataRoot, linuxControlDirectory)
			if fixture.name == "foreign pending" {
				from, to := toInstalled(*intent.Prior), toInstalled(*intent.Target)
				writeLinuxControlFixture(t, control, "maintenance-challenge.json", selfupdate.MaintenanceChallenge{Format: 1, OperationID: intent.ID, Nonce: intent.Nonce, Mode: string(intent.Mode), From: &from, To: &to, ExpiresAt: time.Now().UTC().Add(time.Minute)}, principal)
			}
			fixture.mutate(t, control, intent, principal)
			if err := d.clearOwnMaintenanceControl(context.Background(), intent, principal); err == nil {
				t.Fatal("cleanup accepted refusing maintenance state")
			}
			for _, name := range []string{"maintenance-challenge.json", "maintenance-drained.json", "maintenance-drained.pending"} {
				if _, err := os.Lstat(filepath.Join(control, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("proof state %s changed unexpectedly: %v", name, err)
				}
			}
		})
	}
}

func TestRollbackChallengeHasFreshNonceAndExactReversedImages(t *testing.T) {
	_, intent, _, _ := linuxControlFixture(t)
	rollback := rollbackIntent(intent)
	rollback.Nonce = strings.Repeat("4", 64)
	from, to := toInstalled(*rollback.Prior), toInstalled(*rollback.Target)
	challenge := selfupdate.MaintenanceChallenge{Format: 1, OperationID: rollback.ID, Nonce: rollback.Nonce, Mode: string(rollback.Mode), From: &from, To: &to, ExpiresAt: rollback.CreatedAt.Add(10 * time.Minute)}
	if !linuxChallengeOwned(challenge, rollback) {
		t.Fatal("reversed-image rollback challenge was not recognized as operation-owned")
	}
	challenge.Nonce = intent.Nonce
	if linuxChallengeOwned(challenge, rollback) {
		t.Fatal("rollback challenge reused the original nonce")
	}
	challenge.Nonce = rollback.Nonce
	challenge.To = &from
	if linuxChallengeOwned(challenge, rollback) {
		t.Fatal("rollback challenge accepted a mismatched prior image")
	}
}

func TestLateRollbackChallengeRemainsCleanupOwned(t *testing.T) {
	_, intent, _, _ := linuxControlFixture(t)
	rollback := rollbackIntent(intent)
	rollback.Nonce = strings.Repeat("4", 64)
	rollback.CreatedAt = intent.CreatedAt.Add(6 * time.Hour)
	from, to := toInstalled(*rollback.Prior), toInstalled(*rollback.Target)
	challenge := selfupdate.MaintenanceChallenge{Format: 1, OperationID: rollback.ID, Nonce: rollback.Nonce, Mode: string(rollback.Mode), From: &from, To: &to, ExpiresAt: rollback.CreatedAt.Add(10 * time.Minute)}
	if !linuxChallengeOwned(challenge, rollback) {
		t.Fatal("fresh rollback challenge after delayed recovery was not recognized for cleanup")
	}
}

func TestFreshApplyQuiesceChallengeIsAvailableBeforeManagerStart(t *testing.T) {
	d, intent, tx, principal := linuxControlFixture(t)
	intent.Prior = nil
	intent.ServiceWasActive = false
	intent.ServiceWasEnabled = false
	intent.Target = imagePointer(*intent.Target)
	tx.Prior = nil
	tx.Target = imagePointer(*intent.Target)
	if err := d.writeMaintenanceChallenge(context.Background(), tx, intent, principal); err != nil {
		t.Fatal(err)
	}
	root, err := d.openControl(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file, err := readLinuxControlFile(context.Background(), root, "maintenance-challenge.json", principal, 4096)
	if err != nil {
		t.Fatal(err)
	}
	var challenge selfupdate.MaintenanceChallenge
	if err := decodeLinuxStrictJSON(file.body, 4096, &challenge); err != nil || challenge.From != nil || challenge.To == nil || *challenge.To != toInstalled(*intent.Target) || challenge.Nonce != intent.Nonce {
		t.Fatalf("fresh install challenge=%+v err=%v", challenge, err)
	}
}

func TestProvisioningCreatesOnlyPrivateDataRootAndUsesNoCreateHome(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "manager-data")
	principal := linuxPrincipal{UID: uint32(os.Getuid()), GID: uint32(os.Getgid())}
	command := linuxUseraddCommand(context.Background())
	if got := strings.Join(command.Args, " "); !strings.Contains(got, "--no-create-home") || strings.Contains(got, "--create-home") || command.Env == nil {
		t.Fatalf("unsafe useradd invocation: args=%q env=%v", command.Args, command.Env)
	}
	if err := ensureLinuxDataDirectories(context.Background(), root, principal, true); err != nil {
		t.Fatalf("create new private manager data tree: %v", err)
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatalf("manager data root stat=%v err=%v", info, err)
	}
	for _, relative := range []string{"selfupdate", "selfupdate/control", "selfupdate/stages", "backups"} {
		info, err := os.Lstat(filepath.Join(root, relative))
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
			t.Fatalf("manager data subdirectory %s stat=%v err=%v", relative, info, err)
		}
	}
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := ensureLinuxDataDirectories(context.Background(), root, principal, true); err == nil {
		t.Fatal("provisioning normalized an existing non-private data root")
	}
}

func TestProvisioningRefusesSubstitutedManagerWritableParent(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "manager-data")
	principal := linuxPrincipal{UID: uint32(os.Getuid()), GID: uint32(os.Getgid())}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(root, int(principal.UID), int(principal.GID)); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "outside")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "selfupdate")); err != nil {
		t.Fatal(err)
	}
	if err := ensureLinuxDataDirectories(context.Background(), root, principal, false); err == nil {
		t.Fatal("accepted a manager-writable symlink in the data directory tree")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("outside target was modified: entries=%v err=%v", entries, err)
	}
}

func TestUnchangedShortcutRequiresEveryAuthenticatedAssetToMatch(t *testing.T) {
	unchanged := make([]linuxAssetPlan, 0, len(fixedLinuxAssets))
	for _, spec := range fixedLinuxAssets {
		before := linuxState(100, "a")
		before.Mode = spec.Mode
		unchanged = append(unchanged, linuxAssetPlan{Role: spec.Role, Before: before, After: before})
	}
	if !linuxPlanContentUnchanged(unchanged) {
		t.Fatal("identical authenticated plans were not classified as unchanged")
	}
	// The manager image remains identical while a signed updater/helper bundle
	// changes. The shortcut must allow the normal publication path to run.
	for i := range unchanged {
		if unchanged[i].Role == linuxAssetUpdater {
			unchanged[i].After.SHA256 = strings.Repeat("b", 64)
			break
		}
	}
	if linuxPlanContentUnchanged(unchanged) {
		t.Fatal("same manager image with changed authenticated updater incorrectly qualified as unchanged")
	}
}

func TestExpiredOwnedChallengeCanBeRecognizedForSafeCleanup(t *testing.T) {
	_, intent, _, _ := linuxControlFixture(t)
	from, to := toInstalled(*intent.Prior), toInstalled(*intent.Target)
	challenge := selfupdate.MaintenanceChallenge{Format: 1, OperationID: intent.ID, Nonce: intent.Nonce, Mode: string(intent.Mode), From: &from, To: &to, ExpiresAt: intent.CreatedAt.Add(10 * time.Minute)}
	if !linuxChallengeOwned(challenge, intent) {
		t.Fatal("exact expired challenge should remain recognizable for cleanup")
	}
	challenge.OperationID = strings.Repeat("9", 64)
	if linuxChallengeOwned(challenge, intent) {
		t.Fatal("cleanup ownership accepted a different operation")
	}
}

func TestInventoryIgnoresOnlyValidatedMutableUpdateTrust(t *testing.T) {
	installed := linuxInventoryFixture("installed")
	d := &linuxInstallDriver{assets: make(map[linuxAssetRole]*linuxAssetSnapshot)}
	for _, spec := range fixedLinuxAssets {
		state := installed.Assets[linuxAssetIndex(spec.Role)].State
		d.assets[spec.Role] = &linuxAssetSnapshot{role: spec.Role, state: state}
	}
	if !d.assetsMatchInventory(installed) {
		t.Fatal("exact installed inventory did not match captured assets")
	}
	changed := linuxState(999, "f")
	changed.Mode = 0644
	d.assets[linuxAssetUpdateTrust].state = changed
	if !d.assetsMatchInventory(installed) {
		t.Fatal("operator-controlled update trust change incorrectly caused inventory drift")
	}
	installed.Status = "retained"
	if d.assetsMatchInventory(installed) {
		t.Fatal("retained-failure inventory accepted an update-trust mismatch")
	}
	d.hooks.updateTrust = func(context.Context) error { return errors.New("invalid trust fixture") }
	if d.validateUpdateTrust(context.Background()) == nil {
		t.Fatal("invalid operator update-trust fixture was accepted")
	}
}

func linuxAssetIndex(role linuxAssetRole) int {
	for i, spec := range fixedLinuxAssets {
		if spec.Role == role {
			return i
		}
	}
	return -1
}

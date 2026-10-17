//go:build linux

package install

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLinuxIntentValidFreshManagedAndRetainedTransitions(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func() (linuxIntent, transaction)
	}{
		{"fresh apply", freshLinuxApplyFixture},
		{"managed apply", managedLinuxApplyFixture},
		{"managed apply after updater supersession", supersededLinuxApplyFixture},
		{"retained data apply", retainedLinuxApplyFixture},
		{"managed uninstall", managedLinuxUninstallFixture},
		{"uninstall after updater supersession", supersededLinuxUninstallFixture},
	} {
		t.Run(tc.name, func(t *testing.T) {
			intent, tx := tc.make()
			if err := validateLinuxIntent(intent, tx); err != nil {
				t.Fatalf("valid fixture rejected: %v", err)
			}
			raw, err := marshalLinuxIntent(intent, tx)
			if err != nil {
				t.Fatalf("marshal valid intent: %v", err)
			}
			decoded, err := decodeLinuxIntent(raw, tx)
			if err != nil {
				t.Fatalf("decode marshaled intent: %v", err)
			}
			if decoded.ID != intent.ID || decoded.Mode != intent.Mode || len(decoded.Assets) != len(intent.Assets) {
				t.Fatalf("round trip lost intent data: %#v", decoded)
			}
		})
	}
}

func TestDecodeLinuxIntentRejectsMalformedJSONAndBindings(t *testing.T) {
	intent, tx := freshLinuxApplyFixture()
	valid, err := marshalLinuxIntent(intent, tx)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(valid, &wire); err != nil {
		t.Fatal(err)
	}
	var assets []json.RawMessage
	if err := json.Unmarshal(wire["assets"], &assets); err != nil {
		t.Fatal(err)
	}
	stateCases := []struct {
		name string
		body []byte
		tx   transaction
	}{
		{"unknown field", appendJSONField(valid, `"surprise":true`), tx},
		{"duplicate field", bytes.Replace(valid, []byte(`"format":1`), []byte(`"format":1,"for\u006dat":1`), 1), tx},
		{"required null", bytes.Replace(valid, []byte(`"assets":[`), []byte(`"assets":null,"ignored":[`), 1), tx},
		{"null bootstrap trust", bytes.Replace(valid, []byte(`,"bootstrap_trust":""`), []byte(`,"bootstrap_trust":null`), 1), tx},
		{"null scalar", bytes.Replace(valid, []byte(`"service_was_active":false`), []byte(`"service_was_active":null`), 1), tx},
		{"missing required field", bytes.Replace(valid, []byte(`,"service_was_enabled":false`), nil, 1), tx},
		{"invalid UTF-8", append(append([]byte(nil), valid...), 0xff), tx},
		{"trailing value", append(append([]byte(nil), valid...), []byte(` {}`)...), tx},
		{"over limit", bytes.Repeat([]byte{' '}, 256<<10+1), tx},
		{"transaction ID mismatch", valid, func() transaction { t := tx; t.ID = strings.Repeat("b", 64); return t }()},
		{"transaction target mismatch", valid, func() transaction {
			t := tx
			t.Target = imagePointer(Image{Version: "v1.0.1", Schema: 7, SHA256: strings.Repeat("c", 64)})
			return t
		}()},
	}
	for _, tc := range stateCases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decodeLinuxIntent(tc.body, tc.tx); err == nil {
				t.Fatal("malformed or mismatched intent was accepted")
			}
		})
	}
}

func TestMarshalLinuxUninstallNormalizesNilTrustWithoutMutation(t *testing.T) {
	intent, tx := managedLinuxUninstallFixture()
	intent.BootstrapTrust = nil
	raw, err := marshalLinuxIntent(intent, tx)
	if err != nil {
		t.Fatalf("marshal uninstall with nil empty trust: %v", err)
	}
	if intent.BootstrapTrust != nil {
		t.Fatal("marshal mutated the caller's intent")
	}
	if _, err := decodeLinuxIntent(raw, tx); err != nil {
		t.Fatalf("decode marshaled uninstall: %v", err)
	}
}

func TestValidateLinuxIntentRejectsAssetAuthorityAndTrustChanges(t *testing.T) {
	intent, tx := managedLinuxApplyFixture()
	mutations := []struct {
		name string
		edit func(*linuxIntent)
	}{
		{"unsafe role", func(i *linuxIntent) { i.Assets[0].Role = linuxAssetRole("../../manager") }},
		{"before identity missing", func(i *linuxIntent) { i.Assets[0].Before.Inode = 0 }},
		{"after captures identity", func(i *linuxIntent) { i.Assets[0].After.Device = 8 }},
		{"wrong target manager digest", func(i *linuxIntent) { i.Assets[0].After.SHA256 = strings.Repeat("d", 64) }},
		{"missing target deployment asset", func(i *linuxIntent) { i.Assets[2].After = linuxAssetState{} }},
		{"bootstrap trust replacement", func(i *linuxIntent) { i.Assets[12].After.SHA256 = strings.Repeat("e", 64) }},
		{"manifest absent", func(i *linuxIntent) { i.ManifestSHA256 = "" }},
		{"principal mismatch", func(i *linuxIntent) { i.Principal.UID++ }},
		{"schema mismatch", func(i *linuxIntent) { i.Target.Schema++ }},
		{"noncanonical nonce", func(i *linuxIntent) { i.Nonce = strings.Repeat("A", 64) }},
		{"active without manager", func(i *linuxIntent) { i.Assets[0].Before = linuxAssetState{}; i.ServiceWasActive = true }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			copy := cloneLinuxIntentFixture(intent)
			tc.edit(&copy)
			if err := validateLinuxIntent(copy, tx); err == nil {
				t.Fatal("invalid intent was accepted")
			}
		})
	}
}

func TestRetainedFailedInstallRetryRequiresExactPriorRelation(t *testing.T) {
	valid, tx := retainedLinuxApplyFixture()
	if err := validateLinuxIntent(valid, tx); err != nil {
		t.Fatalf("retained retry fixture rejected: %v", err)
	}
	cases := []struct {
		name string
		edit func(*linuxIntent)
	}{
		{"claims prior installed image", func(i *linuxIntent) { p := linuxImage("a"); i.Prior = &p }},
		{"changes retained principal", func(i *linuxIntent) { i.Principal.UID++ }},
		{"changes schema", func(i *linuxIntent) { i.Target.Schema++ }},
		{"records active service", func(i *linuxIntent) { i.ServiceWasActive = true }},
		{"records enabled service", func(i *linuxIntent) { i.ServiceWasEnabled = true }},
		{"changes captured retained asset", func(i *linuxIntent) { i.Assets[0].Before = linuxState(909, "f") }},
		{"uses installed prior inventory", func(i *linuxIntent) { i.PriorInventory.Status = "installed" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			copy := cloneLinuxIntentFixture(valid)
			tc.edit(&copy)
			if err := validateLinuxIntent(copy, tx); err == nil {
				t.Fatal("inconsistent retained retry was accepted")
			}
		})
	}
}

func TestOperatorUpdateTrustMayChangeButIsCapturedAndPreserved(t *testing.T) {
	intent, tx := managedLinuxApplyFixture()
	changed := linuxState(721, "e")
	changed.Mode = 0644
	intent.Assets[13].Before = changed
	intent.Assets[13].After = plannedLinuxAssetState(changed)
	if err := validateLinuxIntent(intent, tx); err != nil {
		t.Fatalf("managed apply rejected recaptured operator update trust: %v", err)
	}

	intent.Assets[13].After.SHA256 = strings.Repeat("f", 64)
	if err := validateLinuxIntent(intent, tx); err == nil {
		t.Fatal("managed apply accepted a change to operator update trust")
	}

	fresh, freshTx := freshLinuxApplyFixture()
	fresh.Assets[13].Before = changed
	fresh.Assets[13].After = plannedLinuxAssetState(changed)
	if err := validateLinuxIntent(fresh, freshTx); err != nil {
		t.Fatalf("fresh apply rejected preconfigured operator update trust: %v", err)
	}
}

func TestLinuxInventoryRequiresExactOrderedRolesAndCoherentAssets(t *testing.T) {
	installed := linuxInventoryFixture("installed")
	if err := validateLinuxInventory(installed); err != nil {
		t.Fatalf("valid inventory rejected: %v", err)
	}
	raw, err := marshalLinuxInventory(installed)
	if err != nil {
		t.Fatalf("marshal valid inventory: %v", err)
	}
	decoded, err := decodeLinuxInventory(raw)
	if err != nil || decoded.OperationID != installed.OperationID || len(decoded.Assets) != 14 {
		t.Fatalf("decode marshaled inventory: %#v, %v", decoded, err)
	}

	cases := []struct {
		name string
		edit func(*linuxInventory)
	}{
		{"missing role", func(i *linuxInventory) { i.Assets = i.Assets[:13] }},
		{"wrong role order", func(i *linuxInventory) { i.Assets[0], i.Assets[1] = i.Assets[1], i.Assets[0] }},
		{"duplicate role", func(i *linuxInventory) { i.Assets[1].Role = i.Assets[0].Role }},
		{"manager image mismatch", func(i *linuxInventory) { i.Assets[0].State.SHA256 = strings.Repeat("f", 64) }},
		{"uninstalled keeps executable", func(i *linuxInventory) { i.Status = "uninstalled" }},
		{"zero account", func(i *linuxInventory) { i.Principal.GID = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			copy := cloneLinuxInventoryFixture(installed)
			tc.edit(&copy)
			if err := validateLinuxInventory(copy); err == nil {
				t.Fatal("invalid inventory was accepted")
			}
		})
	}
	retained := linuxInventoryFixture("retained")
	if err := validateLinuxInventory(retained); err != nil {
		t.Fatalf("valid retained inventory rejected: %v", err)
	}
	badRetained := cloneLinuxInventoryFixture(retained)
	badRetained.Assets[0].State = linuxState(99, "a")
	if err := validateLinuxInventory(badRetained); err == nil {
		t.Fatal("retained inventory accepted a deployed manager")
	}
	badRetained = cloneLinuxInventoryFixture(retained)
	badRetained.OperationID = strings.Repeat("f", 64)
	if err := validateLinuxInventory(badRetained); err != nil {
		t.Fatalf("inventory identity is structurally valid; origin binding is checked by native evidence: %v", err)
	}
	for _, status := range []string{"installed", "uninstalled"} {
		withUpdateTrust := linuxInventoryFixture(status)
		withUpdateTrust.Assets[13].State = linuxState(900, "e")
		withUpdateTrust.Assets[13].State.Mode = 0644
		if err := validateLinuxInventory(withUpdateTrust); err != nil {
			t.Errorf("%s inventory rejected optional update trust: %v", status, err)
		}
	}
}

func TestDecodeLinuxInventoryRejectsDuplicateNullUnknownAndOversize(t *testing.T) {
	raw, err := marshalLinuxInventory(linuxInventoryFixture("installed"))
	if err != nil {
		t.Fatal(err)
	}
	bad := [][]byte{
		appendJSONField(raw, `"other":1`),
		bytes.Replace(raw, []byte(`"format":1`), []byte(`"format":1,"for\u006dat":1`), 1),
		bytes.Replace(raw, []byte(`"assets":[`), []byte(`"assets":null`), 1),
		bytes.Repeat([]byte{' '}, 64<<10+1),
	}
	for n, body := range bad {
		if _, err := decodeLinuxInventory(body); err == nil {
			t.Errorf("malformed inventory case %d was accepted", n)
		}
	}
}

func freshLinuxApplyFixture() (linuxIntent, transaction) {
	target := linuxImage("a")
	assets := linuxAssetPlans()
	trust := linuxTrustFixture()
	for i := range assets {
		if assets[i].Role == linuxAssetManager {
			assets[i].After.SHA256 = target.SHA256
		}
		if assets[i].Role == linuxAssetBootstrapTrust {
			assets[i].After.SHA256 = sumLinuxFixture(trust)
			assets[i].After.Size = int64(len(trust))
		}
	}
	tx := transaction{Format: 1, ID: strings.Repeat("1", 64), Mode: modeApply, Phase: phasePrepared, IntentSHA256: strings.Repeat("2", 64), Target: imagePointer(target)}
	return linuxIntent{Format: 1, ID: tx.ID, Mode: modeApply, CreatedAt: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC), Nonce: strings.Repeat("3", 64), Target: imagePointer(target), ManifestSHA256: strings.Repeat("4", 64), BootstrapTrust: trust, ExecutorSHA256: assets[1].After.SHA256, Assets: assets}, tx
}

func managedLinuxApplyFixture() (linuxIntent, transaction) {
	prior := linuxImage("a")
	target := linuxImage("c")
	inv := linuxInventoryFixture("installed")
	trust := linuxTrustFixture()
	assets := linuxAssetPlans()
	for i := range assets {
		role := assets[i].Role
		if role == linuxAssetUpdateTrust {
			continue
		}
		assets[i].Before = inv.Assets[i].State
		assets[i].After = linuxState(0, "c")
		assets[i].After.Device = 0
		assets[i].After.Mode = inv.Assets[i].State.Mode
		assets[i].After.Size = 1
		if role == linuxAssetManager {
			assets[i].Before.SHA256 = prior.SHA256
			assets[i].After.SHA256 = target.SHA256
		}
		if role == linuxAssetBootstrapTrust {
			assets[i].Before = linuxInventoryFixture("installed").Assets[i].State
			assets[i].After = linuxAssetState{Present: true, SHA256: sumLinuxFixture(trust), Size: int64(len(trust)), Mode: 0644}
		}
	}
	if assets[1].Before.SHA256 != inv.Assets[1].State.SHA256 {
		assets[1].Before.SHA256 = inv.Assets[1].State.SHA256
	}
	inv.Assets[0].State.SHA256 = prior.SHA256
	tx := transaction{Format: 1, ID: strings.Repeat("1", 64), Mode: modeApply, Phase: phasePrepared, IntentSHA256: strings.Repeat("2", 64), Prior: imagePointer(prior), Target: imagePointer(target)}
	principal := inv.Principal
	return linuxIntent{Format: 1, ID: tx.ID, Mode: modeApply, CreatedAt: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC), Nonce: strings.Repeat("3", 64), Prior: imagePointer(prior), Target: imagePointer(target), PriorInventory: &inv, Principal: &principal, ServiceWasActive: true, ServiceWasEnabled: true, ManifestSHA256: strings.Repeat("4", 64), BootstrapTrust: trust, ExecutorSHA256: assets[1].After.SHA256, Assets: assets}, tx
}

func retainedLinuxApplyFixture() (linuxIntent, transaction) {
	intent, tx := managedLinuxApplyFixture()
	inv := linuxInventoryFixture("retained")
	trust := linuxTrustFixture()
	intent.Prior = nil
	intent.PriorInventory = &inv
	intent.ServiceWasActive = false
	intent.ServiceWasEnabled = false
	intent.Principal = &inv.Principal
	for i := range intent.Assets {
		intent.Assets[i].Before = inv.Assets[i].State
		if intent.Assets[i].Role == linuxAssetUpdateTrust {
			intent.Assets[i].After = linuxAssetState{}
		}
		if intent.Assets[i].Role == linuxAssetBootstrapTrust {
			intent.Assets[i].Before = inv.Assets[i].State
			intent.Assets[i].After = linuxAssetState{Present: true, SHA256: sumLinuxFixture(trust), Size: int64(len(trust)), Mode: 0644}
		}
	}
	tx.Prior = nil
	return intent, tx
}

func supersededLinuxApplyFixture() (linuxIntent, transaction) {
	intent, tx := managedLinuxApplyFixture()
	current := linuxImage("d")
	intent.Prior = imagePointer(current)
	tx.Prior = imagePointer(current)
	intent.Assets[0].Before.SHA256 = current.SHA256
	return intent, tx
}

func managedLinuxUninstallFixture() (linuxIntent, transaction) {
	prior := linuxImage("a")
	inv := linuxInventoryFixture("installed")
	assets := linuxAssetPlans()
	for i := range assets {
		assets[i].Before = inv.Assets[i].State
		assets[i].After = linuxAssetState{}
		if assets[i].Role == linuxAssetManager {
			assets[i].Before.SHA256 = prior.SHA256
		}
		if assets[i].Role == linuxAssetBootstrapTrust {
			assets[i].After = assets[i].Before
			assets[i].After.Device = 0
			assets[i].After.Inode = 0
		}
	}
	inv.Assets[0].State.SHA256 = prior.SHA256
	tx := transaction{Format: 1, ID: strings.Repeat("1", 64), Mode: modeUninstall, Phase: phasePrepared, IntentSHA256: strings.Repeat("2", 64), Prior: imagePointer(prior)}
	principal := inv.Principal
	return linuxIntent{Format: 1, ID: tx.ID, Mode: modeUninstall, CreatedAt: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC), Nonce: strings.Repeat("3", 64), Prior: imagePointer(prior), PriorInventory: &inv, Principal: &principal, ServiceWasActive: true, ServiceWasEnabled: true, BootstrapTrust: []byte{}, ExecutorSHA256: assets[1].Before.SHA256, Assets: assets}, tx
}

func supersededLinuxUninstallFixture() (linuxIntent, transaction) {
	intent, tx := managedLinuxUninstallFixture()
	current := linuxImage("d")
	intent.Prior = imagePointer(current)
	tx.Prior = imagePointer(current)
	intent.Assets[0].Before.SHA256 = current.SHA256
	return intent, tx
}

func linuxInventoryFixture(status string) linuxInventory {
	image := linuxImage("a")
	assets := make([]linuxOwnedAsset, 14)
	for i, role := range linuxFrozenRoles() {
		state := linuxState(100+uint64(i), "b")
		state.Mode = linuxFrozenModes()[i]
		if role == linuxAssetManager {
			state.SHA256 = image.SHA256
		}
		if role == linuxAssetBootstrapTrust {
			trust := linuxTrustFixture()
			state.SHA256 = sumLinuxFixture(trust)
			state.Size = int64(len(trust))
		}
		if (status == "uninstalled" || status == "retained") && role != linuxAssetBootstrapTrust && role != linuxAssetUpdateTrust {
			state = linuxAssetState{}
		}
		if role == linuxAssetUpdateTrust {
			state = linuxAssetState{}
		}
		assets[i] = linuxOwnedAsset{Role: role, State: state}
	}
	return linuxInventory{Format: 1, Status: status, OperationID: strings.Repeat("1", 64), Image: image, Principal: linuxPrincipal{UID: 1000, GID: 1000}, Assets: assets}
}

func linuxAssetPlans() []linuxAssetPlan {
	roles, modes := linuxFrozenRoles(), linuxFrozenModes()
	assets := make([]linuxAssetPlan, 14)
	for i, role := range roles {
		assets[i] = linuxAssetPlan{Role: role, After: linuxState(0, "c")}
		assets[i].After.Mode = modes[i]
		assets[i].After.Device = 0
		if role == linuxAssetUpdateTrust {
			assets[i].After = linuxAssetState{}
		}
	}
	return assets
}

func linuxFrozenRoles() []linuxAssetRole {
	return []linuxAssetRole{linuxAssetManager, linuxAssetUpdater, linuxAssetUnitctl, linuxAssetManagerUnit, linuxAssetGameUnit, linuxAssetUpdaterUnit, linuxAssetRecoveryUnit, linuxAssetManagerDropin, linuxAssetSudoers, linuxAssetLicense, linuxAssetNotices, linuxAssetDeploymentNotice, linuxAssetBootstrapTrust, linuxAssetUpdateTrust}
}

func linuxFrozenModes() []uint32 {
	return []uint32{0755, 0755, 0755, 0644, 0644, 0644, 0644, 0644, 0440, 0644, 0644, 0644, 0644, 0644}
}

func linuxState(inode uint64, digest string) linuxAssetState {
	return linuxAssetState{Present: true, SHA256: strings.Repeat(digest, 64), Size: 1, Mode: 0755, Device: 1, Inode: inode}
}

func linuxImage(digest string) Image {
	return Image{Version: "v1.0.0", Schema: 7, SHA256: strings.Repeat(digest, 64)}
}

func linuxTrustFixture() []byte {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	return []byte(`{"format":1,"repository":"jonasthim/minecraft-server-ui","public_keys":{"key1":"` + key + `"}}`)
}

func sumLinuxFixture(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func appendJSONField(body []byte, field string) []byte {
	trimmed := bytes.TrimSpace(body)
	return append(append(bytes.TrimSuffix(trimmed, []byte("}")), ','), append([]byte(field), '}')...)
}

func cloneLinuxIntentFixture(in linuxIntent) linuxIntent {
	body, _ := json.Marshal(in)
	var out linuxIntent
	_ = json.Unmarshal(body, &out)
	return out
}

func cloneLinuxInventoryFixture(in linuxInventory) linuxInventory {
	body, _ := json.Marshal(in)
	var out linuxInventory
	_ = json.Unmarshal(body, &out)
	return out
}

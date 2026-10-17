//go:build linux

package install

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jonasthim/minecraft-server-ui/release/candidate"
)

const maxLinuxInventoryBytes = 64 << 10
const maxLinuxBootstrapTrustBytes = 64 << 10

type linuxPrincipal struct {
	UID uint32 `json:"uid"`
	GID uint32 `json:"gid"`
}

type linuxOwnedAsset struct {
	Role  linuxAssetRole  `json:"role"`
	State linuxAssetState `json:"state"`
}

type linuxInventory struct {
	Format      int               `json:"format"`
	Status      string            `json:"status"`
	OperationID string            `json:"operation_id"`
	Image       Image             `json:"image"`
	Principal   linuxPrincipal    `json:"principal"`
	Assets      []linuxOwnedAsset `json:"assets"`
}

type linuxAssetPlan struct {
	Role   linuxAssetRole  `json:"role"`
	Before linuxAssetState `json:"before"`
	After  linuxAssetState `json:"after"`
}

type linuxIntent struct {
	Format            int              `json:"format"`
	ID                string           `json:"id"`
	Mode              mode             `json:"mode"`
	CreatedAt         time.Time        `json:"created_at"`
	Nonce             string           `json:"nonce"`
	Prior             *Image           `json:"prior,omitempty"`
	Target            *Image           `json:"target,omitempty"`
	PriorInventory    *linuxInventory  `json:"prior_inventory,omitempty"`
	Principal         *linuxPrincipal  `json:"principal,omitempty"`
	ServiceWasActive  bool             `json:"service_was_active"`
	ServiceWasEnabled bool             `json:"service_was_enabled"`
	ManifestSHA256    string           `json:"manifest_sha256"`
	BootstrapTrust    []byte           `json:"bootstrap_trust"`
	AllowDevelopment  bool             `json:"allow_development"`
	ExecutorSHA256    string           `json:"executor_sha256"`
	Assets            []linuxAssetPlan `json:"assets"`
}

func decodeLinuxIntent(body []byte, tx transaction) (linuxIntent, error) {
	var intent linuxIntent
	if err := decodeLinuxStrictJSON(body, maxIntentBytes, &intent); err != nil {
		return linuxIntent{}, errors.New("invalid Linux installer intent JSON")
	}
	if err := validateLinuxIntent(intent, tx); err != nil {
		return linuxIntent{}, err
	}
	return intent, nil
}

func validateLinuxIntent(intent linuxIntent, tx transaction) error {
	if err := validateTransaction(&tx, tx.Mode, tx.Phase); err != nil {
		return errors.New("Linux installer transaction binding is invalid")
	}
	if intent.Format != 1 || !lowerHex(intent.ID, 64) || !lowerHex(intent.Nonce, 64) ||
		intent.ID != tx.ID || intent.Mode != tx.Mode || !sameOptionalImage(intent.Prior, tx.Prior) || !sameOptionalImage(intent.Target, tx.Target) {
		return errors.New("Linux installer intent identity does not match transaction")
	}
	if intent.CreatedAt.IsZero() || intent.CreatedAt.Location() != time.UTC || intent.CreatedAt.Format("Z07:00") != "Z" {
		return errors.New("Linux installer intent timestamp is invalid")
	}
	timestamp, err := intent.CreatedAt.MarshalJSON()
	if err != nil {
		return errors.New("Linux installer intent timestamp is invalid")
	}
	var roundTrip time.Time
	if err := json.Unmarshal(timestamp, &roundTrip); err != nil || !roundTrip.Equal(intent.CreatedAt) || roundTrip.Location() != time.UTC {
		return errors.New("Linux installer intent timestamp is invalid")
	}
	if intent.Prior != nil && !validImage(*intent.Prior) || intent.Target != nil && !validImage(*intent.Target) {
		return errors.New("Linux installer intent image is invalid")
	}
	if len(intent.Assets) != len(fixedLinuxAssets) {
		return errors.New("Linux installer intent asset plan is incomplete")
	}
	for i, spec := range fixedLinuxAssets {
		plan := intent.Assets[i]
		if plan.Role != spec.Role || !validLinuxCapturedState(spec.Role, plan.Before) || !validLinuxPlannedState(spec.Role, plan.After) {
			return errors.New("Linux installer intent asset plan is invalid")
		}
	}
	if intent.Principal != nil && !validLinuxPrincipal(*intent.Principal) {
		return errors.New("Linux installer intent principal is invalid")
	}
	if intent.ExecutorSHA256 == "" || !lowerHex(intent.ExecutorSHA256, 64) {
		return errors.New("Linux installer executor digest is invalid")
	}

	switch intent.Mode {
	case modeApply:
		if intent.Target == nil || intent.Target.Schema < 1 || intent.Target.Schema != tx.Target.Schema || intent.Prior != nil && intent.Prior.Schema != intent.Target.Schema {
			return errors.New("Linux installer apply image relation is invalid")
		}
		if !lowerHex(intent.ManifestSHA256, 64) || len(intent.BootstrapTrust) == 0 || len(intent.BootstrapTrust) > maxLinuxBootstrapTrustBytes {
			return errors.New("Linux installer apply inputs are invalid")
		}
		if _, err := candidate.ParsePublicTrust(intent.BootstrapTrust, "jonasthim/minecraft-server-ui"); err != nil {
			return errors.New("Linux installer bootstrap trust is invalid")
		}
		if err := validateLinuxApplyIntent(intent); err != nil {
			return err
		}
	case modeUninstall:
		if intent.Prior == nil || intent.Target != nil || intent.PriorInventory == nil || intent.PriorInventory.Status != "installed" ||
			intent.Principal == nil || len(intent.BootstrapTrust) != 0 || intent.ManifestSHA256 != "" || intent.AllowDevelopment {
			return errors.New("Linux installer uninstall inputs are invalid")
		}
		if err := validateLinuxInventory(*intent.PriorInventory); err != nil {
			return errors.New("Linux installer prior inventory is invalid")
		}
		if !sameLinuxPrincipal(*intent.Principal, intent.PriorInventory.Principal) {
			return errors.New("Linux installer principal does not match prior inventory")
		}
		if err := validateLinuxUninstallIntent(intent); err != nil {
			return err
		}
	default:
		return errors.New("Linux installer intent mode is invalid")
	}
	return nil
}

func validateLinuxApplyIntent(intent linuxIntent) error {
	for _, plan := range intent.Assets {
		if plan.Role != linuxAssetUpdateTrust && !plan.After.Present {
			return errors.New("Linux installer target is missing a deployment asset")
		}
	}
	if intent.PriorInventory == nil {
		if intent.Prior != nil {
			return errors.New("Linux installer fresh apply has an unexpected prior image")
		}
		for _, plan := range intent.Assets {
			if plan.Before.Present && plan.Role != linuxAssetUpdateTrust {
				return errors.New("Linux installer fresh apply has captured existing assets")
			}
		}
		if intent.ServiceWasActive || intent.ServiceWasEnabled {
			return errors.New("Linux installer fresh apply has existing service state")
		}
	} else {
		inventory := *intent.PriorInventory
		if err := validateLinuxInventory(inventory); err != nil {
			return errors.New("Linux installer prior inventory is invalid")
		}
		if inventory.Status == "retained" {
			if intent.Prior != nil || intent.Principal == nil || !sameLinuxPrincipal(*intent.Principal, inventory.Principal) || intent.Target.Schema != inventory.Image.Schema || intent.ServiceWasActive || intent.ServiceWasEnabled {
				return errors.New("Linux installer retained-failure identity is inconsistent")
			}
			for i, plan := range intent.Assets {
				if !sameLinuxAssetState(plan.Before, inventory.Assets[i].State) {
					return errors.New("Linux installer retained-failure preimage differs from inventory")
				}
			}
		} else {
			if intent.Prior == nil || intent.Principal == nil || !sameLinuxPrincipal(*intent.Principal, inventory.Principal) || inventory.Image.Schema != intent.Prior.Schema {
				return errors.New("Linux installer prior identity is inconsistent")
			}
			if inventory.Status == "uninstalled" && !sameImage(*intent.Prior, inventory.Image) {
				return errors.New("Linux installer retained image receipt is inconsistent")
			}
			if inventory.Status == "installed" && !intent.Assets[0].Before.Present {
				return errors.New("Linux installer installed manager preimage is missing")
			}
			for i, plan := range intent.Assets {
				prior := inventory.Assets[i].State
				if plan.Role == linuxAssetUpdateTrust {
					continue // operator changes are validated and captured by native Prepare
				}
				if plan.Role == linuxAssetManager {
					if inventory.Status == "uninstalled" {
						if plan.Before != prior {
							return errors.New("Linux installer retained manager preimage is inconsistent")
						}
						continue
					}
					if !plan.Before.Present || plan.Before.SHA256 != intent.Prior.SHA256 {
						return errors.New("Linux installer manager preimage does not match current receipt")
					}
					continue
				}
				if sameLinuxAssetState(plan.Before, prior) {
					continue
				}
				// An apply driver may authorize repair of a known missing deployment
				// asset. The immutable intent captures that absence; only the native
				// driver can establish the user's explicit repair authorization.
				if plan.Role != linuxAssetBootstrapTrust && plan.Role != linuxAssetUpdateTrust && prior.Present && !plan.Before.Present {
					continue
				}
				return errors.New("Linux installer preimage does not match prior inventory")
			}
		}
	}

	if intent.Principal == nil && (intent.Prior != nil || intent.PriorInventory != nil) {
		return errors.New("Linux installer principal is required for a retained installation")
	}
	manager := intent.Assets[0]
	if manager.After.SHA256 != intent.Target.SHA256 || !manager.After.Present {
		return errors.New("Linux installer manager does not match target image")
	}
	updater := intent.Assets[1]
	if intent.ExecutorSHA256 != updater.After.SHA256 {
		return errors.New("Linux installer executor does not match authenticated updater")
	}
	bootstrap := intent.Assets[12]
	sum := sha256.Sum256(intent.BootstrapTrust)
	trustHash := hex.EncodeToString(sum[:])
	if !bootstrap.After.Present || bootstrap.After.SHA256 != trustHash || bootstrap.After.Size != int64(len(intent.BootstrapTrust)) {
		return errors.New("Linux installer bootstrap trust does not match asset plan")
	}
	if bootstrap.Before.Present && !sameLinuxAssetContent(bootstrap.Before, bootstrap.After) {
		return errors.New("Linux installer bootstrap trust rotation is not supported")
	}
	updateTrust := intent.Assets[13]
	if !sameLinuxPlannedContent(updateTrust.Before, updateTrust.After) {
		return errors.New("Linux installer update trust changed unexpectedly")
	}
	if intent.ServiceWasActive && !manager.Before.Present {
		return errors.New("Linux installer active service has no manager preimage")
	}
	if intent.PriorInventory == nil && intent.Principal != nil && !validLinuxPrincipal(*intent.Principal) {
		return errors.New("Linux installer principal is invalid")
	}
	return nil
}

func validateLinuxUninstallIntent(intent linuxIntent) error {
	if intent.Prior.Schema != intent.PriorInventory.Image.Schema || !intent.Assets[0].Before.Present || intent.Assets[0].Before.SHA256 != intent.Prior.SHA256 {
		return errors.New("Linux installer uninstall manager does not match current receipt")
	}
	if intent.ExecutorSHA256 != intent.Assets[1].Before.SHA256 {
		return errors.New("Linux installer uninstall executor does not match installed updater")
	}
	for i, plan := range intent.Assets {
		prior := intent.PriorInventory.Assets[i].State
		if plan.Role == linuxAssetManager {
			if !plan.Before.Present || plan.Before.SHA256 != intent.Prior.SHA256 {
				return errors.New("Linux installer uninstall manager preimage is invalid")
			}
		} else if plan.Role != linuxAssetUpdateTrust && !sameLinuxAssetState(plan.Before, prior) {
			return errors.New("Linux installer uninstall preimage does not match prior inventory")
		}
		if plan.Role == linuxAssetBootstrapTrust || plan.Role == linuxAssetUpdateTrust {
			if !sameLinuxPlannedContent(plan.Before, plan.After) {
				return errors.New("Linux installer uninstall must preserve trust assets")
			}
		} else if plan.After.Present {
			return errors.New("Linux installer uninstall leaves a deployment asset")
		}
	}
	return nil
}

func decodeLinuxInventory(body []byte) (linuxInventory, error) {
	var inventory linuxInventory
	if err := decodeLinuxStrictJSON(body, maxLinuxInventoryBytes, &inventory); err != nil {
		return linuxInventory{}, errors.New("invalid Linux installation inventory JSON")
	}
	if err := validateLinuxInventory(inventory); err != nil {
		return linuxInventory{}, err
	}
	return inventory, nil
}

func validateLinuxInventory(inventory linuxInventory) error {
	if inventory.Format != 1 || (inventory.Status != "installed" && inventory.Status != "uninstalled" && inventory.Status != "retained") || !lowerHex(inventory.OperationID, 64) ||
		!validImage(inventory.Image) || !validLinuxPrincipal(inventory.Principal) || len(inventory.Assets) != len(fixedLinuxAssets) {
		return errors.New("Linux installation inventory header is invalid")
	}
	for i, spec := range fixedLinuxAssets {
		asset := inventory.Assets[i]
		if asset.Role != spec.Role || !validLinuxCapturedState(spec.Role, asset.State) {
			return errors.New("Linux installation inventory asset is invalid")
		}
		if spec.Role == linuxAssetBootstrapTrust {
			if inventory.Status != "retained" && !asset.State.Present {
				return errors.New("Linux installation inventory has no bootstrap trust")
			}
			continue
		}
		if spec.Role == linuxAssetUpdateTrust {
			continue
		}
		if inventory.Status == "installed" && !asset.State.Present {
			return errors.New("installed Linux inventory is missing an owned asset")
		}
		if (inventory.Status == "uninstalled" || inventory.Status == "retained") && asset.State.Present {
			return errors.New("uninstalled Linux inventory retains a deployment asset")
		}
	}
	manager := inventory.Assets[0].State
	if inventory.Status == "installed" && manager.SHA256 != inventory.Image.SHA256 {
		return errors.New("installed Linux inventory manager does not match image")
	}
	if inventory.Status == "retained" && manager.Present {
		return errors.New("retained failed-install inventory claims a deployed manager")
	}
	return nil
}

func marshalLinuxIntent(intent linuxIntent, tx transaction) ([]byte, error) {
	if err := validateLinuxIntent(intent, tx); err != nil {
		return nil, err
	}
	wire := intent
	if wire.Mode == modeUninstall && wire.BootstrapTrust == nil {
		wire.BootstrapTrust = []byte{}
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, errors.New("marshal Linux installer intent")
	}
	if len(body) == 0 || len(body) > maxIntentBytes {
		return nil, errors.New("Linux installer intent exceeds bounds")
	}
	return body, nil
}

func marshalLinuxInventory(inventory linuxInventory) ([]byte, error) {
	if err := validateLinuxInventory(inventory); err != nil {
		return nil, err
	}
	body, err := json.Marshal(inventory)
	if err != nil {
		return nil, errors.New("marshal Linux installation inventory")
	}
	if len(body) == 0 || len(body) > maxLinuxInventoryBytes {
		return nil, errors.New("Linux installation inventory exceeds bounds")
	}
	return body, nil
}

func decodeLinuxStrictJSON(body []byte, limit int, target any) error {
	if len(body) == 0 || len(body) > limit || !utf8.Valid(body) {
		return errors.New("JSON input is empty, oversized or invalid UTF-8")
	}
	check := json.NewDecoder(bytes.NewReader(body))
	if err := scanLinuxJSONValue(check, 0); err != nil {
		return errors.New("JSON structure is invalid")
	}
	if token, err := check.Token(); !errors.Is(err, io.EOF) || token != nil {
		return errors.New("JSON has trailing content")
	}
	if err := requireLinuxJSONFields(body, reflect.TypeOf(target).Elem()); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode JSON fields: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("JSON has trailing value")
	}
	return nil
}

func scanLinuxJSONValue(decoder *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("JSON nesting limit exceeded")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok {
					return errors.New("JSON object key is invalid")
				}
				if _, exists := seen[name]; exists {
					return errors.New("JSON object has duplicate keys")
				}
				seen[name] = struct{}{}
				if err := scanLinuxJSONValue(decoder, depth+1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return errors.New("JSON object is incomplete")
			}
		case '[':
			for decoder.More() {
				if err := scanLinuxJSONValue(decoder, depth+1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return errors.New("JSON array is incomplete")
			}
		default:
			return errors.New("unexpected JSON delimiter")
		}
	}
	return nil
}

var linuxTimeType = reflect.TypeOf(time.Time{})
var linuxBytesType = reflect.TypeOf([]byte(nil))

func requireLinuxJSONFields(body []byte, typ reflect.Type) error {
	var value json.RawMessage
	if err := json.Unmarshal(body, &value); err != nil {
		return errors.New("JSON value is invalid")
	}
	return requireLinuxJSONValue(value, typ)
}

func requireLinuxJSONValue(raw json.RawMessage, typ reflect.Type) error {
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		return errors.New("required JSON value is null")
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ == linuxTimeType || typ == linuxBytesType {
		return nil
	}
	switch typ.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &fields); err != nil || fields == nil {
			return errors.New("required JSON object is missing")
		}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "" || name == "-" {
				continue
			}
			child, exists := fields[name]
			if !exists {
				if field.Type.Kind() == reflect.Pointer {
					continue
				}
				return errors.New("required JSON field is missing")
			}
			if bytes.Equal(bytes.TrimSpace(child), []byte("null")) {
				if field.Type.Kind() == reflect.Pointer {
					continue
				}
				return errors.New("required JSON field is null")
			}
			if err := requireLinuxJSONValue(child, field.Type); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		if typ == linuxBytesType {
			return nil
		}
		var entries []json.RawMessage
		if err := json.Unmarshal(trimmed, &entries); err != nil || entries == nil {
			return errors.New("required JSON array is missing")
		}
		for _, entry := range entries {
			if err := requireLinuxJSONValue(entry, typ.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

func validLinuxPrincipal(principal linuxPrincipal) bool {
	return principal.UID != 0 && principal.GID != 0
}

func validLinuxCapturedState(role linuxAssetRole, state linuxAssetState) bool {
	if !state.Present {
		return state == (linuxAssetState{})
	}
	spec, ok := linuxAssetSpecForRole(role)
	return ok && validLinuxAssetContent(role, state) && state.Device != 0 && state.Inode != 0 && state.Mode == spec.Mode
}

func validLinuxPlannedState(role linuxAssetRole, state linuxAssetState) bool {
	if !state.Present {
		return state == (linuxAssetState{})
	}
	spec, ok := linuxAssetSpecForRole(role)
	return ok && validLinuxAssetContent(role, state) && state.Device == 0 && state.Inode == 0 && state.Mode == spec.Mode
}

func validLinuxAssetContent(role linuxAssetRole, state linuxAssetState) bool {
	if state.Size <= 0 || state.Size > linuxAssetLimit(role) || !lowerHex(state.SHA256, 64) {
		return false
	}
	return true
}

func sameLinuxPrincipal(a, b linuxPrincipal) bool { return a.UID == b.UID && a.GID == b.GID }

func sameImage(a, b Image) bool { return a == b }

func sameLinuxAssetState(a, b linuxAssetState) bool { return a == b }

func sameLinuxAssetContent(a, b linuxAssetState) bool {
	return a.Present == b.Present && a.SHA256 == b.SHA256 && a.Size == b.Size && a.Mode == b.Mode
}

func sameLinuxPlannedContent(before, after linuxAssetState) bool {
	if !before.Present || !after.Present {
		return !before.Present && !after.Present && before == (linuxAssetState{}) && after == (linuxAssetState{})
	}
	return sameLinuxAssetContent(before, after) && after.Device == 0 && after.Inode == 0
}

//go:build linux

package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jonasthim/minecraft-server-ui/internal/selfupdate"
	"github.com/jonasthim/minecraft-server-ui/release/candidate"
)

func (d *linuxInstallDriver) inspectAllAssets(ctx context.Context) error {
	for _, spec := range fixedLinuxAssets {
		if d.assets[spec.Role] != nil {
			continue
		}
		snapshot, err := inspectLinuxAsset(ctx, spec.Role)
		if err != nil {
			return fmt.Errorf("inspect fixed deployment asset %s: %w", spec.Role, err)
		}
		d.assets[spec.Role] = snapshot
	}
	return nil
}

func (d *linuxInstallDriver) anyAssetPresent() bool {
	for _, snapshot := range d.assets {
		if snapshot != nil && snapshot.State().Present {
			return true
		}
	}
	return false
}

func (d *linuxInstallDriver) anyDeploymentAssetPresent() bool {
	for role, snapshot := range d.assets {
		if role != linuxAssetUpdateTrust && role != linuxAssetBootstrapTrust && snapshot != nil && snapshot.State().Present {
			return true
		}
	}
	return false
}

func (d *linuxInstallDriver) assetsMatchInventory(inventory linuxInventory) bool {
	if len(inventory.Assets) != len(fixedLinuxAssets) {
		return false
	}
	for i, spec := range fixedLinuxAssets {
		snapshot := d.assets[spec.Role]
		if snapshot == nil {
			return false
		}
		if spec.Role == linuxAssetUpdateTrust && inventory.Status != "retained" {
			continue
		}
		if !sameLinuxAssetState(snapshot.State(), inventory.Assets[i].State) {
			return false
		}
	}
	return true
}

// assetsMatchTerminalUpdaterSupersession permits only the manager executable
// to differ from inventory when a strict terminal updater snapshot explains
// the receipt advance. Every native deployment asset and principal remains
// bound to the captured inventory.
func (d *linuxInstallDriver) assetsMatchTerminalUpdaterSupersession(inventory linuxInventory, receipt selfupdate.Installed) bool {
	if inventory.Status != "installed" || inventory.Image.Schema != receipt.Schema || len(inventory.Assets) != len(fixedLinuxAssets) {
		return false
	}
	for i, spec := range fixedLinuxAssets {
		snapshot := d.assets[spec.Role]
		if snapshot == nil {
			return false
		}
		actual := snapshot.State()
		if spec.Role == linuxAssetManager {
			if !actual.Present || actual.SHA256 != receipt.SHA256 {
				return false
			}
			continue
		}
		if spec.Role == linuxAssetUpdateTrust {
			continue
		}
		if !sameLinuxAssetState(actual, inventory.Assets[i].State) {
			return false
		}
	}
	return true
}

func (d *linuxInstallDriver) planAssets(ctx context.Context, operation mode, inputs linuxInstallerInputs, trustBytes []byte, prior linuxInventory, havePrior, repair bool, receipt *selfupdate.Installed, updaterSuperseded bool) ([]linuxAssetPlan, error) {
	if err := d.inspectAllAssets(ctx); err != nil {
		return nil, err
	}
	members := map[string]candidate.Member{}
	if inputs != nil {
		for _, member := range inputs.Members() {
			members[member.Name] = member
		}
	}
	plans := make([]linuxAssetPlan, 0, len(fixedLinuxAssets))
	for i, spec := range fixedLinuxAssets {
		snapshot := d.assets[spec.Role]
		if snapshot == nil {
			return nil, errors.New("fixed asset snapshot is unavailable")
		}
		before := snapshot.State()
		if spec.Role == linuxAssetUpdateTrust && before.Present {
			if err := d.validateUpdateTrust(ctx); err != nil {
				return nil, errors.New("operator-controlled update trust is invalid")
			}
		}
		if havePrior {
			expected := prior.Assets[i].State
			if !sameLinuxAssetState(before, expected) {
				if spec.Role == linuxAssetUpdateTrust {
					// This optional trust file is administrator-owned runtime policy.
					// Capture its current exact preimage and preserve it transactionally.
				} else {
					repairableMissing := repair && expected.Present && !before.Present && spec.Role != linuxAssetBootstrapTrust && spec.Role != linuxAssetUpdateTrust
					updaterManager := updaterSuperseded && receipt != nil && spec.Role == linuxAssetManager && before.Present && before.SHA256 == receipt.SHA256 && before.Mode == 0755
					if !repairableMissing && !updaterManager {
						return nil, fmt.Errorf("fixed deployment asset %s differs from retained inventory", spec.Role)
					}
				}
			}
		} else if before.Present {
			if spec.Role != linuxAssetUpdateTrust {
				return nil, fmt.Errorf("fixed deployment asset %s is unmanaged", spec.Role)
			}
		}

		after := linuxAssetState{}
		switch operation {
		case modeApply:
			if spec.Role == linuxAssetBootstrapTrust {
				trustState := plannedLinuxBytes(trustBytes, spec.Mode)
				if before.Present && !sameLinuxAssetContent(before, trustState) {
					return nil, errors.New("bootstrap trust rotation requires a separate reviewed operation")
				}
				after = linuxAssetState{Present: true, SHA256: trustState.SHA256, Size: trustState.Size, Mode: spec.Mode}
			} else if spec.Role == linuxAssetUpdateTrust {
				after = plannedLinuxAssetState(before)
			} else {
				member, ok := members[spec.Member]
				if !ok {
					return nil, fmt.Errorf("authenticated deployment omits required asset %s", spec.Role)
				}
				after = linuxAssetState{Present: true, SHA256: member.SHA256, Size: member.Size, Mode: member.Mode}
			}
		case modeUninstall:
			if spec.Role == linuxAssetBootstrapTrust || spec.Role == linuxAssetUpdateTrust {
				after = plannedLinuxAssetState(before)
			}
		default:
			return nil, errors.New("installer operation is invalid")
		}
		plans = append(plans, linuxAssetPlan{Role: spec.Role, Before: before, After: after})
	}
	return plans, nil
}

func plannedLinuxBytes(body []byte, mode uint32) linuxAssetState {
	sum := sha256.Sum256(body)
	return linuxAssetState{Present: true, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body)), Mode: mode}
}

func (d *linuxInstallDriver) validateUpdateTrust(ctx context.Context) error {
	if d.hooks.updateTrust != nil {
		return d.hooks.updateTrust(ctx)
	}
	_, err := selfupdate.LoadNativeTrust(ctx)
	return err
}

func plannedLinuxAssetState(state linuxAssetState) linuxAssetState {
	state.Device = 0
	state.Inode = 0
	return state
}

func (d *linuxInstallDriver) captureAssetPreimages(ctx context.Context, plans []linuxAssetPlan) error {
	if d.operation == nil {
		return errors.New("private operation directory is unavailable")
	}
	for _, plan := range plans {
		if err := ctx.Err(); err != nil {
			return err
		}
		snapshot := d.assets[plan.Role]
		if snapshot == nil || snapshot.State() != plan.Before {
			return fmt.Errorf("fixed deployment asset %s changed after planning", plan.Role)
		}
		if err := captureLinuxAsset(ctx, snapshot, d.operation); err != nil {
			return fmt.Errorf("capture exact deployment preimage %s: %w", plan.Role, err)
		}
	}
	return nil
}

func openLinuxAssetSource(ctx context.Context, inputs linuxInstallerInputs, role linuxAssetRole, trust []byte, operation *os.Root, intent linuxIntent) (*os.File, error) {
	if role == linuxAssetBootstrapTrust {
		return stageSourceBytes(operation, "source-bootstrap-trust", trust, 0600)
	}
	if inputs == nil {
		return nil, errors.New("authenticated installer input is unavailable")
	}
	spec, ok := linuxAssetSpecForRole(role)
	if !ok || spec.Member == "" {
		return nil, errors.New("asset role has no authenticated member")
	}
	file, err := inputs.OpenMember(ctx, spec.Member)
	if err != nil {
		return nil, err
	}
	member, found := linuxMemberFor(intent, spec.Member)
	if !found {
		_ = file.Close()
		return nil, errors.New("installer intent does not bind the asset member")
	}
	info, err := file.Stat()
	if err != nil || info.Size() != member.Size {
		_ = file.Close()
		return nil, errors.New("authenticated asset member size changed")
	}
	return file, nil
}

func linuxMemberFor(intent linuxIntent, name string) (candidate.Member, bool) {
	// The immutable asset state is the authenticated content binding. The
	// importer rechecks the original member when opened; role-to-name is fixed.
	for _, spec := range fixedLinuxAssets {
		if spec.Member == name {
			for _, plan := range intent.Assets {
				if plan.Role == spec.Role && plan.After.Present {
					return candidate.Member{Name: name, Size: plan.After.Size, SHA256: plan.After.SHA256, Mode: plan.After.Mode}, true
				}
			}
		}
	}
	return candidate.Member{}, false
}

func stageSourceBytes(operation *os.Root, name string, data []byte, mode os.FileMode) (*os.File, error) {
	if operation == nil || !filepath.IsLocal(name) || name == "." || name == ".." {
		return nil, errors.New("private source name is invalid")
	}
	file, err := operation.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, mode)
	if err != nil {
		return nil, err
	}
	if err := writeAll(file, data); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, err
	}
	if _, err := file.Seek(0, 0); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

//go:build linux

package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
)

// LinuxInstallerInspection is a read-only, pinned view of the ordinary
// updater's installed receipt and activation state.
type LinuxInstallerInspection struct {
	Installed Installed
	Present   bool
	Pending   bool
	Terminal  bool
}

// InspectLinuxInstallerState returns a pinned inspection of updater state. It
// never creates state or locks and never runs manager or systemd commands.
func InspectLinuxInstallerState(ctx context.Context) (LinuxInstallerInspection, error) {
	return inspectLinuxInstallerState(ctx, productionLinuxInstallerHooks())
}

func inspectLinuxInstallerState(ctx context.Context, h linuxInstallerHooks) (inspection LinuxInstallerInspection, retErr error) {
	if ctx == nil || h.authorized == nil || h.openProtectedRoot == nil || h.checkProtectedFile == nil {
		return LinuxInstallerInspection{}, errors.New("Linux updater inspection is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return LinuxInstallerInspection{}, err
	}
	if err := h.authorized(); err != nil {
		return LinuxInstallerInspection{}, errors.New("administrator authority is required")
	}
	paths := h.paths
	if !canonicalMaintenancePaths(paths) {
		return LinuxInstallerInspection{}, errors.New("canonical updater paths are unavailable")
	}
	state, err := h.openProtectedRoot(ctx, paths.State)
	if errors.Is(err, os.ErrNotExist) {
		if err := inspectAbsentLinuxUpdaterState(ctx, h); err != nil {
			return LinuxInstallerInspection{Pending: true}, err
		}
		if appeared, err := h.openProtectedRoot(ctx, paths.State); err == nil {
			return LinuxInstallerInspection{Pending: true}, errors.Join(errors.New("updater state appeared during absent-state inspection"), appeared.Close())
		} else if !errors.Is(err, os.ErrNotExist) {
			return LinuxInstallerInspection{Pending: true}, fmt.Errorf("updater state absence could not be revalidated: %w", err)
		}
		return LinuxInstallerInspection{}, nil
	}
	if err != nil {
		return LinuxInstallerInspection{}, fmt.Errorf("protected updater state is unavailable: %w", err)
	}
	bin, err := h.openProtectedRoot(ctx, paths.Bin)
	if err != nil {
		return LinuxInstallerInspection{}, errors.Join(fmt.Errorf("protected manager directory is unavailable: %w", err), state.Close())
	}
	mh := maintenanceHooks{
		paths:              paths,
		authorized:         h.authorized,
		openProtectedRoot:  h.openProtectedRoot,
		openSafeRoot:       safepath.OpenRoot,
		checkProtectedFile: h.checkProtectedFile,
		fence:              syncPrivilegedRoot,
	}
	maintenance := &Maintenance{paths: paths, state: state, bin: bin, hooks: mh}
	installerHooks := linuxInstallerHooks{
		paths:              paths,
		authorized:         h.authorized,
		openProtectedRoot:  h.openProtectedRoot,
		checkProtectedFile: h.checkProtectedFile,
		openDataRoot:       h.openDataRoot,
		managerIDs:         h.managerIDs,
		checkUpdaterLock:   h.checkUpdaterLock,
	}
	session := &LinuxInstaller{maintenance: maintenance, state: state, bin: bin, hooks: installerHooks}
	defer func() {
		retErr = errors.Join(retErr, session.Close())
		if retErr != nil {
			inspection.Terminal = false
		}
	}()
	if err := session.verify(ctx); err != nil {
		return LinuxInstallerInspection{}, fmt.Errorf("protected updater root changed: %w", err)
	}
	if h.checkUpdaterLock != nil {
		if err := h.checkUpdaterLock(state); err != nil {
			return LinuxInstallerInspection{}, errors.New("updater operation lock is unsafe")
		}
	}
	snapshot, err := session.SnapshotUpdater(ctx)
	if err != nil {
		return LinuxInstallerInspection{Pending: true}, fmt.Errorf("updater state requires reconciliation: %w", err)
	}
	installed, present := snapshot.Installed()
	if err := session.verify(ctx); err != nil {
		return LinuxInstallerInspection{Pending: true}, fmt.Errorf("protected updater root changed during inspection: %w", err)
	}
	if h.checkUpdaterLock != nil {
		if err := h.checkUpdaterLock(state); err != nil {
			return LinuxInstallerInspection{Pending: true}, errors.New("updater operation lock changed during inspection")
		}
	}
	if err := ctx.Err(); err != nil {
		return LinuxInstallerInspection{Pending: true}, err
	}
	return LinuxInstallerInspection{Installed: installed, Present: present, Terminal: snapshot.HasTerminal()}, nil
}

func inspectAbsentLinuxUpdaterState(ctx context.Context, h linuxInstallerHooks) error {
	paths := h.paths
	bin, err := h.openProtectedRoot(ctx, paths.Bin)
	if err != nil {
		return fmt.Errorf("manager directory could not be checked for orphaned updater files: %w", err)
	}
	entries, err := installerReadDir(bin)
	if err != nil {
		_ = bin.Close()
		return err
	}
	for _, entry := range entries {
		if entry.Name() == paths.Binary || strings.HasPrefix(entry.Name(), paths.Binary+".") {
			_ = bin.Close()
			return errors.New("manager image or updater scratch exists without updater state")
		}
	}
	if err := verifyMaintenanceRootBinding(ctx, maintenanceHooks{openProtectedRoot: h.openProtectedRoot}, paths.Bin, bin); err != nil {
		_ = bin.Close()
		return err
	}
	if err := bin.Close(); err != nil {
		return err
	}
	if h.openDataRoot == nil {
		return errors.New("manager control inspection is unavailable")
	}
	control, err := h.openDataRoot(ctx, controlDirectory(paths.Data))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("manager control state could not be checked: %w", err)
	}
	entries, err = installerReadDir(control)
	if err != nil {
		_ = control.Close()
		return err
	}
	if len(entries) != 0 {
		_ = control.Close()
		return errors.New("manager control state exists without updater state")
	}
	rootInfo, err := control.Stat(".")
	if err != nil {
		_ = control.Close()
		return err
	}
	current, err := h.openDataRoot(ctx, controlDirectory(paths.Data))
	if err != nil {
		_ = control.Close()
		return errors.New("manager control root changed during inspection")
	}
	currentInfo, statErr := current.Stat(".")
	closeCurrentErr := current.Close()
	if statErr != nil || closeCurrentErr != nil || !os.SameFile(rootInfo, currentInfo) {
		_ = control.Close()
		return errors.Join(errors.New("manager control root changed during inspection"), statErr, closeCurrentErr)
	}
	return control.Close()
}

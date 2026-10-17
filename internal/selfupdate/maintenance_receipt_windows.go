//go:build windows

package selfupdate

import (
	"errors"

	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
	"golang.org/x/sys/windows"
)

func productionMaintenanceHooks() maintenanceHooks {
	paths := platformPaths()
	return maintenanceHooks{
		paths:              paths,
		authorized:         windowsMaintenanceAuthority,
		openProtectedRoot:  openProtectedRoot,
		openSafeRoot:       safepath.OpenRoot,
		checkProtectedFile: protectedFile,
		acquireLock:        launcher.AcquireManager,
		fence:              syncPrivilegedRoot,
	}
}

func windowsMaintenanceAuthority() error {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return errors.New("administrator token could not be verified")
	}
	if user.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		return nil
	}
	if !token.IsElevated() {
		return errors.New("elevated administrator token is required")
	}
	admin, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return errors.New("administrator membership could not be verified")
	}
	member, err := token.IsMember(admin)
	if err != nil || !member {
		return errors.New("elevated administrator token is required")
	}
	return nil
}

//go:build linux

package selfupdate

import (
	"errors"
	"os"

	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
)

func productionMaintenanceHooks() maintenanceHooks {
	paths := platformPaths()
	return maintenanceHooks{
		paths: paths,
		authorized: func() error {
			if osGeteuid() != 0 {
				return errMaintenanceAuthority
			}
			return nil
		},
		openProtectedRoot:  openProtectedRoot,
		openSafeRoot:       safepath.OpenRoot,
		checkProtectedFile: protectedFile,
		acquireLock:        launcher.AcquireManager,
		fence:              syncPrivilegedRoot,
	}
}

func osGeteuid() int { return os.Geteuid() }

var errMaintenanceAuthority = errors.New("administrator authority is required")

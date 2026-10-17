//go:build !linux && !windows

package selfupdate

func productionMaintenanceHooks() maintenanceHooks { return maintenanceHooks{} }

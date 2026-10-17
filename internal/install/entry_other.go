//go:build !linux

package install

import (
	"context"
	"errors"
)

func CheckInstallerStartup(context.Context) (bool, error) { return false, nil }

func platformInstallerEntryBackend() installerEntryBackend {
	return installerEntryBackend{
		authorize: func() error { return nil },
		run: func(_ context.Context, command installerCommand) (any, error) {
			if command.kind == installerCheck {
				return nil, errors.New("installer checks are supported only on Linux")
			}
			return Result{Outcome: OutcomeManualRepair, FailedStage: "preflight"}, errors.New("installer commands are supported only on Linux")
		},
	}
}

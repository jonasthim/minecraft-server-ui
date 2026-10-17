package install

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

const maxInstallerResponseBytes = 16 << 10

type installerCommandKind uint8

const (
	installerApply installerCommandKind = iota + 1
	installerCheck
	installerUninstall
	installerRecover
	installerWorker
	installerRecoverWorker
)

type installerCommand struct {
	kind    installerCommandKind
	options ApplyOptions
	id      string
}

type installerEntryBackend struct {
	// authorize is invoked only after the complete argument list has passed
	// validation and before a command can perform native I/O.
	authorize func() error
	run       func(context.Context, installerCommand) (any, error)
}

// RunInstallerCommand handles the leading "install" command family. It leaves
// all other argv slices and outputs untouched so it can be called before the
// manager's ordinary command switch.
func RunInstallerCommand(ctx context.Context, args []string, out io.Writer) (bool, error) {
	if len(args) == 0 || args[0] != "install" {
		return false, nil
	}
	command, err := parseInstallerCommand(args)
	if err != nil {
		return true, err
	}
	return true, runInstallerCommand(ctx, command, out, platformInstallerEntryBackend())
}

func runInstallerCommand(ctx context.Context, command installerCommand, out io.Writer, backend installerEntryBackend) error {
	if ctx == nil || out == nil || backend.authorize == nil || backend.run == nil {
		return errors.New("installer command is unavailable")
	}
	if err := backend.authorize(); err != nil {
		return errors.New("installer command requires local administrator authority")
	}
	value, runErr := backend.run(ctx, command)
	if value != nil {
		if err := writeInstallerJSON(out, value); err != nil {
			return errors.New("installer result could not be written")
		}
	}
	return runErr
}

func parseInstallerCommand(args []string) (installerCommand, error) {
	bad := func() (installerCommand, error) {
		return installerCommand{}, errors.New("invalid install command arguments")
	}
	if len(args) < 2 || args[0] != "install" {
		return bad()
	}
	switch args[1] {
	case "apply":
		var options ApplyOptions
		seen := map[string]bool{}
		for i := 2; i < len(args); i++ {
			flag := args[i]
			switch flag {
			case "--prepared", "--trust-file", "--version":
				if seen[flag] || i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
					return bad()
				}
				seen[flag] = true
				value := args[i+1]
				i++
				switch flag {
				case "--prepared":
					options.Prepared = value
				case "--trust-file":
					options.TrustFile = value
				case "--version":
					options.Version = value
				}
			case "--repair":
				if seen[flag] {
					return bad()
				}
				seen[flag], options.Repair = true, true
			case "--allow-development":
				if seen[flag] {
					return bad()
				}
				seen[flag], options.AllowDevelopment = true, true
			default:
				return bad()
			}
		}
		if !seen["--prepared"] || !seen["--trust-file"] || !seen["--version"] || !cleanAbsolute(options.Prepared) || !cleanAbsolute(options.TrustFile) || validateApplyOptions(options) != nil {
			return bad()
		}
		return installerCommand{kind: installerApply, options: options}, nil
	case "check":
		if len(args) != 2 {
			return bad()
		}
		return installerCommand{kind: installerCheck}, nil
	case "uninstall":
		if len(args) != 2 {
			return bad()
		}
		return installerCommand{kind: installerUninstall}, nil
	case "recover":
		if len(args) != 2 {
			return bad()
		}
		return installerCommand{kind: installerRecover}, nil
	case "worker", "recover-worker":
		if len(args) != 3 || !lowerHex(args[2], 64) {
			return bad()
		}
		kind := installerWorker
		if args[1] == "recover-worker" {
			kind = installerRecoverWorker
		}
		return installerCommand{kind: kind, id: args[2]}, nil
	default:
		return bad()
	}
}

func writeInstallerJSON(out io.Writer, value any) error {
	body, err := json.Marshal(value)
	if err != nil || len(body) == 0 || len(body)+1 > maxInstallerResponseBytes {
		return errors.New("installer response is invalid")
	}
	body = append(body, '\n')
	for len(body) > 0 {
		n, writeErr := out.Write(body)
		if n < 0 || n > len(body) {
			return errors.New("installer response write failed")
		}
		body = body[n:]
		if writeErr != nil {
			return writeErr
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func validInstallerResult(result Result, id string) bool {
	if result.OperationID != id || (result.FailedStage != "" && !coordinatorStages[result.FailedStage] && result.FailedStage != "worker") {
		return false
	}
	switch result.Outcome {
	case OutcomePending:
		return lowerHex(result.OperationID, 64) && (result.Image == nil || validImage(*result.Image))
	case OutcomeUnchanged:
		return result.OperationID == "" && result.FailedStage == "" && (result.Image == nil || validImage(*result.Image))
	case OutcomeInstalled:
		return result.Image != nil && validImage(*result.Image)
	case OutcomeUninstalled, OutcomeRolledBack, OutcomeManualRepair:
		return result.Image == nil || validImage(*result.Image)
	default:
		return false
	}
}

func validInstallerCheckReport(report CheckReport) bool {
	switch report.State {
	case "absent", "recovery_required", "drifted", "installed", "uninstalled", "retained":
	default:
		return false
	}
	if report.State == "retained" {
		return !report.Coherent && !report.Pending && (report.Image == nil || validImage(*report.Image))
	}
	if report.Pending != (report.State == "recovery_required") || report.Coherent && report.Pending {
		return false
	}
	return report.Image == nil || validImage(*report.Image)
}

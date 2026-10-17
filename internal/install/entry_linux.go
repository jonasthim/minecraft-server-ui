//go:build linux

package install

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
)

const (
	linuxInstallerUnitPrefix   = "minecraft-ui-install-"
	linuxInstallerDispatchWait = 15 * time.Minute
)

type linuxEntryDriver interface {
	driver
	StageExecutor(context.Context, transaction) (string, error)
	VerifyExecutor(context.Context, transaction) error
	Close() error
}

// linuxInstallerEntryHooks are private test seams. The public factory always
// returns fixed production paths, commands and authority checks.
type linuxInstallerEntryHooks struct {
	stateRoot string
	newDriver func(context.Context) (linuxEntryDriver, error)
	dispatch  func(context.Context, string, string, bool) error
	read      func(context.Context, string) (Result, bool, error)
	check     func(context.Context) (CheckReport, error)
}

type linuxInstallerEntryHooksKey struct{}

func withLinuxInstallerEntryHooks(ctx context.Context, hooks linuxInstallerEntryHooks) context.Context {
	return context.WithValue(ctx, linuxInstallerEntryHooksKey{}, hooks)
}

func linuxEntryHooksFrom(ctx context.Context) linuxInstallerEntryHooks {
	if ctx != nil {
		if hooks, ok := ctx.Value(linuxInstallerEntryHooksKey{}).(linuxInstallerEntryHooks); ok {
			return hooks
		}
	}
	return linuxInstallerEntryHooks{
		stateRoot: linuxInstallerState,
		newDriver: func(ctx context.Context) (linuxEntryDriver, error) {
			return newLinuxInstallDriver(ctx)
		},
		dispatch: runLinuxInstallerWorker,
		read:     readLinuxInstallerResult,
		check:    checkLinuxInstallation,
	}
}

func platformInstallerEntryBackend() installerEntryBackend {
	return installerEntryBackend{
		authorize: func() error {
			if os.Geteuid() != 0 {
				return errors.New("root authority is required")
			}
			return nil
		},
		run: func(ctx context.Context, command installerCommand) (any, error) {
			return runLinuxInstallerCommand(ctx, command)
		},
	}
}

func CheckInstallerStartup(ctx context.Context) (bool, error) {
	return CheckLinuxInstallerStartup(ctx)
}

func runLinuxInstallerCommand(ctx context.Context, command installerCommand) (any, error) {
	if ctx == nil {
		return nil, errors.New("installer command context is unavailable")
	}
	hooks := linuxEntryHooksFrom(ctx)
	switch command.kind {
	case installerCheck:
		report, err := hooks.check(ctx)
		if err != nil {
			return nil, errors.New("installer check could not complete safely")
		}
		if !validInstallerCheckReport(report) {
			return nil, errors.New("installer check returned an invalid report")
		}
		return report, nil
	case installerApply:
		return linuxInstallerApply(ctx, hooks, command.options)
	case installerUninstall:
		return linuxInstallerApply(ctx, hooks, ApplyOptions{}, modeUninstall)
	case installerRecover:
		return linuxInstallerRecover(ctx, hooks)
	case installerWorker, installerRecoverWorker:
		return linuxInstallerWorker(ctx, hooks, command)
	default:
		return nil, errors.New("unsupported installer command")
	}
}

func linuxInstallerApply(ctx context.Context, hooks linuxInstallerEntryHooks, options ApplyOptions, requestedMode ...mode) (Result, error) {
	operation := modeApply
	if len(requestedMode) == 1 {
		operation = requestedMode[0]
	}
	d, err := hooks.newDriver(ctx)
	if err != nil {
		return Result{Outcome: OutcomeManualRepair, FailedStage: "preflight"}, errors.New("installer operation could not acquire its protected session")
	}
	closed := false
	closeDriver := func() error {
		if closed {
			return nil
		}
		closed = true
		return d.Close()
	}
	priorResult, done, err := settlePrior(ctx, d)
	if err != nil {
		closeErr := closeDriver()
		return priorResult, errors.Join(errors.New("installer recovery is required"), safeCloseError(closeErr))
	}
	_ = done // A finalized prior transaction is safe to follow with a new request.
	prepared, unchanged, err := d.Prepare(ctx, operation, options)
	if err != nil {
		closeErr := closeDriver()
		return Result{Outcome: OutcomeManualRepair, FailedStage: "prepare"}, errors.Join(errors.New("installer preparation failed"), safeCloseError(closeErr))
	}
	if prepared == nil {
		if unchanged == nil || !validUnchanged(*unchanged, operation, options.Version) {
			closeErr := closeDriver()
			return Result{Outcome: OutcomeManualRepair, FailedStage: "prepare"}, errors.Join(errors.New("installer preparation returned an invalid result"), safeCloseError(closeErr))
		}
		closeErr := closeDriver()
		if closeErr != nil {
			return cloneResult(*unchanged), errors.New("installer session cleanup failed")
		}
		return cloneResult(*unchanged), nil
	}
	if unchanged != nil {
		closeErr := closeDriver()
		return Result{Outcome: OutcomeManualRepair, FailedStage: "prepare"}, errors.Join(errors.New("installer preparation returned conflicting state"), safeCloseError(closeErr))
	}
	tx := cloneTransaction(prepared)
	if err := validateTransaction(tx, operation, phasePrepared); err != nil || operation == modeApply && (tx.Target == nil || tx.Target.Version != options.Version) {
		closeErr := closeDriver()
		return manualResult(tx, "prepare"), errors.Join(errors.New("installer preparation returned invalid transaction"), safeCloseError(closeErr))
	}
	executor, err := d.StageExecutor(ctx, *tx)
	if err != nil {
		closeErr := closeDriver()
		return manualResult(tx, "prepare"), errors.Join(errors.New("installer executor could not be verified and staged"), safeCloseError(closeErr))
	}
	if err := validateFixedExecutorPath(hooks.stateRoot, tx.ID, executor); err != nil {
		closeErr := closeDriver()
		return manualResult(tx, "prepare"), errors.Join(errors.New("installer executor path is invalid"), safeCloseError(closeErr))
	}
	if err := recordNext(ctx, d, nil, tx, phasePrepared, ""); err != nil {
		closeErr := closeDriver()
		return manualResult(tx, "record_prepared"), errors.Join(errors.New("installer prepared state could not be confirmed"), safeCloseError(closeErr))
	}
	if err := closeDriver(); err != nil {
		return pendingInstallerResult(tx.ID, tx.Target), errors.New("installer session could not be released before worker dispatch")
	}
	return dispatchInstallerWorker(ctx, hooks, *tx, executor, false)
}

func linuxInstallerRecover(ctx context.Context, hooks linuxInstallerEntryHooks) (Result, error) {
	d, err := hooks.newDriver(ctx)
	if err != nil {
		return Result{Outcome: OutcomeManualRepair, FailedStage: "recover"}, errors.New("installer recovery could not acquire its protected session")
	}
	closed := false
	closeDriver := func() error {
		if closed {
			return nil
		}
		closed = true
		return d.Close()
	}
	// Only explicit local recovery may reconcile a fully written pending
	// publication. Ordinary apply, check and startup admission remain read-only
	// when they encounter uncertain state. The driver holds the updater lock.
	if pending, ok := d.(interface{ RecoverPending(context.Context) error }); ok {
		if err := pending.RecoverPending(ctx); err != nil {
			closeErr := closeDriver()
			return Result{Outcome: OutcomeManualRepair, FailedStage: "recover"}, errors.Join(errors.New("pending installer state could not be reconciled safely"), safeCloseError(closeErr))
		}
	}
	tx, err := d.Load(ctx)
	if err != nil {
		closeErr := closeDriver()
		return Result{Outcome: OutcomeManualRepair, FailedStage: "recover"}, errors.Join(errors.New("installer transaction could not be verified"), safeCloseError(closeErr))
	}
	if tx == nil {
		closeErr := closeDriver()
		if closeErr != nil {
			return Result{Outcome: OutcomeUnchanged}, errors.New("installer session cleanup failed")
		}
		return Result{Outcome: OutcomeUnchanged}, nil
	}
	owned := cloneTransaction(tx)
	if err := validateTransaction(owned, owned.Mode, owned.Phase); err != nil {
		closeErr := closeDriver()
		return manualResult(owned, "recover"), errors.Join(errors.New("installer transaction is invalid"), safeCloseError(closeErr))
	}
	if owned.Phase == phaseCommitted || owned.Phase == phaseRolledBack {
		result, recoverErr := recoverWith(ctx, d)
		closeErr := closeDriver()
		return result, errors.Join(sanitizeCoordinatorError(recoverErr), safeCloseError(closeErr))
	}
	executor, err := d.StageExecutor(ctx, *owned)
	if err != nil {
		closeErr := closeDriver()
		return manualResult(owned, "recover"), errors.Join(errors.New("fixed installer executor could not be verified for recovery"), safeCloseError(closeErr))
	}
	if err := validateFixedExecutorPath(hooks.stateRoot, owned.ID, executor); err != nil {
		closeErr := closeDriver()
		return manualResult(owned, "recover"), errors.Join(errors.New("installer executor path is invalid"), safeCloseError(closeErr))
	}
	if err := closeDriver(); err != nil {
		return pendingInstallerResult(owned.ID, resultImage(owned)), errors.New("installer session could not be released before worker dispatch")
	}
	return dispatchInstallerWorker(ctx, hooks, *owned, executor, true)
}

func linuxInstallerWorker(ctx context.Context, hooks linuxInstallerEntryHooks, command installerCommand) (Result, error) {
	d, err := hooks.newDriver(ctx)
	if err != nil {
		return Result{Outcome: OutcomeManualRepair, OperationID: command.id, FailedStage: "preflight"}, errors.New("installer worker could not acquire its protected session")
	}
	closed := false
	closeDriver := func() error {
		if closed {
			return nil
		}
		closed = true
		return d.Close()
	}
	tx, err := d.Load(ctx)
	if err != nil || tx == nil || tx.ID != command.id {
		closeErr := closeDriver()
		return Result{Outcome: OutcomeManualRepair, OperationID: command.id, FailedStage: "recover"}, errors.Join(errors.New("installer worker transaction does not match"), safeCloseError(closeErr))
	}
	if err := d.VerifyExecutor(ctx, *tx); err != nil {
		closeErr := closeDriver()
		return Result{Outcome: OutcomeManualRepair, OperationID: command.id, FailedStage: "preflight"}, errors.Join(errors.New("installer worker executable is not authorized"), safeCloseError(closeErr))
	}
	var result Result
	if command.kind == installerWorker {
		if tx.Phase != phasePrepared {
			closeErr := closeDriver()
			return Result{Outcome: OutcomeManualRepair, OperationID: command.id, FailedStage: "recover"}, errors.Join(errors.New("installer worker transaction is not prepared"), safeCloseError(closeErr))
		}
		result, err = continuePreparedWith(ctx, d, command.id)
	} else {
		result, err = recoverWith(ctx, d)
	}
	closeErr := closeDriver()
	if result.OperationID != "" && result.OperationID != command.id {
		return Result{Outcome: OutcomeManualRepair, OperationID: command.id, FailedStage: "recover"}, errors.New("installer worker returned a mismatched result")
	}
	if closeErr != nil {
		if result.FailedStage == "" {
			result.FailedStage = "worker"
		}
		return result, errors.Join(sanitizeCoordinatorError(err), errors.New("installer worker session cleanup failed"))
	}
	return result, sanitizeCoordinatorError(err)
}

func dispatchInstallerWorker(ctx context.Context, hooks linuxInstallerEntryHooks, tx transaction, executor string, recover bool) (Result, error) {
	if ctx.Err() != nil {
		return pendingInstallerResult(tx.ID, resultImage(&tx)), errors.New("installer worker dispatch was cancelled")
	}
	if err := hooks.dispatch(ctx, tx.ID, executor, recover); err != nil {
		if ctx.Err() != nil {
			return pendingInstallerResult(tx.ID, resultImage(&tx)), errors.New("installer worker outcome is pending")
		}
		result, present, readErr := hooks.read(ctx, tx.ID)
		if readErr != nil || !present || !validInstallerResult(result, tx.ID) {
			return pendingInstallerResult(tx.ID, resultImage(&tx)), errors.New("installer worker outcome is pending after dispatch failure")
		}
		if result.FailedStage == "" {
			result.FailedStage = "worker"
		}
		return result, errors.New("installer worker dispatch did not complete cleanly")
	}
	result, present, err := hooks.read(ctx, tx.ID)
	if err != nil || !present || !validInstallerResult(result, tx.ID) {
		return pendingInstallerResult(tx.ID, resultImage(&tx)), errors.New("installer worker result is unavailable or invalid")
	}
	if result.Outcome == OutcomePending || result.FailedStage != "" || result.Outcome == OutcomeManualRepair {
		return result, errors.New("installer worker reported an incomplete operation")
	}
	return result, nil
}

func pendingInstallerResult(id string, image *Image) Result {
	return Result{Outcome: OutcomePending, OperationID: id, Image: cloneInstallerImage(image)}
}

func resultImage(tx *transaction) *Image {
	if tx == nil {
		return nil
	}
	if tx.Target != nil {
		return cloneInstallerImage(tx.Target)
	}
	return cloneInstallerImage(tx.Prior)
}

func cloneInstallerImage(image *Image) *Image {
	if image == nil {
		return nil
	}
	copy := *image
	return &copy
}

func validateFixedExecutorPath(stateRoot, id, path string) error {
	if stateRoot == "" {
		stateRoot = linuxInstallerState
	}
	if !lowerHex(id, 64) || path != filepath.Join(stateRoot, installDirectory, id, linuxExecutorName) || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("installer executor is not at its fixed path")
	}
	return nil
}

func runLinuxInstallerWorker(ctx context.Context, id, executor string, recover bool) error {
	if !lowerHex(id, 64) || validateFixedExecutorPath(linuxInstallerState, id, executor) != nil {
		return errors.New("installer worker dispatch identity is invalid")
	}
	path, args, env := linuxInstallerWorkerSpec(id, executor, recover)
	commandCtx, cancel := context.WithTimeout(ctx, linuxInstallerDispatchWait)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, path, args...)
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Run(); err != nil {
		return errors.New("installer systemd worker did not report clean completion")
	}
	if commandCtx.Err() != nil {
		return errors.New("installer systemd worker wait ended before completion")
	}
	return nil
}

func linuxInstallerWorkerArgs(id, executor string, recover bool) []string {
	unit := linuxInstallerUnitPrefix + id
	workerCommand := "worker"
	if recover {
		workerCommand = "recover-worker"
	}
	return []string{
		"--quiet", "--wait", "--collect", "--unit=" + unit,
		"--service-type=exec", "--property=User=root", "--property=Group=root",
		"--property=UMask=0077", "--property=RuntimeMaxSec=15min",
		"--property=TimeoutStopSec=30s", "--", executor, "install", workerCommand, id,
	}
}

func linuxInstallerWorkerSpec(id, executor string, recover bool) (string, []string, []string) {
	return "/usr/bin/systemd-run", linuxInstallerWorkerArgs(id, executor, recover), []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
}

func sanitizeCoordinatorError(err error) error {
	if err == nil {
		return nil
	}
	var stage interface{ Error() string }
	if errors.As(err, &stage) {
		message := stage.Error()
		if strings.HasPrefix(message, "installer transaction failed at ") {
			return errors.New(message)
		}
	}
	return errors.New("installer coordinator reported a safe failure")
}

func safeCloseError(err error) error {
	if err != nil {
		return errors.New("installer session cleanup failed")
	}
	return nil
}

func readLinuxInstallerResult(ctx context.Context, id string) (Result, bool, error) {
	return readLinuxInstallerResultAt(ctx, linuxInstallerState, id, 0)
}

func readLinuxInstallerResultAt(ctx context.Context, stateRoot, id string, owner uint32) (Result, bool, error) {
	var result Result
	if ctx == nil || ctx.Err() != nil || !lowerHex(id, 64) {
		return result, false, errors.New("installer result identity is invalid")
	}
	root, err := safepath.OpenRoot(ctx, stateRoot)
	if err != nil {
		return result, false, err
	}
	defer root.Close()
	operation, err := openLinuxOperation(ctx, root, id)
	if errors.Is(err, os.ErrNotExist) {
		return result, false, nil
	}
	if err != nil {
		return result, false, err
	}
	defer operation.Close()
	info, err := operation.Lstat(linuxResultName)
	if errors.Is(err, os.ErrNotExist) {
		return result, false, nil
	}
	if err != nil || checkProtectedFile(info, owner) != nil || info.Mode().Perm() != 0600 || info.Size() < 1 || info.Size() > maxInstallerResponseBytes {
		return result, false, errors.New("installer result file is unsafe")
	}
	file, err := operation.Open(linuxResultName)
	if err != nil {
		return result, false, err
	}
	opened, statErr := file.Stat()
	body, readErr := io.ReadAll(io.LimitReader(file, maxInstallerResponseBytes+1))
	closeErr := file.Close()
	after, pathErr := operation.Lstat(linuxResultName)
	if statErr != nil || readErr != nil || closeErr != nil || pathErr != nil || !os.SameFile(info, opened) || !os.SameFile(info, after) || int64(len(body)) != info.Size() || len(body) > maxInstallerResponseBytes {
		return result, false, errors.New("installer result file changed while reading")
	}
	if err := decodeInstallerResultJSON(body, &result); err != nil || !validInstallerResult(result, id) {
		return result, false, errors.New("installer result file is invalid")
	}
	return result, true, nil
}

func decodeInstallerResultJSON(body []byte, result *Result) error {
	if result == nil || len(body) == 0 || len(body) > maxInstallerResponseBytes || !utf8.Valid(body) {
		return errors.New("installer result JSON exceeds its bound")
	}
	check := json.NewDecoder(strings.NewReader(string(body)))
	if err := scanLinuxJSONValue(check, 0); err != nil {
		return errors.New("installer result JSON structure is invalid")
	}
	if token, err := check.Token(); !errors.Is(err, io.EOF) || token != nil {
		return errors.New("installer result JSON has trailing content")
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(result); err != nil {
		return errors.New("installer result JSON fields are invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("installer result JSON has a trailing value")
	}
	return nil
}

package install

import (
	"context"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"

	"github.com/jonasthim/minecraft-server-ui/internal/selfupdate"
)

var coordinatorHexDigits = func() [256]bool {
	var allowed [256]bool
	for _, c := range "0123456789abcdef" {
		allowed[byte(c)] = true
	}
	return allowed
}()

var coordinatorStages = map[string]bool{
	"preflight": true, "prepare": true, "record_prepared": true,
	"quiesce": true, "record_quiescing": true, "record_stopped": true,
	"record_replacing": true, "replace": true, "publish": true,
	"record_published": true, "record_verifying": true, "verify": true,
	"record_committed": true, "finish": true, "record_rollback": true,
	"rollback": true, "record_rolled_back": true,
	"record_manual_repair": true, "recover": true,
}

// coordinatorError deliberately exposes only a fixed stage. Driver error text
// remains available to errors.Is/As for internal handling, but is not emitted
// by Error or copied into the durable journal/result.
type coordinatorError struct {
	stage  string
	causes []error
}

func (e *coordinatorError) Error() string { return "installer transaction failed at " + e.stage }

func (e *coordinatorError) Unwrap() []error {
	causes := make([]error, 0, len(e.causes))
	for _, cause := range e.causes {
		if cause != nil {
			causes = append(causes, cause)
		}
	}
	return causes
}

func fail(stage string, causes ...error) error {
	if !coordinatorStages[stage] {
		stage = "recover"
	}
	return &coordinatorError{stage: stage, causes: causes}
}

// applyWith runs one authenticated installer operation through durable phases.
// Native authority, locks and startup fencing are supplied by the future native
// wrapper; this function is intentionally limited to the deterministic engine.
func applyWith(ctx context.Context, d driver, options ApplyOptions) (Result, error) {
	if err := validateApplyOptions(options); err != nil {
		return Result{Outcome: OutcomeManualRepair, FailedStage: "preflight"}, fail("preflight", err)
	}
	return runWith(ctx, d, modeApply, options)
}

func uninstallWith(ctx context.Context, d driver) (Result, error) {
	return runWith(ctx, d, modeUninstall, ApplyOptions{})
}

func runWith(ctx context.Context, d driver, operation mode, options ApplyOptions) (Result, error) {
	if ctx == nil || d == nil || (operation != modeApply && operation != modeUninstall) {
		return Result{Outcome: OutcomeManualRepair, FailedStage: "preflight"}, fail("preflight", errors.New("invalid installer invocation"))
	}
	opCtx, cancel := context.WithTimeout(ctx, operationBudget)
	defer cancel()
	if err := opCtx.Err(); err != nil {
		return Result{Outcome: OutcomeManualRepair, FailedStage: "preflight"}, fail("preflight", err)
	}

	if terminal, done, err := settlePrior(opCtx, d); err != nil {
		return terminal, err
	} else if done {
		// Terminal work was finalized, so a fresh request may proceed.
	}

	prepared, unchanged, err := d.Prepare(opCtx, operation, options)
	if err != nil {
		return Result{Outcome: OutcomeManualRepair, FailedStage: "prepare"}, fail("prepare", err)
	}
	if prepared == nil {
		if unchanged == nil || !validUnchanged(*unchanged, operation, options.Version) {
			return Result{Outcome: OutcomeManualRepair, FailedStage: "prepare"}, fail("prepare", errors.New("native preparation returned an invalid unchanged result"))
		}
		return cloneResult(*unchanged), nil
	}
	if unchanged != nil {
		return Result{Outcome: OutcomeManualRepair, FailedStage: "prepare"}, fail("prepare", errors.New("native preparation returned conflicting results"))
	}
	tx := cloneTransaction(prepared)
	if err := validateTransaction(tx, operation, phasePrepared); err != nil {
		return manualResult(tx, "prepare"), fail("prepare", err)
	}
	if operation == modeApply && tx.Target.Version != options.Version {
		return manualResult(tx, "prepare"), fail("prepare", errors.New("prepared target does not match requested version"))
	}

	if err := recordNext(opCtx, d, nil, tx, phasePrepared, ""); err != nil {
		// Record may have published despite returning an error. No service or asset
		// effect is authorized until a later durable phase has been confirmed.
		return manualResult(tx, "record_prepared"), err
	}

	current := *cloneTransaction(tx)
	if err := advance(opCtx, d, &current, phaseQuiescing, ""); err != nil {
		return rollbackAfterFailure(ctx, d, current, "record_quiescing", err)
	}
	if err := d.Quiesce(opCtx, cloneValue(current)); err != nil {
		return rollbackAfterFailure(ctx, d, current, "quiesce", err)
	}
	if err := advance(opCtx, d, &current, phaseStopped, ""); err != nil {
		return rollbackAfterFailure(ctx, d, current, "record_stopped", err)
	}
	if err := advance(opCtx, d, &current, phaseReplacing, ""); err != nil {
		return rollbackAfterFailure(ctx, d, current, "record_replacing", err)
	}
	if err := d.Replace(opCtx, cloneValue(current)); err != nil {
		return rollbackAfterFailure(ctx, d, current, "replace", err)
	}
	if err := d.Publish(opCtx, cloneValue(current)); err != nil {
		return rollbackAfterFailure(ctx, d, current, "publish", err)
	}
	if err := advance(opCtx, d, &current, phasePublished, ""); err != nil {
		return rollbackAfterFailure(ctx, d, current, "record_published", err)
	}
	if err := advance(opCtx, d, &current, phaseVerifying, ""); err != nil {
		return rollbackAfterFailure(ctx, d, current, "record_verifying", err)
	}
	if err := d.Verify(opCtx, cloneValue(current)); err != nil {
		return rollbackAfterFailure(ctx, d, current, "verify", err)
	}
	if err := advance(opCtx, d, &current, phaseCommitted, ""); err != nil {
		return rollbackAfterFailure(ctx, d, current, "record_committed", err)
	}
	result := committedResult(current)
	if err := d.Finish(opCtx, cloneValue(current)); err != nil {
		result.FailedStage = "finish"
		return result, fail("finish", err)
	}
	return result, nil
}

func settlePrior(ctx context.Context, d driver) (Result, bool, error) {
	prior, err := d.Load(ctx)
	if err != nil {
		return Result{Outcome: OutcomeManualRepair, FailedStage: "recover"}, true, fail("recover", err)
	}
	if prior == nil {
		return Result{}, false, nil
	}
	tx := cloneTransaction(prior)
	if err := validateTransaction(tx, tx.Mode, tx.Phase); err != nil {
		return manualResult(tx, "recover"), true, fail("recover", err)
	}
	if tx.Phase != phaseCommitted && tx.Phase != phaseRolledBack {
		return manualResult(tx, "recover"), true, fail("recover", errors.New("installer recovery is required"))
	}
	if err := d.Finish(ctx, cloneValue(*tx)); err != nil {
		result := terminalResult(*tx)
		result.FailedStage = "finish"
		return result, true, fail("finish", err)
	}
	return terminalResult(*tx), true, nil
}

func recoverWith(ctx context.Context, d driver) (Result, error) {
	if ctx == nil || d == nil {
		return Result{Outcome: OutcomeManualRepair, FailedStage: "recover"}, fail("recover", errors.New("invalid installer recovery invocation"))
	}
	opCtx, cancel := context.WithTimeout(ctx, operationBudget)
	defer cancel()
	if err := opCtx.Err(); err != nil {
		return Result{Outcome: OutcomeManualRepair, FailedStage: "recover"}, fail("recover", err)
	}
	tx, err := d.Load(opCtx)
	if err != nil {
		return Result{Outcome: OutcomeManualRepair, FailedStage: "recover"}, fail("recover", err)
	}
	if tx == nil {
		return Result{Outcome: OutcomeUnchanged}, nil
	}
	current := *cloneTransaction(tx)
	if err := validateTransaction(&current, current.Mode, current.Phase); err != nil {
		return manualResult(&current, "recover"), fail("recover", err)
	}
	if current.Phase == phaseCommitted || current.Phase == phaseRolledBack {
		if err := d.Finish(opCtx, cloneValue(current)); err != nil {
			result := terminalResult(current)
			result.FailedStage = "finish"
			return result, fail("finish", err)
		}
		return terminalResult(current), nil
	}
	return recoverRollback(opCtx, d, current)
}

func recoverRollback(ctx context.Context, d driver, current transaction) (Result, error) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackBudget)
	defer cancel()
	if current.Phase != phaseRollingBack {
		if err := advance(cleanup, d, &current, phaseRollingBack, "recover"); err != nil {
			return manualResult(&current, "record_rollback"), err
		}
	}
	if err := d.Rollback(cleanup, cloneValue(current)); err != nil {
		return recordManualRepair(cleanup, d, current, "rollback", err)
	}
	if err := advance(cleanup, d, &current, phaseRolledBack, "recover"); err != nil {
		return manualResult(&current, "record_rolled_back"), err
	}
	result := terminalResult(current)
	if err := d.Finish(cleanup, cloneValue(current)); err != nil {
		result.FailedStage = "finish"
		return result, fail("finish", err)
	}
	return result, nil
}

func rollbackAfterFailure(parent context.Context, d driver, current transaction, originalStage string, original error) (Result, error) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(parent), rollbackBudget)
	defer cancel()
	if err := advance(cleanup, d, &current, phaseRollingBack, originalStage); err != nil {
		return manualResult(&current, "record_rollback"), fail("record_rollback", combineCauses(original, err)...)
	}
	if err := d.Rollback(cleanup, cloneValue(current)); err != nil {
		return recordManualRepair(cleanup, d, current, originalStage, original, err)
	}
	if err := advance(cleanup, d, &current, phaseRolledBack, originalStage); err != nil {
		return manualResult(&current, "record_rolled_back"), fail("record_rolled_back", combineCauses(original, err)...)
	}
	result := terminalResult(current)
	result.FailedStage = originalStage
	if err := d.Finish(cleanup, cloneValue(current)); err != nil {
		result.FailedStage = "finish"
		return result, fail("finish", original, err)
	}
	return result, fail(originalStage, original)
}

func recordManualRepair(ctx context.Context, d driver, current transaction, failedStage string, causes ...error) (Result, error) {
	manual := cloneTransaction(&current)
	manual.Phase = phaseManualRepair
	manual.FailedStage = failedStage
	if err := d.Record(ctx, cloneTransaction(&current), cloneValue(*manual)); err != nil {
		causes = append(causes, err)
		return manualResult(manual, "record_manual_repair"), fail("record_manual_repair", causes...)
	}
	return manualResult(manual, failedStage), fail(failedStage, causes...)
}

func advance(ctx context.Context, d driver, current *transaction, nextPhase phase, failedStage string) error {
	next := cloneTransaction(current)
	next.Phase = nextPhase
	next.FailedStage = failedStage
	stage := recordStage(nextPhase)
	if err := recordNext(ctx, d, current, next, nextPhase, failedStage); err != nil {
		return fail(stage, err)
	}
	*current = *next
	return nil
}

func recordNext(ctx context.Context, d driver, previous *transaction, next *transaction, nextPhase phase, failedStage string) error {
	if next == nil {
		return fail(recordStage(nextPhase), errors.New("missing next transaction"))
	}
	copy := cloneTransaction(next)
	copy.Phase = nextPhase
	copy.FailedStage = failedStage
	if err := d.Record(ctx, cloneTransaction(previous), cloneValue(*copy)); err != nil {
		return fail(recordStage(nextPhase), err)
	}
	return nil
}

func recordStage(p phase) string {
	switch p {
	case phasePrepared:
		return "record_prepared"
	case phaseQuiescing:
		return "record_quiescing"
	case phaseStopped:
		return "record_stopped"
	case phaseReplacing:
		return "record_replacing"
	case phasePublished:
		return "record_published"
	case phaseVerifying:
		return "record_verifying"
	case phaseCommitted:
		return "record_committed"
	case phaseRollingBack:
		return "record_rollback"
	case phaseRolledBack:
		return "record_rolled_back"
	case phaseManualRepair:
		return "record_manual_repair"
	default:
		return "recover"
	}
}

func validateApplyOptions(options ApplyOptions) error {
	if !cleanAbsolute(options.Prepared) || !cleanAbsolute(options.TrustFile) {
		return errors.New("prepared and trust paths must be clean absolute paths")
	}
	if _, err := selfupdate.ParseStable(options.Version); err != nil {
		return errors.New("version must be an exact stable release")
	}
	return nil
}

func cleanAbsolute(value string) bool {
	return value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value
}

func validUnchanged(result Result, operation mode, requestedVersion string) bool {
	if result.Outcome != OutcomeUnchanged || result.OperationID != "" || result.FailedStage != "" {
		return false
	}
	if operation == modeApply {
		return result.Image != nil && validImage(*result.Image) && result.Image.Version == requestedVersion
	}
	return result.Image == nil
}

func validateTransaction(tx *transaction, expectedMode mode, expectedPhase phase) error {
	if tx == nil || tx.Format != 1 || !lowerHex(tx.ID, 64) || !lowerHex(tx.IntentSHA256, 64) || tx.Mode != expectedMode || !validPhase(tx.Phase) || tx.Phase != expectedPhase {
		return errors.New("installer transaction header is invalid")
	}
	if tx.FailedStage != "" && !coordinatorStages[tx.FailedStage] {
		return errors.New("installer transaction stage is invalid")
	}
	if (tx.Phase == phasePrepared || tx.Phase == phaseQuiescing || tx.Phase == phaseStopped || tx.Phase == phaseReplacing || tx.Phase == phasePublished || tx.Phase == phaseVerifying || tx.Phase == phaseCommitted) && tx.FailedStage != "" {
		return errors.New("installer transaction has an unexpected failure stage")
	}
	if (tx.Phase == phaseRollingBack || tx.Phase == phaseRolledBack || tx.Phase == phaseManualRepair) && tx.FailedStage == "" {
		return errors.New("installer recovery transaction is missing its failure stage")
	}
	if tx.Prior != nil && !validImage(*tx.Prior) || tx.Target != nil && !validImage(*tx.Target) {
		return errors.New("installer transaction image is invalid")
	}
	switch tx.Mode {
	case modeApply:
		if tx.Target == nil || (tx.Prior != nil && tx.Prior.Schema != tx.Target.Schema) {
			return errors.New("installer apply transaction image relation is invalid")
		}
	case modeUninstall:
		if tx.Prior == nil || tx.Target != nil {
			return errors.New("installer uninstall transaction image relation is invalid")
		}
	default:
		return errors.New("installer transaction mode is invalid")
	}
	return nil
}

func validPhase(value phase) bool {
	switch value {
	case phasePrepared, phaseQuiescing, phaseStopped, phaseReplacing, phasePublished, phaseVerifying, phaseCommitted, phaseRollingBack, phaseRolledBack, phaseManualRepair:
		return true
	default:
		return false
	}
}

func validImage(image Image) bool {
	if _, err := selfupdate.ParseStable(image.Version); err != nil || image.Schema < 1 || !lowerHex(image.SHA256, 64) {
		return false
	}
	return true
}

func lowerHex(value string, size int) bool {
	if len(value) != size {
		return false
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded)*2 != size || strings.ToLower(value) != value {
		return false
	}
	for i := range value {
		if !coordinatorHexDigits[value[i]] {
			return false
		}
	}
	return true
}

func committedResult(tx transaction) Result {
	result := Result{Outcome: OutcomeInstalled, OperationID: tx.ID}
	if tx.Mode == modeUninstall {
		result.Outcome = OutcomeUninstalled
	} else if tx.Target != nil {
		result.Image = imagePointer(*tx.Target)
	}
	return result
}

func terminalResult(tx transaction) Result {
	if tx.Phase == phaseCommitted {
		return committedResult(tx)
	}
	result := Result{Outcome: OutcomeRolledBack, OperationID: tx.ID}
	if tx.Prior != nil {
		result.Image = imagePointer(*tx.Prior)
	}
	return result
}

func manualResult(tx *transaction, stage string) Result {
	result := Result{Outcome: OutcomeManualRepair, FailedStage: stage}
	if tx != nil && lowerHex(tx.ID, 64) {
		result.OperationID = tx.ID
	}
	return result
}

func cloneTransaction(tx *transaction) *transaction {
	if tx == nil {
		return nil
	}
	copy := *tx
	if tx.Prior != nil {
		prior := *tx.Prior
		copy.Prior = &prior
	}
	if tx.Target != nil {
		target := *tx.Target
		copy.Target = &target
	}
	return &copy
}

func cloneValue(tx transaction) transaction { return *cloneTransaction(&tx) }

func cloneResult(result Result) Result {
	copy := result
	if result.Image != nil {
		image := *result.Image
		copy.Image = &image
	}
	return copy
}

func combineCauses(first error, rest error) []error {
	causes := make([]error, 0, 2)
	if first != nil {
		causes = append(causes, first)
	}
	if rest != nil {
		causes = append(causes, rest)
	}
	return causes
}

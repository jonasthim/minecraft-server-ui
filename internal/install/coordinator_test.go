package install

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

var coordinatorTestImage = Image{Version: "v1.2.3", Schema: 42, SHA256: strings.Repeat("a", 64)}
var coordinatorTestPrior = Image{Version: "v1.2.2", Schema: 42, SHA256: strings.Repeat("b", 64)}

type coordinatorContextKey struct{}

type coordinatorFixture struct {
	journal                *transaction
	prepared               *transaction
	unchanged              *Result
	prepareErr             error
	failRecord             phase
	failRecordAfterPublish phase
	recordErr              error
	failOperation          string
	operationErr           error
	events                 []string
	rollbackCalls          int
	finishCalls            int
	rollbackValue          any
	rollbackDone           bool
	rollbackDeadline       time.Time
	mutateArgs             bool
	finishErr              error
}

func (f *coordinatorFixture) Prepare(_ context.Context, m mode, _ ApplyOptions) (*transaction, *Result, error) {
	f.events = append(f.events, "prepare")
	if f.prepareErr != nil {
		return nil, nil, f.prepareErr
	}
	if f.unchanged != nil {
		copy := cloneResult(*f.unchanged)
		return nil, &copy, nil
	}
	tx := cloneTransaction(f.prepared)
	tx.Mode = m
	return tx, nil, nil
}

func (f *coordinatorFixture) Load(context.Context) (*transaction, error) {
	f.events = append(f.events, "load")
	return cloneTransaction(f.journal), nil
}

func (f *coordinatorFixture) Record(_ context.Context, previous *transaction, next transaction) error {
	f.events = append(f.events, "record:"+string(next.Phase))
	if f.failRecord == next.Phase {
		return f.recordFailure()
	}
	if !reflect.DeepEqual(previous, f.journal) {
		return errors.New("CAS previous transaction mismatch")
	}
	f.journal = cloneTransaction(&next)
	if f.failRecordAfterPublish == next.Phase {
		return f.recordFailure()
	}
	return nil
}

func (f *coordinatorFixture) recordFailure() error {
	if f.recordErr != nil {
		return f.recordErr
	}
	return errors.New("private journal path leaked by fake")
}

func (f *coordinatorFixture) Quiesce(_ context.Context, tx transaction) error {
	f.events = append(f.events, "quiesce")
	f.maybeMutate(&tx)
	return f.operationFailure("quiesce")
}

func (f *coordinatorFixture) Replace(_ context.Context, tx transaction) error {
	f.events = append(f.events, "replace")
	f.maybeMutate(&tx)
	return f.operationFailure("replace")
}

func (f *coordinatorFixture) Publish(_ context.Context, tx transaction) error {
	f.events = append(f.events, "publish")
	f.maybeMutate(&tx)
	return f.operationFailure("publish")
}

func (f *coordinatorFixture) Verify(_ context.Context, tx transaction) error {
	f.events = append(f.events, "verify")
	f.maybeMutate(&tx)
	return f.operationFailure("verify")
}

func (f *coordinatorFixture) Rollback(ctx context.Context, tx transaction) error {
	f.events = append(f.events, "rollback")
	f.rollbackCalls++
	f.rollbackDone = ctx.Err() == nil
	f.rollbackValue = ctx.Value(coordinatorContextKey{})
	f.rollbackDeadline, _ = ctx.Deadline()
	f.maybeMutate(&tx)
	return f.operationFailure("rollback")
}

func (f *coordinatorFixture) Finish(_ context.Context, tx transaction) error {
	f.events = append(f.events, "finish")
	f.finishCalls++
	f.maybeMutate(&tx)
	if f.finishErr != nil {
		return f.finishErr
	}
	return nil
}

func (f *coordinatorFixture) operationFailure(stage string) error {
	if f.failOperation == stage {
		if f.operationErr != nil {
			return f.operationErr
		}
		return errors.New("private native path leaked by fake")
	}
	return nil
}

func (f *coordinatorFixture) maybeMutate(tx *transaction) {
	if !f.mutateArgs {
		return
	}
	tx.ID = strings.Repeat("f", 64)
	tx.IntentSHA256 = strings.Repeat("e", 64)
	if tx.Prior != nil {
		tx.Prior.SHA256 = strings.Repeat("d", 64)
	}
	if tx.Target != nil {
		tx.Target.SHA256 = strings.Repeat("c", 64)
	}
}

func preparedFixture(m mode) *transaction {
	tx := &transaction{
		Format:       1,
		ID:           strings.Repeat("1", 64),
		Mode:         m,
		Phase:        phasePrepared,
		IntentSHA256: strings.Repeat("2", 64),
	}
	if m == modeApply {
		prior := coordinatorTestPrior
		target := coordinatorTestImage
		tx.Prior, tx.Target = &prior, &target
	} else {
		prior := coordinatorTestImage
		tx.Prior = &prior
	}
	return tx
}

func validApplyOptions() ApplyOptions {
	return ApplyOptions{Prepared: "/tmp/prepared", TrustFile: "/tmp/trust.json", Version: "v1.2.3"}
}

func TestApplyRecordsEachPhaseBeforeItsEffect(t *testing.T) {
	f := &coordinatorFixture{prepared: preparedFixture(modeApply)}
	got, err := applyWith(context.Background(), f, validApplyOptions())
	if err != nil {
		t.Fatalf("applyWith: %v", err)
	}
	if got.Outcome != OutcomeInstalled || got.OperationID != strings.Repeat("1", 64) || got.Image == nil || *got.Image != coordinatorTestImage {
		t.Fatalf("result=%+v", got)
	}
	want := []string{
		"load", "prepare", "record:prepared", "record:quiescing", "quiesce",
		"record:stopped", "record:replacing", "replace", "publish",
		"record:published", "record:verifying", "verify", "record:committed", "finish",
	}
	if !reflect.DeepEqual(f.events, want) {
		t.Fatalf("events=%v, want %v", f.events, want)
	}
}

func TestApplyUnchangedAndInvalidOptionsHaveNoNativeEffects(t *testing.T) {
	f := &coordinatorFixture{unchanged: &Result{Outcome: OutcomeUnchanged, Image: imagePointer(coordinatorTestImage)}}
	got, err := applyWith(context.Background(), f, validApplyOptions())
	if err != nil || got.Outcome != OutcomeUnchanged {
		t.Fatalf("unchanged: result=%+v err=%v", got, err)
	}
	if want := []string{"load", "prepare"}; !reflect.DeepEqual(f.events, want) {
		t.Fatalf("events=%v, want %v", f.events, want)
	}

	f = &coordinatorFixture{unchanged: &Result{Outcome: OutcomeUnchanged, Image: imagePointer(coordinatorTestPrior)}}
	got, err = applyWith(context.Background(), f, validApplyOptions())
	if err == nil || got.Outcome != OutcomeManualRepair || got.FailedStage != "prepare" {
		t.Fatalf("unchanged result with a different image was accepted: result=%+v err=%v", got, err)
	}

	f = &coordinatorFixture{}
	bad := validApplyOptions()
	bad.Prepared = "relative/prepared"
	_, err = applyWith(context.Background(), f, bad)
	if err == nil {
		t.Fatal("relative prepared path accepted")
	}
	if got := f.events; len(got) != 0 {
		t.Fatalf("invalid options reached driver: %v", got)
	}
}

func TestApplyUncertainPreparedRecordDoesNotQuiesceOrRollback(t *testing.T) {
	f := &coordinatorFixture{prepared: preparedFixture(modeApply), failRecord: phasePrepared}
	got, err := applyWith(context.Background(), f, validApplyOptions())
	if err == nil || got.Outcome != OutcomeManualRepair || got.FailedStage != "record_prepared" || got.OperationID != strings.Repeat("1", 64) {
		t.Fatalf("result=%+v err=%v", got, err)
	}
	if strings.Contains(err.Error(), "/tmp") || strings.Contains(err.Error(), "private journal") {
		t.Fatalf("raw driver error leaked: %v", err)
	}
	if f.rollbackCalls != 0 || containsEvent(f.events, "quiesce") || containsEvent(f.events, "replace") {
		t.Fatalf("uncertain first journal caused effects: events=%v rollback=%d", f.events, f.rollbackCalls)
	}
	if !errors.Is(err, f.operationErr) && !strings.Contains(err.Error(), "record_prepared") {
		t.Fatalf("authored stage missing: %v", err)
	}
}

func TestApplyCancellationUsesIndependentRollbackContextAndReturnsFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), coordinatorContextKey{}, "trace"))
	f := &coordinatorFixture{prepared: preparedFixture(modeApply), failOperation: "quiesce", operationErr: context.Canceled}
	// Cancel the caller after the coordinator enters the native quiesce call.
	// The fixture's operation hook is represented by a driver wrapper below.
	wrapped := cancelAfterQuiesce{coordinatorFixture: f, cancel: cancel}
	got, err := applyWith(ctx, &wrapped, validApplyOptions())
	if err == nil || !errors.Is(err, context.Canceled) || got.Outcome != OutcomeRolledBack || got.FailedStage != "quiesce" {
		t.Fatalf("result=%+v err=%v", got, err)
	}
	if f.rollbackCalls != 1 || !f.rollbackDone || f.rollbackValue != "trace" {
		t.Fatalf("rollback did not run once on detached value-preserving context: calls=%d done=%v value=%v", f.rollbackCalls, f.rollbackDone, f.rollbackValue)
	}
	if remaining := time.Until(f.rollbackDeadline); remaining < 4*time.Minute || remaining > rollbackBudget {
		t.Fatalf("rollback deadline is not independently bounded to five minutes: remaining=%s", remaining)
	}
	if !containsEvent(f.events, "record:rolling_back") || !containsEvent(f.events, "record:rolled_back") {
		t.Fatalf("rollback intent/terminal phase missing: %v", f.events)
	}
}

type cancelAfterQuiesce struct {
	*coordinatorFixture
	cancel context.CancelFunc
}

func (d *cancelAfterQuiesce) Quiesce(ctx context.Context, tx transaction) error {
	d.events = append(d.events, "quiesce")
	d.cancel()
	return context.Canceled
}

func TestCommittedFinishFailureIsReportedWithoutRollback(t *testing.T) {
	f := &coordinatorFixture{prepared: preparedFixture(modeApply), finishErr: errors.New("native cleanup failed")}
	got, err := applyWith(context.Background(), f, validApplyOptions())
	if err == nil || got.Outcome != OutcomeInstalled || got.FailedStage != "finish" || got.Image == nil || *got.Image != coordinatorTestImage {
		t.Fatalf("result=%+v err=%v", got, err)
	}
	if f.rollbackCalls != 0 || f.journal == nil || f.journal.Phase != phaseCommitted {
		t.Fatalf("committed cleanup failure rolled back: calls=%d journal=%+v", f.rollbackCalls, f.journal)
	}
}

func TestCoordinatorDefensivelyCopiesDriverTransactions(t *testing.T) {
	f := &coordinatorFixture{prepared: preparedFixture(modeApply), mutateArgs: true}
	got, err := applyWith(context.Background(), f, validApplyOptions())
	if err != nil {
		t.Fatalf("applyWith: %v", err)
	}
	if got.Outcome != OutcomeInstalled || got.OperationID != strings.Repeat("1", 64) || got.Image == nil || *got.Image != coordinatorTestImage {
		t.Fatalf("driver mutation escaped into result: %+v", got)
	}
	if f.journal == nil || f.journal.ID != strings.Repeat("1", 64) || f.journal.Target == nil || *f.journal.Target != coordinatorTestImage || f.journal.Phase != phaseCommitted {
		t.Fatalf("driver mutation escaped into persisted coordinator state: %+v", f.journal)
	}
}

func TestApplyEffectFailuresRollbackOnceAndKeepErrorsRedacted(t *testing.T) {
	secret := errors.New("raw private path /var/lib/secret")
	for _, stage := range []string{"quiesce", "replace", "publish", "verify"} {
		t.Run(stage, func(t *testing.T) {
			f := &coordinatorFixture{prepared: preparedFixture(modeApply), failOperation: stage, operationErr: secret}
			got, err := applyWith(context.Background(), f, validApplyOptions())
			if err == nil || !errors.Is(err, secret) || got.Outcome != OutcomeRolledBack || got.FailedStage != stage {
				t.Fatalf("result=%+v err=%v", got, err)
			}
			if strings.Contains(err.Error(), "/var/lib/secret") || f.rollbackCalls != 1 || f.journal == nil || f.journal.Phase != phaseRolledBack {
				t.Fatalf("failure was not redacted and rolled back once: err=%v calls=%d journal=%+v", err, f.rollbackCalls, f.journal)
			}
		})
	}
}

func TestApplyRollbackFailureLeavesManualRepairAndBothCauses(t *testing.T) {
	original := errors.New("original effect failure")
	rollback := errors.New("rollback private path failure")
	f := &coordinatorFixture{prepared: preparedFixture(modeApply), failOperation: "replace", operationErr: original}
	f.failOperation = "replace"
	wrapped := failRollbackAfterReplace{coordinatorFixture: f, rollbackErr: rollback}
	got, err := applyWith(context.Background(), &wrapped, validApplyOptions())
	if err == nil || !errors.Is(err, original) || !errors.Is(err, rollback) || got.Outcome != OutcomeManualRepair || got.FailedStage != "replace" {
		t.Fatalf("result=%+v err=%v", got, err)
	}
	if strings.Contains(err.Error(), "private path") || f.journal == nil || f.journal.Phase != phaseManualRepair || f.rollbackCalls != 1 || containsEvent(f.events, "finish") {
		t.Fatalf("unsafe rollback outcome: err=%v journal=%+v calls=%d events=%v", err, f.journal, f.rollbackCalls, f.events)
	}
}

type failRollbackAfterReplace struct {
	*coordinatorFixture
	rollbackErr error
}

func (d *failRollbackAfterReplace) Rollback(ctx context.Context, tx transaction) error {
	d.events = append(d.events, "rollback")
	d.rollbackCalls++
	d.rollbackDone = ctx.Err() == nil
	return d.rollbackErr
}

func TestApplyRecordFailureBeforeEffectKeepsDurableOrder(t *testing.T) {
	f := &coordinatorFixture{prepared: preparedFixture(modeApply), failRecord: phaseReplacing}
	got, err := applyWith(context.Background(), f, validApplyOptions())
	if err == nil || got.Outcome != OutcomeRolledBack || got.FailedStage != "record_replacing" {
		t.Fatalf("result=%+v err=%v", got, err)
	}
	if containsEvent(f.events, "replace") || f.rollbackCalls != 1 || f.journal.Phase != phaseRolledBack {
		t.Fatalf("replace ran without its durable phase: events=%v journal=%+v", f.events, f.journal)
	}
}

func TestApplyRollbackJournalCASFailuresPreserveUncertainState(t *testing.T) {
	original := errors.New("original quiesce failure")
	recordErr := errors.New("private journal storage detail")
	cases := []struct {
		name                 string
		phase                phase
		publishBeforeFailure bool
		wantJournalPhase     phase
		wantRollbackCalls    int
	}{
		{
			name:              "rolling_back CAS fails before publication",
			phase:             phaseRollingBack,
			wantJournalPhase:  phaseQuiescing,
			wantRollbackCalls: 0,
		},
		{
			name:                 "rolling_back CAS fails after uncertain publication",
			phase:                phaseRollingBack,
			publishBeforeFailure: true,
			wantJournalPhase:     phaseRollingBack,
			wantRollbackCalls:    0,
		},
		{
			name:              "rolled_back CAS fails before publication",
			phase:             phaseRolledBack,
			wantJournalPhase:  phaseRollingBack,
			wantRollbackCalls: 1,
		},
		{
			name:                 "rolled_back CAS fails after uncertain publication",
			phase:                phaseRolledBack,
			publishBeforeFailure: true,
			wantJournalPhase:     phaseRolledBack,
			wantRollbackCalls:    1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &coordinatorFixture{
				prepared:      preparedFixture(modeApply),
				failOperation: "quiesce",
				operationErr:  original,
				recordErr:     recordErr,
			}
			if tc.publishBeforeFailure {
				f.failRecordAfterPublish = tc.phase
			} else {
				f.failRecord = tc.phase
			}

			got, err := applyWith(context.Background(), f, validApplyOptions())
			stage := "record_rollback"
			if tc.phase == phaseRolledBack {
				stage = "record_rolled_back"
			}
			if err == nil || got.Outcome != OutcomeManualRepair || got.FailedStage != stage || got.OperationID != strings.Repeat("1", 64) || got.Image != nil {
				t.Fatalf("result=%+v err=%v, want manual repair at %s", got, err, stage)
			}
			if !errors.Is(err, original) || !errors.Is(err, recordErr) {
				t.Fatalf("error lost original or journal cause: %v", err)
			}
			if strings.Contains(err.Error(), "private journal") || strings.Contains(err.Error(), "quiesce failure") {
				t.Fatalf("private cause text leaked: %v", err)
			}
			if f.journal == nil || f.journal.Phase != tc.wantJournalPhase {
				t.Fatalf("journal phase=%v, want %v", f.journal, tc.wantJournalPhase)
			}
			if f.rollbackCalls != tc.wantRollbackCalls {
				t.Fatalf("rollback calls=%d, want %d; events=%v", f.rollbackCalls, tc.wantRollbackCalls, f.events)
			}
			if f.finishCalls != 0 || containsEvent(f.events, "finish") {
				t.Fatalf("Finish ran after an uncertain rollback record; events=%v", f.events)
			}
			if tc.phase == phaseRollingBack && containsEvent(f.events, "rollback") {
				t.Fatalf("rollback ran before rolling_back was durably confirmed; events=%v", f.events)
			}
		})
	}
}

func TestApplyRejectsTargetVersionMismatchAndUnknownJournalPhase(t *testing.T) {
	f := &coordinatorFixture{prepared: preparedFixture(modeApply)}
	f.prepared.Target.Version = "v1.2.4"
	got, err := applyWith(context.Background(), f, validApplyOptions())
	if err == nil || got.Outcome != OutcomeManualRepair || containsEvent(f.events, "record:prepared") {
		t.Fatalf("mismatched requested image reached journal: result=%+v events=%v err=%v", got, f.events, err)
	}

	tx := preparedFixture(modeApply)
	tx.Phase = "future_phase"
	f = &coordinatorFixture{journal: tx}
	got, err = recoverWith(context.Background(), f)
	if err == nil || got.Outcome != OutcomeManualRepair || f.rollbackCalls != 0 || len(f.events) != 1 {
		t.Fatalf("unknown phase was interpreted: result=%+v events=%v err=%v", got, f.events, err)
	}
}

func TestRecoverRollsBackInterruptedProgressOnceAndFinalizes(t *testing.T) {
	tx := preparedFixture(modeApply)
	tx.Phase = phaseReplacing
	f := &coordinatorFixture{journal: tx}
	got, err := recoverWith(context.Background(), f)
	if err != nil || got.Outcome != OutcomeRolledBack || got.Image == nil || *got.Image != coordinatorTestPrior {
		t.Fatalf("recover: result=%+v err=%v", got, err)
	}
	if f.rollbackCalls != 1 || f.journal.Phase != phaseRolledBack || !containsEvent(f.events, "record:rolling_back") || !containsEvent(f.events, "finish") {
		t.Fatalf("incomplete recovery sequence: calls=%d journal=%+v events=%v", f.rollbackCalls, f.journal, f.events)
	}
}

func TestRecoverCommittedJournalOnlyFinalizes(t *testing.T) {
	tx := preparedFixture(modeApply)
	tx.Phase = phaseCommitted
	f := &coordinatorFixture{journal: tx}
	got, err := recoverWith(context.Background(), f)
	if err != nil || got.Outcome != OutcomeInstalled || got.Image == nil || *got.Image != coordinatorTestImage {
		t.Fatalf("recover: result=%+v err=%v", got, err)
	}
	if f.rollbackCalls != 0 || !containsEvent(f.events, "finish") {
		t.Fatalf("committed transaction was rolled back or not finalized: %v", f.events)
	}
}

func TestRecoverMalformedJournalDoesNotPrepareOrMutate(t *testing.T) {
	tx := preparedFixture(modeApply)
	tx.ID = "not-an-id"
	f := &coordinatorFixture{journal: tx}
	got, err := recoverWith(context.Background(), f)
	if err == nil || got.Outcome != OutcomeManualRepair || got.Image != nil {
		t.Fatalf("result=%+v err=%v", got, err)
	}
	if f.rollbackCalls != 0 || len(f.events) != 1 || f.events[0] != "load" {
		t.Fatalf("malformed journal caused effects: %v", f.events)
	}
}

func TestUninstallHappyPathPreservesOutcomeShape(t *testing.T) {
	tx := preparedFixture(modeUninstall)
	f := &coordinatorFixture{prepared: tx}
	got, err := uninstallWith(context.Background(), f)
	if err != nil || got.Outcome != OutcomeUninstalled || got.OperationID != tx.ID || got.Image != nil {
		t.Fatalf("uninstall: result=%+v err=%v", got, err)
	}
}


func containsEvent(events []string, want string) bool {
	for _, event := range events {
		if event == want {
			return true
		}
	}
	return false
}

// Ensure the detached cleanup remains bounded even when caller context has no
// deadline; a long timeout is intentionally inspected without waiting for it.
func TestRollbackBudgetConstantIsFiveMinutes(t *testing.T) {
	if rollbackBudget != 5*time.Minute {
		t.Fatalf("rollbackBudget=%s", rollbackBudget)
	}
}

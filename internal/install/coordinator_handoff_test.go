package install

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestPreparedWorkerContinuesWithoutRepeatingPreparation(t *testing.T) {
	for _, m := range []mode{modeApply, modeUninstall} {
		t.Run(string(m), func(t *testing.T) {
			tx := preparedFixture(m)
			f := &coordinatorFixture{journal: tx}
			result, err := continuePreparedWith(context.Background(), f, tx.ID)
			if err != nil || result.OperationID != tx.ID || result.Outcome != committedResult(*tx).Outcome {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			want := []string{"load", "record:quiescing", "quiesce", "record:stopped", "record:replacing", "replace", "publish", "record:published", "record:verifying", "verify", "record:committed", "finish"}
			if !reflect.DeepEqual(f.events, want) {
				t.Fatalf("events=%v", f.events)
			}
		})
	}
}

func TestPreparedWorkerRefusesWrongMissingOrAlreadyStartedTransaction(t *testing.T) {
	for _, stage := range []phase{"", phasePrepared, phaseQuiescing, phaseStopped, phaseReplacing, phasePublished, phaseVerifying, phaseCommitted, phaseRollingBack, phaseRolledBack, phaseManualRepair} {
		t.Run(string(stage), func(t *testing.T) {
			tx := preparedFixture(modeApply)
			if stage == "" {
				tx = nil
			} else {
				tx.Phase = stage
			}
			f := &coordinatorFixture{journal: tx}
			id := strings.Repeat("1", 64)
			if stage == phasePrepared {
				id = strings.Repeat("a", 64)
			}
			result, err := continuePreparedWith(context.Background(), f, id)
			if err == nil || result.Outcome != OutcomeManualRepair || !reflect.DeepEqual(f.events, []string{"load"}) {
				t.Fatalf("result=%+v error=%v events=%v", result, err, f.events)
			}
		})
	}
}

func TestPreparedWorkerFailureRetainsIndependentRollback(t *testing.T) {
	f := &coordinatorFixture{journal: preparedFixture(modeApply), failOperation: "replace", operationErr: context.Canceled}
	result, err := continuePreparedWith(context.Background(), f, f.journal.ID)
	if err == nil || result.Outcome != OutcomeRolledBack || f.rollbackCalls != 1 || !f.rollbackDone {
		t.Fatalf("result=%+v error=%v rollback=%d live=%v", result, err, f.rollbackCalls, f.rollbackDone)
	}
}

func TestPreparedWorkerInvalidAuthorityInputsDoNotReachDriver(t *testing.T) {
	for _, id := range []string{"", "../escape", strings.Repeat("A", 64)} {
		f := &coordinatorFixture{journal: preparedFixture(modeApply)}
		_, err := continuePreparedWith(context.Background(), f, id)
		if err == nil || len(f.events) != 0 {
			t.Fatalf("invalid ID reached driver: error=%v events=%v", err, f.events)
		}
	}
	if _, err := continuePreparedWith(nil, &coordinatorFixture{}, strings.Repeat("1", 64)); err == nil {
		t.Fatal("nil context accepted")
	}
	if _, err := continuePreparedWith(context.Background(), nil, strings.Repeat("1", 64)); err == nil {
		t.Fatal("nil driver accepted")
	}
}

package launcher

import (
	"errors"
	"testing"
)

func TestProcessIdentityFailureStageReturnsEmptyForUnclassifiedErrors(t *testing.T) {
	for _, err := range []error{nil, (*processIdentityStageError)(nil), errors.New("private native detail")} {
		if got := ProcessIdentityFailureStage(err); got != "" {
			t.Fatalf("unclassified process identity stage = %q, want empty", got)
		}
	}
}

func TestProcessIdentityFailureStageIsFiniteAndPreservesCause(t *testing.T) {
	cause := errors.New("private native detail")
	for stage, want := range map[processIdentityStage]string{
		processIdentityOpenProcess:          "open_process",
		processIdentityWaitProcess:          "wait_process",
		processIdentityGetProcessTimes:      "get_process_times",
		processIdentityQueryImage:           "query_image",
		processIdentityBootIdentity:         "boot_identity",
		processIdentityProcStat:             "proc_stat",
		processIdentityProcExecutable:       "proc_executable",
		processIdentityProcWorkingDirectory: "proc_working_directory",
		processIdentityBrokerPath:           "broker_path",
		processIdentityBrokerInvoke:         "broker_invoke",
		processIdentityBrokerDecode:         "broker_decode",
		processIdentityBrokerIdentity:       "broker_identity",
		processIdentityProcessMetadata:      "process_metadata",
	} {
		err := processIdentityStageFailure(stage, cause)
		if got := ProcessIdentityFailureStage(err); got != want {
			t.Fatalf("classified process identity stage = %q, want %q", got, want)
		}
		if !errors.Is(err, cause) {
			t.Fatalf("stage %q wrapper did not preserve the original error cause", stage)
		}
	}
	if got := processIdentityStageFailure(processIdentityStage("private detail"), cause); got != cause {
		t.Fatal("unknown stage unexpectedly wrapped an error")
	}
	if got := ProcessIdentityFailureStage(processIdentityStageFailure(processIdentityStage("private detail"), cause)); got != "" {
		t.Fatalf("unknown process identity stage = %q, want empty", got)
	}
}

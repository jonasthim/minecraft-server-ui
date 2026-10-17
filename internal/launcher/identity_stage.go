package launcher

import "errors"

type processIdentityStage string

const (
	processIdentityOpenProcess          processIdentityStage = "open_process"
	processIdentityWaitProcess          processIdentityStage = "wait_process"
	processIdentityGetProcessTimes      processIdentityStage = "get_process_times"
	processIdentityQueryImage           processIdentityStage = "query_image"
	processIdentityBootIdentity         processIdentityStage = "boot_identity"
	processIdentityProcStat             processIdentityStage = "proc_stat"
	processIdentityProcExecutable       processIdentityStage = "proc_executable"
	processIdentityProcWorkingDirectory processIdentityStage = "proc_working_directory"
	processIdentityBrokerPath           processIdentityStage = "broker_path"
	processIdentityBrokerInvoke         processIdentityStage = "broker_invoke"
	processIdentityBrokerDecode         processIdentityStage = "broker_decode"
	processIdentityBrokerIdentity       processIdentityStage = "broker_identity"
	processIdentityProcessMetadata      processIdentityStage = "process_metadata"
)

type processIdentityStageError struct {
	stage processIdentityStage
	cause error
}

func (e *processIdentityStageError) Error() string { return e.cause.Error() }
func (e *processIdentityStageError) Unwrap() error { return e.cause }

func processIdentityStageFailure(stage processIdentityStage, cause error) error {
	if cause == nil {
		return nil
	}
	switch stage {
	case processIdentityOpenProcess, processIdentityWaitProcess, processIdentityGetProcessTimes,
		processIdentityQueryImage, processIdentityBootIdentity, processIdentityProcStat,
		processIdentityProcExecutable, processIdentityProcWorkingDirectory,
		processIdentityBrokerPath, processIdentityBrokerInvoke, processIdentityBrokerDecode,
		processIdentityBrokerIdentity, processIdentityProcessMetadata:
		return &processIdentityStageError{stage: stage, cause: cause}
	default:
		return cause
	}
}

// ProcessIdentityFailureStage returns a fixed process-identity stage when err
// wraps a recognized probe failure. Unknown errors return an empty string;
// callers should never expose the wrapped error text.
func ProcessIdentityFailureStage(err error) string {
	var stageErr *processIdentityStageError
	if !errors.As(err, &stageErr) || stageErr == nil {
		return ""
	}
	switch stageErr.stage {
	case processIdentityOpenProcess, processIdentityWaitProcess, processIdentityGetProcessTimes,
		processIdentityQueryImage, processIdentityBootIdentity, processIdentityProcStat,
		processIdentityProcExecutable, processIdentityProcWorkingDirectory,
		processIdentityBrokerPath, processIdentityBrokerInvoke, processIdentityBrokerDecode,
		processIdentityBrokerIdentity, processIdentityProcessMetadata:
		return string(stageErr.stage)
	default:
		return ""
	}
}

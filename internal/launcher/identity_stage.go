package launcher

import "errors"

type processIdentityStage string

const (
	processIdentityOpenProcess     processIdentityStage = "open_process"
	processIdentityWaitProcess     processIdentityStage = "wait_process"
	processIdentityGetProcessTimes processIdentityStage = "get_process_times"
	processIdentityQueryImage      processIdentityStage = "query_image"
	processIdentityBootIdentity    processIdentityStage = "boot_identity"
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
		processIdentityQueryImage, processIdentityBootIdentity:
		return &processIdentityStageError{stage: stage, cause: cause}
	default:
		return cause
	}
}

// ProcessIdentityFailureStage returns a fixed native process-identity stage
// when err wraps a recognized Windows probe failure. Unknown errors return an
// empty string; callers should never expose the wrapped error text.
func ProcessIdentityFailureStage(err error) string {
	var stageErr *processIdentityStageError
	if !errors.As(err, &stageErr) || stageErr == nil {
		return ""
	}
	switch stageErr.stage {
	case processIdentityOpenProcess, processIdentityWaitProcess, processIdentityGetProcessTimes,
		processIdentityQueryImage, processIdentityBootIdentity:
		return string(stageErr.stage)
	default:
		return ""
	}
}

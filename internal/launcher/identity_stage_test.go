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
	err := processIdentityStageFailure(processIdentityQueryImage, cause)
	if got := ProcessIdentityFailureStage(err); got != "query_image" {
		t.Fatalf("classified process identity stage = %q", got)
	}
	if !errors.Is(err, cause) {
		t.Fatal("stage wrapper did not preserve the original error cause")
	}
	if got := processIdentityStageFailure(processIdentityStage("private detail"), cause); got != cause {
		t.Fatal("unknown stage unexpectedly wrapped an error")
	}
	if got := ProcessIdentityFailureStage(processIdentityStageFailure(processIdentityStage("private detail"), cause)); got != "" {
		t.Fatalf("unknown process identity stage = %q, want empty", got)
	}
}

package domain

import (
	"fmt"
	"testing"
)

func TestErrorVocabulary(t *testing.T) {
	for _, tc := range []struct {
		code   ErrorCode
		status int
	}{{CodeNotFound, 404}, {CodeForbidden, 403}, {CodeValidationFailed, 422}, {CodeConflict, 409}, {CodeUnauthorized, 401}, {CodeInternal, 500}} {
		err := E(tc.code, "message")
		if got := AsError(fmt.Errorf("context: %w", err)); got.HTTPStatus() != tc.status {
			t.Errorf("%s = %d", tc.code, got.HTTPStatus())
		}
	}
	if Validation(nil) != nil {
		t.Fatal("empty validation produced error")
	}
}

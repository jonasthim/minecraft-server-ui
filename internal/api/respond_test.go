package api

import (
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestErrorResponseDoesNotLeakInternalCause(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, fmt.Errorf("secret path password"))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	WriteError(rec, domain.NotFound("instance"))
	if rec.Code != 404 {
		t.Fatal(rec.Code)
	}
}
func TestDecodeJSONRejectsTrailingAndUnknownFields(t *testing.T) {
	for _, body := range []string{`{"name":"a"} {"name":"b"}`, `{"name":"a"}garbage`, `{"unknown":true}`} {
		var dst struct {
			Name string `json:"name"`
		}
		if err := DecodeJSON(httptest.NewRequest("POST", "/", strings.NewReader(body)), &dst); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}

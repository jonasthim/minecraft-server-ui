package auth

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

func TestOIDCSignedLoginAuditsAccountAndRoleTransitions(t *testing.T) {
	provider := newFakeOIDCProvider(t, map[string]any{"groups": []string{"viewers"}, "preferred_username": "audited-user", "client_secret": "claim-secret-must-not-persist", "password": "claim-password-must-not-persist"})
	service, server := newOIDCTestSetup(t, provider, map[string]domain.Role{"viewers": domain.RoleViewer, "operators": domain.RoleOperator}, "deny", true, true)
	for i, groups := range [][]string{{"viewers"}, {"operators"}, {"operators"}, {"unmapped"}} {
		provider.mu.Lock()
		provider.extra["groups"] = groups
		provider.mu.Unlock()
		result := doOIDCLogin(t, server, "")
		if (result.cookie != nil) != (i < 3) {
			t.Fatalf("login %d session result mismatch", i)
		}
	}
	user, err := service.users.GetByUsername(context.Background(), "audited-user")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := db.NewAuditRepo(service.users.DB).List(context.Background(), "", "", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, entry := range entries {
		counts[entry.Action]++
		if entry.UserID == nil || *entry.UserID != user.ID || entry.Username != user.Username || entry.Target != user.Username {
			t.Fatalf("missing verified identity attribution for %s", entry.Action)
		}
		if entry.Details["method"] != "oidc" {
			t.Fatalf("missing OIDC provenance for %s", entry.Action)
		}
		switch entry.Action {
		case "user.create":
			if entry.Details["issuer"] != provider.Server.URL || entry.Details["subject"] != "user-123" || entry.Details["new_role"] != "viewer" || entry.Details["source"] != "oidc_provider" {
				t.Fatalf("missing creation provenance: %+v", entry.Details)
			}
		case "user.role.sync":
			if entry.Details["previous_role"] != "viewer" || entry.Details["new_role"] != "operator" || entry.Details["source"] != "oidc_provider" || entry.Details["issuer"] != provider.Server.URL || entry.Details["subject"] != "user-123" {
				t.Fatalf("missing role transition: %+v", entry.Details)
			}
		}
	}
	if counts["user.create"] != 1 || counts["user.role.sync"] != 1 || counts["auth.login"] != 3 || len(entries) != 5 {
		t.Fatalf("transitions duplicated or missing: %+v", counts)
	}
	encoded, _ := json.Marshal(entries)
	for _, secret := range []string{"claim-secret-must-not-persist", "claim-password-must-not-persist", "fake-access-token", "test-secret", "mcui_"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("credential leaked into audit")
		}
	}
}

func TestServiceAuditorPreservesDetailsAndRedactsCredentials(t *testing.T) {
	service := newTestService(t)
	request := httptest.NewRequest("GET", "/callback", nil)
	request = request.WithContext(WithUser(request.Context(), &domain.User{ID: 42, Username: "verified-actor"}))
	service.auditor.Record(request, "user.role.sync", "instance", "target-user", map[string]any{"previous_role": "viewer", "new_role": "operator", "client_secret": "private", "nested": map[string]any{"authorization": "private", "method": "oidc"}})
	entries, err := db.NewAuditRepo(service.users.DB).List(context.Background(), "", "", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d events", len(entries))
	}
	entry := entries[0]
	if entry.UserID == nil || *entry.UserID != 42 || entry.Username != "verified-actor" || entry.InstanceID != "instance" || entry.Target != "target-user" {
		t.Fatal("audit attribution discarded")
	}
	if entry.Details["previous_role"] != "viewer" || entry.Details["new_role"] != "operator" || entry.Details["client_secret"] != "[redacted]" {
		t.Fatalf("audit details discarded or unredacted: %+v", entry.Details)
	}
	encoded, _ := json.Marshal(entry.Details)
	if strings.Contains(string(encoded), "private") {
		t.Fatal("nested credential leaked")
	}
}

package selfupdate

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// Source-level rights regression runs on Linux too; native SCM qualification
// remains separate. The identity path must never use the control helper.
func TestWindowsServiceIdentityUsesQueryOnlyAccess(t *testing.T) {
	b, err := os.ReadFile("native_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	start := strings.Index(s, "func openSCMQuery(")
	end := strings.Index(s, "func verifiedServicePID(")
	if start < 0 || end < start {
		t.Fatal("missing separate query-only SCM path")
	}
	body := s[start:end]
	for _, want := range []string{"windows.SC_MANAGER_CONNECT", "windows.SERVICE_QUERY_STATUS"} {
		if !strings.Contains(body, want) {
			t.Fatal("query mask missing", want)
		}
	}
	for _, bad := range []string{"mgr.Connect", "ALL_ACCESS"} {
		if strings.Contains(body, bad) {
			t.Fatal("privileged query path", bad)
		}
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), "native_windows.go", b, 0)
	if err != nil {
		t.Fatal(err)
	}
	masks := map[string]string{"OpenSCManager": "SC_MANAGER_CONNECT", "OpenService": "SERVICE_QUERY_STATUS"}
	counts := map[string]int{}
	for _, decl := range parsed.Decls {
		function, ok := decl.(*ast.FuncDecl)
		if !ok || function.Name.Name != "openSCMQuery" {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			name, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			want, ok := masks[name.Sel.Name]
			if !ok {
				return true
			}
			counts[name.Sel.Name]++
			if len(call.Args) != 3 {
				t.Fatal("query access argument contract changed")
			}
			access, ok := call.Args[2].(*ast.SelectorExpr)
			if !ok || access.Sel.Name != want {
				t.Fatal("query access mask broadened", name.Sel.Name)
			}
			return true
		})
	}
	if counts["OpenSCManager"] != 1 || counts["OpenService"] != 1 {
		t.Fatal("missing explicit query rights", counts)
	}
	identity := s[end : strings.Index(s[end:], "func brokerAvailable(")+end]
	if !strings.Contains(identity, "openSCMQuery(name)") || strings.Contains(identity, "openSCMService(") || !strings.Contains(identity, "windows.PROCESS_QUERY_LIMITED_INFORMATION") {
		t.Fatal("identity does not select minimum access")
	}
}

func TestCandidateProbesKeepFixedManagerAccountBoundary(t *testing.T) {
	b, err := os.ReadFile("native_linux.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	start := strings.Index(s, "func (d linuxDriver) ProbeCandidate(")
	end := strings.Index(s[start:], "func (d linuxDriver) LockManager(") + start
	probe := s[start:end]
	for _, want := range []string{"\"/usr/sbin/runuser\"", "\"-u\", \"minecraft-ui\", \"--\"", "\".next\", \"update-capability\"", "15*time.Second"} {
		if !strings.Contains(probe, want) {
			t.Fatal("candidate probe account/command/deadline changed", want)
		}
	}
	if strings.Contains(probe, "update-schema") || strings.Contains(probe, "MINECRAFT_UI_DATA_DIR") {
		t.Fatal("candidate capability acquired DB access")
	}
	b, err = os.ReadFile("native_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	s = string(b)
	for _, want := range []string{"d.probe(ctx, d.paths.Binary+\".next\", expected, true)", "copyVerified(ctx, bin, source, bin, probe, expectedImage)", "configuredSID.String() != accountSID", "capability.json"} {
		if !strings.Contains(s, want) {
			t.Fatal("Windows fixed same-account private-copy capability contract changed", want)
		}
	}
}

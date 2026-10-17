package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNativeReceiptSchemaRequiresNativeExecutionReceipt(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "manager")
	if err := os.WriteFile(binary, []byte("ordinary fixture bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeReceiptSchema(binary, "linux", "minecraft-ui", "v0.0.1"); err == nil {
		t.Fatal("accepted an input without a native execution receipt")
	}
}

func TestProduceRefusesExistingOutputWithoutInspectingInputs(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("fixture producer is native Linux amd64 only")
	}
	root := t.TempDir()
	output := filepath.Join(root, "already-there")
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(output, "keep")
	if err := os.WriteFile(marker, []byte("owned elsewhere"), 0600); err != nil {
		t.Fatal(err)
	}
	err := produce(context.Background(), options{
		source: filepath.Join(root, "missing-source"),
		input:  filepath.Join(root, "missing-input"),
		output: output,
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing output error=%v", err)
	}
	if body, readErr := os.ReadFile(marker); readErr != nil || string(body) != "owned elsewhere" {
		t.Fatalf("existing output changed body=%q err=%v", body, readErr)
	}
}

func TestValidateNativeInputsRefusesIncompleteVersionMatrix(t *testing.T) {
	if err := validateNativeInputs(filepath.Join(t.TempDir(), "empty")); err == nil {
		t.Fatal("accepted an incomplete native input matrix")
	}
}

func TestValidateNativeInputsAcceptsWorkflowWindowsExeLayout(t *testing.T) {
	root := t.TempDir()
	for _, platform := range []string{"linux", "windows"} {
		for _, version := range []string{"v0.0.1", "v0.0.2", "v0.0.3"} {
			for _, item := range []struct {
				name, command string
				schema        int64
			}{{"manager", "minecraft-ui", 18}, {"updater", "minecraft-ui-update", 0}, {"bootstrap", "minecraft-ui-bootstrap", 0}} {
				binaryPath := filepath.Join(root, platform, version, item.name)
				if platform == "windows" {
					binaryPath += ".exe"
				}
				if err := os.MkdirAll(filepath.Dir(binaryPath), 0700); err != nil {
					t.Fatal(err)
				}
				body := []byte(platform + " fixture " + version + " " + item.command)
				if err := os.WriteFile(binaryPath, body, 0600); err != nil {
					t.Fatal(err)
				}
				digest := sha256.Sum256(body)
				receipt, err := json.Marshal(receipt{
					Format: 1, OS: platform, Arch: "amd64", Command: item.command,
					Version: version, BinarySHA256: hex.EncodeToString(digest[:]), Schema: item.schema,
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(binaryPath+".version.json", receipt, 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if err := validateNativeInputs(root); err != nil {
		t.Fatalf("workflow-shaped Linux and Windows native inputs rejected: %v", err)
	}

	missing := inputPath(root, "windows", "v0.0.2", "updater")
	if err := os.Rename(missing, missing+".missing"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(missing+".version.json", missing+".missing.version.json"); err != nil {
		t.Fatal(err)
	}
	// An extensionless Windows file must not satisfy the declared native input.
	wrongPath := filepath.Join(root, "windows", "v0.0.2", "updater")
	if err := os.WriteFile(wrongPath, []byte("wrong path"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wrongPath+".version.json", []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateNativeInputs(root); err == nil {
		t.Fatal("accepted a Windows matrix with a missing .exe input and extensionless substitute")
	}
}

func TestInputPathUsesWindowsExecutableExtension(t *testing.T) {
	if got, want := inputPath("/fixtures", "windows", "v0.0.1", "manager"), filepath.Join("/fixtures", "windows", "v0.0.1", "manager.exe"); got != want {
		t.Fatalf("Windows input path=%q", got)
	}
	if got, want := inputPath("/fixtures", "linux", "v0.0.1", "manager"), filepath.Join("/fixtures", "linux", "v0.0.1", "manager"); got != want {
		t.Fatalf("Linux input path=%q", got)
	}
}

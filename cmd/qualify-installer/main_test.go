package main

import (
	"bytes"
	"context"
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

package providerutil

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOfflineVerifyLaunchRejectsUnpinnedJarAndArgv(t *testing.T) {
	for _, kind := range []string{"base", "memory", "hash", "argv", "external", "manifest", "runtime", "symlink", "noncanonical-memory"} {
		t.Run(kind, func(t *testing.T) {
			installer, plan, java := setup(t, jar(t, false))
			spec, err := installer.Publish(context.Background(), "test", plan, java, nil, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			spec.JavaRuntimeID = "temurin-21-" + runtime.GOOS + "-x64-" + strings.Repeat("1", 64)
			bin := "java"
			if runtime.GOOS == "windows" {
				bin += ".exe"
			}
			spec.Executable = filepath.Join(installer.dataDir, "runtimes", spec.JavaRuntimeID, "bin", bin)
			switch kind {
			case "memory":
				spec.Args = append([]string{"-Xms512M", "-Xmx2048M"}, spec.Args...)
			case "hash":
				if err = os.WriteFile(spec.Args[1], []byte("tampered"), 0600); err != nil {
					t.Fatal(err)
				}
			case "argv":
				spec.Args = append([]string{"-javaagent:/tmp/escape.jar"}, spec.Args...)
			case "external":
				spec.Args[1] = filepath.Join(installer.dataDir, "outside.jar")
			case "manifest":
				path := filepath.Join(filepath.Dir(spec.Args[1]), "manifest.json")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var record manifest
				if err = json.Unmarshal(raw, &record); err != nil {
					t.Fatal(err)
				}
				record.Plan.Selection.LoaderVersion = "233"
				raw, _ = json.Marshal(record)
				if err = os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "runtime":
				spec.JavaRuntimeID = "temurin-17-" + runtime.GOOS + "-x64-" + strings.Repeat("1", 64)
			case "symlink":
				path := filepath.Dir(spec.Args[1])
				if err = os.Rename(path, path+"-old"); err != nil {
					t.Fatal(err)
				}
				if err = os.Symlink(path+"-old", path); err != nil {
					t.Skip(err)
				}
			case "noncanonical-memory":
				spec.Args = append([]string{"-Xms512M", "-Xmx2G"}, spec.Args...)
			}
			err = VerifyLaunch(context.Background(), installer.dataDir, "test", spec)
			valid := kind == "base" || kind == "memory"
			if valid && err != nil {
				t.Fatal(err)
			}
			if !valid && err == nil {
				t.Fatalf("accepted unsafe launch: %s", kind)
			}
		})
	}
}

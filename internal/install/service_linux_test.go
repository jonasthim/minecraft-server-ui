//go:build linux

package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestParseLinuxManagerShowRequiresExactUniqueProperties(t *testing.T) {
	good := "LoadState=loaded\nActiveState=inactive\nSubState=dead\nMainPID=0\nUnitFileState=disabled\n"
	status, err := parseLinuxManagerStatus(good, true)
	if err != nil || status.LoadState != "loaded" || status.UnitFileState != "disabled" {
		t.Fatalf("parse valid manager state: %#v, %v", status, err)
	}
	for name, input := range map[string]string{
		"duplicate": good + "MainPID=0\n",
		"unknown":   strings.Replace(good, "UnitFileState=disabled", "Surprise=x", 1),
		"missing":   strings.Replace(good, "MainPID=0\n", "", 1),
		"malformed": strings.Replace(good, "MainPID=0", "MainPID=-1", 1),
		"overflow":  strings.Replace(good, "MainPID=0", "MainPID=999999999999999999999", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseLinuxManagerStatus(input, true); err == nil {
				t.Fatal("malformed show data accepted")
			}
		})
	}
}

func TestLinuxManagerStatusProofStates(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		stopped bool
		active  bool
	}{
		{"missing", "LoadState=not-found\nActiveState=inactive\nSubState=dead\nMainPID=0\nUnitFileState=\n", true, false},
		{"stopped", "LoadState=loaded\nActiveState=inactive\nSubState=dead\nMainPID=0\nUnitFileState=enabled\n", true, false},
		{"failed", "LoadState=loaded\nActiveState=failed\nSubState=failed\nMainPID=0\nUnitFileState=static\n", true, false},
		{"active", "LoadState=loaded\nActiveState=active\nSubState=running\nMainPID=17\nUnitFileState=indirect\n", false, true},
		{"transitional", "LoadState=loaded\nActiveState=activating\nSubState=start\nMainPID=17\nUnitFileState=enabled\n", false, false},
		{"pid mismatch", "LoadState=loaded\nActiveState=active\nSubState=running\nMainPID=0\nUnitFileState=enabled\n", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, err := parseLinuxManagerStatus(tc.text, true)
			if err != nil {
				t.Fatal(err)
			}
			if got := linuxStatusStopped(status); got != tc.stopped {
				t.Fatalf("stopped=%v, want %v", got, tc.stopped)
			}
			if got := linuxStatusActive(status); got != tc.active {
				t.Fatalf("active=%v, want %v", got, tc.active)
			}
		})
	}
}

func TestLinuxSystemctlUsesFixedArgvAndBoundsOutput(t *testing.T) {
	var gotPath string
	var gotArgs []string
	ctx := withLinuxServiceTestHooks(context.Background(), linuxServiceTestHooks{run: func(ctx context.Context, path string, args ...string) ([]byte, error) {
		gotPath, gotArgs = path, append([]string(nil), args...)
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("systemctl invocation has no deadline")
		}
		return []byte(strings.Repeat("x", linuxServiceMaxOutput+1)), nil
	}})
	_, err := runLinuxSystemctl(ctx, "show", "--property=MainPID", "minecraft-ui.service")
	if err == nil {
		t.Fatal("oversized command output accepted")
	}
	if gotPath != "/usr/bin/systemctl" || strings.Join(gotArgs, " ") != "show --property=MainPID minecraft-ui.service" {
		t.Fatalf("unexpected argv: %q %q", gotPath, gotArgs)
	}
}

func TestLinuxSystemctlRespectsCancellationWithoutLeakingCommandOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = withLinuxServiceTestHooks(ctx, linuxServiceTestHooks{run: func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		cancel()
		<-ctx.Done()
		return []byte("sensitive output"), ctx.Err()
	}})
	_, err := runLinuxSystemctl(ctx, "stop", "minecraft-ui.service")
	if err == nil || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("unsafe cancellation error: %v", err)
	}
}

func TestParseLinuxGameUnitsRejectsUnsafeNamesAndMalformedRows(t *testing.T) {
	if got, err := parseLinuxGameUnits("minecraft@alpha.service loaded active running Minecraft\n"); err != nil || len(got) != 1 || got[0] != "minecraft@alpha.service" {
		t.Fatalf("valid game unit: %#v, %v", got, err)
	}
	for _, row := range []string{
		"minecraft@../escape.service loaded inactive dead unsafe\n",
		"minecraft@-bad.service loaded inactive dead unsafe\n",
		"attacker.service loaded inactive dead unsafe\n",
		"minecraft@alpha.service loaded\n",
	} {
		if _, err := parseLinuxGameUnits(row); err == nil {
			t.Fatalf("unsafe row accepted: %q", row)
		}
	}
}

func TestRequireLinuxGamesStoppedReenumeratesAndChecksExactUnits(t *testing.T) {
	calls := 0
	ctx := withLinuxServiceTestHooks(context.Background(), linuxServiceTestHooks{run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if strings.HasPrefix(args[0], "list-units") {
			calls++
			if calls == 2 {
				return []byte("minecraft@beta.service loaded inactive dead Game\n"), nil
			}
			return []byte("minecraft@alpha.service loaded inactive dead Game\n"), nil
		}
		if strings.Join(args, " ") == "show --no-pager --property=LoadState,ActiveState,SubState,MainPID minecraft@alpha.service" {
			return []byte("LoadState=loaded\nActiveState=inactive\nSubState=dead\nMainPID=0\n"), nil
		}
		t.Fatalf("unexpected systemctl args: %q", args)
		return nil, nil
	}})
	if err := requireLinuxGamesStopped(ctx); err == nil {
		t.Fatal("changing game unit set accepted")
	}
}

func TestRequireLinuxGamesStoppedRefusesActiveGames(t *testing.T) {
	ctx := withLinuxServiceTestHooks(context.Background(), linuxServiceTestHooks{run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if strings.HasPrefix(args[0], "list-units") {
			return []byte("minecraft@alpha.service loaded active running Game\n"), nil
		}
		return []byte("LoadState=loaded\nActiveState=active\nSubState=running\nMainPID=17\n"), nil
	}})
	if err := requireLinuxGamesStopped(ctx); err == nil {
		t.Fatal("active game accepted")
	}
}

func TestRequireLinuxManagerImageChecksFileAndProcessIdentity(t *testing.T) {
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "manager")
	if err := os.WriteFile(imagePath, []byte("manager image"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(imagePath, 0755); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(imagePath)
	if err != nil {
		t.Fatal(err)
	}
	info = serviceTestFileInfo{FileInfo: info}
	fields := []string{"S", "1", "1", "1", "0", "-1", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "20", "0", "0", "123", "0", "0", "0"}
	start := []byte("17 (manager) " + strings.Join(fields, " ") + "\n")
	readCount := 0
	ctx := withLinuxServiceTestHooks(context.Background(), linuxServiceTestHooks{
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if strings.HasPrefix(args[0], "show") {
				return []byte("LoadState=loaded\nActiveState=active\nSubState=running\nMainPID=17\nUnitFileState=enabled\n"), nil
			}
			return nil, errors.New("unexpected command")
		},
		lstat: func(path string) (os.FileInfo, error) {
			if path == linuxManagerExecutablePath {
				return info, nil
			}
			return nil, os.ErrNotExist
		},
		stat: func(path string) (os.FileInfo, error) {
			if path == linuxManagerExecutablePath || path == "/proc/17/exe" {
				return info, nil
			}
			return nil, os.ErrNotExist
		},
		readFile: func(path string) ([]byte, error) {
			if path != "/proc/17/stat" {
				return nil, os.ErrNotExist
			}
			readCount++
			return start, nil
		},
		open: func(path string) (*os.File, error) { return os.Open(imagePath) },
	})
	if err := requireLinuxManagerImage(ctx, Image{Version: "v1.2.3", Schema: 1, SHA256: "" + sha256Hex([]byte("manager image"))}); err != nil {
		t.Fatal(err)
	}
	if readCount < 2 {
		t.Fatalf("process start identity read %d times", readCount)
	}
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

type serviceTestFileInfo struct{ os.FileInfo }

func (i serviceTestFileInfo) Sys() any {
	st := *i.FileInfo.Sys().(*syscall.Stat_t)
	st.Uid = 0
	st.Nlink = 1
	return &st
}

func TestLinuxManagerImageRejectsExecutableMismatchAndChangedPID(t *testing.T) {
	for _, mode := range []string{"executable", "pid", "replacement", "ticks"} {
		t.Run(mode, func(t *testing.T) {
			ctx, expected, managerInfo, otherInfo, statBody := linuxManagerImageFixture(t)
			base := serviceHooks(ctx)
			showCount := 0
			base.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
				if len(args) > 0 && args[0] == "show" {
					showCount++
					pid := "17"
					if mode == "pid" && showCount > 1 {
						pid = "18"
					}
					return []byte("LoadState=loaded\nActiveState=active\nSubState=running\nMainPID=" + pid + "\nUnitFileState=enabled\n"), nil
				}
				return nil, errors.New("unexpected command")
			}
			lstatCount := 0
			base.lstat = func(path string) (os.FileInfo, error) {
				if path == linuxManagerExecutablePath {
					lstatCount++
					if mode == "replacement" && lstatCount > 1 {
						return otherInfo, nil
					}
					return managerInfo, nil
				}
				return nil, os.ErrNotExist
			}
			base.stat = func(path string) (os.FileInfo, error) {
				if path == linuxManagerExecutablePath {
					return managerInfo, nil
				}
				if path == "/proc/17/exe" {
					if mode == "executable" {
						return otherInfo, nil
					}
					return managerInfo, nil
				}
				return nil, os.ErrNotExist
			}
			statReadCount := 0
			base.readFile = func(path string) ([]byte, error) {
				if path == "/proc/17/stat" {
					statReadCount++
					if mode == "ticks" && statReadCount > 1 {
						changed := strings.Replace(string(statBody), " 123 ", " 124 ", 1)
						return []byte(changed), nil
					}
					return statBody, nil
				}
				return nil, os.ErrNotExist
			}
			ctx = withLinuxServiceTestHooks(context.Background(), base)
			if err := requireLinuxManagerImage(ctx, expected); err == nil {
				t.Fatal("changed image/process accepted")
			}
		})
	}
}

func TestSetLinuxManagerEnabledRefusesMissingManagerUnit(t *testing.T) {
	var enableCalled bool
	ctx := withLinuxServiceTestHooks(context.Background(), linuxServiceTestHooks{run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if strings.HasPrefix(args[0], "show") {
			return []byte("LoadState=not-found\nActiveState=inactive\nSubState=dead\nMainPID=0\nUnitFileState=\n"), nil
		}
		if args[0] == "enable" {
			enableCalled = true
		}
		return nil, nil
	}})
	if err := setLinuxManagerEnabled(ctx, true); err == nil {
		t.Fatal("missing manager unit accepted")
	}
	if enableCalled {
		t.Fatal("missing manager unit was enabled")
	}
}

func linuxManagerImageFixture(t *testing.T) (context.Context, Image, os.FileInfo, os.FileInfo, []byte) {
	t.Helper()
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "manager")
	otherPath := filepath.Join(dir, "other")
	if err := os.WriteFile(imagePath, []byte("manager image"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(imagePath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherPath, []byte("other image"), 0755); err != nil {
		t.Fatal(err)
	}
	managerInfo, err := os.Stat(imagePath)
	if err != nil {
		t.Fatal(err)
	}
	otherInfo, err := os.Stat(otherPath)
	if err != nil {
		t.Fatal(err)
	}
	managerInfo = serviceTestFileInfo{FileInfo: managerInfo}
	fields := []string{"S", "1", "1", "1", "0", "-1", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "20", "0", "0", "123", "0", "0", "0"}
	statBody := []byte("17 (manager) " + strings.Join(fields, " ") + "\n")
	ctx := withLinuxServiceTestHooks(context.Background(), linuxServiceTestHooks{
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if len(args) > 0 && args[0] == "show" {
				return []byte("LoadState=loaded\nActiveState=active\nSubState=running\nMainPID=17\nUnitFileState=enabled\n"), nil
			}
			return nil, errors.New("unexpected command")
		},
		lstat: func(path string) (os.FileInfo, error) {
			if path == linuxManagerExecutablePath {
				return managerInfo, nil
			}
			return nil, os.ErrNotExist
		},
		stat: func(path string) (os.FileInfo, error) {
			if path == linuxManagerExecutablePath || path == "/proc/17/exe" {
				return managerInfo, nil
			}
			return nil, os.ErrNotExist
		},
		readFile: func(path string) ([]byte, error) {
			if path == "/proc/17/stat" {
				return statBody, nil
			}
			return nil, os.ErrNotExist
		},
		open: func(string) (*os.File, error) { return os.Open(imagePath) },
	})
	return ctx, Image{Version: "v1.2.3", Schema: 1, SHA256: sha256Hex([]byte("manager image"))}, managerInfo, otherInfo, statBody
}

func TestReloadLinuxUnitsPreservesActiveGameUnit(t *testing.T) {
	var daemonReload, gameStop bool
	listCount := 0
	ctx := withLinuxServiceTestHooks(context.Background(), linuxServiceTestHooks{run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(joined, "list-units "):
			listCount++
			return []byte("minecraft@alpha.service loaded active running Game\n"), nil
		case strings.HasPrefix(joined, "show "):
			return []byte("LoadState=loaded\nActiveState=active\nSubState=running\nMainPID=41\n"), nil
		case joined == "daemon-reload":
			daemonReload = true
			return nil, nil
		case strings.HasPrefix(joined, "stop minecraft@"):
			gameStop = true
			return nil, nil
		default:
			t.Fatalf("unexpected systemctl argv: %q", args)
			return nil, nil
		}
	}})
	if err := reloadLinuxUnits(ctx); err != nil {
		t.Fatal(err)
	}
	if !daemonReload || gameStop || listCount != 4 {
		t.Fatalf("reload proof incomplete: daemonReload=%v gameStop=%v listings=%d", daemonReload, gameStop, listCount)
	}
}

func TestRequireLinuxGamesStoppedRejectsStateVisibleInFinalEnumeration(t *testing.T) {
	listCount := 0
	var showCount int
	ctx := withLinuxServiceTestHooks(context.Background(), linuxServiceTestHooks{run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "list-units" {
			listCount++
			state := "inactive dead"
			if listCount == 2 {
				state = "activating start"
			}
			return []byte("minecraft@alpha.service loaded " + state + " Game\n"), nil
		}
		if args[0] == "show" {
			showCount++
			return []byte("LoadState=loaded\nActiveState=inactive\nSubState=dead\nMainPID=0\n"), nil
		}
		t.Fatalf("unexpected systemctl argv: %q", args)
		return nil, nil
	}})
	if err := requireLinuxGamesStopped(ctx); err == nil {
		t.Fatal("game activation visible in final enumeration accepted")
	}
	if listCount != 2 || showCount != 1 {
		t.Fatalf("unexpected proof sequence: listings=%d shows=%d", listCount, showCount)
	}
}

func TestLinuxSystemctlUsesOperationSpecificDeadlinesCappedByCaller(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		minimum time.Duration
		maximum time.Duration
	}{
		{"stop", []string{"stop", "minecraft-ui.service"}, 154 * time.Second, 155 * time.Second},
		{"start", []string{"start", "minecraft-ui.service"}, 5*time.Minute + 59*time.Second, 6 * time.Minute},
		{"inspection", []string{"show", "minecraft-ui.service"}, 29 * time.Second, 30 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var remaining time.Duration
			ctx := withLinuxServiceTestHooks(context.Background(), linuxServiceTestHooks{run: func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
				deadline, ok := ctx.Deadline()
				if !ok {
					t.Fatal("systemctl invocation has no deadline")
				}
				remaining = time.Until(deadline)
				return nil, nil
			}})
			if _, err := runLinuxSystemctl(ctx, tc.args...); err != nil {
				t.Fatal(err)
			}
			if remaining < tc.minimum || remaining > tc.maximum {
				t.Fatalf("deadline remaining %s outside [%s,%s]", remaining, tc.minimum, tc.maximum)
			}
		})
	}

	caller, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ctx := withLinuxServiceTestHooks(caller, linuxServiceTestHooks{run: func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 2*time.Second {
			t.Fatal("child command deadline exceeded caller deadline")
		}
		return nil, nil
	}})
	if _, err := runLinuxSystemctl(ctx, "stop", "minecraft-ui.service"); err != nil {
		t.Fatal(err)
	}
}

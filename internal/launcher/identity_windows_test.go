//go:build windows

package launcher

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"golang.org/x/sys/windows"
)

// QueryFullProcessImageName can retain the 8.3 spelling used to start Go's test
// binary while CaptureIdentity normalizes the LaunchSpec through EvalSymlinks.
func TestWindowsProcessImageAliasesRequireSameCanonicalFilesystemPath(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(exe)
	if err != nil {
		t.Fatal(err)
	}
	start, got, _, err := processIdentity(os.Getpid())
	if err != nil || start == "" || !equalExecutable(got, want) {
		t.Fatalf("process image=%q want=%q start=%q error=%v", got, want, start, err)
	}
	name, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]uint16, 32768)
	n, err := windows.GetShortPathName(name, &buffer[0], uint32(len(buffer)))
	if err != nil || n == 0 || n >= uint32(len(buffer)) {
		t.Fatalf("short image path: length=%d error=%v", n, err)
	}
	short := windows.UTF16ToString(buffer[:n])
	if short == want {
		t.Log("volume did not provide a distinct 8.3 alias; canonical native image check still exercised")
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	id, err := CaptureIdentity("image", os.Getpid(), domain.LaunchSpec{Executable: short, WorkingDir: cwd})
	if err != nil || id.Executable != want {
		t.Fatalf("capture using short alias: identity=%+v error=%v", id, err)
	}
	if err := VerifyIdentity(id); err != nil {
		t.Fatal(err)
	}
	// An existing different file must never become an acceptable image identity.
	other := filepath.Join(t.TempDir(), "other.exe")
	if err := os.WriteFile(other, []byte("different executable"), 0600); err != nil {
		t.Fatal(err)
	}
	id.Executable = other
	if err := VerifyIdentity(id); err == nil {
		t.Fatal("different image accepted for the same live PID")
	}
	id.Executable = filepath.Join(t.TempDir(), "missing.exe")
	if alive, err := ProbeIdentity(id); alive || err == nil {
		t.Fatalf("unavailable image binding classified as process exit: alive=%v error=%v", alive, err)
	}
}

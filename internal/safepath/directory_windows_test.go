package safepath

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

type windowsDirectoryInfo struct{ attributes uint32 }

func (windowsDirectoryInfo) Name() string       { return "fixture" }
func (windowsDirectoryInfo) Size() int64        { return 0 }
func (windowsDirectoryInfo) Mode() os.FileMode  { return os.ModeDir }
func (windowsDirectoryInfo) ModTime() time.Time { return time.Time{} }
func (windowsDirectoryInfo) IsDir() bool        { return true }
func (w windowsDirectoryInfo) Sys() any {
	return &syscall.Win32FileAttributeData{FileAttributes: w.attributes}
}
func TestWindowsDriveAndReparsePolicy(t *testing.T) {
	if safeDirectory(windowsDirectoryInfo{syscall.FILE_ATTRIBUTE_DIRECTORY | syscall.FILE_ATTRIBUTE_REPARSE_POINT}) {
		t.Fatal("reparse directory accepted")
	}
	if !safeDirectory(windowsDirectoryInfo{syscall.FILE_ATTRIBUTE_DIRECTORY}) {
		t.Fatal("ordinary Windows directory refused")
	}
	for _, path := range []string{`\\server\share\data`, `\\?\C:\data`, `\\.\C:\data`, `C:data`, `\data`} {
		if root, err := OpenRoot(context.Background(), path); err == nil {
			root.Close()
			t.Fatal("unsupported root accepted", path)
		}
	}
	root, err := OpenRoot(t.Context(), filepath.Clean(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	root.Close()
}

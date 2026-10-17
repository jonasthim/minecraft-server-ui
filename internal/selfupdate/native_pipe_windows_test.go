//go:build windows

package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsUpdaterFailureDiagnostics(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		code uint32
	}{
		{"recovery_private_error", &windowsUpdaterFailure{windowsUpdaterRecovery, errors.New("private-path-or-secret")}, 0x4d010000},
		{"recovery_deadline", &windowsUpdaterFailure{windowsUpdaterRecoveryDeadline, context.DeadlineExceeded}, 0x4d020000},
		{"account_private_error", &windowsUpdaterFailure{windowsUpdaterAccount, errors.New("private-sid-or-receipt")}, 0x4d030000},
		{"descriptor_invalid_parameter", &windowsUpdaterFailure{windowsUpdaterDescriptor, windows.ERROR_INVALID_PARAMETER}, 0x4d040057},
		{"create_access_denied", &windowsUpdaterFailure{windowsUpdaterPipeCreate, windows.ERROR_ACCESS_DENIED}, 0x4d050005},
		{"connect_disconnected_client", &windowsUpdaterFailure{windowsUpdaterPipeConnect, windows.ERROR_NO_DATA}, 0x4d0600e8},
		{"wrapped_connect_error", fmt.Errorf("private-error: %w", &windowsUpdaterFailure{windowsUpdaterPipeConnect, fmt.Errorf("private-path: %w", windows.ERROR_INVALID_HANDLE)}), 0x4d060006},
		{"non_win32_large_status_not_truncated", &windowsUpdaterFailure{windowsUpdaterPipeCreate, windows.Errno(0xc0000005)}, 0x4d050000},
		{"unknown_error", errors.New("private-arbitrary-body"), 0x4dff0000},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := windowsUpdaterExitCode(tt.err); got != tt.code {
				t.Fatalf("SCM code = %#x, want %#x", got, tt.code)
			}
		})
	}
	wrapped := &windowsUpdaterFailure{windowsUpdaterPipeConnect, fmt.Errorf("private-cause: %w", windows.ERROR_NO_DATA)}
	if strings.Contains(wrapped.Error(), "private") || !errors.Is(wrapped, windows.ERROR_NO_DATA) {
		t.Fatal("fixed diagnostic exposed text or lost native cause")
	}
}

// These tests create local, unique pipe names only. They never open the updater's
// production name, install services/accounts or bypass service/PID authorization.
func newWindowsUpdaterTestPipe(t *testing.T) (windows.Handle, *uint16) {
	t.Helper()
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		t.Fatal("query test identity:", err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatal("query test user:", err)
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		t.Fatal("create private test descriptor:", err)
	}
	name, err := windows.UTF16PtrFromString(fmt.Sprintf(`\\.\pipe\MinecraftUIUpdate-test-%d-%d`, os.Getpid(), time.Now().UnixNano()))
	if err != nil {
		t.Fatal("create unique test name:", err)
	}
	handle, err := createWindowsUpdaterPipe(name, descriptor)
	if err != nil {
		t.Fatal("create actual updater-mode test pipe:", err)
	}
	t.Cleanup(func() { windows.CloseHandle(handle) })
	return handle, name
}

func openWindowsUpdaterTestClient(t *testing.T, name *uint16) windows.Handle {
	t.Helper()
	client, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal("open local test client:", err)
	}
	t.Cleanup(func() { windows.CloseHandle(client) })
	return client
}

func TestWindowsUpdaterPipeIdleWaitAndCancellation(t *testing.T) {
	handle, _ := newWindowsUpdaterTestPipe(t)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	err := connectWindowsUpdaterPipe(ctx, handle)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("idle pipe returned before a client or cancellation: native result %v", err)
	}
}

func TestWindowsUpdaterPipeClientBeforeConnect(t *testing.T) {
	handle, name := newWindowsUpdaterTestPipe(t)
	client := openWindowsUpdaterTestClient(t, name)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := connectWindowsUpdaterPipe(ctx, handle); err != nil {
		t.Fatal("connect existing client:", err)
	}
	var count uint32
	if err := windows.WriteFile(client, []byte("status"), &count, nil); err != nil || count != 6 {
		t.Fatal("write fixed test command:", err)
	}
	buffer := make([]byte, 128)
	if err := windows.ReadFile(handle, buffer, &count, nil); err != nil || string(buffer[:count]) != "status" {
		t.Fatal("receive fixed test command:", err)
	}
}

func TestWindowsUpdaterPipeNativeClosedClientState(t *testing.T) {
	handle, name := newWindowsUpdaterTestPipe(t)
	client, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal("open early test client:", err)
	}
	if err := windows.CloseHandle(client); err != nil {
		t.Fatal("close early test client:", err)
	}
	// Preserve direct native evidence for the hosted failure's exact state;
	// the shipping helper must recover from this rather than return it.
	err = windows.ConnectNamedPipe(handle, nil)
	if !errors.Is(err, windows.ERROR_NO_DATA) {
		t.Fatalf("closed client native result = %v, want ERROR_NO_DATA", err)
	}
	if windowsUpdaterExitCode(&windowsUpdaterFailure{windowsUpdaterPipeConnect, err}) != 0x4d0600e8 {
		t.Fatal("closed client lost fixed SCM diagnostic")
	}
}

func TestWindowsUpdaterPipeClosedClientRecovery(t *testing.T) {
	handle, name := newWindowsUpdaterTestPipe(t)
	early, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal("open early client:", err)
	}
	if err := windows.CloseHandle(early); err != nil {
		t.Fatal("close early client:", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	connected := make(chan error, 1)
	go func() { connected <- connectWindowsUpdaterPipe(ctx, handle) }()
	var pending <-chan error = connected
	// CreateFile fails with PIPE_BUSY until the old connection is reset.
	var client windows.Handle
	for {
		client, err = windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_PIPE_BUSY) {
			t.Fatal("open subsequent client:", err)
		}
		select {
		case err := <-pending:
			if err != nil {
				t.Fatal("server failed instead of resetting closed client:", err)
			}
			// A nonblocking availability transition may precede this open;
			// the separate idle contract checks premature success.
			pending = nil
		case <-ctx.Done():
			t.Fatal("subsequent client was never admitted")
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer windows.CloseHandle(client)
	if pending != nil {
		select {
		case err := <-pending:
			if err != nil {
				t.Fatal("connect subsequent client:", err)
			}
		case <-ctx.Done():
			t.Fatal("subsequent accept did not complete")
		}
	}
	var count uint32
	if err := windows.WriteFile(client, []byte("status"), &count, nil); err != nil || count != 6 {
		t.Fatal("write subsequent fixed command:", err)
	}
	buffer := make([]byte, 128)
	if err := windows.ReadFile(handle, buffer, &count, nil); err != nil || string(buffer[:count]) != "status" {
		t.Fatal("receive subsequent fixed command:", err)
	}
}

func TestWindowsUpdaterPipeCancelledAccept(t *testing.T) {
	handle, _ := newWindowsUpdaterTestPipe(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := connectWindowsUpdaterPipe(ctx, handle); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled accept returned %v", err)
	}
}

func TestWindowsUpdaterPipeInvalidHandleFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := connectWindowsUpdaterPipe(ctx, windows.InvalidHandle); !errors.Is(err, windows.ERROR_INVALID_HANDLE) {
		t.Fatalf("invalid handle returned %v", err)
	}
}

func TestWindowsUpdaterPipeFirstInstanceCollision(t *testing.T) {
	first, name := newWindowsUpdaterTestPipe(t)
	// The shipping first-instance/single-instance settings must refuse a
	// second server. Windows may check the exhausted instance count first.
	second, err := createWindowsUpdaterPipe(name, nil)
	if err == nil || second != windows.InvalidHandle {
		if second != windows.InvalidHandle {
			windows.CloseHandle(second)
		}
		t.Fatal("duplicate pipe server accepted")
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_PIPE_BUSY) {
		t.Fatal("duplicate native error differs:", err)
	}
	want := uint32(0x4d050005)
	if errors.Is(err, windows.ERROR_PIPE_BUSY) {
		want = 0x4d0500e7
	}
	if windowsUpdaterExitCode(&windowsUpdaterFailure{windowsUpdaterPipeCreate, err}) != want {
		t.Fatal("duplicate server lost fixed SCM diagnostic")
	}
	client := openWindowsUpdaterTestClient(t, name)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := connectWindowsUpdaterPipe(ctx, first); err != nil {
		t.Fatal("original server damaged by duplicate attempt:", err)
	}
	var count uint32
	if err := windows.WriteFile(client, []byte("status"), &count, nil); err != nil || count != 6 {
		t.Fatal("write original-server command:", err)
	}
	buffer := make([]byte, 128)
	if err := windows.ReadFile(first, buffer, &count, nil); err != nil || string(buffer[:count]) != "status" {
		t.Fatal("original server lost command after collision:", err)
	}
}

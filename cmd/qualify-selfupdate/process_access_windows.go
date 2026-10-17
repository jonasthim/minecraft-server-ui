package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

// Called only by the disposable hosted harness as its existing dedicated
// manager account. It never changes process/security/service state or performs
// a requested destructive operation; each stronger OpenProcess must be denied.
func inspectUpdaterProcessAccess(ctx context.Context, out io.Writer) error {
	fail := func() error { return errors.New("installed updater process access qualification failed") }
	if err := ctx.Err(); err != nil {
		return fail()
	}
	controller, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return fail()
	}
	defer windows.CloseServiceHandle(controller)
	name, _ := windows.UTF16PtrFromString("MinecraftUIUpdater")
	service, err := windows.OpenService(controller, name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return fail()
	}
	defer windows.CloseServiceHandle(service)
	var status windows.SERVICE_STATUS_PROCESS
	var needed uint32
	if err := windows.QueryServiceStatusEx(service, windows.SC_STATUS_PROCESS_INFO, (*byte)(unsafe.Pointer(&status)), uint32(unsafe.Sizeof(status)), &needed); err != nil || status.CurrentState != uint32(svc.Running) || status.ProcessId < 1 {
		return fail()
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, status.ProcessId)
	if err != nil {
		return fail()
	}
	defer windows.CloseHandle(process)
	var length uint32 = 32768
	buffer := make([]uint16, length)
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &length); err != nil {
		return fail()
	}
	const expectedPath = `C:\Program Files\MinecraftUI\selfupdate.exe`
	image := windows.UTF16ToString(buffer[:length])
	actual, err := os.Stat(image)
	if err != nil || !strings.EqualFold(image, expectedPath) {
		return fail()
	}
	expected, err := os.Stat(expectedPath)
	if err != nil || !os.SameFile(actual, expected) {
		return fail()
	}
	for _, access := range []uint32{
		windows.PROCESS_TERMINATE, windows.PROCESS_VM_READ, windows.PROCESS_VM_WRITE,
		windows.PROCESS_VM_OPERATION, windows.PROCESS_DUP_HANDLE, windows.PROCESS_CREATE_THREAD,
		windows.WRITE_DAC, windows.WRITE_OWNER,
	} {
		if ctx.Err() != nil {
			return fail()
		}
		denied, err := windows.OpenProcess(access, false, status.ProcessId)
		if err == nil {
			windows.CloseHandle(denied)
			return fail()
		}
		if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return fail()
		}
	}
	if ctx.Err() != nil {
		return fail()
	}
	pid := status.ProcessId
	if err := windows.QueryServiceStatusEx(service, windows.SC_STATUS_PROCESS_INFO, (*byte)(unsafe.Pointer(&status)), uint32(unsafe.Sizeof(status)), &needed); err != nil || status.CurrentState != uint32(svc.Running) || status.ProcessId != pid {
		return fail()
	}
	if err := json.NewEncoder(out).Encode(struct {
		Format                int  `json:"format"`
		QueryLimited          bool `json:"query_limited"`
		ImageBound            bool `json:"image_bound"`
		TerminateDenied       bool `json:"terminate_denied"`
		VMReadDenied          bool `json:"vm_read_denied"`
		VMWriteDenied         bool `json:"vm_write_denied"`
		VMOperationDenied     bool `json:"vm_operation_denied"`
		DuplicateHandleDenied bool `json:"duplicate_handle_denied"`
		CreateThreadDenied    bool `json:"create_thread_denied"`
		WriteDACDenied        bool `json:"write_dac_denied"`
		WriteOwnerDenied      bool `json:"write_owner_denied"`
	}{1, true, true, true, true, true, true, true, true, true, true}); err != nil {
		return fail()
	}
	return nil
}

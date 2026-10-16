//go:build windows

package loaderutil

import (
	"context"
	"fmt"
	"golang.org/x/sys/windows"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

func prepareChild(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW}
}
func confineChild(pid int) (func(), error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	var closeOnce sync.Once
	closeJob := func() { closeOnce.Do(func() { windows.CloseHandle(job) }) }
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		closeJob()
		return nil, err
	}
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		closeJob()
		return nil, err
	}
	defer windows.CloseHandle(proc)
	if err = windows.AssignProcessToJobObject(job, proc); err != nil {
		closeJob()
		return nil, err
	}
	// os/exec closes CreateProcess's primary-thread handle. Enumerate the suspended
	// child's sole thread after containment, then resume it. No JVM code runs
	// before successful job assignment; any failure leaves it suspended for kill.
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		closeJob()
		return nil, err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != uint32(pid) {
			continue
		}
		thread, e := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if e != nil {
			closeJob()
			return nil, e
		}
		_, e = windows.ResumeThread(thread)
		windows.CloseHandle(thread)
		if e != nil {
			closeJob()
			return nil, e
		}
		return closeJob, nil
	}
	closeJob()
	return nil, fmt.Errorf("suspended JVM thread not found: %w", err)
}

func runConfined(ctx context.Context, cmd *exec.Cmd) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	prepareChild(cmd)
	cmd.WaitDelay = 2 * time.Second
	if e := cmd.Start(); e != nil {
		return e
	}
	closeJob, e := confineChild(cmd.Process.Pid)
	if e != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return e
	}
	defer closeJob()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case e := <-done:
		return e
	case <-ctx.Done():
		closeJob()
		<-done
		return ctx.Err()
	}
}

//go:build windows

package launcher

import (
	"context"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"golang.org/x/sys/windows"
	"math"
	"path/filepath"
	"time"
	"unsafe"
)

var getProcessMemoryInfo = windows.NewLazySystemDLL("psapi.dll").NewProc("GetProcessMemoryInfo")

type processMemoryCounters struct {
	Size, PageFaults                                                                                                               uint32
	PeakWorkingSet, WorkingSet, QuotaPeakPaged, QuotaPaged, QuotaPeakNonPaged, QuotaNonPaged, Pagefile, PeakPagefile, PrivateUsage uintptr
}

func sampleProcessMetrics(ctx context.Context, id domain.ProcessIdentity) (ProcessCounters, error) {
	if err := ctx.Err(); err != nil {
		return ProcessCounters{}, err
	}
	h, e := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ|windows.SYNCHRONIZE, false, uint32(id.PID))
	if e != nil {
		return ProcessCounters{}, e
	}
	defer windows.CloseHandle(h)
	return countersFromHandle(h, id)
}
func countersFromHandle(h windows.Handle, id domain.ProcessIdentity) (ProcessCounters, error) {
	out := ProcessCounters{ObservedAt: time.Now().UTC(), Source: "windows_process_api"}
	var creation, exit, kernel, user windows.Filetime
	if e := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); e != nil {
		return out, e
	}
	var boot struct {
		Identifier   windows.GUID
		FirmwareType uint32
		BootFlags    uint64
	}
	var size uint32
	if e := windows.NtQuerySystemInformation(windows.SystemBootEnvironmentInformation, unsafe.Pointer(&boot), uint32(unsafe.Sizeof(boot)), &size); e != nil {
		return out, e
	}
	start := fmt.Sprintf("%s:%08x%08x", boot.Identifier.String(), creation.HighDateTime, creation.LowDateTime)
	var image [32768]uint16
	n := uint32(len(image))
	if e := windows.QueryFullProcessImageName(h, 0, &image[0], &n); e != nil {
		return out, e
	}
	if start != id.StartIdentity || !equalExecutable(windows.UTF16ToString(image[:n]), id.Executable) || !filepath.IsAbs(id.WorkingDir) {
		return out, fmt.Errorf("JVM identity changed")
	}
	kernelTicks := uint64(kernel.HighDateTime)<<32 | uint64(kernel.LowDateTime)
	userTicks := uint64(user.HighDateTime)<<32 | uint64(user.LowDateTime)
	if kernelTicks > math.MaxInt64/100 || userTicks > math.MaxInt64/100-kernelTicks {
		return out, fmt.Errorf("JVM CPU counter overflow")
	}
	cpu := int64(kernelTicks+userTicks) * 100
	out.CPUNanos = &cpu
	memory := processMemoryCounters{Size: uint32(unsafe.Sizeof(processMemoryCounters{}))}
	result, _, _ := getProcessMemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&memory)), uintptr(memory.Size))
	if result != 0 {
		rss, private := int64(memory.WorkingSet), int64(memory.PrivateUsage)
		out.ResidentBytes = &rss
		out.PrivateBytes = &private
	} else {
		out.GapReason = "memory_unavailable"
	}
	state, e := windows.WaitForSingleObject(h, 0)
	if e != nil || state != uint32(windows.WAIT_TIMEOUT) {
		return ProcessCounters{}, fmt.Errorf("JVM exited during sample")
	}
	return out, nil
}
func readProcessCounters(pid int, start string) ProcessCounters {
	// ReadNativeInspection is a Linux broker entrypoint. Keep native Windows
	// collection on the explicitly verified handle path above.
	return ProcessCounters{ObservedAt: time.Now().UTC(), Source: "windows_process_api", GapReason: "counters_unavailable"}
}

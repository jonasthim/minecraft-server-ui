//go:build windows

package selfupdate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"
)

const updaterPipe = `\\.\pipe\MinecraftUIUpdate-v1`
const updaterService = "MinecraftUIUpdater"
const probeService = "MinecraftUIUpdateProbe"

func platformPaths() nativePaths {
	return nativePaths{Data: `C:\ProgramData\MinecraftUI`, Trust: `C:\Program Files\MinecraftUI\update-trust.json`, State: `C:\ProgramData\MinecraftUIUpdate`, Bin: `C:\Program Files\MinecraftUI`, Binary: "minecraft-ui.exe", Updater: `C:\Program Files\MinecraftUI\selfupdate.exe`}
}
func openProtectedRoot(ctx context.Context, path string) (*os.Root, error) {
	for p := path; ; p = filepath.Dir(p) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		kind := windowsExistingAncestor
		if p == path {
			kind = windowsApplicationDirectory
		}
		// Walk outward only after the existing application directory has
		// passed strict checks. Each ancestor therefore contains an already
		// checked child which untrusted principals cannot delete or replace.
		if err := protectedWindowsPath(p, kind); err != nil {
			return nil, err
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	return safepath.OpenRoot(ctx, path)
}
func protectedFile(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("native update file is not regular")
	}
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	var attributes windows.ByHandleFileInformation
	err = windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &attributes)
	closeErr := file.Close()
	if err != nil || closeErr != nil || attributes.NumberOfLinks != 1 || attributes.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("native update file is linked or unsafe")
	}
	return protectedWindowsPath(filepath.Join(root.Name(), name), windowsRegularFile)
}
func protectedWindowsPath(path string, kind windowsPathKind) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return errors.New("native update path could not be verified")
	}
	attributes, err := windows.GetFileAttributes(name)
	if err != nil || !safeWindowsPathAttributes(attributes, kind) {
		return errors.New("native update path is missing, reparse, or has an unsafe type")
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return errors.New("native update ACL could not be verified")
	}
	installer, _, _, e := windows.LookupSID("", `NT SERVICE\TrustedInstaller`)
	if e != nil {
		return errors.New("trusted installer identity unavailable")
	}
	return validateWindowsSecurityDescriptor(sd, kind, installer.String())
}

func safeWindowsPathAttributes(attributes uint32, kind windowsPathKind) bool {
	if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return false
	}
	switch kind {
	case windowsExistingAncestor, windowsApplicationDirectory:
		return attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
	case windowsRegularFile:
		return attributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0
	default:
		return false
	}
}

func validateWindowsSecurityDescriptor(sd *windows.SECURITY_DESCRIPTOR, kind windowsPathKind, installer string) error {
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return errors.New("native update ACL is unrestricted")
	}
	trusted := func(sid *windows.SID) bool {
		if sid == nil {
			return false
		}
		id := sid.String()
		return id == "S-1-5-18" || id == "S-1-5-32-544" || id == installer
	}
	owner, _, err := sd.Owner()
	if err != nil || !trusted(owner) {
		return errors.New("native updater path has an untrusted owner")
	}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return err
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return errors.New("native update ACL requires explicit review")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !trusted(sid) && untrustedWindowsAccess(uint32(ace.Mask), kind) {
			return errors.New("native update path is writable by an untrusted principal")
		}
	}
	return nil
}
func openSCMService(name string) (*mgr.Mgr, *mgr.Service, error) {
	m, err := mgr.Connect()
	if err != nil {
		return nil, nil, err
	}
	service, err := m.OpenService(name)
	if err != nil {
		m.Disconnect()
		return nil, nil, err
	}
	return m, service, nil
}

// openSCMQuery is also used by the unprivileged manager pipe client. Service
// control and configuration deliberately keep their separate privileged helper.
func openSCMQuery(name string) (*mgr.Mgr, *mgr.Service, error) {
	h, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return nil, nil, windowsAvailabilityError(windowsAvailabilitySCMConnect, err)
	}
	m := &mgr.Mgr{Handle: h}
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		m.Disconnect()
		return nil, nil, windowsAvailabilityError(windowsAvailabilityServiceOpen, err)
	}
	service, err := windows.OpenService(h, n, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		m.Disconnect()
		return nil, nil, windowsAvailabilityError(windowsAvailabilityServiceOpen, err)
	}
	return m, &mgr.Service{Name: name, Handle: service}, nil
}
func verifiedServicePID(name, path string) (uint32, error) {
	m, service, err := openSCMQuery(name)
	if err != nil {
		return 0, err
	}
	defer m.Disconnect()
	defer service.Close()
	status, err := service.Query()
	if err != nil {
		return 0, windowsAvailabilityError(windowsAvailabilityServiceQuery, err)
	}
	if status.State != svc.Running || status.ProcessId < 1 {
		return 0, windowsAvailabilityError(windowsAvailabilityServiceState, nil)
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, status.ProcessId)
	if err != nil {
		return 0, windowsAvailabilityError(windowsAvailabilityProcessOpen, err)
	}
	defer windows.CloseHandle(process)
	if err := verifiedWindowsProcessImage(process, path); err != nil {
		return 0, err
	}
	return status.ProcessId, nil
}

func verifiedWindowsProcessImage(process windows.Handle, path string) error {
	var length uint32 = 32768
	buffer := make([]uint16, length)
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &length); err != nil {
		return windowsAvailabilityError(windowsAvailabilityProcessImage, err)
	}
	image := windows.UTF16ToString(buffer[:length])
	if !strings.EqualFold(image, path) {
		return windowsAvailabilityError(windowsAvailabilityImagePath, nil)
	}
	actual, err := os.Stat(image)
	if err != nil {
		return windowsAvailabilityError(windowsAvailabilityImageStat, err)
	}
	expected, err := os.Stat(path)
	if err != nil {
		return windowsAvailabilityError(windowsAvailabilityExpectedStat, err)
	}
	if !os.SameFile(actual, expected) {
		return windowsAvailabilityError(windowsAvailabilityImageBinding, nil)
	}
	return nil
}
func brokerAvailable(ctx context.Context, paths nativePaths) bool {
	available, _ := windowsBrokerAvailability(ctx)
	return available
}
func brokerRequest(ctx context.Context, _ nativePaths, id string) error {
	reply, err := pipeCall(ctx, "activate "+id)
	if err != nil {
		return err
	}
	if reply != "accepted" {
		return errors.New("native Windows activation was refused")
	}
	return nil
}
func pipeCall(ctx context.Context, command string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	pipeName, _ := windows.UTF16PtrFromString(updaterPipe)
	var handle windows.Handle
	var err error
	for {
		handle, err = windows.CreateFile(pipeName, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return "", windowsAvailabilityError(windowsAvailabilityPipeOpen, errors.Join(ctx.Err(), err))
		case <-time.After(50 * time.Millisecond):
		}
	}
	defer windows.CloseHandle(handle)
	if err := verifiedWindowsUpdaterPipe(handle); err != nil {
		return "", err
	}
	mode := uint32(windows.PIPE_NOWAIT | windows.PIPE_READMODE_MESSAGE)
	if err := windows.SetNamedPipeHandleState(handle, &mode, nil, nil); err != nil {
		return "", windowsAvailabilityError(windowsAvailabilityPipeMode, err)
	}
	var written uint32
	if err := windows.WriteFile(handle, []byte(command), &written, nil); err != nil {
		return "", windowsAvailabilityError(windowsAvailabilityPipeWrite, err)
	}
	if written != uint32(len(command)) {
		return "", windowsAvailabilityError(windowsAvailabilityPipeWriteCount, nil)
	}
	buffer := make([]byte, 128)
	for {
		var count uint32
		err := windows.ReadFile(handle, buffer, &count, nil)
		if err == nil && count > 0 {
			return string(buffer[:count]), nil
		}
		if err != nil && err != windows.ERROR_NO_DATA && err != windows.ERROR_PIPE_LISTENING {
			return "", windowsAvailabilityError(windowsAvailabilityPipeRead, err)
		}
		select {
		case <-ctx.Done():
			return "", windowsAvailabilityError(windowsAvailabilityPipeReadWait, errors.Join(ctx.Err(), err))
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func verifiedWindowsUpdaterPipe(handle windows.Handle) error {
	var serverPID uint32
	if err := windows.GetNamedPipeServerProcessId(handle, &serverPID); err != nil {
		return windowsAvailabilityError(windowsAvailabilityPipeServerPID, err)
	}
	expected, err := verifiedServicePID(updaterService, platformPaths().Updater)
	if err != nil {
		return err
	}
	if serverPID != expected {
		return windowsAvailabilityError(windowsAvailabilityServerBinding, nil)
	}
	return nil
}

// Availability diagnostics are read-only and fixed-field. They distinguish the
// failed check without exposing error text, paths, PIDs, SIDs or pipe payloads.
type windowsAvailabilityStage uint8

const (
	windowsAvailabilityTrust windowsAvailabilityStage = iota + 1
	windowsAvailabilityPipeOpen
	windowsAvailabilityPipeServerPID
	windowsAvailabilitySCMConnect
	windowsAvailabilityServiceOpen
	windowsAvailabilityServiceQuery
	windowsAvailabilityServiceState
	windowsAvailabilityProcessOpen
	windowsAvailabilityProcessImage
	windowsAvailabilityImagePath
	windowsAvailabilityImageStat
	windowsAvailabilityExpectedStat
	windowsAvailabilityImageBinding
	windowsAvailabilityServerBinding
	windowsAvailabilityPipeMode
	windowsAvailabilityPipeWrite
	windowsAvailabilityPipeWriteCount
	windowsAvailabilityPipeRead
	windowsAvailabilityPipeReadWait
	windowsAvailabilityServerRefused
	windowsAvailabilityUnexpectedReply
	windowsAvailabilityServerTrust
	windowsAvailabilityServerState
	windowsAvailabilityServerInstalled
	windowsAvailabilityServerPending
	windowsAvailabilityLegacyDisabled
	windowsAvailabilityLegacyBusy
	windowsAvailabilityInternal
)

type windowsAvailabilityFailure struct {
	stage windowsAvailabilityStage
	cause error
}

func (e *windowsAvailabilityFailure) code() uint32 {
	stage := e.stage
	if stage < windowsAvailabilityTrust || stage > windowsAvailabilityInternal {
		stage = windowsAvailabilityInternal
	}
	var native windows.Errno
	var code uint32
	if errors.As(e.cause, &native) && native <= 0xffff {
		code = uint32(native)
	}
	return 0x4e000000 | uint32(stage)<<16 | code
}
func (e *windowsAvailabilityFailure) Error() string {
	return fmt.Sprintf("Native updater unavailable (Windows code=0x%08x).", e.code())
}
func (e *windowsAvailabilityFailure) Unwrap() error { return e.cause }

func windowsAvailabilityError(stage windowsAvailabilityStage, cause error) error {
	return &windowsAvailabilityFailure{stage, cause}
}

// AvailabilityStatus is the optional single-probe interface used by the
// authenticated status composition. It retains the legacy canonical/trust
// prerequisites and never caches a last error across requests.
func (b *nativeBroker) AvailabilityStatus(ctx context.Context) (bool, string) {
	if !b.matches {
		return false, b.UnavailableReason()
	}
	if _, err := LoadNativeTrust(ctx); err != nil {
		return false, windowsAvailabilityError(windowsAvailabilityTrust, err).Error()
	}
	return windowsBrokerAvailability(ctx)
}

func windowsBrokerAvailability(ctx context.Context) (bool, string) {
	reply, err := pipeCall(ctx, "status")
	return windowsAvailabilityResult(reply, err)
}

func windowsAvailabilityResult(reply string, err error) (bool, string) {
	if err != nil {
		var failure *windowsAvailabilityFailure
		if errors.As(err, &failure) {
			return false, failure.Error()
		}
		return false, windowsAvailabilityError(windowsAvailabilityInternal, err).Error()
	}
	var stage windowsAvailabilityStage
	switch reply {
	case "ready":
		return true, ""
	case "refused":
		stage = windowsAvailabilityServerRefused
	case "disabled":
		stage = windowsAvailabilityLegacyDisabled
	case "busy":
		stage = windowsAvailabilityLegacyBusy
	default:
		if failure, ok := parseWindowsStatusFailure(reply); ok {
			return false, failure.Error()
		}
		stage = windowsAvailabilityUnexpectedReply
	}
	return false, windowsAvailabilityError(stage, nil).Error()
}

func windowsStatusFailure(stage windowsAvailabilityStage, cause error) string {
	return fmt.Sprintf("unavailable:%08x", (&windowsAvailabilityFailure{stage, cause}).code())
}

func parseWindowsStatusFailure(reply string) (*windowsAvailabilityFailure, bool) {
	const prefix = "unavailable:"
	if len(reply) != len(prefix)+8 || !strings.HasPrefix(reply, prefix) {
		return nil, false
	}
	code, err := strconv.ParseUint(reply[len(prefix):], 16, 32)
	if err != nil || code>>24 != 0x4e || fmt.Sprintf("%s%08x", prefix, code) != reply {
		return nil, false
	}
	stage := windowsAvailabilityStage(code >> 16)
	// Only the four status-only server checks can author these replies. A
	// forged local stage, arbitrary text or another protocol reply is refused.
	if stage < windowsAvailabilityServerTrust || stage > windowsAvailabilityServerPending {
		return nil, false
	}
	return &windowsAvailabilityFailure{stage, windows.Errno(code & 0xffff)}, true
}

type windowsDriver struct {
	paths nativePaths
	prior Installed
}

func (d windowsDriver) Fence(ctx context.Context, root *os.Root) error {
	return syncPrivilegedRoot(ctx, root)
}
func (d windowsDriver) Stop(ctx context.Context) error {
	m, s, err := openSCMService("MinecraftUI")
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	actions, e := s.RecoveryActions()
	if e != nil {
		return e
	}
	for _, action := range actions {
		if action.Type != mgr.NoAction {
			return errors.New("disable custom manager SCM recovery actions before self-update")
		}
	}
	status, err := s.Query()
	if err != nil {
		return err
	}
	if status.State == svc.Stopped {
		return nil
	}
	if _, err := s.Control(svc.Stop); err != nil {
		return err
	}
	for {
		status, err = s.Query()
		if err != nil {
			return err
		}
		if status.State == svc.Stopped {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
func (d windowsDriver) Start(ctx context.Context) error {
	m, s, err := openSCMService("MinecraftUI")
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	return s.Start()
}
func (d windowsDriver) LockManager(ctx context.Context) (func(), error) {
	for {
		release, err := launcher.AcquireManager(d.paths.Data)
		if err == nil {
			return release, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
func (d windowsDriver) WriteChallenge(ctx context.Context, c Challenge) error {
	root, err := safepath.OpenRoot(ctx, controlDirectory(d.paths.Data))
	if err != nil {
		return err
	}
	defer root.Close()
	for _, name := range []string{"challenge.json", "drained.json", "drained.pending", "probe.json", "probe.pending"} {
		if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	body, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := writeFile(ctx, root, "challenge.json", bytesReader(body), int64(len(body)), ""); err != nil {
		return err
	}
	return syncPrivilegedRoot(ctx, root)
}
func (d windowsDriver) ClearControl(ctx context.Context) error {
	root, err := safepath.OpenRoot(ctx, controlDirectory(d.paths.Data))
	if err != nil {
		return err
	}
	defer root.Close()
	for _, name := range []string{"challenge.json", "drained.json", "drained.pending", "probe.json", "probe.pending", "request.json"} {
		if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return syncPrivilegedRoot(ctx, root)
}
func (d windowsDriver) ReadDrain(ctx context.Context) (DrainReceipt, error) {
	var receipt DrainReceipt
	root, err := safepath.OpenRoot(ctx, controlDirectory(d.paths.Data))
	if err != nil {
		return receipt, err
	}
	defer root.Close()
	body, err := readChecked(ctx, root, "drained.json", 2048)
	if err != nil {
		return receipt, err
	}
	if strictJSON(body, &receipt) != nil || receipt.Format != 1 || time.Since(receipt.CreatedAt) < 0 || time.Since(receipt.CreatedAt) > 3*time.Minute {
		return receipt, errors.New("invalid Windows drain receipt")
	}
	return receipt, nil
}
func (d windowsDriver) ProbeSchema(ctx context.Context, previous bool) (Capability, error) {
	source := d.paths.Binary
	if previous {
		source += ".previous"
	}
	return d.probe(ctx, source, d.prior, false)
}
func (d windowsDriver) ProbeCandidate(ctx context.Context, expected Installed) (Capability, error) {
	return d.probe(ctx, d.paths.Binary+".next", expected, true)
}
func (d windowsDriver) probe(ctx context.Context, source string, expectedImage Installed, capabilityOnly bool) (Capability, error) {
	var result Capability
	bin, err := openProtectedRoot(ctx, d.paths.Bin)
	if err != nil {
		logWindowsProbeFailure("bin_open", err)
		return result, err
	}
	defer bin.Close()
	probe := "minecraft-ui-probe.exe"
	if err := bin.Remove(probe); err != nil && !errors.Is(err, os.ErrNotExist) {
		logWindowsProbeFailure("probe_remove", err)
		return result, err
	}
	if err := copyVerified(ctx, bin, source, bin, probe, expectedImage); err != nil {
		logWindowsProbeFailure("probe_copy", err)
		return result, err
	}
	if err := d.Fence(ctx, bin); err != nil {
		logWindowsProbeFailure("bin_fence", err)
		return result, err
	}
	control, err := safepath.OpenRoot(ctx, controlDirectory(d.paths.Data))
	if err != nil {
		logWindowsProbeFailure("control_open", err)
		return result, err
	}
	defer control.Close()
	for _, name := range []string{"probe.json", "probe.pending", "capability.json"} {
		if err := control.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			logWindowsProbeFailure("control_remove", err)
			return result, err
		}
	}
	challenge, err := ReadChallenge(ctx, d.paths.Data)
	if err != nil {
		logWindowsProbeFailure("challenge_read", err)
		return result, err
	}
	if capabilityOnly {
		request := CandidateProbeRequest{Format: 1, Nonce: challenge.Nonce, Version: expectedImage.Version, Schema: expectedImage.Schema, SHA256: expectedImage.SHA256}
		body, e := json.Marshal(request)
		if e != nil {
			logWindowsProbeFailure("capability_encode", e)
			return result, e
		}
		if e := writeFile(ctx, control, "capability.json", bytesReader(body), int64(len(body)), ""); e != nil {
			logWindowsProbeFailure("capability_write", e)
			return result, e
		}
	}
	defer control.Remove("capability.json")
	m, service, err := openSCMService(probeService)
	if err != nil {
		logWindowsProbeFailure("service_open", err)
		return result, err
	}
	defer m.Disconnect()
	defer service.Close()
	configuration, err := service.Config()
	expected := `"` + filepath.Join(d.paths.Bin, probe) + `" update-schema-service`
	if err != nil {
		logWindowsProbeFailure("service_config", err)
		return result, errors.New("fixed Windows schema probe service binding mismatch")
	}
	if !strings.EqualFold(configuration.BinaryPathName, expected) {
		failure := errors.New("fixed Windows schema probe service binding mismatch")
		logWindowsProbeFailure("service_binding", failure)
		return result, failure
	}
	accountSID, e := (&windowsUpdater{paths: d.paths}).accountSID(ctx)
	if e != nil {
		logWindowsProbeFailure("account_sid", e)
		return result, e
	}
	// The helper retains the exact configuredSID.String() != accountSID rejection.
	if e := verifyConfiguredServiceSID(configuration.ServiceStartName, accountSID, windows.ComputerName, windows.LookupSID); e != nil {
		logWindowsProbeFailure("configured_sid", e)
		return result, errors.New("schema probe must run as the configured unprivileged manager account")
	}
	actions, e := service.RecoveryActions()
	if e != nil {
		logWindowsProbeFailure("recovery_actions", e)
		return result, e
	}
	for _, action := range actions {
		if action.Type != mgr.NoAction {
			failure := errors.New("schema probe recovery actions must be disabled")
			logWindowsProbeFailure("recovery_action_check", failure)
			return result, failure
		}
	}

	if err := service.Start(); err != nil {
		logWindowsProbeFailure("service_start", err)
		return result, err
	}
	deadline, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for {
		status, err := service.Query()
		if err != nil {
			logWindowsProbeFailure("service_query", err)
			return result, err
		}
		if status.State == svc.Stopped {
			logWindowsProbeStopped(status)
			break
		}
		select {
		case <-deadline.Done():
			_, _ = service.Control(svc.Stop)
			failure := errors.New("Windows schema probe timed out")
			logWindowsProbeFailure("service_timeout", failure)
			return result, failure
		case <-time.After(50 * time.Millisecond):
		}
	}
	body, err := readChecked(ctx, control, "probe.json", 4096)
	if err != nil {
		logWindowsProbeFailure("result_read", err)
		return result, err
	}
	var report ProbeResult
	if strictJSON(body, &report) != nil || report.Format != 1 || report.Nonce != challenge.Nonce || report.Version != expectedImage.Version || report.ExecutableSHA256 != expectedImage.SHA256 {
		failure := errors.New("Windows schema probe identity mismatch")
		logWindowsProbeFailure("result_validate", failure)
		return result, failure
	}
	authorized := report.ActorAuthorized
	return Capability{Format: 1, Version: report.Version, Schema: report.Schema, ActorAuthorized: &authorized}, nil
}
func (d windowsDriver) WaitHealthy(ctx context.Context, installed Installed, nonce string) error {
	deadline, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("health redirect forbidden") }}
	defer client.CloseIdleConnections()
	for {
		if _, err := verifiedServicePID("MinecraftUI", filepath.Join(d.paths.Bin, d.paths.Binary)); err == nil {
			req, _ := http.NewRequestWithContext(deadline, http.MethodGet, "http://127.0.0.1:8080/internal/update-ready", nil)
			req.Header.Set("Authorization", "Bearer "+nonce)
			resp, err := client.Do(req)
			if err == nil {
				body, e := io.ReadAll(io.LimitReader(resp.Body, 4097))
				resp.Body.Close()
				var report Readiness
				if e == nil && len(body) <= 4096 && resp.StatusCode == 200 && strictJSON(body, &report) == nil && report.Format == 1 && report.Status == "ready" && report.Version == installed.Version && report.Schema == installed.Schema && report.ExecutableSHA256 == installed.SHA256 {
					return nil
				}
			}
		}
		select {
		case <-deadline.Done():
			return errors.New("Windows manager readiness gate failed")
		case <-time.After(250 * time.Millisecond):
		}
	}
}

type windowsUpdater struct {
	ctx   context.Context
	paths nativePaths
	mu    sync.Mutex
	busy  bool
}

// SCM service-specific failure codes contain only a fixed operation and an
// optional 16-bit Win32 code. Never include error text, paths, SIDs or requests.
const (
	windowsUpdaterRecovery uint8 = iota + 1
	windowsUpdaterRecoveryDeadline
	windowsUpdaterAccount
	windowsUpdaterDescriptor
	windowsUpdaterPipeCreate
	windowsUpdaterPipeConnect
	windowsUpdaterProcessSecurity
)

type windowsUpdaterFailure struct {
	stage uint8
	cause error
}

func (e *windowsUpdaterFailure) Error() string { return "native Windows updater operation failed" }
func (e *windowsUpdaterFailure) Unwrap() error { return e.cause }

func windowsUpdaterExitCode(err error) uint32 {
	var failure *windowsUpdaterFailure
	stage := uint8(255)
	if errors.As(err, &failure) {
		stage = failure.stage
	}
	var native windows.Errno
	var code uint32
	if errors.As(err, &native) && native <= 0xffff {
		code = uint32(native)
	}
	return 0x4d000000 | uint32(stage)<<16 | code
}

func (s *windowsUpdater) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	recoveryCtx, recoveryCancel := context.WithTimeout(ctx, 4*time.Minute)
	defer recoveryCancel()
	recovered := make(chan error, 1)
	go func() {
		if err := s.prepareProcessQuery(recoveryCtx); err != nil {
			recovered <- &windowsUpdaterFailure{windowsUpdaterProcessSecurity, err}
			return
		}
		recovered <- s.recover(recoveryCtx)
	}()
	progress := time.NewTicker(10 * time.Second)
	defer progress.Stop()
	checkpoint := uint32(1)
recoveryLoop:
	for {
		select {
		case err := <-recovered:
			if err != nil {
				var startup *windowsUpdaterFailure
				if errors.As(err, &startup) {
					return true, windowsUpdaterExitCode(err)
				}
				return true, windowsUpdaterExitCode(&windowsUpdaterFailure{windowsUpdaterRecovery, err})
			}
			break recoveryLoop
		case <-recoveryCtx.Done():
			return true, windowsUpdaterExitCode(&windowsUpdaterFailure{windowsUpdaterRecoveryDeadline, recoveryCtx.Err()})
		case <-progress.C:
			checkpoint++
			status <- svc.Status{State: svc.StartPending, CheckPoint: checkpoint, WaitHint: 60000}
		}
	}
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	done := make(chan error, 1)
	go func() { done <- s.servePipe(ctx) }()
	for {
		select {
		case err := <-done:
			if err != nil {
				return true, windowsUpdaterExitCode(err)
			}
			return false, 0
		case change := <-requests:
			if change.Cmd == svc.Stop || change.Cmd == svc.Shutdown {
				cancel()
				status <- svc.Status{State: svc.StopPending}
				<-done
				return false, 0
			}
		case <-ctx.Done():
			return false, 0
		}
	}
}

// The only production caller binds this grant to its own process and the
// protected installer receipt, before publishing Running or accepting commands.
func (s *windowsUpdater) prepareProcessQuery(ctx context.Context) error {
	sid, err := s.accountSID(ctx)
	if err != nil {
		return err
	}
	manager, err := managerProcessQuerySID(sid)
	if err != nil {
		return err
	}
	return grantUpdaterProcessQuery(ctx, windows.CurrentProcess(), manager)
}

func managerProcessQuerySID(value string) (*windows.SID, error) {
	// Parse the textual boundary before handing it to x/sys. Its StringToSid
	// implementation copies the returned SID into Go-owned memory, so the
	// SubAuthority accessors (which dereference pointers returned by Win32) are
	// unsafe under checkptr for that value. The exact shape and canonical
	// decimal fields below prove the same account boundary without those
	// accessors; StringToSid and String still validate the Windows SID itself.
	parts := strings.Split(value, "-")
	if len(parts) != 8 || parts[0] != "S" || parts[1] != "1" || parts[2] != "5" || parts[3] != "21" {
		return nil, errors.New("conventional unprivileged manager account SID is required")
	}
	var fields [4]uint32
	for i, part := range parts[4:] {
		parsed, err := strconv.ParseUint(part, 10, 32)
		if err != nil || strconv.FormatUint(parsed, 10) != part {
			return nil, errors.New("conventional unprivileged manager account SID is required")
		}
		fields[i] = uint32(parsed)
	}
	if fields[3] < 1000 {
		return nil, errors.New("conventional unprivileged manager account SID is required")
	}

	sid, err := windows.StringToSid(value)
	if err != nil || sid == nil || !sid.IsValid() || sid.String() != value {
		return nil, errors.New("conventional unprivileged manager account SID is required")
	}
	return sid, nil
}

func processQueryACL(sd *windows.SECURITY_DESCRIPTOR, manager *windows.SID, requireGrant bool) (*windows.ACL, []string, error) {
	unsafeACL := func() (*windows.ACL, []string, error) {
		return nil, nil, errors.New("updater process ACL requires explicit review")
	}
	if sd == nil || !sd.IsValid() || manager == nil || !manager.IsValid() {
		return unsafeACL()
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || owner.String() != "S-1-5-18" && owner.String() != "S-1-5-32-544" {
		return unsafeACL()
	}
	group, _, err := sd.Group()
	if err != nil || group == nil || !group.IsValid() {
		return unsafeACL()
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil || dacl.AceCount == 0 {
		return unsafeACL()
	}
	var entries []string
	managerCount := 0
	seenAllow, system, admins := false, false, false
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return nil, nil, err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE && ace.Header.AceType != windows.ACCESS_DENIED_ACE_TYPE || ace.Header.AceSize < 16 || ace.Header.AceFlags&^uint8(windows.VALID_INHERIT_FLAGS) != 0 {
			return unsafeACL()
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		// Bound the SID before asking the native validator to inspect it.
		count := *(*byte)(unsafe.Add(unsafe.Pointer(sid), 1))
		if 16+uint32(count)*4 != uint32(ace.Header.AceSize) || !sid.IsValid() {
			return unsafeACL()
		}
		effective := ace.Header.AceFlags&windows.INHERIT_ONLY_ACE == 0
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			// Do not merge into noncanonical ordering or override a denial
			// which could apply through any of the manager token's groups.
			if seenAllow || sid.Equals(manager) || effective && uint32(ace.Mask)&(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_QUERY_INFORMATION|windows.GENERIC_ALL|windows.GENERIC_READ) != 0 {
				return unsafeACL()
			}
		} else {
			seenAllow = true
			switch sid.String() {
			case "S-1-5-18":
				system = system || effective && ace.Mask != 0
			case "S-1-5-32-544":
				admins = admins || effective && ace.Mask != 0
			default:
				if !sid.Equals(manager) || ace.Mask != windows.PROCESS_QUERY_LIMITED_INFORMATION || ace.Header.AceFlags != 0 {
					return unsafeACL()
				}
				managerCount++
			}
		}
		entries = append(entries, string(unsafe.Slice((*byte)(unsafe.Pointer(ace)), int(ace.Header.AceSize))))
	}
	if !system || !admins || managerCount > 1 || requireGrant && managerCount != 1 {
		return unsafeACL()
	}
	return dacl, entries, nil
}

func mergeUpdaterProcessQuery(sd *windows.SECURITY_DESCRIPTOR, manager *windows.SID) (*windows.ACL, error) {
	if manager == nil {
		return nil, errors.New("manager process SID is required")
	}
	if _, err := managerProcessQuerySID(manager.String()); err != nil {
		return nil, err
	}
	dacl, _, err := processQueryACL(sd, manager, false)
	if err != nil {
		return nil, err
	}
	if _, _, err := processQueryACL(sd, manager, true); err == nil {
		return dacl, nil // Exact preexisting grant is already sufficient.
	}
	var pin runtime.Pinner
	pin.Pin(manager)
	defer pin.Unpin()
	return windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.PROCESS_QUERY_LIMITED_INFORMATION,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee:           windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_USER, TrusteeValue: windows.TrusteeValueFromSID(manager)},
	}}, dacl)
}

func grantUpdaterProcessQuery(ctx context.Context, process windows.Handle, manager *windows.SID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	const information = windows.OWNER_SECURITY_INFORMATION | windows.GROUP_SECURITY_INFORMATION | windows.DACL_SECURITY_INFORMATION
	before, err := windows.GetSecurityInfo(process, windows.SE_KERNEL_OBJECT, information)
	if err != nil {
		return err
	}
	_, original, err := processQueryACL(before, manager, false)
	if err != nil {
		return err
	}
	merged, err := mergeUpdaterProcessQuery(before, manager)
	if err != nil {
		return err
	}
	// Build a descriptor only for validation/snapshot; publish only its DACL.
	owner, _, _ := before.Owner()
	group, _, _ := before.Group()
	control, _, err := before.Control()
	if err != nil {
		return err
	}
	candidate, err := windows.NewSecurityDescriptor()
	if err != nil {
		return err
	}
	if err := candidate.SetOwner(owner, false); err != nil {
		return err
	}
	if err := candidate.SetGroup(group, false); err != nil {
		return err
	}
	if err := candidate.SetDACL(merged, true, false); err != nil {
		return err
	}
	_, expected, err := processQueryACL(candidate, manager, true)
	if err != nil {
		return err
	}
	if !preservesProcessQueryEntries(original, expected) {
		return errors.New("updater process ACL merge changed existing permissions")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := windows.SetSecurityInfo(process, windows.SE_KERNEL_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, merged, nil); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	after, err := windows.GetSecurityInfo(process, windows.SE_KERNEL_OBJECT, information)
	if err != nil {
		return err
	}
	_, actual, err := processQueryACL(after, manager, true)
	if err != nil {
		return err
	}
	afterOwner, _, _ := after.Owner()
	afterGroup, _, _ := after.Group()
	afterControl, _, err := after.Control()
	if err != nil || !owner.Equals(afterOwner) || !group.Equals(afterGroup) || control != afterControl || len(expected) != len(actual) {
		return errors.New("published updater process security differs from the required grant")
	}
	for index := range expected {
		if expected[index] != actual[index] {
			return errors.New("published updater process ACL differs from the required grant")
		}
	}
	return ctx.Err()
}

func preservesProcessQueryEntries(original, merged []string) bool {
	if len(merged) < len(original) || len(merged) > len(original)+1 {
		return false
	}
	index := 0
	for _, entry := range merged {
		if index < len(original) && original[index] == entry {
			index++
		}
	}
	return index == len(original)
}

func (s *windowsUpdater) recover(ctx context.Context) error {
	state, err := openProtectedRoot(ctx, s.paths.State)
	if err != nil {
		return err
	}
	defer state.Close()
	if _, err := state.Lstat("activation.json"); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	release, err := launcher.AcquireManager(s.paths.State)
	if err != nil {
		return err
	}
	defer release()
	installed, err := readInstalled(ctx, state)
	if err != nil {
		return err
	}
	bin, err := openProtectedRoot(ctx, s.paths.Bin)
	if err != nil {
		return err
	}
	defer bin.Close()
	worker := activationWorker{state: state, bin: bin, binary: s.paths.Binary, driver: windowsDriver{s.paths, installed}}
	journal, e := worker.load(ctx)
	if e != nil {
		return e
	}
	worker.driver = windowsDriver{s.paths, journal.Prior}
	return worker.recover(ctx)
}
func (s *windowsUpdater) accountSID(ctx context.Context) (string, error) {
	state, err := openProtectedRoot(ctx, s.paths.State)
	if err != nil {
		return "", err
	}
	defer state.Close()
	if err := protectedFile(state, "account.json"); err != nil {
		return "", err
	}
	body, err := readChecked(ctx, state, "account.json", 1024)
	if err != nil {
		return "", err
	}
	var account struct {
		SID string `json:"sid"`
	}
	if strictJSON(body, &account) != nil {
		return "", errors.New("invalid native service account receipt")
	}
	sid, err := windows.StringToSid(account.SID)
	if err != nil || sid.String() == "S-1-5-18" || sid.String() == "S-1-5-32-544" {
		return "", errors.New("unprivileged manager SID is required")
	}
	return sid.String(), nil
}
func (s *windowsUpdater) servePipe(ctx context.Context) error {
	sid, err := s.accountSID(ctx)
	if err != nil {
		return &windowsUpdaterFailure{windowsUpdaterAccount, err}
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;" + sid + ")")
	if err != nil {
		return &windowsUpdaterFailure{windowsUpdaterDescriptor, err}
	}
	name, _ := windows.UTF16PtrFromString(updaterPipe)
	for {
		handle, err := createWindowsUpdaterPipe(name, descriptor)
		if err != nil {
			return &windowsUpdaterFailure{windowsUpdaterPipeCreate, err}
		}
		if err := connectWindowsUpdaterPipe(ctx, handle); err != nil {
			windows.CloseHandle(handle)
			if err == ctx.Err() {
				return nil
			}
			return &windowsUpdaterFailure{windowsUpdaterPipeConnect, err}
		}
		var clientPID uint32
		reply := "refused"
		if windows.GetNamedPipeClientProcessId(handle, &clientPID) == nil {
			expected, e := verifiedServicePID("MinecraftUI", filepath.Join(s.paths.Bin, s.paths.Binary))
			if e == nil && clientPID == expected {
				buffer := make([]byte, 128)
				deadline := time.Now().Add(2 * time.Second)
				for time.Now().Before(deadline) {
					var n uint32
					e := windows.ReadFile(handle, buffer, &n, nil)
					if e == nil && n > 0 {
						reply = s.accept(ctx, string(buffer[:n]))
						break
					}
					if e != windows.ERROR_NO_DATA && e != windows.ERROR_PIPE_LISTENING {
						break
					}
					time.Sleep(25 * time.Millisecond)
				}
			}
		}
		var n uint32
		_ = windows.WriteFile(handle, []byte(reply), &n, nil)
		flushed := make(chan error, 1)
		go func() { flushed <- windows.FlushFileBuffers(handle) }()
		select {
		case <-flushed:
		case <-time.After(2 * time.Second):
			_ = windows.CloseHandle(handle)
			<-flushed
			handle = windows.InvalidHandle
		}
		_ = windows.DisconnectNamedPipe(handle)
		_ = windows.CloseHandle(handle)
		select {
		case <-ctx.Done():
			return nil
		default:
		}
	}
}

// Keep the production flags and connection-state handling in the same helpers
// exercised by native tests on uniquely named pipes.
func createWindowsUpdaterPipe(name *uint16, descriptor *windows.SECURITY_DESCRIPTOR) (windows.Handle, error) {
	security := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	return windows.CreateNamedPipe(name, windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_FIRST_PIPE_INSTANCE, windows.PIPE_TYPE_MESSAGE|windows.PIPE_READMODE_MESSAGE|windows.PIPE_NOWAIT|windows.PIPE_REJECT_REMOTE_CLIENTS, 1, 128, 128, 1000, &security)
}

func connectWindowsUpdaterPipe(ctx context.Context, handle windows.Handle) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := windows.ConnectNamedPipe(handle, nil)
		if err == nil || err == windows.ERROR_PIPE_CONNECTED {
			return nil
		}
		if err == windows.ERROR_NO_DATA {
			// A client connected and closed before accept. Reset the same
			// first-instance handle; its private DACL and name ownership stay
			// intact. This connection never reaches command authorization.
			if err := windows.DisconnectNamedPipe(handle); err != nil {
				return err
			}
		} else if err != windows.ERROR_PIPE_LISTENING {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (s *windowsUpdater) accept(ctx context.Context, command string) string {
	if command == "status" {
		if _, err := LoadNativeTrust(ctx); err != nil {
			return windowsStatusFailure(windowsAvailabilityServerTrust, err)
		}
		state, err := openProtectedRoot(ctx, s.paths.State)
		if err != nil {
			return windowsStatusFailure(windowsAvailabilityServerState, err)
		}
		defer state.Close()
		if _, err := readInstalled(ctx, state); err != nil {
			return windowsStatusFailure(windowsAvailabilityServerInstalled, err)
		}
		if err := checkPendingNative(ctx, state); err != nil {
			return windowsStatusFailure(windowsAvailabilityServerPending, err)
		}
		return "ready"
	}
	parts := strings.Split(command, " ")
	if len(parts) != 2 || parts[0] != "activate" || !validHash(parts[1]) {
		return "refused"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy {
		return "busy"
	}
	state, err := openProtectedRoot(ctx, s.paths.State)
	if err != nil {
		return "refused"
	}
	defer state.Close()
	release, err := launcher.AcquireManager(s.paths.State)
	if err != nil {
		return "busy"
	}
	installed, err := readInstalled(ctx, state)
	if err != nil {
		release()
		return "refused"
	}
	bin, err := openProtectedRoot(ctx, s.paths.Bin)
	if err != nil {
		release()
		return "refused"
	}
	worker := activationWorker{state: state, bin: bin, binary: s.paths.Binary, driver: windowsDriver{s.paths, installed}}
	err = worker.cleanupTerminal(ctx, installed)
	bin.Close()
	if err != nil {
		release()
		return "refused"
	}
	if _, err := copyRequest(ctx, s.paths, state, parts[1]); err != nil {
		release()
		return "refused"
	}
	release()
	s.busy = true
	go func() {
		defer func() { s.mu.Lock(); s.busy = false; s.mu.Unlock() }()
		operation, cancel := context.WithTimeout(ctx, 9*time.Minute)
		defer cancel()
		_ = s.activate(operation)
	}()
	return "accepted"
}
func (s *windowsUpdater) activate(ctx context.Context) error {
	state, err := openProtectedRoot(ctx, s.paths.State)
	if err != nil {
		return err
	}
	defer state.Close()
	release, err := launcher.AcquireManager(s.paths.State)
	if err != nil {
		return err
	}
	defer release()
	trust, err := LoadNativeTrust(ctx)
	if err != nil {
		return err
	}
	installed, err := readInstalled(ctx, state)
	if err != nil {
		return err
	}
	request, err := readRequest(ctx, state)
	if err != nil {
		return err
	}
	candidate, next, err := importCandidate(ctx, s.paths, trust, installed, request)
	if err != nil {
		return err
	}
	defer candidate.Close()
	bin, err := openProtectedRoot(ctx, s.paths.Bin)
	if err != nil {
		return err
	}
	defer bin.Close()
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	worker := activationWorker{state: state, bin: bin, binary: s.paths.Binary, driver: windowsDriver{s.paths, installed}}
	return worker.activate(ctx, activationJournal{Request: request, Nonce: hex.EncodeToString(nonce), Prior: installed, Candidate: next}, candidate)
}
func RunNativeUpdater(ctx context.Context, args []string, out io.Writer) error {
	paths := platformPaths()
	if len(args) == 1 && args[0] == "service" {
		var token windows.Token
		if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
			return err
		}
		defer token.Close()
		user, err := token.GetTokenUser()
		if err != nil || user.User.Sid.String() != "S-1-5-18" {
			return errors.New("Windows updater service must run as LocalSystem")
		}
		return svc.Run(updaterService, &windowsUpdater{ctx: ctx, paths: paths})
	}
	if len(args) == 2 && args[0] == "bootstrap" {
		sid, err := windows.StringToSid(args[1])
		if err != nil {
			return err
		}
		state, err := openProtectedRoot(ctx, paths.State)
		if err != nil {
			return err
		}
		defer state.Close()
		if _, err := state.Lstat("installed.json"); !errors.Is(err, os.ErrNotExist) {
			return errors.New("bootstrap receipt already exists")
		}
		if err := protectedFile(state, "bootstrap-capability.json"); err != nil {
			return err
		}
		body, err := readChecked(ctx, state, "bootstrap-capability.json", 2048)
		if err != nil {
			return err
		}
		var capability Capability
		if strictJSON(body, &capability) != nil || capability.Format != 1 || capability.Schema < 1 {
			return errors.New("invalid native installer capability")
		}
		bin, err := openProtectedRoot(ctx, paths.Bin)
		if err != nil {
			return err
		}
		defer bin.Close()
		hash, err := hashFile(ctx, bin, paths.Binary)
		if err != nil {
			return err
		}
		worker := activationWorker{state: state}
		if err := worker.recordInstalled(ctx, Installed{Format: 1, Version: capability.Version, Schema: capability.Schema, SHA256: hash}); err != nil {
			return err
		}
		account, _ := json.Marshal(map[string]string{"sid": sid.String()})
		if err := writeFile(ctx, state, "account.json", bytesReader(account), int64(len(account)), ""); err != nil {
			return err
		}
		adminSID, err := windows.StringToSid("S-1-5-32-544")
		if err != nil {
			return err
		}
		for _, name := range []string{"installed.json", "account.json"} {
			if err := windows.SetNamedSecurityInfo(filepath.Join(paths.State, name), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION, adminSID, nil, nil, nil); err != nil {
				return err
			}
		}
		if err := state.Remove("bootstrap-capability.json"); err != nil {
			return err
		}
		return syncPrivilegedRoot(ctx, state)
	}
	return errors.New("Windows native updater accepts only fixed service or administrator bootstrap")
}

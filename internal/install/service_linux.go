//go:build linux

package install

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

const (
	linuxManagerUnit           = "minecraft-ui.service"
	linuxManagerExecutablePath = "/usr/local/bin/minecraft-ui"
	linuxServiceMaxOutput      = 256 << 10
	linuxServiceCommandTimeout = 30 * time.Second
	linuxManagerStopTimeout    = 155 * time.Second
	linuxManagerStartTimeout   = 6 * time.Minute
	linuxManagerImageMaxBytes  = 256 << 20
	linuxProcStatMaxBytes      = 4096
)

type linuxServiceStatus struct {
	LoadState, ActiveState, SubState, UnitFileState string
	MainPID                                         int
}

type linuxServiceTestHooks struct {
	run      func(context.Context, string, ...string) ([]byte, error)
	stat     func(string) (os.FileInfo, error)
	lstat    func(string) (os.FileInfo, error)
	readFile func(string) ([]byte, error)
	open     func(string) (*os.File, error)
}

type linuxServiceHooksKey struct{}

func withLinuxServiceTestHooks(ctx context.Context, hooks linuxServiceTestHooks) context.Context {
	return context.WithValue(ctx, linuxServiceHooksKey{}, hooks)
}

func serviceHooks(ctx context.Context) linuxServiceTestHooks {
	hooks, _ := ctx.Value(linuxServiceHooksKey{}).(linuxServiceTestHooks)
	return hooks
}

func runLinuxSystemctl(ctx context.Context, args ...string) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("Linux service context is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.New("Linux service command was cancelled")
	}
	commandCtx, cancel := context.WithTimeout(ctx, linuxSystemctlTimeout(args...))
	defer cancel()
	hooks := serviceHooks(ctx)
	var output []byte
	var err error
	if hooks.run != nil {
		output, err = hooks.run(commandCtx, "/usr/bin/systemctl", append([]string(nil), args...)...)
	} else {
		output, err = runLinuxCommand(commandCtx, args...)
	}
	if commandCtx.Err() != nil {
		return nil, errors.New("Linux service command did not complete")
	}
	if err != nil {
		return nil, errors.New("Linux service command failed")
	}
	if len(output) > linuxServiceMaxOutput {
		return nil, errors.New("Linux service command output exceeded its bound")
	}
	return output, nil
}

func linuxSystemctlTimeout(args ...string) time.Duration {
	if len(args) == 2 && args[0] == "stop" && args[1] == linuxManagerUnit {
		return linuxManagerStopTimeout
	}
	if len(args) == 2 && args[0] == "start" && args[1] == linuxManagerUnit {
		return linuxManagerStartTimeout
	}
	return linuxServiceCommandTimeout
}

func runLinuxCommand(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/systemctl", args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
	writer := &limitedServiceOutput{limit: linuxServiceMaxOutput}
	cmd.Stdout, cmd.Stderr = writer, writer
	err := cmd.Run()
	if writer.exceeded {
		return nil, errors.New("output bound exceeded")
	}
	if err != nil {
		return nil, err
	}
	return writer.buf, nil
}

type limitedServiceOutput struct {
	buf      []byte
	limit    int
	exceeded bool
}

func (w *limitedServiceOutput) Write(p []byte) (int, error) {
	remaining := w.limit + 1 - len(w.buf)
	if remaining > 0 {
		n := len(p)
		if n > remaining {
			n = remaining
		}
		w.buf = append(w.buf, p[:n]...)
	}
	if len(w.buf) > w.limit || len(p) > remaining {
		w.exceeded = true
	}
	return len(p), nil
}

func inspectLinuxManager(ctx context.Context) (linuxServiceStatus, error) {
	out, err := runLinuxSystemctl(ctx, "show", "--no-pager", "--property=LoadState,ActiveState,SubState,MainPID,UnitFileState", linuxManagerUnit)
	if err != nil {
		return linuxServiceStatus{}, err
	}
	return parseLinuxManagerStatus(string(out), true)
}

func parseLinuxManagerStatus(raw string, withUnitFileState bool) (linuxServiceStatus, error) {
	keys := []string{"LoadState", "ActiveState", "SubState", "MainPID"}
	if withUnitFileState {
		keys = append(keys, "UnitFileState")
	}
	values := make(map[string]string, len(keys))
	scanner := bufio.NewScanner(strings.NewReader(raw))
	scanner.Buffer(make([]byte, 1024), linuxServiceMaxOutput)
	for scanner.Scan() {
		line := scanner.Text()
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			return linuxServiceStatus{}, errors.New("Linux service status is malformed")
		}
		known := false
		for _, key := range keys {
			if name == key {
				known = true
				break
			}
		}
		if !known {
			return linuxServiceStatus{}, errors.New("Linux service status contains an unknown property")
		}
		if _, exists := values[name]; exists {
			return linuxServiceStatus{}, errors.New("Linux service status repeats a property")
		}
		values[name] = value
	}
	if err := scanner.Err(); err != nil {
		return linuxServiceStatus{}, errors.New("Linux service status exceeded its bound")
	}
	if len(values) != len(keys) {
		return linuxServiceStatus{}, errors.New("Linux service status is incomplete")
	}
	pidText := values["MainPID"]
	if pidText == "" {
		return linuxServiceStatus{}, errors.New("Linux service PID is malformed")
	}
	for _, c := range pidText {
		if c < '0' || c > '9' {
			return linuxServiceStatus{}, errors.New("Linux service PID is malformed")
		}
	}
	pid64, err := strconv.ParseInt(pidText, 10, 32)
	if err != nil || pid64 < 0 {
		return linuxServiceStatus{}, errors.New("Linux service PID is malformed")
	}
	status := linuxServiceStatus{LoadState: values["LoadState"], ActiveState: values["ActiveState"], SubState: values["SubState"], MainPID: int(pid64), UnitFileState: values["UnitFileState"]}
	if status.LoadState != "loaded" && status.LoadState != "not-found" {
		return linuxServiceStatus{}, errors.New("Linux service load state is unrecognized")
	}
	switch status.ActiveState {
	case "active", "reloading", "inactive", "failed", "activating", "deactivating":
	default:
		return linuxServiceStatus{}, errors.New("Linux service active state is unrecognized")
	}
	if withUnitFileState {
		switch status.UnitFileState {
		case "enabled", "disabled", "static", "indirect":
		case "":
			if status.LoadState != "not-found" {
				return linuxServiceStatus{}, errors.New("Linux service unit-file state is unrecognized")
			}
		default:
			return linuxServiceStatus{}, errors.New("Linux service unit-file state is unrecognized")
		}
	}
	if status.LoadState == "not-found" && (status.ActiveState != "inactive" || status.SubState != "dead" || status.MainPID != 0 || status.UnitFileState != "") {
		return linuxServiceStatus{}, errors.New("Linux service absence is incoherent")
	}
	return status, nil
}

func linuxStatusStopped(s linuxServiceStatus) bool {
	if s.LoadState == "not-found" {
		return s.ActiveState == "inactive" && s.SubState == "dead" && s.MainPID == 0 && s.UnitFileState == ""
	}
	if s.LoadState != "loaded" || !(knownLinuxUnitFileState(s.UnitFileState) || s.UnitFileState == "") || s.MainPID != 0 {
		return false
	}
	return s.ActiveState == "inactive" && s.SubState == "dead" || s.ActiveState == "failed" && s.SubState == "failed"
}

func linuxStatusActive(s linuxServiceStatus) bool {
	return s.LoadState == "loaded" && (knownLinuxUnitFileState(s.UnitFileState) || s.UnitFileState == "") && s.ActiveState == "active" && s.SubState == "running" && s.MainPID > 0
}

func knownLinuxUnitFileState(s string) bool {
	switch s {
	case "enabled", "disabled", "static", "indirect":
		return true
	default:
		return false
	}
}

func stopLinuxManager(ctx context.Context) error {
	status, err := inspectLinuxManager(ctx)
	if err != nil {
		return err
	}
	if status.LoadState != "loaded" || !knownLinuxUnitFileState(status.UnitFileState) {
		return errors.New("manager service is unavailable or has an unsupported unit-file state")
	}
	return withLinuxGamesPreserved(ctx, func() error {
		if _, err := runLinuxSystemctl(ctx, "stop", linuxManagerUnit); err != nil {
			return err
		}
		status, err = inspectLinuxManager(ctx)
		if err != nil {
			return err
		}
		if !linuxStatusStopped(status) || status.LoadState != "loaded" {
			return errors.New("manager service did not prove a stopped state")
		}
		return nil
	})
}

func startLinuxManager(ctx context.Context) error {
	status, err := inspectLinuxManager(ctx)
	if err != nil {
		return err
	}
	if status.LoadState != "loaded" || !knownLinuxUnitFileState(status.UnitFileState) {
		return errors.New("manager service is unavailable or has an unsupported unit-file state")
	}
	return withLinuxGamesPreserved(ctx, func() error {
		_, err = runLinuxSystemctl(ctx, "start", linuxManagerUnit)
		return err
	})
}

func reloadLinuxUnits(ctx context.Context) error {
	return withLinuxGamesPreserved(ctx, func() error {
		_, err := runLinuxSystemctl(ctx, "daemon-reload")
		return err
	})
}

func setLinuxManagerEnabled(ctx context.Context, enabled bool) error {
	status, err := inspectLinuxManager(ctx)
	if err != nil {
		return err
	}
	if status.LoadState != "loaded" || !knownLinuxUnitFileState(status.UnitFileState) {
		return errors.New("manager service is unavailable or has an unsupported unit-file state")
	}
	action, want := "disable", "disabled"
	if enabled {
		action, want = "enable", "enabled"
	}
	return withLinuxGamesPreserved(ctx, func() error {
		if _, err := runLinuxSystemctl(ctx, action, linuxManagerUnit); err != nil {
			return err
		}
		status, err = inspectLinuxManager(ctx)
		if err != nil {
			return err
		}
		if status.LoadState != "loaded" || status.UnitFileState != want {
			return errors.New("manager service enablement did not verify")
		}
		return nil
	})
}

func requireLinuxGamesStopped(ctx context.Context) error {
	before, err := enumerateLinuxGameUnitListings(ctx)
	if err != nil {
		return err
	}
	for _, unit := range before {
		status, err := inspectLinuxGameUnit(ctx, unit.Name)
		if err != nil {
			return err
		}
		if !linuxStatusStopped(status) {
			return errors.New("managed game service is active or uncertain")
		}
	}
	after, err := enumerateLinuxGameUnitListings(ctx)
	if err != nil {
		return err
	}
	if !sameLinuxGameListingSets(before, after) {
		return errors.New("managed game service set changed during inspection")
	}
	for _, unit := range after {
		if !linuxGameListingStopped(unit) {
			return errors.New("managed game service is active or uncertain")
		}
		status, err := inspectLinuxGameUnit(ctx, unit.Name)
		if err != nil {
			return err
		}
		if !linuxStatusStopped(status) {
			return errors.New("managed game service is active or uncertain")
		}
	}
	return nil
}

func inspectLinuxGameUnit(ctx context.Context, unit string) (linuxServiceStatus, error) {
	out, err := runLinuxSystemctl(ctx, "show", "--no-pager", "--property=LoadState,ActiveState,SubState,MainPID", unit)
	if err != nil {
		return linuxServiceStatus{}, err
	}
	status, err := parseLinuxManagerStatus(string(out), false)
	if err != nil {
		return linuxServiceStatus{}, errors.New("managed game service status is uncertain")
	}
	return status, nil
}

type linuxGameUnitListing struct{ Name, LoadState, ActiveState, SubState string }

func linuxGameListingStopped(unit linuxGameUnitListing) bool {
	if unit.LoadState == "not-found" {
		return unit.ActiveState == "inactive" && unit.SubState == "dead"
	}
	return unit.LoadState == "loaded" && (unit.ActiveState == "inactive" && unit.SubState == "dead" || unit.ActiveState == "failed" && unit.SubState == "failed")
}

func enumerateLinuxGameUnits(ctx context.Context) ([]string, error) {
	listings, err := enumerateLinuxGameUnitListings(ctx)
	if err != nil {
		return nil, err
	}
	units := make([]string, len(listings))
	for i := range listings {
		units[i] = listings[i].Name
	}
	return units, nil
}

func enumerateLinuxGameUnitListings(ctx context.Context) ([]linuxGameUnitListing, error) {
	out, err := runLinuxSystemctl(ctx, "list-units", "--all", "--type=service", "--no-legend", "--no-pager", "--plain", "minecraft@*.service")
	if err != nil {
		return nil, err
	}
	return parseLinuxGameUnitListings(string(out))
}

func parseLinuxGameUnits(raw string) ([]string, error) {
	listings, err := parseLinuxGameUnitListings(raw)
	if err != nil {
		return nil, err
	}
	units := make([]string, len(listings))
	for i := range listings {
		units[i] = listings[i].Name
	}
	return units, nil
}

func parseLinuxGameUnitListings(raw string) ([]linuxGameUnitListing, error) {
	units := make([]linuxGameUnitListing, 0)
	seen := make(map[string]bool)
	scanner := bufio.NewScanner(strings.NewReader(raw))
	scanner.Buffer(make([]byte, 1024), linuxServiceMaxOutput)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 {
			return nil, errors.New("managed game unit listing is malformed")
		}
		switch fields[1] {
		case "loaded", "not-found", "error", "masked":
		default:
			return nil, errors.New("managed game unit listing is malformed")
		}
		switch fields[2] {
		case "active", "reloading", "inactive", "failed", "activating", "deactivating":
		default:
			return nil, errors.New("managed game unit listing is malformed")
		}
		if fields[3] == "" {
			return nil, errors.New("managed game unit listing is malformed")
		}
		unit := fields[0]
		if !strings.HasPrefix(unit, "minecraft@") || !strings.HasSuffix(unit, ".service") {
			return nil, errors.New("managed game unit name is unsafe")
		}
		id := strings.TrimSuffix(strings.TrimPrefix(unit, "minecraft@"), ".service")
		if !validLinuxInstanceID(id) || domain.ValidateInstanceID(id) != nil {
			return nil, errors.New("managed game unit name is unsafe")
		}
		if seen[unit] {
			return nil, errors.New("managed game unit listing repeats a name")
		}
		seen[unit] = true
		units = append(units, linuxGameUnitListing{Name: unit, LoadState: fields[1], ActiveState: fields[2], SubState: fields[3]})
	}
	if err := scanner.Err(); err != nil {
		return nil, errors.New("managed game unit listing exceeded its bound")
	}
	return units, nil
}

func sameLinuxGameListingSets(a, b []linuxGameUnitListing) bool {
	if len(a) != len(b) {
		return false
	}
	left, right := make([]string, len(a)), make([]string, len(b))
	for i := range a {
		left[i] = a[i].Name
	}
	for i := range b {
		right[i] = b[i].Name
	}
	return sameLinuxGameUnits(left, right)
}

func validLinuxInstanceID(id string) bool {
	if len(id) < 1 || len(id) > 32 {
		return false
	}
	for i, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || i > 0 && c == '-') {
			return false
		}
	}
	return true
}

func requireLinuxManagerImage(ctx context.Context, expected Image) error {
	if !validImage(expected) {
		return errors.New("expected manager image is invalid")
	}
	status, err := inspectLinuxManager(ctx)
	if err != nil || !linuxStatusActive(status) {
		return errors.New("manager service is not stably active")
	}
	pid := status.MainPID
	startBefore, err := linuxProcessStartTicks(ctx, pid)
	if err != nil {
		return errors.New("manager process identity is unavailable")
	}
	fileInfo, digest, err := hashProtectedLinuxManager(ctx)
	if err != nil {
		return errors.New("installed manager image is not protected")
	}
	if digest != expected.SHA256 {
		return errors.New("installed manager image does not match expected image")
	}
	runningInfo, err := linuxServiceStat(ctx, "/proc/"+strconv.Itoa(pid)+"/exe")
	if err != nil || !sameLinuxFileIdentity(fileInfo, runningInfo) {
		return errors.New("running manager image identity does not match installed image")
	}
	startAfter, err := linuxProcessStartTicks(ctx, pid)
	if err != nil || startBefore != startAfter {
		return errors.New("manager process changed during image inspection")
	}
	current, err := inspectLinuxManager(ctx)
	if err != nil || !linuxStatusActive(current) || current.MainPID != pid {
		return errors.New("manager service changed during image inspection")
	}
	finalInfo, finalDigest, err := hashProtectedLinuxManager(ctx)
	if err != nil || finalDigest != expected.SHA256 || !sameLinuxFileIdentity(fileInfo, finalInfo) {
		return errors.New("installed manager image changed during inspection")
	}
	return nil
}

func hashProtectedLinuxManager(ctx context.Context) (os.FileInfo, string, error) {
	info, err := linuxServiceLstat(ctx, linuxManagerExecutablePath)
	if err != nil {
		return nil, "", err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 || st.Nlink != 1 || !info.Mode().IsRegular() || info.Mode().Perm() != 0755 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || info.Size() < 1 || info.Size() > linuxManagerImageMaxBytes {
		return nil, "", errors.New("manager executable metadata is unsafe")
	}
	hooks := serviceHooks(ctx)
	var file *os.File
	if hooks.open != nil {
		file, err = hooks.open(linuxManagerExecutablePath)
	} else {
		file, err = os.Open(linuxManagerExecutablePath)
	}
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !sameLinuxFileIdentity(info, opened) || opened.Size() != info.Size() {
		return nil, "", errors.New("manager executable changed while opening")
	}
	h := sha256.New()
	buffer := make([]byte, 64<<10)
	n, err := copyLinuxImage(ctx, h, file, buffer)
	if err != nil || n != info.Size() || n > linuxManagerImageMaxBytes {
		return nil, "", errors.New("manager executable hash could not be bounded")
	}
	after, err := linuxServiceLstat(ctx, linuxManagerExecutablePath)
	if err != nil || !sameLinuxFileIdentity(info, after) || after.Size() != info.Size() {
		return nil, "", errors.New("manager executable changed while hashing")
	}
	return info, hex.EncodeToString(h.Sum(nil)), nil
}

func linuxServiceStat(ctx context.Context, path string) (os.FileInfo, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, errors.New("Linux process inspection was cancelled")
	}
	hooks := serviceHooks(ctx)
	if hooks.stat != nil {
		return hooks.stat(path)
	}
	return os.Stat(path)
}

func linuxServiceLstat(ctx context.Context, path string) (os.FileInfo, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, errors.New("Linux image inspection was cancelled")
	}
	hooks := serviceHooks(ctx)
	if hooks.lstat != nil {
		return hooks.lstat(path)
	}
	return os.Lstat(path)
}

func linuxProcessStartTicks(ctx context.Context, pid int) (uint64, error) {
	if pid <= 0 {
		return 0, errors.New("invalid process id")
	}
	path := "/proc/" + strconv.Itoa(pid) + "/stat"
	hooks := serviceHooks(ctx)
	var body []byte
	var err error
	if hooks.readFile != nil {
		body, err = hooks.readFile(path)
	} else {
		body, err = os.ReadFile(path)
	}
	if ctx.Err() != nil {
		return 0, errors.New("process identity inspection was cancelled")
	}
	if err != nil || len(body) == 0 || len(body) > linuxProcStatMaxBytes {
		return 0, errors.New("process identity record is unavailable")
	}
	closeParen := strings.LastIndexByte(string(body), ')')
	if closeParen < 0 || closeParen+1 >= len(body) {
		return 0, errors.New("process identity record is malformed")
	}
	fields := strings.Fields(string(body[closeParen+1:]))
	if len(fields) <= 19 {
		return 0, errors.New("process identity record is incomplete")
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || start == 0 {
		return 0, errors.New("process identity record is invalid")
	}
	return start, nil
}

func sameLinuxFileIdentity(a, b os.FileInfo) bool {
	if a == nil || b == nil {
		return false
	}
	if os.SameFile(a, b) {
		return true
	}
	as, aok := a.Sys().(*syscall.Stat_t)
	bs, bok := b.Sys().(*syscall.Stat_t)
	return aok && bok && as.Dev == bs.Dev && as.Ino == bs.Ino
}

func copyLinuxImage(ctx context.Context, dst io.Writer, src io.Reader, buffer []byte) (int64, error) {
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, readErr := src.Read(buffer)
		if n > 0 {
			written, writeErr := dst.Write(buffer[:n])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != n {
				return total, io.ErrShortWrite
			}
			if total > linuxManagerImageMaxBytes {
				return total, errors.New("manager image exceeded its bound")
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return total, nil
			}
			return total, readErr
		}
		if n == 0 {
			return total, io.ErrNoProgress
		}
	}
}

type linuxGameActivity struct {
	unit        string
	activeState string
	subState    string
	pid         int
}

func withLinuxGamesPreserved(ctx context.Context, operation func() error) error {
	before, err := snapshotLinuxGameActivity(ctx)
	if err != nil {
		return err
	}
	opErr := operation()
	after, checkErr := snapshotLinuxGameActivity(ctx)
	if checkErr != nil {
		return errors.Join(opErr, errors.New("managed game services could not be revalidated"))
	}
	if len(before) != len(after) {
		return errors.Join(opErr, errors.New("manager operation changed the managed game service set"))
	}
	for i := range before {
		if before[i] != after[i] {
			return errors.Join(opErr, errors.New("manager operation changed a managed game service"))
		}
	}
	return opErr
}

func snapshotLinuxGameActivity(ctx context.Context) ([]linuxGameActivity, error) {
	units, err := enumerateLinuxGameUnits(ctx)
	if err != nil {
		return nil, err
	}
	activity := make([]linuxGameActivity, 0, len(units))
	for _, unit := range units {
		out, err := runLinuxSystemctl(ctx, "show", "--no-pager", "--property=LoadState,ActiveState,SubState,MainPID", unit)
		if err != nil {
			return nil, err
		}
		status, err := parseLinuxManagerStatus(string(out), false)
		if err != nil {
			return nil, errors.New("managed game service state is uncertain")
		}
		entry := linuxGameActivity{unit: unit, activeState: status.ActiveState, subState: status.SubState, pid: status.MainPID}
		if !linuxStatusStopped(status) && !linuxStatusActive(status) {
			return nil, errors.New("managed game service state is uncertain")
		}
		activity = append(activity, entry)
	}
	after, err := enumerateLinuxGameUnits(ctx)
	if err != nil || !sameLinuxGameUnits(units, after) {
		return nil, errors.New("managed game service set changed during inspection")
	}
	sort.Slice(activity, func(i, j int) bool { return activity[i].unit < activity[j].unit })
	return activity, nil
}

func sameLinuxGameUnits(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	left, right := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(left)
	sort.Strings(right)
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

type jvmInfo struct {
	Major                         int
	Vendor, Version, Architecture string
}

// cappedOutput prevents an unexpected Java launcher from filling manager memory.
type cappedOutput struct {
	mu       sync.Mutex
	data     []byte
	overflow bool
}

func (b *cappedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	remaining := (64 << 10) - len(b.data)
	if len(p) > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	b.data = append(b.data, p...)
	return n, nil
}

// Temurin 16.0.2+7 was published under Eclipse Foundation before the vendor
// rename. Callers still verify official origin, archive hash and exact build;
// this alias must not admit Foundation builds for other versions or majors.
func temurinVendor(vendor string, major int, version string) bool {
	return vendor == "Eclipse Adoptium" || (vendor == "Eclipse Foundation" && major == 16 && version == "16.0.2+7")
}

// The verified 8u504-b01 JVM reports Temurin, although its release implementor
// is Eclipse Adoptium. Keep this JVM-only alias pinned to that observed build.
func temurinJVMVendor(vendor string, major int, version string) bool {
	return temurinVendor(vendor, major, version) || (vendor == "Temurin" && major == 8 && version == "1.8.0_504-b01")
}

func parseJVM(output string) (jvmInfo, error) {
	properties := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		p := strings.SplitN(strings.TrimSpace(line), " = ", 2)
		if len(p) == 2 {
			if _, exists := properties[p[0]]; exists {
				return jvmInfo{}, fmt.Errorf("duplicate JVM property")
			}
			properties[p[0]] = strings.TrimSpace(p[1])
		}
	}
	v := properties["java.version"]
	majorText := strings.SplitN(v, ".", 2)[0]
	if majorText == "1" {
		parts := strings.Split(v, ".")
		if len(parts) < 2 {
			return jvmInfo{}, fmt.Errorf("invalid legacy Java version")
		}
		majorText = parts[1]
	}
	major, err := strconv.Atoi(majorText)
	if err != nil || !supportedMajor(major) {
		return jvmInfo{}, fmt.Errorf("unsupported JVM version")
	}

	arch := properties["os.arch"]
	if arch != "amd64" && arch != "x86_64" {
		return jvmInfo{}, fmt.Errorf("Java executable is not x64")
	}
	version := properties["java.runtime.version"]
	if !versionPattern.MatchString(version) {
		return jvmInfo{}, fmt.Errorf("missing JVM runtime version")
	}
	if !temurinJVMVendor(properties["java.vendor"], major, version) {
		return jvmInfo{}, fmt.Errorf("Java executable vendor does not match Temurin build")
	}
	return jvmInfo{Major: major, Vendor: properties["java.vendor"], Version: version, Architecture: arch}, nil
}
func javaEnvironment() []string {
	out := []string{}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		name = strings.ToUpper(name)
		switch name {
		case "JAVA_TOOL_OPTIONS", "_JAVA_OPTIONS", "JDK_JAVA_OPTIONS", "JAVA_HOME", "CLASSPATH", "LD_PRELOAD", "LD_LIBRARY_PATH":
			continue
		}
		if strings.HasPrefix(name, "DYLD_") {
			continue
		}
		out = append(out, entry)
	}
	return out
}
func probeJava(ctx context.Context, executable string) (jvmInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-XshowSettings:properties", "-version")
	cmd.Dir = filepath.Dir(executable)
	cmd.Env = javaEnvironment()
	output := &cappedOutput{}
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return jvmInfo{}, fmt.Errorf("inspect Java executable: %w", ctx.Err())
		}
		return jvmInfo{}, fmt.Errorf("Java executable version check failed")
	}
	if output.overflow {
		return jvmInfo{}, fmt.Errorf("Java executable output exceeded limit")
	}
	return parseJVM(string(output.data))
}
func (m *Manager) verifyProbe(ctx context.Context, r domain.JavaRuntime) error {
	got, err := m.probe(ctx, r.Executable)
	if err != nil {
		return fmt.Errorf("verify Temurin executable: %w", err)
	}
	if got.Major != r.Major || !temurinJVMVendor(got.Vendor, got.Major, got.Version) || got.Version != r.Version || (got.Architecture != "amd64" && got.Architecture != "x86_64") {
		return fmt.Errorf("Temurin executable does not match verified metadata")
	}
	return nil
}
func validateRelease(root string, r domain.JavaRuntime) error {
	path := filepath.Join(root, "release")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return fmt.Errorf("Temurin archive lacks a valid release file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read Temurin release: %w", err)
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		name, value, ok := strings.Cut(line, "=")
		if ok {
			if _, exists := values[name]; exists {
				return fmt.Errorf("duplicate Temurin release field")
			}
			values[name] = strings.Trim(value, "\"\r")
		}
	}
	// The historical Java 16 release file records its build in FULL_VERSION.
	version := values["JAVA_RUNTIME_VERSION"]
	if version == "" && values["IMPLEMENTOR"] == "Eclipse Foundation" {
		version = values["FULL_VERSION"]
	}
	if !temurinVendor(values["IMPLEMENTOR"], r.Major, version) || (values["OS_ARCH"] != "x86_64" && values["OS_ARCH"] != "amd64") {
		return fmt.Errorf("Temurin release vendor or architecture mismatch")
	}
	if version != "" && version != r.Version {
		return fmt.Errorf("Temurin release version mismatch")
	}
	return nil
}

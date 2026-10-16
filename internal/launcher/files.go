// Package launcher owns the independent per-instance process, console and save watchdog.
package launcher

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

type InstancePaths struct{ Root, Server, State, Logs string }

// Paths derives paths from the canonical absolute data root and rejects existing
// symlink components. Callers must keep this tree private to the service account.
func Paths(dataDir, id string) (InstancePaths, error) {
	if err := domain.ValidateInstanceID(id); err != nil {
		return InstancePaths{}, err
	}
	if !filepath.IsAbs(dataDir) {
		return InstancePaths{}, fmt.Errorf("data directory must be absolute")
	}
	root := filepath.Join(filepath.Clean(dataDir), "instances", id)
	for p := root; ; p = filepath.Dir(p) {
		fi, err := os.Lstat(p)
		if err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return InstancePaths{}, fmt.Errorf("symlink in instance path")
		}
		if err != nil && !os.IsNotExist(err) {
			return InstancePaths{}, err
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	p := InstancePaths{Root: root, Server: filepath.Join(root, "server"), State: filepath.Join(root, "state"), Logs: filepath.Join(root, "logs")}
	for _, path := range []string{p.Server, p.State, p.Logs} {
		if fi, err := os.Lstat(path); err == nil && (fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir()) {
			return InstancePaths{}, fmt.Errorf("unsafe instance directory")
		}
	}
	return p, nil
}
func ValidateSpec(s domain.LaunchSpec, p InstancePaths) error {
	if !filepath.IsAbs(s.Executable) || strings.ContainsRune(s.Executable, 0) {
		return fmt.Errorf("executable must be an absolute path")
	}
	if filepath.Clean(s.WorkingDir) != p.Server {
		return fmt.Errorf("working directory must be the instance server directory")
	}
	for _, a := range s.Args {
		if strings.ContainsRune(a, 0) {
			return fmt.Errorf("NUL in argument")
		}
	}
	for k, v := range s.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, 0) {
			return fmt.Errorf("invalid environment override")
		}
	}
	fi, err := os.Stat(s.Executable)
	if err != nil {
		return fmt.Errorf("executable: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("executable must be regular")
	}
	return nil
}
func prepare(p InstancePaths) error {
	for _, path := range []string{p.Root, p.Server, p.State, p.Logs} {
		if err := os.MkdirAll(path, 0700); err != nil {
			return err
		}
	}
	return nil
}
func WriteSpec(dataDir, id string, s domain.LaunchSpec) error {
	p, err := Paths(dataDir, id)
	if err != nil {
		return err
	}
	if err = ValidateSpec(s, p); err != nil {
		return err
	}
	if err = prepare(p); err != nil {
		return err
	}
	// LaunchSpec deliberately excludes Env from public JSON. This private disk
	// envelope is the only location where explicit environment values persist.
	return WriteJSON(filepath.Join(p.State, "launch.json"), diskSpec{Spec: s, Env: s.Env})
}

type diskSpec struct {
	Spec domain.LaunchSpec `json:"spec"`
	Env  map[string]string `json:"env,omitempty"`
}

func ReadJSON(path string, v any) error {
	f, err := openPrivate(path, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("state file must be regular")
	}
	if fi.Size() > 1<<20 {
		return fmt.Errorf("state file too large")
	}
	return json.NewDecoder(f).Decode(v)
}
func WriteJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return replaceFile(name, path)
}

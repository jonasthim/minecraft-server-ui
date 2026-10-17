package outer

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/jonasthim/minecraft-server-ui/release/candidate"
)

func write(t *testing.T, path string, body []byte) {
	t.Helper()
	if e := os.WriteFile(path, body, 0600); e != nil {
		t.Fatal(e)
	}
}
func read(t *testing.T, path string) []byte {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return b
}

func nativeFixture(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "go.mod"), []byte("module github.com/jonasthim/minecraft-server-ui\n\ngo 1.27.0\n"))
	c := Config{Repository: "owner/project", Version: "v1.2.3", TrustFile: filepath.Join(root, "trust.json"), Output: filepath.Join(root, "out")}
	write(t, c.TrustFile, []byte(`{"format":1,"repository":"owner/project","public_keys":{"fixture":"`+base64.StdEncoding.EncodeToString(make([]byte, 32))+`"}}`))
	for _, command := range []string{"minecraft-ui-bootstrap", "minecraft-ui-update"} {
		dir := filepath.Join(root, "cmd", command)
		if e := os.MkdirAll(dir, 0700); e != nil {
			t.Fatal(e)
		}
		write(t, filepath.Join(dir, "main.go"), []byte("package main\nvar version=\"dev\"\nfunc main(){}\n"))
		for _, platform := range []string{"linux", "windows"} {
			path := filepath.Join(root, platform+"-"+command)
			cmd := exec.Command("go", "build", "-buildvcs=false", "-trimpath", "-ldflags=-s -w -X main.version="+c.Version, "-o", path, "./cmd/"+command)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "GOOS="+platform, "GOARCH=amd64", "CGO_ENABLED=0", "GOWORK=off")
			if b, e := cmd.CombinedOutput(); e != nil {
				t.Fatalf("format fixture build: %v %s", e, b)
			}
			r, _ := json.Marshal(candidate.VersionReceipt{Format: 1, OS: platform, Arch: "amd64", Command: command, Version: c.Version, BinarySHA256: digest(read(t, path))})
			write(t, path+".version.json", r)
			if command == "minecraft-ui-bootstrap" {
				if platform == "linux" {
					c.LinuxPreparer = path
				} else {
					c.WindowsPreparer = path
				}
			}
		}
	}
	return c
}

func TestProducer(t *testing.T) {
	c := nativeFixture(t)
	t.Run("exact deterministic snapshots and provenance", func(t *testing.T) {
		one := c
		one.Output = filepath.Join(t.TempDir(), "first")
		two := c
		two.Output = filepath.Join(t.TempDir(), "second")
		if e := Produce(context.Background(), one); e != nil {
			t.Fatal(e)
		}
		if e := Produce(context.Background(), two); e != nil {
			t.Fatal(e)
		}
		files, e := os.ReadDir(one.Output)
		if e != nil || len(files) != 5 {
			t.Fatal(files, e)
		}
		for _, f := range files {
			if !bytes.Equal(read(t, filepath.Join(one.Output, f.Name())), read(t, filepath.Join(two.Output, f.Name()))) {
				t.Fatal("nondeterministic", f.Name())
			}
		}
		for _, pair := range [][2]string{{"minecraft-ui-bootstrap-linux-amd64", c.LinuxPreparer}, {"minecraft-ui-bootstrap-windows-amd64.exe", c.WindowsPreparer}} {
			if !bytes.Equal(read(t, filepath.Join(one.Output, pair[0])), read(t, pair[1])) {
				t.Fatal("binary bytes differ")
			}
		}
		var entry Entry
		if e = json.Unmarshal(read(t, filepath.Join(one.Output, "bootstrap-entry.json")), &entry); e != nil {
			t.Fatal(e)
		}
		if entry.Format != 1 || entry.PreparerVersion != c.Version || entry.Repository != c.Repository || entry.Development || entry.TrustFileSHA256 != digest(read(t, c.TrustFile)) || len(entry.Assets) != 4 {
			t.Fatal(entry)
		}
		for _, a := range entry.Assets {
			b := read(t, filepath.Join(one.Output, a.Name))
			if int64(len(b)) != a.Size || digest(b) != a.SHA256 {
				t.Fatal(a)
			}
		}
	})
	t.Run("receipts and executable identity refuse independently", func(t *testing.T) {
		original := read(t, c.LinuxPreparer+".version.json")
		for _, name := range []string{"command", "platform", "version", "hash", "schema", "unknown", "duplicate", "missing"} {
			t.Run(name, func(t *testing.T) {
				defer write(t, c.LinuxPreparer+".version.json", original)
				var r map[string]any
				_ = json.Unmarshal(original, &r)
				switch name {
				case "command":
					r["command"] = "minecraft-ui-update"
				case "platform":
					r["os"] = "windows"
				case "version":
					r["version"] = "v1.2.4"
				case "hash":
					r["binary_sha256"] = strings.Repeat("0", 64)
				case "schema":
					r["schema"] = 1
				case "unknown":
					r["extra"] = true
				case "missing":
					delete(r, "version")
				}
				body, _ := json.Marshal(r)
				if name == "duplicate" {
					body = append([]byte(`{"format":1,`), original[1:]...)
				}
				write(t, c.LinuxPreparer+".version.json", body)
				x := c
				x.Output = filepath.Join(t.TempDir(), "refused")
				if e := Produce(context.Background(), x); e == nil {
					t.Fatal("accepted invalid receipt")
				}
				if _, e := os.Lstat(x.Output); !os.IsNotExist(e) {
					t.Fatal("published failed output", e)
				}
			})
		}
		for _, source := range []string{c.WindowsPreparer, strings.Replace(c.LinuxPreparer, "bootstrap", "update", 1)} {
			x := c
			x.LinuxPreparer = source
			x.Output = filepath.Join(t.TempDir(), "refused")
			if e := Produce(context.Background(), x); e == nil {
				t.Fatal("accepted wrong executable")
			}
		}
	})
	t.Run("existing outputs symlinks and cancelled operation preserve state", func(t *testing.T) {
		for _, kind := range []string{"file", "directory", "dangling"} {
			t.Run(kind, func(t *testing.T) {
				x := c
				x.Output = filepath.Join(t.TempDir(), "preserved")
				switch kind {
				case "file":
					write(t, x.Output, []byte("preserve"))
				case "directory":
					_ = os.Mkdir(x.Output, 0700)
					write(t, filepath.Join(x.Output, "sentinel"), []byte("preserve"))
				case "dangling":
					if e := os.Symlink("missing", x.Output); e != nil {
						t.Skip(e)
					}
				}
				before, _ := os.Lstat(x.Output)
				if e := Produce(context.Background(), x); e == nil {
					t.Fatal("accepted existing output")
				}
				after, e := os.Lstat(x.Output)
				if e != nil || !os.SameFile(before, after) {
					t.Fatal("output replaced", e)
				}
			})
		}
		x := c
		link := filepath.Join(t.TempDir(), "link")
		if e := os.Symlink(c.LinuxPreparer, link); e == nil {
			x.LinuxPreparer = link
			x.Output = filepath.Join(t.TempDir(), "no-output")
			if e := Produce(context.Background(), x); e == nil {
				t.Fatal("accepted source symlink")
			}
		}
		x = c
		x.Output = filepath.Join(t.TempDir(), "cancelled")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if e := Produce(ctx, x); e == nil {
			t.Fatal("accepted cancellation")
		}
	})
	t.Run("invalid trust identity bounds and path", func(t *testing.T) {
		for _, body := range [][]byte{[]byte(`{"format":1,"repository":"other/repo","public_keys":{}}`), bytes.Repeat([]byte(" "), 16385), []byte(`{"format":1,"format":1}`)} {
			x := c
			x.TrustFile = filepath.Join(t.TempDir(), "trust")
			write(t, x.TrustFile, body)
			x.Output = filepath.Join(t.TempDir(), "refused")
			if e := Produce(context.Background(), x); e == nil {
				t.Fatal("accepted invalid trust")
			}
		}
		for _, version := range []string{"latest", "v01.2.3", "v1.2.3-pre", "v18446744073709551616.0.0"} {
			x := c
			x.Version = version
			if e := Produce(context.Background(), x); e == nil {
				t.Fatal("accepted invalid preparer version")
			}
		}
	})
}

func renderScript(t *testing.T, development bool) []byte {
	t.Helper()
	payload := []byte("trusted test preparer bytes\n")
	data := struct {
		Config
		Binary                    Asset
		TrustLiteral, TrustBase64 string
	}{Config{Repository: "owner/project", Version: "v1.2.3", Development: development}, Asset{"minecraft-ui-bootstrap-linux-amd64", int64(len(payload)), digest(payload)}, `'{"fixture":true}'`, "e30="}
	tmpl, e := template.ParseFS(templates, "templates/linux.sh.tmpl")
	if e != nil {
		t.Fatal(e)
	}
	var b bytes.Buffer
	if e = tmpl.Execute(&b, data); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}

func TestLinuxEntryBehavior(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("Bash behavior is Linux source evidence")
	}
	for _, test := range []struct {
		name, mode, version        string
		development, flag, success bool
	}{
		{"verified literal arguments", "good", "latest", false, false, true},
		{"tampered never runs", "tamper", "latest", false, false, false},
		{"oversized never runs", "oversize", "latest", false, false, false},
		{"truncated never runs", "truncate", "latest", false, false, false},
		{"failed download scrubbed", "error", "latest", false, false, false},
		{"wrong redirect never runs", "wronghost", "latest", false, false, false},
		{"signed GitHub redirect kept private", "redirect", "v2.3.4", false, false, true},
		{"malicious version never interpreted", "good", "v1.2.3;$(touch PWNED)", false, false, false},
		{"development requires opt in", "good", "latest", true, false, false},
		{"development explicit", "good", "latest", true, true, true},
		{"owner refuses development flag", "good", "latest", false, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, filepath.Join(root, "unowned-sentinel"), []byte("preserve sibling"))
			bin := filepath.Join(root, "bin")
			if e := os.Mkdir(bin, 0700); e != nil {
				t.Fatal(e)
			}
			write(t, filepath.Join(root, "payload"), []byte("trusted test preparer bytes\n"))
			mockCurl := `#!/bin/bash
printf '%s\n' "${@: -1}" >> "$OUTER_URLS"
case "$OUTER_MODE" in
error) printf 'private-secret signed?token=private-secret\n' >&2; exit 1;;
wronghost) printf '302\nhttps://attacker.invalid/file?secret=private-secret\n' >&2;exit 0;;
redirect) if [[ "${@: -1}" == https://github.com/* ]];then printf '302\nhttps://release-assets.githubusercontent.com/fixed?token=private-secret\n' >&2;exit 0;fi;;
esac
case "$OUTER_MODE" in
tamper) printf 'untrust test preparer bytes\n';;
oversize) cat "$OUTER_PAYLOAD";printf X;;
truncate) printf short;;
*) cat "$OUTER_PAYLOAD";;
esac
printf '200\n\n' >&2
`
			write(t, filepath.Join(bin, "curl"), []byte(mockCurl))
			_ = os.Chmod(filepath.Join(bin, "curl"), 0700)
			write(t, filepath.Join(bin, "mktemp"), []byte("#!/bin/bash\np=$(/usr/bin/mktemp \"$@\") || exit 1\nprintf '%s' \"$p\" > \"$OUTER_SCRATCH\"\nprintf '%s\\n' \"$p\"\n"))
			_ = os.Chmod(filepath.Join(bin, "mktemp"), 0700)
			// A trusted fake runner seam replaces only the final native invocation.
			// Downloaded fixture bytes never execute. All validation remains shipping code.
			script := string(renderScript(t, test.development))
			write(t, filepath.Join(root, "os-release"), []byte("ID=debian\n"))
			script = strings.Replace(script, "/etc/os-release", filepath.Join(root, "os-release"), 1)
			marker := `if ! "$scratch/preparer" "${args[@]}"`
			if strings.Count(script, marker) != 1 {
				t.Fatal("runner seam drift")
			}
			script = strings.Replace(script, marker, `outer_test_run() { printf '%s\0' "$@" > "$OUTER_ARGS"; printf '{"prepared":true}\n'; }
if ! outer_test_run "${args[@]}"`, 1)
			path := filepath.Join(root, "entry.sh")
			write(t, path, []byte(script))
			output := filepath.Join(root, "literal $(touch PWNED); output")
			args := []string{path, "--version", test.version, "--output", output}
			if test.flag {
				args = append(args, "--allow-development")
			}
			cmd := exec.Command("bash", args...)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "OUTER_MODE="+test.mode, "OUTER_PAYLOAD="+filepath.Join(root, "payload"), "OUTER_ARGS="+filepath.Join(root, "args"), "OUTER_SCRATCH="+filepath.Join(root, "scratch"), "OUTER_URLS="+filepath.Join(root, "urls"))
			response, e := cmd.CombinedOutput()
			if (e == nil) != test.success {
				t.Fatalf("success=%v err=%v output=%s", test.success, e, response)
			}
			argv, e := os.ReadFile(filepath.Join(root, "args"))
			if test.success {
				if e != nil {
					t.Fatal("runner not reached", e)
				}
				fields := strings.Split(string(argv), "\x00")
				if len(fields) < 10 || fields[6] != test.version || fields[8] != output {
					t.Fatal(fields)
				}
			} else if e == nil {
				t.Fatal("unverified runner invoked")
			}
			if bytes.Contains(response, []byte("private-secret")) {
				t.Fatal("leaked private response", string(response))
			}
			if _, e := os.Stat(filepath.Join(root, "PWNED")); !os.IsNotExist(e) {
				t.Fatal("arguments executed as code")
			}
			if scratch, e := os.ReadFile(filepath.Join(root, "scratch")); e == nil {
				if _, e := os.Lstat(string(scratch)); !os.IsNotExist(e) {
					t.Fatal("scratch retained", string(scratch), e)
				}
			}
			if string(read(t, filepath.Join(root, "unowned-sentinel"))) != "preserve sibling" {
				t.Fatal("cleanup changed unowned sibling")
			}
			if urls, e := os.ReadFile(filepath.Join(root, "urls")); e == nil && !bytes.HasPrefix(urls, []byte("https://github.com/owner/project/releases/download/v1.2.3/minecraft-ui-bootstrap-linux-amd64\n")) {
				t.Fatal("preparer URL not pinned", string(urls))
			}
		})
	}
}

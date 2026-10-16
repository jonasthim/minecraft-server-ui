#!/bin/sh
# Isolated test only. Does not invoke host systemd/sudo or install any host file.
set -eu
repo=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
fixture=$(mktemp -d /home/thim/.cache/dilla-tmp/task24-namespace-XXXXXX)
root=$fixture/root
mkdir -p "$root/dev" "$root/usr/local/lib/minecraft-ui" "$root/usr/local/bin" "$root/usr/bin" "$root/etc/minecraft-ui" "$root/var/lib/minecraft-ui-update" "$root/var/lib/minecraft-ui" "$fixture/data/selfupdate/control"
touch "$root/dev/null"
chmod 0755 "$root" "$root/usr" "$root/usr/local" "$root/usr/local/lib" "$root/usr/local/lib/minecraft-ui" "$root/usr/local/bin" "$root/usr/bin" "$root/etc" "$root/etc/minecraft-ui" "$root/var" "$root/var/lib"
chmod 0700 "$root/var/lib/minecraft-ui-update" "$root/var/lib/minecraft-ui" "$fixture/data"
(cd "$repo" && CGO_ENABLED=0 go build -o "$root/usr/local/lib/minecraft-ui/selfupdate" ./cmd/minecraft-ui-update)
cat > "$fixture/systemctl.go" <<'GO'
package main
import("os";"strings")
func main(){if len(os.Args)!=4||os.Args[1]!="start"||os.Args[2]!="--no-block"||os.Args[3]!="minecraft-ui-update@"+strings.Repeat("a",64)+".service"{os.Exit(2)};if os.WriteFile("/var/lib/minecraft-ui/observed-systemctl",[]byte(strings.Join(os.Args[1:]," ")),0600)!=nil{os.Exit(1)}}
GO
CGO_ENABLED=0 go build -o "$root/usr/bin/systemctl" "$fixture/systemctl.go"
cat > "$fixture/prepare.go" <<'GO'
package main
import("crypto/ed25519";"crypto/rand";"crypto/sha256";"encoding/base64";"encoding/hex";"encoding/json";"os";"path/filepath";"strings";"time")
func write(path string,value any){b,e:=json.Marshal(value);if e!=nil{panic(e)};if e:=os.WriteFile(path,b,0600);e!=nil{panic(e)}}
func main(){root,data:=os.Args[1],os.Args[2];pub,_,e:=ed25519.GenerateKey(rand.Reader);if e!=nil{panic(e)};write(filepath.Join(root,"etc/minecraft-ui/update-trust.json"),map[string]any{"format":1,"repository":"example/fixture","public_keys":map[string]string{"test-only":base64.StdEncoding.EncodeToString(pub)}});binary:=[]byte("old-test-executable");h:=sha256.Sum256(binary);if e:=os.WriteFile(filepath.Join(root,"usr/local/bin/minecraft-ui"),binary,0755);e!=nil{panic(e)};write(filepath.Join(root,"var/lib/minecraft-ui-update/installed.json"),map[string]any{"format":1,"version":"v1.0.0","schema":16,"sha256":hex.EncodeToString(h[:])});write(filepath.Join(data,"selfupdate/control/request.json"),map[string]any{"stage_id":strings.Repeat("a",64),"actor_id":1,"requested_at":time.Now().UTC().Add(-time.Second)})}
GO
go run "$fixture/prepare.go" "$root" "$fixture/data"
# The test root and every ancestor become uid0 only in this private user NS.
# The whole caller root is read-only except the fixed service data bind.
unshare --user --map-root-user --mount sh -eu -c '
 root=$1
 data=$2
 mount --bind "$root" "$root"
 mount -o remount,bind,ro "$root"
 mount --bind "$data" "$root/var/lib/minecraft-ui"
 actual=$(chroot "$root" /usr/local/lib/minecraft-ui/selfupdate status)
 [ "$actual" = ready ]
 chroot "$root" /usr/local/lib/minecraft-ui/selfupdate request aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
 [ -f "$root/var/lib/minecraft-ui/observed-systemctl" ]
 [ ! -e "$root/var/lib/minecraft-ui-update/manager.lock" ]
 [ ! -e "$root/var/lib/minecraft-ui-update/request.json" ]
' task24-namespace "$root" "$fixture/data"
printf 'PASS: status/request work with read-only privileged state; only fixed template start was observed. Fixture: %s\n' "$fixture"

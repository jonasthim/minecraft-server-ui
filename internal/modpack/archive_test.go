package modpack

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

func packZip(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for p, v := range files {
		f, e := w.Create(p)
		if e != nil {
			t.Fatal(e)
		}
		f.Write(v)
	}
	if e := w.Close(); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func indexEntry(p, server string) map[string]any {
	h1 := sha1.Sum([]byte("jar"))
	h5 := sha512.Sum512([]byte("jar"))
	return map[string]any{"path": p, "env": map[string]string{"server": server}, "hashes": map[string]string{"sha1": hex.EncodeToString(h1[:]), "sha512": hex.EncodeToString(h5[:])}, "downloads": []string{"https://cdn.modrinth.com/data/abc/versions/def/a.jar"}, "fileSize": 3}
}
func mrIndex(files ...map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{"formatVersion": 1, "game": "minecraft", "name": "pack", "versionId": "one", "dependencies": map[string]string{"minecraft": "1.20.1", "fabric-loader": "0.16.0"}, "files": files})
	return b
}
func TestMRServerEnvironmentAndOverridePrecedence(t *testing.T) {
	b := packZip(t, map[string][]byte{"modrinth.index.json": mrIndex(indexEntry("mods/required.jar", "required"), indexEntry("mods/optional.jar", "optional"), indexEntry("mods/client.jar", "unsupported")), "overrides/config/a.cfg": []byte("common"), "server-overrides/config/a.cfg": []byte("server"), "client-overrides/config/client.cfg": []byte("client")})
	p, e := ParseArchive(context.Background(), bytes.NewReader(b), int64(len(b)), "modrinth")
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Files) != 3 || p.Files["mods/optional.jar"].Server != "optional" || string(p.Files["config/a.cfg"].Bytes) != "server" {
		t.Fatalf("%+v", p)
	}
	if _, ok := p.Files["mods/client.jar"]; ok {
		t.Fatal("client file included")
	}
}
func TestArchiveRejectsTraversalLinksAndCaseCollision(t *testing.T) {
	for _, p := range []string{"../escape", "overrides/config/A.cfg", "overrides/world/level.dat", "overrides/run.sh"} {
		entries := map[string][]byte{"modrinth.index.json": mrIndex(), p: []byte("bad")}
		if p == "overrides/config/A.cfg" {
			entries["overrides/config/a.cfg"] = []byte("other")
		}
		b := packZip(t, entries)
		if _, e := ParseArchive(context.Background(), bytes.NewReader(b), int64(len(b)), "modrinth"); e == nil {
			t.Fatalf("accepted %s", p)
		}
	}
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	h := &zip.FileHeader{Name: "overrides/config/link.cfg"}
	h.SetMode(os.ModeSymlink | 0777)
	f, _ := w.CreateHeader(h)
	f.Write([]byte("target"))
	f, _ = w.Create("modrinth.index.json")
	f.Write(mrIndex())
	w.Close()
	if _, e := ParseArchive(context.Background(), bytes.NewReader(b.Bytes()), int64(b.Len()), "modrinth"); e == nil {
		t.Fatal("link accepted")
	}
}
func TestMRRejectsMissingHashAndAmbiguousServerEnvironment(t *testing.T) {
	for _, entry := range []map[string]any{indexEntry("mods/a.jar", ""), indexEntry("mods/a.jar", "required")} {
		if entry["env"].(map[string]string)["server"] == "required" {
			delete(entry["hashes"].(map[string]string), "sha512")
		}
		b := packZip(t, map[string][]byte{"modrinth.index.json": mrIndex(entry)})
		if _, e := ParseArchive(context.Background(), bytes.NewReader(b), int64(len(b)), "modrinth"); e == nil {
			t.Fatal("ambiguous entry accepted")
		}
	}
}
func TestBinaryResourcesOnlyUseApprovedAddonRoots(t *testing.T) {
	binary := []byte{0, 255, 128, 1}
	for _, name := range []string{"kubejs/assets/example/textures/icon.png", "kubejs/assets/example/sound.ogg", "kubejs/data/example/structures/tower.nbt"} {
		b := packZip(t, map[string][]byte{"modrinth.index.json": mrIndex(), "overrides/" + name: binary})
		pack, e := ParseArchive(t.Context(), bytes.NewReader(b), int64(len(b)), "modrinth")
		if e != nil || pack.Files[name].ArchiveEntry == "" || pack.Files[name].Size != 4 {
			t.Fatal(name, pack, e)
		}
	}
	for _, name := range []string{"config/image.png", "world/structure.nbt", "kubejs/assets/world.nbt", "kubejs/data/icon.png", "kubejs/assets/run.sh", "kubejs/client_scripts/client.js"} {
		b := packZip(t, map[string][]byte{"modrinth.index.json": mrIndex(), "overrides/" + name: binary})
		if _, e := ParseArchive(t.Context(), bytes.NewReader(b), int64(len(b)), "modrinth"); e == nil {
			t.Fatal("binary outside approved root accepted", name)
		}
	}
}

package loaderutil

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"os"
	"testing"
)

func TestLanguageEvidenceRejectsUnverifiedGeneration(t *testing.T) {
	if _, e := VerifiedLanguageEvidence(t.Context(), t.TempDir(), "alpha", domain.LaunchSpec{}); e == nil {
		t.Fatal("unverified language evidence accepted")
	}
}
func TestForgeLanguageManifestRequiresActualProviderAndVersion(t *testing.T) {
	for _, test := range []struct {
		manifest, service string
		valid             bool
	}{{"Implementation-Version: 47.4.0\r\n", "net.minecraftforge.fml.javafmlmod.FMLJavaModLanguageProvider", true}, {"Specification-Version: 47\r\n", "net.minecraftforge.fml.javafmlmod.FMLJavaModLanguageProvider", false}, {"Implementation-Version: 47.4.0\r\n", "wrong.Provider", false}} {
		var b bytes.Buffer
		w := zip.NewWriter(&b)
		f, _ := w.Create("META-INF/MANIFEST.MF")
		f.Write([]byte("Manifest-Version: 1.0\r\n" + test.manifest))
		f, _ = w.Create("META-INF/services/net.minecraftforge.forgespi.language.IModLanguageProvider")
		f.Write([]byte(test.service))
		w.Close()
		version, e := forgeLanguageManifest(b.Bytes(), "javafml")
		if (e == nil) != test.valid || test.valid && version != "47.4.0" {
			t.Fatal(test, version, e)
		}
	}
}

func TestOfficialHistoricalNeoLanguageMetadata(t *testing.T) {
	if os.Getenv("MODPACK_OFFICIAL_SMOKE") != "1" {
		t.Skip("explicit inert official smoke only")
	}
	raw, e := NewClient(nil).Bytes(t.Context(), "https://maven.neoforged.net/releases/net/neoforged/fancymodloader/loader/2.0.17/loader-2.0.17.jar", 1<<20)
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != neo204253LanguageSHA256 {
		t.Fatal("official historical language artifact changed")
	}
	for _, name := range []string{"javafml", "lowcodefml"} {
		version, e := languageManifest(raw, name, "net.neoforged", "IModLanguageProvider")
		if e != nil || version != "2.0" {
			t.Fatal(name, version, e)
		}
	}
	t.Logf("inert official Neo20.4.253 language library2.0.17 bytes=%d sha256=%s package Implementation-Version=2.0; full-generation verification remains required by adapter", len(raw), neo204253LanguageSHA256)
}

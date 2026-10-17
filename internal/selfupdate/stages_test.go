package selfupdate

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStageDiscoveryReverifiesImmutableBytesAndReceipt(t *testing.T) {
	for _, mode := range []string{"valid", "archive", "executable", "receipt", "signature", "expired", "schema", "extra", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			archive := archiveFixture(t, "linux", []string{"minecraft-ui", "LICENSE", "THIRD_PARTY_NOTICES.txt"})
			trust, b, sig := stageFixture(t, "linux", archive)
			s := Stager{Dir: t.TempDir(), Trust: trust, CurrentVersion: "v1.2.2", Schema: 16, OS: "linux", Arch: "amd64", Now: func() time.Time { return fixtureNow }}
			stage, err := s.Stage(context.Background(), "v1.2.3", b, sig, bytes.NewReader(archive))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			file := ""
			switch mode {
			case "archive":
				file = archiveName("linux")
			case "executable":
				file = "minecraft-ui"
			case "receipt":
				file = "ready.json"
			case "signature":
				file = "release.sig"
			case "expired":
				s.Now = func() time.Time { return fixtureNow.Add(2 * time.Hour) }
			case "schema":
				s.Schema++
			case "extra":
				file = "unexpected"
			case "canceled":
				cancel()
			}
			if file != "" {
				if err := os.WriteFile(filepath.Join(s.Dir, stage.ID, file), []byte("tampered"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			stages, err := s.List(ctx)
			if mode == "valid" {
				if err != nil || len(stages) != 1 || stages[0] != stage {
					t.Fatal(stages, err)
				}
			} else if err == nil || len(stages) != 0 {
				t.Fatal("unsafe stage exposed", stages, err)
			}
			if mode == "executable" {
				if _, err := s.Stage(context.Background(), "v1.2.3", b, sig, nil); err == nil {
					t.Fatal("idempotent retry trusted damaged stage")
				}
			}
		})
	}
}
func TestStageDiscoveryIsBoundedAndFirstUseDoesNotCreateFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "absent")
	s := Stager{Dir: dir}
	stages, err := s.List(context.Background())
	if err != nil || len(stages) != 0 {
		t.Fatal(stages, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("discovery created staging directory")
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if err := os.Mkdir(filepath.Join(dir, digest([]byte{byte(i)})), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.List(context.Background()); err == nil {
		t.Fatal("unbounded stages accepted")
	}
}

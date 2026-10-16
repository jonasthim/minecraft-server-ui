package content

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/catalog"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/server/loaderutil"
	"sync/atomic"
	"testing"
	"time"
)

func TestPackParticipantRejectsForgedAndExpiredCapabilities(t *testing.T) {
	p := &PackParticipant{}
	if e := p.Commit(t.Context(), nil); domain.AsError(e).Code != domain.CodeForbidden {
		t.Fatal("forged capability", e)
	}
	if e := p.ValidatePostimages(t.Context()); domain.AsError(e).Code != domain.CodeForbidden {
		t.Fatal("forged validation", e)
	}
}

func TestPackCandidateCannotReuseStaleLanguageEvidenceAfterVerificationFails(t *testing.T) {
	tuple := Tuple{Minecraft: "1.20.1", Loader: "forge", LoaderVersion: "47.4.0"}
	evidence := loaderutil.LanguageEvidence{Selection: domain.VersionSelection{Flavor: domain.FlavorForge, MinecraftVersion: tuple.Minecraft, LoaderVersion: tuple.LoaderVersion}, Generation: "verified-old-generation", Languages: map[string]loaderutil.LanguageArtifact{"javafml": {Version: "47"}}}
	item := Installed{ID: "installed", Kind: catalog.Mod, Enabled: true, Descriptor: Descriptor{ID: "example", Version: "1.0", Loader: "forge", Minecraft: "1.20.1", LanguageLoader: "javafml", LanguageRequirement: "[47,)"}, LanguageEvidence: &evidence}
	if blockers := ValidatePackCandidate(tuple, []Installed{item}); len(blockers) > 0 {
		t.Fatal("fixture old evidence must satisfy graph", blockers)
	}
	for _, provider := range []string{"failing", "absent"} {
		t.Run(provider, func(t *testing.T) {
			s := &Service{}
			if provider == "failing" {
				s.languageEvidence = func(context.Context, string, domain.VersionSelection) (loaderutil.LanguageEvidence, error) {
					return loaderutil.LanguageEvidence{}, errors.New("current generation verification failed")
				}
			}
			candidate, blockers, e := s.PreparePackCandidate(t.Context(), "alpha", tuple, []Installed{item})
			if e == nil && (len(blockers) == 0 || candidate[0].LanguageEvidence != nil) {
				t.Fatal("stale language evidence survived failed fresh verification", candidate, blockers)
			}
			if item.LanguageEvidence == nil {
				t.Fatal("caller candidate mutated")
			}
		})
	}
}

func TestPackParticipantRejectsInstalledForgeEvidenceAfterFreshAdapterFailure(t *testing.T) {
	f := setup(t, true)
	if _, e := f.db.Exec("UPDATE instances SET flavor='forge',minecraft_version='1.20.1',loader_version='47.4.0' WHERE id='alpha'"); e != nil {
		t.Fatal(e)
	}
	tuple := Tuple{Minecraft: "1.20.1", Loader: "forge", LoaderVersion: "47.4.0"}
	evidence := loaderutil.LanguageEvidence{Selection: domain.VersionSelection{Flavor: domain.FlavorForge, MinecraftVersion: tuple.Minecraft, LoaderVersion: tuple.LoaderVersion}, Generation: "previously-verified-generation", Languages: map[string]loaderutil.LanguageArtifact{"javafml": {Version: "47"}}}
	item := Installed{ID: "installed", Kind: catalog.Mod, Enabled: true, Descriptor: Descriptor{ID: "example", Version: "1.0", Loader: "forge", Minecraft: "1.20.1", LanguageLoader: "javafml", LanguageRequirement: "[47,)"}, LanguageEvidence: &evidence}
	raw, e := json.Marshal(item)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.db.Exec("INSERT INTO content_items(instance_id,id,manifest_json) VALUES('alpha',?,?)", item.ID, string(raw)); e != nil {
		t.Fatal(e)
	}
	manifest, e := f.s.Snapshot(t.Context(), "alpha")
	if e != nil || len(ValidatePackCandidate(tuple, manifest.Items)) > 0 {
		t.Fatal(manifest, e)
	}
	f.s.languageEvidence = func(context.Context, string, domain.VersionSelection) (loaderutil.LanguageEvidence, error) {
		return evidence, nil
	}
	prepared, blockers, e := f.s.PreparePackCandidate(t.Context(), "alpha", tuple, manifest.Items)
	if e != nil || len(blockers) > 0 {
		t.Fatal(blockers, e)
	}
	f.s.languageEvidence = func(context.Context, string, domain.VersionSelection) (loaderutil.LanguageEvidence, error) {
		return loaderutil.LanguageEvidence{}, errors.New("current committed generation verification failed")
	}
	queue := jobs.New(f.db, nil)
	var entered atomic.Bool
	if e = queue.Register("pack_language_test", func(ctx context.Context, execution *jobs.Execution) error {
		return f.s.instances.WithStoppedJob(ctx, execution, func(session *instance.StoppedSession) error {
			return f.s.WithPackParticipant(ctx, session, manifest, prepared, admin, func(context.Context, instance.Detail, instance.Directories, *PackParticipant) error {
				entered.Store(true)
				return nil
			})
		})
	}); e != nil {
		t.Fatal(e)
	}
	if e = queue.Start(t.Context()); e != nil {
		t.Fatal(e)
	}
	defer queue.Stop(context.Background())
	job, e := queue.Enqueue(t.Context(), jobs.Request{InstanceID: "alpha", Kind: "pack_language_test", RequestedBy: admin.ID})
	if e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, e = queue.Get(t.Context(), job.ID)
		if e != nil {
			t.Fatal(e)
		}
		if job.State == domain.JobFailed {
			if entered.Load() {
				t.Fatal("stale evidence admitted participant callback")
			}
			return
		}
		if job.State == domain.JobSucceeded {
			t.Fatal("installed stale Forge evidence admitted participant")
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("participant job timed out")
}

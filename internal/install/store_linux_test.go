//go:build linux

package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func openTestJournalStore(t *testing.T) (*journalStore, *os.Root, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	store, err := newJournalStoreWithConfig(context.Background(), root, journalStoreConfig{testRoot: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, root, dir
}

func journalTestTransaction(intent string) transaction {
	tx := preparedFixture(modeApply)
	sum := sha256.Sum256([]byte(intent))
	tx.IntentSHA256 = hex.EncodeToString(sum[:])
	return *tx
}

func TestJournalStoreLoadDistinguishesAbsentAndRejectsMalformedJournal(t *testing.T) {
	store, root, _ := openTestJournalStore(t)
	got, err := store.Load(context.Background())
	if err != nil || got != nil {
		t.Fatalf("Load absent = (%v, %v), want (nil, nil)", got, err)
	}
	for name, body := range map[string]string{
		"unknown":   `{"format":1,"id":"` + strings.Repeat("1", 64) + `","mode":"apply","phase":"prepared","intent_sha256":"` + strings.Repeat("2", 64) + `","prior":{"version":"v1.2.2","schema":1,"sha256":"` + strings.Repeat("3", 64) + `"},"target":{"version":"v1.2.3","schema":1,"sha256":"` + strings.Repeat("4", 64) + `"},"extra":true}`,
		"trailing":  `{} {}`,
		"duplicate": `{"format":1,"format":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := root.WriteFile(journalName, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Load(context.Background()); err == nil {
				t.Fatal("Load accepted malformed journal")
			}
			if err := root.Remove(journalName); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestJournalStoreCreateIntentIsExclusiveAndLoadBindsDigest(t *testing.T) {
	store, root, dir := openTestJournalStore(t)
	ctx := context.Background()
	body := []byte(`{"intent":"opaque native bytes"}`)
	const id = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digest, err := store.CreateIntent(ctx, id, body)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	if digest != hex.EncodeToString(sum[:]) {
		t.Fatalf("digest = %q", digest)
	}
	if _, err := store.CreateIntent(ctx, id, []byte("different")); err == nil {
		t.Fatal("CreateIntent clobbered an existing operation")
	}
	tx := journalTestTransaction(string(body))
	tx.ID = id
	if err := store.Record(ctx, nil, tx); err != nil {
		t.Fatal(err)
	}
	restarted, err := newJournalStoreWithConfig(ctx, root, journalStoreConfig{testRoot: true})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	got, err := restarted.Load(ctx)
	if err != nil || got == nil || !reflect.DeepEqual(*got, tx) {
		t.Fatalf("Load = (%+v, %v), want tx", got, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "install", id, "intent.json"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx); err == nil {
		t.Fatal("Load accepted an intent digest mismatch")
	}
}

func TestJournalStoreRecordCASUsesCapturedIdentityAndRefreshesSnapshot(t *testing.T) {
	store, root, dir := openTestJournalStore(t)
	ctx := context.Background()
	body := []byte("intent")
	tx := journalTestTransaction(string(body))
	if _, err := store.CreateIntent(ctx, tx.ID, body); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(ctx, nil, tx); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(ctx)
	if err != nil || loaded == nil {
		t.Fatalf("Load = (%v, %v)", loaded, err)
	}
	// Replacing the journal with byte-identical contents must still invalidate the CAS.
	path := filepath.Join(dir, journalName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".swap", raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".swap", path); err != nil {
		t.Fatal(err)
	}
	next := *loaded
	next.Phase = phaseQuiescing
	if err := store.Record(ctx, loaded, next); err == nil {
		t.Fatal("Record accepted a byte-identical replacement inode")
	}
	if _, err := store.Load(ctx); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.Load(ctx)
	if err != nil || loaded == nil {
		t.Fatalf("reload = (%v, %v)", loaded, err)
	}
	next = *loaded
	next.Phase = phaseQuiescing
	if err := store.Record(ctx, loaded, next); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.Load(ctx)
	if err != nil || loaded == nil || loaded.Phase != phaseQuiescing {
		t.Fatalf("phase after successful Record = (%+v, %v)", loaded, err)
	}
	if err := root.Remove(journalName); err != nil {
		t.Fatal(err)
	}
}

func TestJournalStoreRecordRejectsImmutableChangesAndForeignPending(t *testing.T) {
	store, root, _ := openTestJournalStore(t)
	ctx := context.Background()
	body := []byte("intent")
	tx := journalTestTransaction(string(body))
	if _, err := store.CreateIntent(ctx, tx.ID, body); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(ctx, nil, tx); err != nil {
		t.Fatal(err)
	}
	prior, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	next := *prior
	next.Phase = phaseQuiescing
	next.Target = &Image{Version: "v1.2.3", Schema: 2, SHA256: strings.Repeat("4", 64)}
	if err := store.Record(ctx, prior, next); err == nil {
		t.Fatal("Record accepted an immutable target change")
	}
	foreign := []byte("foreign pending")
	if err := root.WriteFile(pendingName, foreign, 0600); err != nil {
		t.Fatal(err)
	}
	next = *prior
	next.Phase = phaseQuiescing
	if err := store.Record(ctx, prior, next); err == nil {
		t.Fatal("Record overwrote a foreign pending file")
	}
	actual, err := root.ReadFile(pendingName)
	if err != nil || string(actual) != string(foreign) {
		t.Fatalf("foreign pending changed: (%q, %v)", actual, err)
	}
}

func TestJournalStorePersistsEveryTransactionPhase(t *testing.T) {
	store, _, _ := openTestJournalStore(t)
	ctx := context.Background()
	body := []byte("intent")
	tx := journalTestTransaction(string(body))
	if _, err := store.CreateIntent(ctx, tx.ID, body); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(ctx, nil, tx); err != nil {
		t.Fatal(err)
	}
	phases := []phase{
		phaseQuiescing, phaseStopped, phaseReplacing, phasePublished, phaseVerifying,
		phaseCommitted, phaseRollingBack, phaseRolledBack, phaseManualRepair,
	}
	for _, phase := range phases {
		prior, err := store.Load(ctx)
		if err != nil || prior == nil {
			t.Fatalf("Load before %s = (%v, %v)", phase, prior, err)
		}
		next := *prior
		next.Phase = phase
		if phase == phaseRollingBack || phase == phaseRolledBack || phase == phaseManualRepair {
			next.FailedStage = "recover"
		} else {
			next.FailedStage = ""
		}
		if err := store.Record(ctx, prior, next); err != nil {
			t.Fatalf("Record phase %s: %v", phase, err)
		}
		got, err := store.Load(ctx)
		if err != nil || got == nil || got.Phase != phase {
			t.Fatalf("Load after %s = (%+v, %v)", phase, got, err)
		}
	}
}

func TestJournalStoreRecordRejectsEachImmutableIdentityField(t *testing.T) {
	mutations := map[string]func(*transaction){
		"operation id": func(tx *transaction) { tx.ID = strings.Repeat("b", 64) },
		"mode": func(tx *transaction) {
			target := *tx.Target
			tx.Mode, tx.Prior, tx.Target = modeUninstall, &target, nil
		},
		"intent digest": func(tx *transaction) { tx.IntentSHA256 = strings.Repeat("5", 64) },
		"prior image": func(tx *transaction) {
			prior := *tx.Prior
			prior.SHA256 = strings.Repeat("6", 64)
			tx.Prior = &prior
		},
		"target image": func(tx *transaction) {
			target := *tx.Target
			target.SHA256 = strings.Repeat("7", 64)
			tx.Target = &target
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			store, _, _ := openTestJournalStore(t)
			ctx := context.Background()
			body := []byte("intent")
			prior := journalTestTransaction(string(body))
			if _, err := store.CreateIntent(ctx, prior.ID, body); err != nil {
				t.Fatal(err)
			}
			if err := store.Record(ctx, nil, prior); err != nil {
				t.Fatal(err)
			}
			loaded, err := store.Load(ctx)
			if err != nil || loaded == nil {
				t.Fatalf("Load = (%v, %v)", loaded, err)
			}
			next := *loaded
			next.Phase = phaseQuiescing
			mutate(&next)
			if err := store.Record(ctx, loaded, next); err == nil {
				t.Fatal("Record accepted an immutable identity change")
			}
		})
	}
}

func TestJournalStoreCancelledAndUncertainPublicationFailClosed(t *testing.T) {
	store, root, _ := openTestJournalStore(t)
	tx := journalTestTransaction("intent")
	if _, err := store.CreateIntent(context.Background(), tx.ID, []byte("intent")); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Record(cancelled, nil, tx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Record error = %v", err)
	}
	store.config.afterPublish = func() error { return errors.New("injected directory fence failure") }
	if err := store.Record(context.Background(), nil, tx); err == nil {
		t.Fatal("Record lost an injected post-rename fence failure")
	}
	if _, err := root.Lstat(journalName); err != nil {
		t.Fatalf("published journal was removed after uncertain failure: %v", err)
	}
	if _, err := root.Lstat(pendingName); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending marker remains after rename: %v", err)
	}
	if _, err := store.Load(context.Background()); err == nil {
		t.Fatal("uncertain store allowed Load to guess publication state")
	}
	store.config.afterPublish = nil
	if err := store.Record(context.Background(), nil, tx); err == nil {
		t.Fatal("store allowed a guessed mutation after uncertain publication")
	}
}

func TestJournalStoreRejectsIntentLinksAndWrongIdentity(t *testing.T) {
	store, _, dir := openTestJournalStore(t)
	ctx := context.Background()
	const id = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	body := []byte("intent")
	if _, err := store.CreateIntent(ctx, id, body); err != nil {
		t.Fatal(err)
	}
	tx := journalTestTransaction(string(body))
	tx.ID = id
	if err := store.Record(ctx, nil, tx); err != nil {
		t.Fatal(err)
	}
	intent := filepath.Join(dir, "install", id, "intent.json")
	if err := os.Link(intent, intent+".link"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx); err == nil {
		t.Fatal("Load accepted a multiply-linked intent")
	}
	if err := os.Remove(intent + ".link"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(intent); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(filepath.Dir(intent), "foreign.json")
	if err := os.WriteFile(foreign, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreign, intent); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx); err == nil {
		t.Fatal("Load accepted a symlink intent")
	}
}

func TestJournalStoreLoadRejectsMissingAndTruncatedIntent(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{name: "missing"},
		{name: "truncated", body: []byte("{")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, root, dir := openTestJournalStore(t)
			ctx := context.Background()
			full := []byte(`{"intent":"complete"}`)
			tx := journalTestTransaction(string(full))
			if _, err := store.CreateIntent(ctx, tx.ID, full); err != nil {
				t.Fatal(err)
			}
			if err := store.Record(ctx, nil, tx); err != nil {
				t.Fatal(err)
			}
			intent := filepath.Join(dir, installDirectory, tx.ID, intentFileName)
			if tc.name == "missing" {
				if err := os.Remove(intent); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(intent, tc.body, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Load(ctx); err == nil {
				t.Fatal("Load accepted a missing or truncated intent")
			}
			if _, err := root.Lstat(journalName); err != nil {
				t.Fatalf("journal unexpectedly changed: %v", err)
			}
		})
	}
}

func TestJournalStorePrepublicationRacePreservesCompetingJournal(t *testing.T) {
	store, root, _ := openTestJournalStore(t)
	tx := journalTestTransaction("intent")
	if _, err := store.CreateIntent(context.Background(), tx.ID, []byte("intent")); err != nil {
		t.Fatal(err)
	}
	foreign := []byte("foreign journal")
	store.config.beforePublish = func() error {
		return root.WriteFile(journalName, foreign, 0600)
	}
	if err := store.Record(context.Background(), nil, tx); err == nil {
		t.Fatal("Record published over a competing journal")
	}
	actual, err := root.ReadFile(journalName)
	if err != nil || string(actual) != string(foreign) {
		t.Fatalf("competing journal changed: (%q, %v)", actual, err)
	}
}

func TestJournalStorePrepublicationFailureCleansOwnedPendingAndKeepsOldJournal(t *testing.T) {
	store, root, _ := openTestJournalStore(t)
	ctx := context.Background()
	body := []byte("intent")
	priorTx := journalTestTransaction(string(body))
	if _, err := store.CreateIntent(ctx, priorTx.ID, body); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(ctx, nil, priorTx); err != nil {
		t.Fatal(err)
	}
	prior, err := store.Load(ctx)
	if err != nil || prior == nil {
		t.Fatalf("Load prior = (%v, %v)", prior, err)
	}
	next := *prior
	next.Phase = phaseQuiescing
	cancelled, cancel := context.WithCancel(ctx)
	defer cancel()
	store.config.beforePublish = func() error {
		cancel()
		return context.Canceled
	}
	if err := store.Record(cancelled, prior, next); !errors.Is(err, context.Canceled) {
		t.Fatalf("Record error = %v, want canceled publication", err)
	}
	if _, err := root.Lstat(pendingName); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned pending marker remains: %v", err)
	}
	loaded, err := store.Load(ctx)
	if err != nil || loaded == nil || loaded.Phase != phasePrepared {
		t.Fatalf("old journal after cleanup = (%+v, %v)", loaded, err)
	}
	store.config.beforePublish = nil
	if err := store.Record(ctx, loaded, next); err != nil {
		t.Fatalf("Record after cleanup: %v", err)
	}
}

func TestJournalStorePrepublicationCleanupPreservesReplacedPending(t *testing.T) {
	store, root, dir := openTestJournalStore(t)
	tx := journalTestTransaction("intent")
	if _, err := store.CreateIntent(context.Background(), tx.ID, []byte("intent")); err != nil {
		t.Fatal(err)
	}
	foreign := []byte("foreign pending")
	store.config.beforePublish = func() error {
		pending := filepath.Join(dir, pendingName)
		if err := os.Rename(pending, pending+".owned-copy"); err != nil {
			return err
		}
		return root.WriteFile(pendingName, foreign, 0600)
	}
	if err := store.Record(context.Background(), nil, tx); err == nil {
		t.Fatal("Record ignored pending replacement")
	}
	actual, err := root.ReadFile(pendingName)
	if err != nil || string(actual) != string(foreign) {
		t.Fatalf("foreign pending changed: (%q, %v)", actual, err)
	}
}

func TestJournalStoreCloseIsIdempotentAndRejectsUse(t *testing.T) {
	store, _, _ := openTestJournalStore(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background()); err == nil {
		t.Fatal("closed store accepted Load")
	}
}

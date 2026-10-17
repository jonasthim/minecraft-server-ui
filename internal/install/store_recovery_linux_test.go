//go:build linux

package install

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func writeJournalPendingFixture(t *testing.T, root *os.Root, tx transaction) []byte {
	t.Helper()
	body, err := json.Marshal(tx)
	if err != nil {
		t.Fatal(err)
	}
	file, err := root.OpenFile(pendingName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeAll(file, body); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := syncDirectory(root); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestJournalStoreRecoverPendingPromotesPreparedAndNextTransition(t *testing.T) {
	for _, initial := range []bool{false, true} {
		t.Run(map[bool]string{false: "initial", true: "phase_advance"}[initial], func(t *testing.T) {
			store, root, _ := openTestJournalStore(t)
			ctx := context.Background()
			intent := []byte(`{"native":"authenticated"}`)
			tx := journalTestTransaction(string(intent))
			tx.ID = strings.Repeat("a", 64)
			if _, err := store.CreateIntent(ctx, tx.ID, intent); err != nil {
				t.Fatal(err)
			}
			if initial {
				if err := store.Record(ctx, nil, tx); err != nil {
					t.Fatal(err)
				}
				tx.Phase = phaseQuiescing
			}
			want := *cloneTransaction(&tx)
			body := writeJournalPendingFixture(t, root, tx)
			if _, err := store.Load(ctx); err == nil {
				t.Fatal("Load consumed or ignored pending publication")
			}
			if err := store.RecoverPending(ctx); err != nil {
				t.Fatalf("RecoverPending: %v", err)
			}
			got, err := store.Load(ctx)
			if err != nil || got == nil || !reflect.DeepEqual(*got, want) {
				t.Fatalf("Load after recovery = (%+v, %v), want %+v", got, err, want)
			}
			if _, err := root.Lstat(pendingName); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("pending remains after promotion: %v", err)
			}
			published, err := root.ReadFile(journalName)
			if err != nil || string(published) != string(body) {
				t.Fatalf("published bytes = %q, %v", published, err)
			}
		})
	}
}

func TestJournalStoreRecoverPendingAbsentIsReadOnlyNoop(t *testing.T) {
	store, root, _ := openTestJournalStore(t)
	before, err := readInstallTestDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverPending(context.Background()); err != nil {
		t.Fatalf("RecoverPending with absent slot: %v", err)
	}
	after, err := readInstallTestDir(root)
	if err != nil || len(after) != len(before) {
		t.Fatalf("state root changed on absent recovery: before=%v after=%v err=%v", before, after, err)
	}
	if _, err := root.Lstat(journalName); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absent recovery created journal: %v", err)
	}
}

func readInstallTestDir(root *os.Root) ([]os.DirEntry, error) {
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	return dir.ReadDir(-1)
}

func TestJournalStoreRecoverPendingPreservesReplacementAtFence(t *testing.T) {
	for _, target := range []string{"pending", "current"} {
		t.Run(target, func(t *testing.T) {
			store, root, _ := openTestJournalStore(t)
			ctx := context.Background()
			intent := []byte(`{"native":"authenticated"}`)
			tx := journalTestTransaction(string(intent))
			tx.ID = strings.Repeat("c", 64)
			if _, err := store.CreateIntent(ctx, tx.ID, intent); err != nil {
				t.Fatal(err)
			}
			if err := store.Record(ctx, nil, tx); err != nil {
				t.Fatal(err)
			}
			currentRaw, err := root.ReadFile(journalName)
			if err != nil {
				t.Fatal(err)
			}
			currentInfo, err := root.Lstat(journalName)
			if err != nil {
				t.Fatal(err)
			}
			next := tx
			next.Phase = phaseQuiescing
			pendingRaw := writeJournalPendingFixture(t, root, next)
			foreign := []byte(`foreign replacement`)
			store.config.beforePublish = func() error {
				name := pendingName
				body := foreign
				backup := "captured-pending.fixture"
				if target == "current" {
					name, body = journalName, currentRaw
					backup = "captured-journal.fixture"
				}
				if err := root.Rename(name, backup); err != nil {
					return err
				}
				if err := root.WriteFile(name, body, 0600); err != nil {
					return err
				}
				if target == "current" {
					latest, err := root.Lstat(journalName)
					if err != nil || os.SameFile(currentInfo, latest) {
						return errors.Join(errors.New("fixture failed to replace current journal inode"), err)
					}
				}
				return nil
			}
			if err := store.RecoverPending(ctx); err == nil {
				t.Fatal("recovery accepted replaced publication operand")
			}
			if target == "pending" {
				actual, err := root.ReadFile(pendingName)
				if err != nil || string(actual) != string(foreign) {
					t.Fatalf("foreign pending replacement changed: %q, %v", actual, err)
				}
			} else {
				actual, err := root.ReadFile(journalName)
				if err != nil || string(actual) != string(currentRaw) {
					t.Fatalf("replacement current journal changed: %q, %v", actual, err)
				}
				actualPending, err := root.ReadFile(pendingName)
				if err != nil || string(actualPending) != string(pendingRaw) {
					t.Fatalf("pending candidate changed: %q, %v", actualPending, err)
				}
			}
		})
	}
}

func TestJournalStoreRecoverPendingPreservesInvalidCandidates(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *journalStore, *os.Root, transaction)
	}{
		{name: "partial", mutate: func(t *testing.T, _ *journalStore, root *os.Root, _ transaction) {
			if err := root.WriteFile(pendingName, []byte(`{"format":`), 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "wrong immutable operation", mutate: func(t *testing.T, store *journalStore, root *os.Root, tx transaction) {
			if err := store.Record(context.Background(), nil, tx); err != nil {
				t.Fatal(err)
			}
			tx.Phase = phaseQuiescing
			tx.IntentSHA256 = strings.Repeat("f", 64)
			writeJournalPendingFixture(t, root, tx)
		}},
		{name: "impossible phase jump", mutate: func(t *testing.T, store *journalStore, root *os.Root, tx transaction) {
			if err := store.Record(context.Background(), nil, tx); err != nil {
				t.Fatal(err)
			}
			tx.Phase = phaseCommitted
			writeJournalPendingFixture(t, root, tx)
		}},
		{name: "missing intent", mutate: func(t *testing.T, _ *journalStore, root *os.Root, tx transaction) {
			writeJournalPendingFixture(t, root, tx)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, root, _ := openTestJournalStore(t)
			intent := []byte(`{"native":"authenticated"}`)
			tx := journalTestTransaction(string(intent))
			tx.ID = strings.Repeat("b", 64)
			if tc.name != "missing intent" {
				if _, err := store.CreateIntent(context.Background(), tx.ID, intent); err != nil {
					t.Fatal(err)
				}
			}
			tc.mutate(t, store, root, tx)
			before, err := root.ReadFile(pendingName)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.RecoverPending(context.Background()); err == nil {
				t.Fatal("RecoverPending accepted invalid candidate")
			}
			after, err := root.ReadFile(pendingName)
			if err != nil || string(after) != string(before) {
				t.Fatalf("invalid pending changed: before=%q after=%q err=%v", before, after, err)
			}
			if _, err := root.Lstat(journalName); tc.name == "missing intent" && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid pending published a journal: %v", err)
			}
		})
	}
}

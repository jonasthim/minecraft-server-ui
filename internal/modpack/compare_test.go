package modpack

import "testing"

func TestThreeWayDecisionsPreserveEditsAndMissing(t *testing.T) {
	cases := []struct{ old, local, next, action string }{
		{"", "", "n", "add"}, {"o", "o", "n", "update"}, {"o", "o", "", "delete"},
		{"o", "edit", "n", "conflict"}, {"o", "edit", "", "conflict"}, {"o", "", "n", "conflict"},
		{"o", "edit", "o", "keep"}, {"", "local", "", "keep"}, {"o", "n", "n", "keep"},
	}
	for _, c := range cases {
		got, e := Compare(map[string]string{"config/a.cfg": c.old}, map[string]string{"config/a.cfg": c.local}, map[string]string{"config/a.cfg": c.next}, nil, nil)
		if e != nil || len(got) != 1 || got[0].Action != c.action {
			t.Fatalf("%+v: %+v %v", c, got, e)
		}
	}
}
func TestSelectionsPersistWithoutOverwritingPinnedOrExcluded(t *testing.T) {
	for _, policy := range []Selection{{Pinned: true}, {Excluded: true}, {Optional: true, Selected: false}} {
		got, e := Compare(map[string]string{"mods/a.jar": "old"}, map[string]string{"mods/a.jar": "local"}, map[string]string{"mods/a.jar": "new"}, map[string]Selection{"mods/a.jar": policy}, nil)
		if e != nil || got[0].Action != "keep" {
			t.Fatalf("%+v: %+v %v", policy, got, e)
		}
	}
	got, e := Compare(map[string]string{"config/a.cfg": "old"}, map[string]string{"config/a.cfg": "edit"}, map[string]string{"config/a.cfg": "new"}, nil, map[string]string{"config/a.cfg": "upstream"})
	if e != nil || got[0].Action != "update" || got[0].Before != "edit" {
		t.Fatal(got, e)
	}
}
func TestCompareRejectsUnknownChoicesAndCaseCollisions(t *testing.T) {
	if _, e := Compare(nil, nil, map[string]string{"config/A.cfg": "a", "config/a.cfg": "b"}, nil, nil); e == nil {
		t.Fatal("collision accepted")
	}
	if _, e := Compare(nil, nil, nil, nil, map[string]string{"config/no.cfg": "upstream"}); e == nil {
		t.Fatal("unbound choice accepted")
	}
}

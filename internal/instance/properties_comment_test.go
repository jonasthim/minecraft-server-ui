package instance

import (
	"os"
	"path/filepath"
	"testing"
)

func readCommentProperties(t *testing.T, raw string) map[string]string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "server.properties"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	properties, err := unknownPropertiesAt(root, "server.properties")
	if err != nil {
		t.Fatal(err)
	}
	return properties
}

func TestUnknownPropertiesCommentBackslashDoesNotConsumeProperty(t *testing.T) {
	for _, marker := range []string{"#", "!", " \t\f#", " \t\f!"} {
		t.Run(marker, func(t *testing.T) {
			got := readCommentProperties(t, marker+" ordinary comment \\\nordinary=value\nretained=present\n")
			if got["ordinary"] != "value" || got["retained"] != "present" || len(got) != 2 {
				t.Fatalf("comment consumed a natural property: %v", got)
			}
		})
	}
}

func TestUnknownPropertiesContinuedValuesKeepCommentMarkers(t *testing.T) {
	got := readCommentProperties(t, "custom=first\\\n \t#middle\\\n \f!last\nordinary=value\n")
	if got["custom"] != "first#middle!last" || got["ordinary"] != "value" || len(got) != 2 {
		t.Fatalf("continued values were treated as comments: %v", got)
	}
}

func TestUnknownPropertiesRetainsKeyAndValueContinuations(t *testing.T) {
	got := readCommentProperties(t, "cus\\\n tom=first\\\n  second\nescaped\\:key=value\n")
	if got["custom"] != "firstsecond" || got["escaped:key"] != "value" || len(got) != 2 {
		t.Fatalf("genuine continuations or escaped keys changed: %v", got)
	}
}

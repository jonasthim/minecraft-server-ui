package instance

import (
	"strings"
	"testing"
)

func TestBackupPropertiesRemovesDecodedPrivateControls(t *testing.T) {
	raw := []byte("# public\nmanagement-server-secret=private\nmanagement\\u002dserver-secret=escaped\nmanagement-server-\\\n secret=continued\nrcon.password=hidden\nmax-players=20\ncustom\\:key=value\n")
	out, err := BackupProperties(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private", "escaped", "continued", "hidden"} {
		if strings.Contains(string(out), secret) {
			t.Fatalf("credential leaked: %s", out)
		}
	}
	if !strings.Contains(string(out), "max-players=20") || !strings.Contains(string(out), "custom\\:key=value") {
		t.Fatal("ordinary properties lost")
	}
}

func TestBackupPropertiesCommentBackslashDoesNotHidePrivateControls(t *testing.T) {
	controls := []string{
		"management-server-secret=private-value\n",
		"management-server-enabled=true\n",
		"management-server-host=private-host\n",
		"management-server-port=25599\n",
		"management-server-tls-enabled=true\n",
		"management-server-tls-keystore=private-file\n",
		"management-server-tls-keystore-password=private-password\n",
		"management-server-allowed-origins=private-origin\n",
		"management-server-future-control=private-value\n",
		"management\\u002dserver-secret=private-escaped\n",
		"management-server-\\\n secret=private-continued\n",
		"rcon.password=private-password\n",
		"rcon.port=25575\n",
		"rcon.future-control=private-value\n",
		"enable-rcon=true\n",
	}
	for _, marker := range []string{"#", "!", " \t\f#", " \t\f!"} {
		for _, control := range controls {
			t.Run(marker+"/"+strings.Split(control, "=")[0], func(t *testing.T) {
				comment := marker + " ordinary comment \\\n"
				ordinary := "ordinary=value\n"
				raw := comment + control + ordinary
				got, err := BackupProperties([]byte(raw))
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != comment+ordinary {
					t.Fatalf("comment consumed private control or ordinary configuration: got %q; want %q", got, comment+ordinary)
				}
			})
		}
	}
}

func TestBackupPropertiesCommentMarkersInsideContinuedValuesRemainData(t *testing.T) {
	ordinary := "custom=first\\\n \t#literal value\\\n \f!literal ending\n"
	private := "management-server-secret=private-first\\\n #private-middle\\\n !private-last\n"
	for _, raw := range []string{ordinary, private + ordinary} {
		got, err := BackupProperties([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != ordinary {
			t.Fatalf("continued value comment markers were misclassified: got %q; want %q", got, ordinary)
		}
	}
}

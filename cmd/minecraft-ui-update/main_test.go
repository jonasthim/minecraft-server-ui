package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"testing"
)

func TestInstallerRecoveryGatePrecedesOrdinaryRecovery(t *testing.T) {
	for _, test := range []struct {
		name                     string
		args                     []string
		handled, allowed, denied bool
		want                     []string
	}{
		{"installer command", []string{"install", "recover"}, true, false, false, []string{"installer"}},
		{"live installer permit", []string{"recover"}, false, true, false, []string{"installer", "gate"}},
		{"invalid installer state", []string{"recover"}, false, false, true, []string{"installer", "gate"}},
		{"ordinary recovery", []string{"recover"}, false, false, false, []string{"installer", "gate", "updater"}},
		{"ordinary command", []string{"check"}, false, false, false, []string{"installer", "updater"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			refused := errors.New("startup refused")
			backend := commandBackend{
				installer: func(context.Context, []string, io.Writer) (bool, error) {
					calls = append(calls, "installer")
					return test.handled, nil
				},
				startup: func(context.Context) (bool, error) {
					calls = append(calls, "gate")
					if test.denied {
						return false, refused
					}
					return test.allowed, nil
				},
				updater: func(context.Context, []string, io.Writer) error { calls = append(calls, "updater"); return nil },
			}
			err := run(context.Background(), test.args, io.Discard, backend)
			if !reflect.DeepEqual(calls, test.want) || errors.Is(err, refused) != test.denied {
				t.Fatal(calls, err)
			}
		})
	}
}

func TestVersionDoesNotInspectNativeState(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), []string{"version"}, &out, commandBackend{}); err != nil || out.String() != "minecraft-ui-update 0.0.0-dev\n" {
		t.Fatal(out.String(), err)
	}
}

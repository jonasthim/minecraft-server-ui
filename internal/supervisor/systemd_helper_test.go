//go:build linux

package supervisor

import (
	"os/exec"
	"testing"
)

func TestPrivilegedHelperRejectsUnsafeVerbsAndIDs(t *testing.T) {
	for _, args := range [][]string{{"restart", "test"}, {"start", "../escape"}, {"start", "--all"}, {"start", "test.service"}, {"start", "test", "extra"}, {"start", "con"}, {"start", "A"}} {
		command := exec.Command("/bin/sh", append([]string{"../../deploy/linux/unitctl"}, args...)...)
		err := command.Run()
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 {
			t.Fatalf("unsafe helper request accepted %q: %v", args, err)
		}
	}
}

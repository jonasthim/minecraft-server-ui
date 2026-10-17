//go:build windows

package selfupdate

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
)

type serviceAccountSIDLookup func(system, account string) (*windows.SID, string, uint32, error)
type localComputerNameLookup func() (string, error)

// verifyConfiguredServiceSID resolves only the SCM local-account spelling
// against the authoritative local computer name, then requires an exact SID
// match. Other account forms pass unchanged to the normal Windows resolver.
func verifyConfiguredServiceSID(accountName, expectedSID string, computerName localComputerNameLookup, lookup serviceAccountSIDLookup) error {
	if accountName == "" || strings.ContainsRune(accountName, '\x00') || lookup == nil {
		return errors.New("configured service account identity is invalid")
	}
	lookupName := accountName
	if strings.HasPrefix(accountName, `./`) || accountName == "." {
		return errors.New("configured local service account name is malformed")
	}
	if strings.HasPrefix(accountName, `.\`) {
		localName := accountName[2:]
		if localName == "" || strings.TrimSpace(localName) != localName || strings.ContainsAny(localName, `/\`) || strings.ContainsRune(localName, '\x00') {
			return errors.New("configured local service account name is malformed")
		}
		if computerName == nil {
			return errors.New("local computer name is unavailable")
		}
		localComputer, err := computerName()
		if err != nil {
			return fmt.Errorf("local computer name lookup failed: %w", err)
		}
		if localComputer == "" || strings.TrimSpace(localComputer) != localComputer || strings.ContainsAny(localComputer, `/\`) || strings.ContainsRune(localComputer, '\x00') {
			return errors.New("local computer name is invalid")
		}
		lookupName = localComputer + `\` + localName
	}
	expected, err := windows.StringToSid(expectedSID)
	if err != nil || expected == nil || !expected.IsValid() || expected.String() != expectedSID {
		return errors.New("expected service account identity is invalid")
	}
	actual, _, _, err := lookup("", lookupName)
	if err != nil {
		return fmt.Errorf("configured service account lookup failed: %w", err)
	}
	if actual == nil || !actual.IsValid() || actual.String() != expectedSID {
		return errors.New("configured service account SID does not match")
	}
	return nil
}

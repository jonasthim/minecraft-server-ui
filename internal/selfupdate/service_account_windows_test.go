//go:build windows

package selfupdate

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestVerifyConfiguredServiceSIDNormalizesLocalAccount(t *testing.T) {
	const expected = "S-1-5-21-111111111-222222222-333333333-1001"
	computerCalls := 0
	lookupCalls := 0
	err := verifyConfiguredServiceSID(`.\manager`, expected, func() (string, error) {
		computerCalls++
		return "HOSTNAME", nil
	}, func(system, account string) (*windows.SID, string, uint32, error) {
		lookupCalls++
		if system != "" || account != `HOSTNAME\manager` {
			t.Fatalf("lookup received unexpected arguments")
		}
		sid, err := windows.StringToSid(expected)
		return sid, "HOSTNAME", windows.SidTypeUser, err
	})
	if err != nil || computerCalls != 1 || lookupCalls != 1 {
		t.Fatalf("local account lookup failed or used unexpected calls: computer=%d lookup=%d", computerCalls, lookupCalls)
	}
}

func TestVerifyConfiguredServiceSIDPassesOtherNamesUnchanged(t *testing.T) {
	const expected = "S-1-5-21-111111111-222222222-333333333-1002"
	for _, input := range []string{`DOMAIN\service`, `NT AUTHORITY\SYSTEM`, "unqualified-user"} {
		t.Run(strings.ReplaceAll(input, `\`, "_"), func(t *testing.T) {
			computerCalled := false
			err := verifyConfiguredServiceSID(input, expected, func() (string, error) {
				computerCalled = true
				return "HOSTNAME", nil
			}, func(system, account string) (*windows.SID, string, uint32, error) {
				if system != "" || account != input {
					t.Fatalf("qualified account name was changed")
				}
				sid, err := windows.StringToSid(expected)
				return sid, "DOMAIN", windows.SidTypeUser, err
			})
			if err != nil || computerCalled {
				t.Fatalf("other account form failed or used local normalization")
			}
		})
	}
}

func TestVerifyConfiguredServiceSIDRejectsMalformedLocalNames(t *testing.T) {
	for _, input := range []string{"", "bad\x00name", `.\`, `./manager`, `.`, `.\manager\other`, `.\manager/other`} {
		t.Run(strings.ReplaceAll(strings.ReplaceAll(input, `\`, "_"), "/", "_"), func(t *testing.T) {
			called := false
			err := verifyConfiguredServiceSID(input, "S-1-5-21-111111111-222222222-333333333-1001", func() (string, error) {
				called = true
				return "HOSTNAME", nil
			}, func(string, string) (*windows.SID, string, uint32, error) {
				called = true
				return nil, "", 0, nil
			})
			if err == nil || called {
				t.Fatal("malformed local account name reached a resolver")
			}
		})
	}
}

func TestVerifyConfiguredServiceSIDRejectsMismatchAndLookupFailure(t *testing.T) {
	expected := "S-1-5-21-111111111-222222222-333333333-1001"
	computerName := func() (string, error) { return "HOSTNAME", nil }
	if err := verifyConfiguredServiceSID(`.\manager`, expected, computerName, func(string, string) (*windows.SID, string, uint32, error) {
		sid, err := windows.StringToSid("S-1-5-21-111111111-222222222-333333333-1002")
		return sid, "HOSTNAME", windows.SidTypeUser, err
	}); err == nil {
		t.Fatal("accepted an account whose SID differed from the expected SID")
	}
	lookupFailure := errors.New("lookup failure")
	if err := verifyConfiguredServiceSID(`.\manager`, expected, computerName, func(string, string) (*windows.SID, string, uint32, error) {
		return nil, "", 0, lookupFailure
	}); !errors.Is(err, lookupFailure) {
		t.Fatal("lookup failure identity was not preserved")
	}
}

func TestVerifyConfiguredServiceSIDMatchesCurrentLocalTokenAccount(t *testing.T) {
	computer, err := windows.ComputerName()
	if err != nil {
		t.Fatal("local computer name API failed")
	}
	token, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || token == nil || token.User.Sid == nil {
		t.Fatal("current token account API failed")
	}
	name, domain, _, err := token.User.Sid.LookupAccount("")
	if err != nil {
		t.Fatal("current token account lookup API failed")
	}
	if !strings.EqualFold(domain, computer) {
		t.Skip("current token is not a local machine account")
	}
	if name == "" {
		t.Fatal("current local token account name is empty")
	}
	if err := verifyConfiguredServiceSID(`.\`+name, token.User.Sid.String(), windows.ComputerName, windows.LookupSID); err != nil {
		t.Fatal("dot-qualified local account did not resolve to the current token SID")
	}
}

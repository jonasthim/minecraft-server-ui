//go:build windows

package managerstate

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestDuplicateMaintenanceMembershipTokenFromProcessPrimary(t *testing.T) {
	var processToken windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &processToken); err != nil {
		t.Fatal("current process token could not be opened")
	}
	defer windows.CloseHandle(windows.Handle(processToken))

	processUser, err := processToken.GetTokenUser()
	if err != nil || processUser.User.Sid == nil {
		t.Fatal("current process token user could not be read")
	}
	groups, err := processToken.GetTokenGroups()
	if err != nil {
		t.Fatal("current process token groups could not be read")
	}
	var enabledGroup *windows.SID
	for _, group := range groups.AllGroups() {
		if group.Attributes&windows.SE_GROUP_ENABLED != 0 && group.Sid != nil {
			enabledGroup = group.Sid
			break
		}
	}
	if enabledGroup == nil {
		t.Fatal("current process token has no enabled group for membership check")
	}
	if _, err := processToken.IsMember(enabledGroup); err == nil {
		t.Fatal("primary process token unexpectedly passed CheckTokenMembership")
	}

	membershipToken, err := duplicateMaintenanceMembershipToken(processToken)
	if err != nil {
		t.Fatal("process token could not be duplicated for membership check")
	}
	defer windows.CloseHandle(windows.Handle(membershipToken))

	membershipUser, err := membershipToken.GetTokenUser()
	if err != nil || membershipUser.User.Sid == nil || !processUser.User.Sid.Equals(membershipUser.User.Sid) {
		t.Fatal("membership token does not preserve the actual process identity")
	}
	member, err := membershipToken.IsMember(enabledGroup)
	if err != nil || !member {
		t.Fatal("impersonation token could not verify its enabled process group")
	}
}

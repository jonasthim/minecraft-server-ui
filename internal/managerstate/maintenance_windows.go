//go:build windows

package managerstate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
	"golang.org/x/sys/windows"
)

func authorizeMaintenance() error {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &token); err != nil {
		return errors.New("administrator token could not be verified")
	}
	defer windows.CloseHandle(windows.Handle(token))
	user, err := token.GetTokenUser()
	if err != nil {
		return errors.New("administrator token could not be verified")
	}
	if user.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		return nil
	}
	if !token.IsElevated() {
		return errors.New("elevated administrator token is required")
	}
	admin, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return errors.New("administrator membership could not be verified")
	}
	membershipToken, err := duplicateMaintenanceMembershipToken(token)
	if err != nil {
		return errors.New("administrator membership could not be verified")
	}
	defer windows.CloseHandle(windows.Handle(membershipToken))
	member, err := membershipToken.IsMember(admin)
	if err != nil || !member {
		return errors.New("elevated administrator token is required")
	}
	return nil
}

func duplicateMaintenanceMembershipToken(processToken windows.Token) (windows.Token, error) {
	var membershipToken windows.Token
	err := windows.DuplicateTokenEx(processToken, windows.TOKEN_QUERY, nil,
		windows.SecurityIdentification, windows.TokenImpersonation, &membershipToken)
	return membershipToken, err
}

func runningImageHash(ctx context.Context) (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", errors.New("running manager image path is unavailable")
	}
	return runningImageDigest(ctx, path)
}

// fenceMaintenance requires the fixed local C: NTFS volume because Windows
// directory handles do not provide the required metadata durability fence.
func fenceMaintenance(ctx context.Context, data, maintenance *os.Root) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if data == nil || maintenance == nil {
		return errors.New("maintenance fence roots are unavailable")
	}
	dataPath := filepath.Clean(data.Name())
	if !filepath.IsAbs(dataPath) || !strings.EqualFold(filepath.VolumeName(dataPath), "C:") {
		return errors.New("maintenance durability requires the fixed C: volume")
	}
	checkedData, err := safepath.OpenRoot(ctx, dataPath)
	if err != nil {
		return errors.New("manager data path could not be revalidated")
	}
	defer checkedData.Close()
	dataBefore, err := data.Stat(".")
	checkedInfo, checkErr := checkedData.Stat(".")
	if err != nil || checkErr != nil || !os.SameFile(dataBefore, checkedInfo) {
		return errors.New("manager data path changed before durability fence")
	}
	maintenanceInfo, err := data.Lstat("maintenance")
	maintenanceOpened, openErr := maintenance.Stat(".")
	maintenancePath, pathErr := os.Lstat(filepath.Join(dataPath, "maintenance"))
	if err != nil || openErr != nil || pathErr != nil ||
		!os.SameFile(maintenanceInfo, maintenanceOpened) ||
		!os.SameFile(maintenanceInfo, maintenancePath) {
		return errors.New("maintenance directory changed before durability fence")
	}
	fileName, err := windows.UTF16PtrFromString(dataPath)
	if err != nil {
		return errors.New("manager data path is invalid")
	}
	volumePath := make([]uint16, 260)
	const cDriveRoot = "C:\\"
	if err := windows.GetVolumePathName(fileName, &volumePath[0], uint32(len(volumePath))); err != nil ||
		!strings.EqualFold(windows.UTF16ToString(volumePath), cDriveRoot) {
		return errors.New("maintenance durability requires an unmounted C: path")
	}
	mount, _ := windows.UTF16PtrFromString(cDriveRoot)
	if windows.GetDriveType(mount) != windows.DRIVE_FIXED {
		return errors.New("maintenance durability requires a fixed local disk")
	}
	fs := make([]uint16, 64)
	var serial uint32
	if err := windows.GetVolumeInformation(mount, nil, 0, &serial, nil, nil, &fs[0], uint32(len(fs))); err != nil ||
		!strings.EqualFold(windows.UTF16ToString(fs), "NTFS") {
		return errors.New("maintenance durability requires NTFS")
	}
	const cVolumeDevice = "\\\\.\\C:"
	volumePathName, _ := windows.UTF16PtrFromString(cVolumeDevice)
	handle, err := windows.CreateFile(volumePathName, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return errors.New("maintenance volume fence could not be opened")
	}
	defer windows.CloseHandle(handle)
	var openedSerial uint32
	openedFS := make([]uint16, 64)
	if err := windows.GetVolumeInformationByHandle(handle, nil, 0, &openedSerial, nil, nil, &openedFS[0], uint32(len(openedFS))); err != nil ||
		openedSerial != serial || !strings.EqualFold(windows.UTF16ToString(openedFS), "NTFS") {
		return errors.New("maintenance volume identity changed")
	}
	if err := windows.FlushFileBuffers(handle); err != nil {
		return errors.New("maintenance volume flush failed; publication is uncertain")
	}
	if err := verifyDataBinding(ctx, dataPath, data); err != nil {
		return errors.New("manager data path changed after durability fence")
	}
	return ctx.Err()
}

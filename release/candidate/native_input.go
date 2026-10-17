package candidate

import (
	"context"
	"errors"
)

// SnapshotNativeInput copies and validates an owner-supplied executable and its
// adjacent .version.json receipt without executing either. The destination must
// be absent inside a private, caller-controlled directory whose ancestry cannot
// be changed by another principal for the entire call and subsequent consumption.
// On error the caller must discard that private directory, including partial
// snapshots. Returned provenance does not prove a native invocation occurred.
func SnapshotNativeInput(ctx context.Context, source, destination, platform, command, version string, schema int64) (Member, error) {
	if (platform != "linux" && platform != "windows") || !validVersion(version) {
		return Member{}, errors.New("invalid native executable platform or version")
	}
	switch command {
	case "minecraft-ui":
		if schema < 1 {
			return Member{}, errors.New("manager native input requires positive schema")
		}
	case "minecraft-ui-update", "minecraft-ui-bootstrap":
		if schema != 0 {
			return Member{}, errors.New("helper native input requires schema zero")
		}
	default:
		return Member{}, errors.New("unknown native executable command")
	}
	in, err := snapshot(ctx, source, destination, command, 0755, MaxBinaryBytes)
	if err != nil {
		return Member{}, err
	}
	if err = validateExecutable(in.path, platform, command); err != nil {
		return Member{}, err
	}
	if err = verifyReceipt(ctx, source, in, platform, command, version, schema); err != nil {
		return Member{}, err
	}
	return in.Member, nil
}

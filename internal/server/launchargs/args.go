// Package launchargs defines the coordinator's bounded JVM memory argv policy.
package launchargs

import (
	"fmt"
	"strconv"
	"strings"
)

// StripMemory accepts a provider base argv or one exact canonical coordinator
// prefix. The coordinator must bind its chosen maximum to DB instance.MemoryMiB
// before sealing the activation. This offline check does not open manager.db.
func StripMemory(args []string) ([]string, error) {
	base := args
	if len(args) > 0 && strings.HasPrefix(args[0], "-Xm") {
		if len(args) < 2 || !strings.HasPrefix(args[1], "-Xmx") || !strings.HasSuffix(args[1], "M") {
			return nil, fmt.Errorf("canonical Xms/Xmx prefix required")
		}
		raw := strings.TrimSuffix(strings.TrimPrefix(args[1], "-Xmx"), "M")
		maximum, err := strconv.Atoi(raw)
		if err != nil || maximum < 256 || maximum > 1048576 || strconv.Itoa(maximum) != raw || args[0] != "-Xms"+strconv.Itoa(min(512, maximum))+"M" {
			return nil, fmt.Errorf("memory prefix differs from canonical bounded policy")
		}
		base = args[2:]
	}
	for _, arg := range base {
		if strings.HasPrefix(arg, "-Xm") {
			return nil, fmt.Errorf("duplicate or misplaced memory switch")
		}
	}
	return append([]string(nil), base...), nil
}

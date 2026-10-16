package console

import (
	"strings"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

func TestPublicCommandBoundary(t *testing.T) {
	selection := domain.VersionSelection{Flavor: domain.FlavorVanilla, MinecraftVersion: "1.21.9"}
	for _, text := range []string{"list", "say Hello 世界", "seed"} {
		if _, err := ValidateCommand(selection, domain.RoleAdmin, text); err != nil {
			t.Fatalf("%q: %v", text, err)
		}
	}
	for _, text := range []string{"", " list", "list ", "List", "/list", "minecraft:list", "list uuids", "say @a", "say a\nstop", "say a\rstop", "say a\x00", "say a\u2028stop", "say a\u202Estop", "save-off", "save-on", "save-all flush", "stop", "restart", "reload", "execute run say x", "function x", "schedule function x 1t", "return run say x", "op user", "gamerule pvp false", "say " + strings.Repeat("x", 513)} {
		if _, err := ValidateCommand(selection, domain.RoleAdmin, text); err == nil {
			t.Errorf("accepted %q", text)
		}
	}
	if _, err := ValidateCommand(selection, domain.RoleViewer, "list"); err == nil {
		t.Error("viewer can execute")
	}
	if _, err := ValidateCommand(selection, domain.RoleOperator, "seed"); err == nil {
		t.Error("operator can see seed")
	}
	if _, err := ValidateCommand(selection, domain.RoleOperator, "say hello"); err != nil {
		t.Error(err)
	}
	selection.MinecraftVersion = "27.1"
	if _, err := ValidateCommand(selection, domain.RoleAdmin, "list"); err == nil {
		t.Error("accepted unreviewed version")
	}
	selection.MinecraftVersion = "1.12.1"
	if _, err := ValidateCommand(selection, domain.RoleAdmin, "list"); err == nil {
		t.Error("accepted old version")
	}
}

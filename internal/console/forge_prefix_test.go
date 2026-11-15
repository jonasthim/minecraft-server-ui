package console

import (
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

func TestNativeMessageAcceptsMinecraftLoggerTagsOnly(t *testing.T) {
	for line, want := range map[string]string{
		"[12:33:47] [Server thread/INFO]: There are 0 of a max of 2 players online: ":                             "There are 0 of a max of 2 players online: ",
		"[12:33:47] [Server thread/INFO] [minecraft/MinecraftServer]: There are 0 of a max of 2 players online: ": "There are 0 of a max of 2 players online: ",
		"[12:33:47] [Server thread/INFO] [minecraft/DedicatedServer]: There are 0/2 players online:":              "There are 0/2 players online:",
	} {
		if got, ok := nativeMessage(line); !ok || got != want {
			t.Fatalf("%q: got %q %v", line, got, ok)
		}
	}
	for _, line := range []string{
		"[12:33:47] [Server thread/INFO] [examplemod/Main]: Saved the game",
		"[12:33:47] [Server thread/WARN] [minecraft/MinecraftServer]: Saved the game",
		"[12:33:47] [Async Chat Thread/INFO] [minecraft/MinecraftServer]: Saved the game",
	} {
		if _, ok := nativeMessage(line); ok {
			t.Fatalf("accepted %q", line)
		}
	}
}

// 26.3 routes command feedback to the console as system chat. The prefix is
// accepted only for versions that emit it.
func TestSystemChatFeedbackIsVersionGated(t *testing.T) {
	got, ok := nativeMessage("[12:40:17] [Server thread/INFO]: System chat: Automatic saving is now enabled")
	if !ok || got != "System chat: Automatic saving is now enabled" {
		t.Fatalf("got %q %v", got, ok)
	}
	if !ackSaveOn("26.3")(got) {
		t.Fatal("26.3 save-on feedback rejected")
	}
	for _, version := range []string{"26.1", "1.21.9", "1.12.2"} {
		if ackSaveOn(version)(got) {
			t.Fatalf("%s accepted system chat feedback", version)
		}
	}
	if !ackSaveOn("26.3")("Automatic saving is now enabled") {
		t.Fatal("26.3 unprefixed feedback rejected")
	}
	if got, ok = nativeMessage("[12:40:17] [Server thread/INFO]: <Alex> System chat: Automatic saving is now enabled"); ok && ackSaveOn("26.3")(got) {
		t.Fatal("player chat spoofed a save-on acknowledgement")
	}
}

// Paper's console layout has no thread name. It is accepted only for Paper
// instances; plugin output carries a [Plugin] prefix and stays rejected.
func TestPaperConsoleLayoutIsFlavorGated(t *testing.T) {
	line := "[13:26:00 INFO]: Automatic saving is now enabled"
	if got, ok := nativeMessageFor(domain.FlavorPaper, line); !ok || got != "Automatic saving is now enabled" {
		t.Fatalf("paper: %q %v", got, ok)
	}
	for _, flavor := range []domain.Flavor{domain.FlavorVanilla, domain.FlavorFabric, domain.FlavorForge, domain.FlavorNeoForge} {
		if _, ok := nativeMessageFor(flavor, line); ok {
			t.Fatalf("%s accepted Paper layout", flavor)
		}
	}
	for _, spoof := range []string{"[13:26:00 INFO]: [SomePlugin] Automatic saving is now enabled", "[13:26:00 INFO]: <Alex> Automatic saving is now enabled", "[13:26:00 WARN]: Automatic saving is now enabled"} {
		if got, ok := nativeMessageFor(domain.FlavorPaper, spoof); ok && ackSaveOn("1.21.10")(got) {
			t.Fatalf("spoof accepted: %q", spoof)
		}
	}
	if got, ok := nativeMessageFor(domain.FlavorPaper, "[13:26:00] [Server thread/INFO]: Saved the game"); !ok || got != "Saved the game" {
		t.Fatalf("paper vanilla layout: %q %v", got, ok)
	}
}

package console

import "testing"

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

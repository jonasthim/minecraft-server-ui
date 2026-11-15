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

// 26.3 routes command feedback to the console as system chat.
func TestNativeMessageStrips26_3SystemChatPrefix(t *testing.T) {
	got, ok := nativeMessage("[12:40:17] [Server thread/INFO]: System chat: Automatic saving is now enabled")
	if !ok || got != "Automatic saving is now enabled" || !ackSaveOn("26.3")(got) {
		t.Fatalf("got %q %v", got, ok)
	}
	if got, ok = nativeMessage("[12:40:17] [Server thread/INFO]: <Alex> System chat: Automatic saving is now enabled"); ok && ackSaveOn("26.3")(got) {
		t.Fatal("player chat spoofed a save-on acknowledgement")
	}
}

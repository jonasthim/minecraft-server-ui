package game

import "testing"

func TestNativeGrammarAndChatCannotSpoof(t *testing.T) {
	for _, tc := range []struct{ body, kind, name string }{
		{"Alex drowned", "death", "Alex"},
		{"Alex was slain by Zombie", "death", "Alex"},
		{"Alex has made the advancement [Stone Age]", "advancement", "Alex"},
		{"Alex has completed the challenge [Monster Hunted]", "advancement", "Alex"},
		{"Alex has reached the goal [Adventure]", "advancement", "Alex"},
		{"Alex discovered floor was lava", "death", "Alex"},
		{"Alex discovered the floor was lava", "death", "Alex"},
	} {
		e, ok := parse("[12:34:56] [Server thread/INFO]: " + tc.body)
		if !ok || e.Kind != tc.kind || e.Player.Name != tc.name || e.Player.UUID != "" || e.Player.Authority != "name_only" {
			t.Fatalf("%s: %#v %v", tc.body, e, ok)
		}
	}
	for _, line := range []string{
		"[12:34:56] [Server thread/INFO]: <Alex> Steve drowned",
		"[12:34:56] [Server thread/INFO]: [Not Secure] <Alex> Steve drowned",
		"[12:34:56] [Server thread/INFO]: * Alex Steve drowned",
		"[12:34:56] [Server thread/INFO]: [Server] Alex drowned",
		"[12:34:56] [Server thread/INFO]: [Plugin] Alex drowned",
		"[12:34:56] [Async Chat Thread/INFO]: Alex drowned",
		"[12:34:56] [Server thread/WARN]: Alex drowned",
		"[25:99:00] [Server thread/INFO]: Alex drowned",
		"Alex drowned", "[12:34:56] [Server thread/INFO]: Alex died mysteriously",
		"[12:34:56] [Server thread/INFO]: Alex has made the advancement []",
		"[12:34:56] [Server thread/INFO]: Alex has made the advancement [Stone Age] trailing",
		"[12:34:56] [Server thread/INFO]: Alex has made the advancement [redacted]",
		"[12:34:56] [Server thread/INFO]: Alex drowned\n",
	} {
		if e, ok := parse(line); ok {
			t.Fatalf("untrusted line accepted %q: %#v", line, e)
		}
	}
}

func Test26_3SystemChatGrammar(t *testing.T) {
	for _, body := range []string{"Alex drowned", "Alex has made the advancement [Stone Age]"} {
		if e, ok := parse("[12:34:56] [Server thread/INFO]: System chat: " + body); !ok || e.Player.Name != "Alex" {
			t.Fatal(body, e, ok)
		}
	}
	for _, body := range []string{"<Alex> Steve drowned", "[Server] Alex drowned", "* Alex Steve drowned", "<Alex> System chat: Steve drowned"} {
		if e, ok := parse("[12:34:56] [Server thread/INFO]: System chat: " + body); ok {
			t.Fatal(body, e)
		}
	}
}

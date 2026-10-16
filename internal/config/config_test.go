package config

import "testing"

func TestFromEnvironment(t *testing.T) {
	t.Setenv("MINECRAFT_UI_ADDR", "127.0.0.1:9090")
	t.Setenv("MINECRAFT_UI_DATA_DIR", t.TempDir())
	c, err := FromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if c.Address != "127.0.0.1:9090" {
		t.Fatal(c.Address)
	}
	t.Setenv("MINECRAFT_UI_ADDR", "invalid")
	if _, err := FromEnvironment(); err == nil {
		t.Fatal("invalid listen address accepted")
	}
}

func TestBaseURLAndCookiePolicy(t *testing.T) {
	t.Setenv("MINECRAFT_UI_ADDR", "127.0.0.1:8080")
	t.Setenv("MINECRAFT_UI_BASE_URL", "https://minecraft.example.org")
	t.Setenv("MINECRAFT_UI_INSECURE_COOKIES", "")
	c, err := FromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if c.InsecureCookies {
		t.Fatal("HTTPS cookies must be secure")
	}
	t.Setenv("MINECRAFT_UI_BASE_URL", "http://127.0.0.1:8080")
	c, err = FromEnvironment()
	if err != nil || !c.InsecureCookies {
		t.Fatalf("loopback config=%+v err=%v", c, err)
	}
	t.Setenv("MINECRAFT_UI_INSECURE_COOKIES", "nope")
	if _, err := FromEnvironment(); err == nil {
		t.Fatal("invalid boolean accepted")
	}
}

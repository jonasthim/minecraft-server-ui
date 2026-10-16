package neoforge

import (
	"context"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"testing"
)

func TestRejectFloorBeforeNetwork(t *testing.T) {
	p := New(t.TempDir(), nil)
	if _, err := p.Resolve(context.Background(), domain.VersionSelection{Flavor: domain.FlavorNeoForge, MinecraftVersion: "1.20.1", LoaderVersion: "1.0.0"}); err == nil {
		t.Fatal("floor accepted")
	}
}
func TestRejectFloatingLoader(t *testing.T) {
	p := New(t.TempDir(), nil)
	if _, err := p.Resolve(context.Background(), domain.VersionSelection{Flavor: domain.FlavorNeoForge, MinecraftVersion: "1.20.2", LoaderVersion: "latest"}); err == nil {
		t.Fatal("floating loader accepted")
	}
}

package loaderutil

import (
	"reflect"
	"testing"
)

// Minecraft 1.21.9's bundler ships org.ow2.asm:asm 9.6 while Fabric Loader
// 0.20.1 ships asm 9.10.1; Fabric refuses duplicate ASM on the classpath.
func TestWithoutLoaderProvidedLibrariesDropsBundledDuplicates(t *testing.T) {
	game := []string{
		"minecraft/libraries/com/mojang/brigadier/1.3.10/brigadier-1.3.10.jar",
		"minecraft/libraries/org/ow2/asm/asm/9.6/asm-9.6.jar",
		"minecraft/versions/1.21.9/server-1.21.9.jar",
	}
	provided := []string{"net.fabricmc:fabric-loader:0.20.1", "net.fabricmc:intermediary:1.21.9", "org.ow2.asm:asm:9.10.1", "org.ow2.asm:asm-tree:9.10.1"}
	got, err := withoutLoaderProvidedLibraries(game, provided)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"minecraft/libraries/com/mojang/brigadier/1.3.10/brigadier-1.3.10.jar", "minecraft/versions/1.21.9/server-1.21.9.jar"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	// Legacy non-bundled servers keep their single jar.
	if got, err = withoutLoaderProvidedLibraries([]string{"server.jar"}, provided); err != nil || !reflect.DeepEqual(got, []string{"server.jar"}) {
		t.Fatalf("legacy classpath changed: %v %v", got, err)
	}
	// A bundled library path that cannot be read as a Maven layout is refused
	// rather than silently kept or dropped.
	if _, err = withoutLoaderProvidedLibraries([]string{"minecraft/libraries/asm.jar"}, provided); err == nil {
		t.Fatal("accepted malformed bundled library path")
	}
}

package domain

import "testing"

func validInstance() Instance {
	return Instance{ID: "survival", Name: "Survival", Flavor: FlavorPaper, MinecraftVersion: "1.21.4", JavaMajor: 21, MemoryMiB: 2048, GamePort: 25565}
}
func TestInstanceValidate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Instance)
	}{
		{"path traversal", func(i *Instance) { i.ID = "../escape" }},
		{"empty name", func(i *Instance) { i.Name = " " }},
		{"unsupported flavor", func(i *Instance) { i.Flavor = "bedrock" }},
		{"old version", func(i *Instance) { i.MinecraftVersion = "1.11.2" }},
		{"snapshot", func(i *Instance) { i.MinecraftVersion = "24w14a" }},
		{"port", func(i *Instance) { i.GamePort = 0 }},
		{"memory", func(i *Instance) { i.MemoryMiB = 0 }},
		{"java", func(i *Instance) { i.JavaMajor = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := validInstance()
			tc.change(&i)
			if i.Validate() == nil {
				t.Fatal("accepted invalid instance")
			}
		})
	}
	for _, version := range []string{"1.12.2", "1.20", "1.21.4", "26.1", "26.3"} {
		i := validInstance()
		i.MinecraftVersion = version
		if err := i.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}
func TestPlainFilename(t *testing.T) {
	for _, s := range []string{"", "..", "../server.jar", "a/b", `a\b`, "C:server.jar"} {
		if ValidateFilename(s) == nil {
			t.Errorf("accepted %q", s)
		}
	}
	if err := ValidateFilename("server.jar"); err != nil {
		t.Fatal(err)
	}
}

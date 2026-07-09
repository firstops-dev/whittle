package router

import "testing"

// Every built-in example policy MUST load cleanly and have a description — a
// shipped preset that fails validation would break `whittle policy init`.
func TestPresets_AllValid(t *testing.T) {
	names := PresetNames()
	if len(names) == 0 {
		t.Fatal("no example policies embedded")
	}
	for _, n := range names {
		b, err := Preset(n)
		if err != nil {
			t.Fatalf("Preset(%q): %v", n, err)
		}
		if _, _, err := Load(b); err != nil {
			t.Errorf("built-in policy %q does not load: %v", n, err)
		}
		if PresetDescription(n) == "" {
			t.Errorf("built-in policy %q has no description", n)
		}
	}
}

// Preset on an unknown name is a clear error, not a panic.
func TestPreset_UnknownName(t *testing.T) {
	if _, err := Preset("does-not-exist"); err == nil {
		t.Fatal("expected an error for an unknown preset name")
	}
}

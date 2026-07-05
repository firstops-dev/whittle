package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHookSelfHeal covers the daemon's hook-repair invariants without touching
// the real ~/.claude: install is non-destructive, detection is accurate, a
// vanished hook is repaired, and repair never duplicates or clobbers.
func TestHookSelfHeal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	settings := filepath.Join(home, ".claude", "settings.json")

	// seed with an UNRELATED user hook + an unrelated top-level key
	seed := map[string]any{
		"model": "sonnet",
		"hooks": map[string]any{"PostToolUse": []any{
			map[string]any{"matcher": "Bash", "hooks": []any{map[string]any{"type": "command", "command": "/usr/local/bin/other-tool"}}},
		}},
	}
	_ = os.MkdirAll(filepath.Dir(settings), 0o755)
	b, _ := json.MarshalIndent(seed, "", "  ")
	_ = os.WriteFile(settings, b, 0o644)

	if hookInstalled() {
		t.Fatal("hook should not be reported installed before install")
	}
	// install
	if err := installClaudeHook(); err != nil {
		t.Fatal(err)
	}
	if !hookInstalled() {
		t.Fatal("hook should be installed")
	}
	assertOtherHookPreserved(t, settings)
	assertWhittleCount(t, settings, 1)

	// simulate the observed failure: the whittle entry vanishes externally
	if err := removeClaudeHook(); err != nil {
		t.Fatal(err)
	}
	if hookInstalled() {
		t.Fatal("hook should read as missing after external removal")
	}
	assertOtherHookPreserved(t, settings) // removal must keep the unrelated hook

	// self-heal (what the daemon's superviseHook does)
	if err := installClaudeHook(); err != nil {
		t.Fatal(err)
	}
	if !hookInstalled() {
		t.Fatal("self-heal should have reinstalled the hook")
	}
	// re-running install must NOT create a second whittle entry
	if err := installClaudeHook(); err != nil {
		t.Fatal(err)
	}
	assertWhittleCount(t, settings, 1)
	assertOtherHookPreserved(t, settings)
	// top-level key untouched
	if readSettings(t, settings)["model"] != "sonnet" {
		t.Fatal("unrelated top-level settings key was lost")
	}
}

func readSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("settings.json is not valid JSON: %v", err)
	}
	return m
}

func assertOtherHookPreserved(t *testing.T, path string) {
	t.Helper()
	if !strings.Contains(string(mustRead(t, path)), "other-tool") {
		t.Fatal("unrelated user hook was clobbered")
	}
}

func assertWhittleCount(t *testing.T, path string, want int) {
	t.Helper()
	got := strings.Count(string(mustRead(t, path)), "/hook")
	if got != want {
		t.Fatalf("whittle hook entries = %d, want %d", got, want)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

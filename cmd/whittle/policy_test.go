package main

import (
	"strings"
	"testing"

	"github.com/firstops-dev/whittle/router"
)

func TestModelFamily(t *testing.T) {
	cases := map[string]string{
		"claude-haiku-4-5-20251001": "haiku",
		"claude-sonnet-4-5":         "sonnet",
		"claude-opus-4-7":           "opus",
		"gpt-5":                     "",
	}
	for in, want := range cases {
		if got := modelFamily(in); got != want {
			t.Errorf("modelFamily(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBetterModel(t *testing.T) {
	// A "model": value (proven-valid, what Claude Code sends) beats anything else.
	if !betterModel("claude-opus-4-7", true, "claude-opus-4-8", false) {
		t.Error("a model-value id should win over a higher-versioned plain id")
	}
	// Among plain ids, a dated id beats a bare one.
	if !betterModel("claude-sonnet-4-5-20250929", false, "claude-sonnet-4-5", false) {
		t.Error("a dated id should win over a bare id")
	}
	// Among equally-ranked, the higher version wins.
	if !betterModel("claude-opus-4-8", false, "claude-opus-4-7", false) {
		t.Error("higher version should win among equals")
	}
}

// fillModelsWith substitutes by family and leaves the result a valid policy.
func TestFillModelsWith(t *testing.T) {
	preset, err := router.Preset("coding")
	if err != nil {
		t.Fatal(err)
	}
	detected := map[string]string{
		"haiku":  "claude-haiku-4-5-20251001",
		"sonnet": "claude-sonnet-4-5-20250929",
		"opus":   "claude-opus-4-7",
	}
	out, notes := fillModelsWith(preset, detected)
	if len(notes) != 3 {
		t.Fatalf("expected 3 substitutions, got %d: %v", len(notes), notes)
	}
	for _, id := range detected {
		if !strings.Contains(string(out), id) {
			t.Errorf("filled policy missing detected id %q", id)
		}
	}
	if _, _, err := router.Load(out); err != nil {
		t.Fatalf("filled policy no longer loads: %v", err)
	}
	// No detection → unchanged, no notes.
	same, n := fillModelsWith(preset, nil)
	if len(n) != 0 || string(same) != string(preset) {
		t.Errorf("empty detection should leave the preset unchanged")
	}
}

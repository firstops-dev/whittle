package router

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// presetFS carries the built-in example policies inside the binary so
// `whittle policy init` works from a bare `go install` with no extra files.
//
//go:embed policies/*.json
var presetFS embed.FS

// presetDescriptions is the one-line summary shown by `whittle policy list`,
// keyed by preset name. Every embedded preset must have an entry (enforced by
// TestPresets_AllValid).
var presetDescriptions = map[string]string{
	"default":   "Mixed-use (coding + writing + analysis): hard reasoning or confident quantitative → opus, clearly-trivial non-high-stakes → haiku, everything else KEEPS the model you asked for (rewrites only by rule). Empirically calibrated signal composition.",
	"coding":    "Coding workflow: hard work → opus, quick edits → haiku, else sonnet. Uses ML signals when smart mode is on; falls back to keywords/context otherwise.",
	"heuristic": "Same tiering, heuristics only (no ML signals) — works without the model sidecar.",
}

// PresetNames returns the built-in example-policy names, sorted.
func PresetNames() []string {
	entries, _ := fs.ReadDir(presetFS, "policies")
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Strings(names)
	return names
}

// PresetDescription returns the one-line summary for a preset ("" if unknown).
func PresetDescription(name string) string { return presetDescriptions[name] }

// Preset returns the raw JSON of a built-in policy by name.
func Preset(name string) ([]byte, error) {
	b, err := presetFS.ReadFile("policies/" + name + ".json")
	if err != nil {
		return nil, fmt.Errorf("no built-in policy %q (see `whittle policy list`)", name)
	}
	return b, nil
}

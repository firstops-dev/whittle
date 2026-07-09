package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/firstops-dev/whittle/router"
)

var (
	anyModelID   = regexp.MustCompile(`claude-(?:haiku|sonnet|opus)-\d[0-9a-z-]*`)
	modelValueID = regexp.MustCompile(`"model"\s*:\s*"(claude-(?:haiku|sonnet|opus)-\d[0-9a-z-]*)"`)
	datedID      = regexp.MustCompile(`-\d{8}$`)
)

func modelFamily(id string) string {
	switch {
	case strings.Contains(id, "haiku"):
		return "haiku"
	case strings.Contains(id, "sonnet"):
		return "sonnet"
	case strings.Contains(id, "opus"):
		return "opus"
	}
	return ""
}

// detectClaudeModels scans Claude Code's config for the model id per family
// (haiku/sonnet/opus) the account actually uses, so `policy init` fills the tiers
// with ids Anthropic will accept — not the guessable placeholders that 4xx on
// every rewrite. Ranking: an id used as a "model": value (proven-valid, what
// Claude Code sends) beats a dated id beats a bare/lower one.
func detectClaudeModels() map[string]string {
	home, _ := os.UserHomeDir()
	files := []string{filepath.Join(home, ".claude.json"), filepath.Join(home, ".claude", "settings.json")}

	type cand struct {
		id      string
		isValue bool
	}
	best := map[string]cand{}
	consider := func(id string, isValue bool) {
		fam := modelFamily(id)
		if fam == "" {
			return
		}
		cur, ok := best[fam]
		if !ok || betterModel(id, isValue, cur.id, cur.isValue) {
			best[fam] = cand{id, isValue}
		}
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		s := string(b)
		for _, m := range modelValueID.FindAllStringSubmatch(s, -1) {
			consider(m[1], true)
		}
		for _, id := range anyModelID.FindAllString(s, -1) {
			consider(id, false)
		}
	}
	out := map[string]string{}
	for fam, c := range best {
		out[fam] = c.id
	}
	return out
}

func betterModel(aID string, aVal bool, bID string, bVal bool) bool {
	// A full dated id first (bare aliases often 404). Then the highest version /
	// newest date — the config's "model" field proved STALE (it named opus-4-7
	// while Claude Code actually sent opus-4-8), so version outranks it; a "model":
	// occurrence is only a final tiebreak between otherwise-identical ids.
	if ad, bd := datedID.MatchString(aID), datedID.MatchString(bID); ad != bd {
		return ad
	}
	if aID != bID {
		return aID > bID
	}
	return aVal && !bVal
}

// fillModels substitutes the model ids detected from Claude Code's config.
func fillModels(preset []byte) ([]byte, []string) {
	return fillModelsWith(preset, detectClaudeModels())
}

// fillModelsWith substitutes the given family→id map into a preset's tiers, by
// family. Returns the (possibly unchanged) JSON and human-readable notes.
func fillModelsWith(preset []byte, detected map[string]string) ([]byte, []string) {
	var doc struct {
		Tiers []struct{ Name, Model string } `json:"tiers"`
	}
	if json.Unmarshal(preset, &doc) != nil {
		return preset, nil
	}
	out := preset
	var notes []string
	for _, t := range doc.Tiers {
		if id, ok := detected[modelFamily(t.Model)]; ok && id != t.Model {
			out = bytes.ReplaceAll(out, []byte(`"`+t.Model+`"`), []byte(`"`+id+`"`))
			notes = append(notes, fmt.Sprintf("%s: %s → %s", t.Name, t.Model, id))
		}
	}
	return out, notes
}

// cmdPolicy manages router policies: list/show built-in presets, write one to
// ~/.whittle/router.json, or validate a policy file with the real loader.
func cmdPolicy(args []string) {
	if len(args) == 0 {
		policyUsage()
		os.Exit(2)
	}
	switch args[0] {
	case "list":
		for _, n := range router.PresetNames() {
			fmt.Printf("  %-10s %s\n", n, router.PresetDescription(n))
		}
	case "show":
		policyShow(args[1:])
	case "init":
		policyInit(args[1:])
	case "validate":
		policyValidate(args[1:])
	default:
		policyUsage()
		os.Exit(2)
	}
}

func policyShow(args []string) {
	name := "default"
	if len(args) > 0 {
		name = args[0]
	}
	b, err := router.Preset(name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "whittle:", err)
		os.Exit(1)
	}
	os.Stdout.Write(b)
}

func policyInit(args []string) {
	fs := flag.NewFlagSet("policy init", flag.ExitOnError)
	force := fs.Bool("force", false, "overwrite an existing policy file")
	out := fs.String("o", filepath.Join(whittleHome(), "router.json"), "output path")
	_ = fs.Parse(args)
	name := "default"
	if fs.NArg() > 0 {
		name = fs.Arg(0)
	}

	b, err := router.Preset(name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "whittle:", err)
		os.Exit(1)
	}
	// Fill the tiers with the real model ids the account uses (from Claude Code's
	// config) so the policy works on first request instead of 4xx-ing every rewrite.
	b, notes := fillModels(b)
	// Defensive: the result must still load (TestPresets_AllValid guards the base).
	if _, _, verr := router.Load(b); verr != nil {
		fmt.Fprintln(os.Stderr, "whittle: policy failed to validate (bug):", verr)
		os.Exit(1)
	}
	if _, err := os.Stat(*out); err == nil && !*force {
		fmt.Printf("whittle: %s already exists — pass -force to overwrite, or -o <path> to write elsewhere\n", *out)
		os.Exit(1)
	}
	must(os.MkdirAll(filepath.Dir(*out), 0o755))
	must(os.WriteFile(*out, b, 0o644))
	fmt.Printf("  ✓ wrote %q policy → %s\n", name, *out)
	if len(notes) > 0 {
		fmt.Println("  set tier models from your Claude Code config (verify these are right):")
		for _, n := range notes {
			fmt.Println("      " + n)
		}
	} else {
		fmt.Println("  ⚠ could not detect your model IDs — the tiers use PLACEHOLDER IDs that")
		fmt.Println("    Anthropic will reject. Edit the tier \"model\" values to real IDs (the full")
		fmt.Println("    dated form, e.g. claude-sonnet-4-5-20250929), then `whittle policy validate`.")
	}
	fmt.Println("  Then start the router:")
	fmt.Printf("    whittle route -install    # or `whittle route` in the foreground\n")
	fmt.Printf("    export ANTHROPIC_BASE_URL=http://%s\n", router.DefaultAddr)
}

func policyValidate(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: whittle policy validate <file>")
		os.Exit(2)
	}
	b, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "whittle:", err)
		os.Exit(1)
	}
	p, warns, err := router.Load(b)
	if err != nil {
		fmt.Println("INVALID:", err)
		os.Exit(1)
	}
	fmt.Printf("VALID ✓  tiers=%d routes=%d default=%s\n", len(p.Tiers), len(p.Routes), p.Default)
	for _, w := range warns {
		fmt.Println("  warn:", w)
	}
}

func policyUsage() {
	fmt.Fprintln(os.Stderr, `whittle policy — router policy management

  whittle policy list                       list the built-in policies
  whittle policy show [name]                print a built-in policy (default: default)
  whittle policy init [name] [-force] [-o path]
                                            write a built-in policy to ~/.whittle/router.json
                                            (see router/policies/default.md for customization)
  whittle policy validate <file>            validate a policy file (loader errors + warnings)`)
}

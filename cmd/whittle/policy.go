package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/firstops-dev/whittle/router"
)

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
	name := "coding"
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
	name := "coding"
	if fs.NArg() > 0 {
		name = fs.Arg(0)
	}

	b, err := router.Preset(name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "whittle:", err)
		os.Exit(1)
	}
	// Defensive: a shipped preset must load (TestPresets_AllValid guards this too).
	if _, _, verr := router.Load(b); verr != nil {
		fmt.Fprintln(os.Stderr, "whittle: built-in policy failed to validate (bug):", verr)
		os.Exit(1)
	}
	if _, err := os.Stat(*out); err == nil && !*force {
		fmt.Printf("whittle: %s already exists — pass -force to overwrite, or -o <path> to write elsewhere\n", *out)
		os.Exit(1)
	}
	must(os.MkdirAll(filepath.Dir(*out), 0o755))
	must(os.WriteFile(*out, b, 0o644))
	fmt.Printf("  ✓ wrote %q policy → %s\n", name, *out)
	fmt.Println("  Next: adjust the tier model IDs to match your account, then start the router —")
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

  whittle policy list                       list the built-in example policies
  whittle policy show [name]                print a built-in policy (default: coding)
  whittle policy init [name] [-force] [-o path]
                                            write a built-in policy to ~/.whittle/router.json
  whittle policy validate <file>            validate a policy file (loader errors + warnings)`)
}

package main

// whittle setup / start / stop / status / cleanup — the one-command experience.
//
//	whittle setup    materialize the model sidecar to ~/.whittle, build its venv,
//	                 install the Claude Code PostToolUse hook, register a launchd
//	                 agent (macOS) and start everything
//	whittle stop     stop the background services (keep hook + install)
//	whittle cleanup  stop + remove the hook + unregister launchd (keeps ~/.whittle)
//	whittle status   health of router, sidecar, hook
//	whittle daemon   (internal) foreground supervisor launchd runs: Go server +
//	                 Python sidecar child, restarted on exit
//
// Fail-open philosophy carries into setup: if Python is unavailable the ML
// sidecar is skipped with a notice and whittle runs deterministic-only.

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/firstops-dev/whittle"
	"github.com/firstops-dev/whittle/server"
)

const (
	routerAddr = "127.0.0.1:45871"
	modelAddr  = "127.0.0.1:45872"
	agentLabel = "dev.firstops.whittle"
)

func whittleHome() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".whittle")
}

func cmdSetup(_ []string) {
	fmt.Println("whittle setup")
	dir := whittleHome()
	must(os.MkdirAll(filepath.Join(dir, "logs"), 0o755))

	// 1. materialize the embedded sidecar + venv (optional — fail open)
	if err := setupSidecar(dir); err != nil {
		fmt.Printf("  ! ML sidecar skipped (%v)\n    deterministic compression still fully works;\n    re-run `whittle setup` after installing python3 to enable prose.\n", err)
	}

	// 2. Claude Code PostToolUse hook
	if err := installClaudeHook(); err != nil {
		fmt.Println("  ! Claude Code hook not installed:", err)
	} else {
		fmt.Println("  ✓ Claude Code PostToolUse hook installed (~/.claude/settings.json)")
	}

	// 2b. MCP retrieval tool (whittle_get) — best-effort via the claude CLI
	if self, err := os.Executable(); err == nil {
		if err := exec.Command("claude", "mcp", "add", "--scope", "user", "whittle", "--", self, "mcp").Run(); err == nil {
			fmt.Println("  ✓ whittle_get MCP tool registered (claude mcp)")
		} else {
			fmt.Println("  ! whittle_get MCP not registered (claude CLI unavailable?) — retrieval hints will still degrade gracefully")
		}
	}

	// 3. background services via launchd (macOS) — always-on
	if runtime.GOOS == "darwin" {
		if err := installLaunchAgent(dir); err != nil {
			fmt.Println("  ! launchd registration failed:", err)
		} else {
			fmt.Println("  ✓ launchd agent registered (" + agentLabel + ") — starts at login, kept alive")
		}
	} else {
		fmt.Println("  ! launchd is macOS-only; run `whittle daemon` under your supervisor (systemd unit sample in README)")
	}

	waitHealthy("http://"+routerAddr+"/health", 30*time.Second, "router")
	fmt.Println("whittle is running. Try: whittle status")
}

func setupSidecar(dir string) error {
	py, err := exec.LookPath("python3")
	if err != nil {
		return fmt.Errorf("python3 not found in PATH")
	}
	modelDir := filepath.Join(dir, "model")
	must(os.MkdirAll(modelDir, 0o755))
	err = fs.WalkDir(whittle.SidecarFS, "model", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(whittle.SidecarFS, path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(modelDir, filepath.Base(path)), b, 0o644)
	})
	if err != nil {
		return err
	}
	venv := filepath.Join(dir, "venv")
	if _, err := os.Stat(filepath.Join(venv, "bin", "python")); err != nil {
		fmt.Println("  … creating venv + installing model dependencies (a few minutes; torch is large)")
		if out, err := exec.Command(py, "-m", "venv", venv).CombinedOutput(); err != nil {
			return fmt.Errorf("venv: %v: %s", err, out)
		}
		pip := filepath.Join(venv, "bin", "pip")
		if out, err := exec.Command(pip, "install", "-q", "-r", filepath.Join(modelDir, "requirements.txt")).CombinedOutput(); err != nil {
			return fmt.Errorf("pip install: %v: %s", err, out)
		}
	}
	fmt.Println("  ✓ model sidecar installed (~/.whittle/model, venv ready; model weights download on first start)")
	return nil
}

// cmdDaemon is what launchd runs: the Go router in-process plus a supervised
// Python sidecar child. GPU: Apple silicon uses MPS automatically (the sidecar
// falls back CUDA > MPS > CPU on its own).
func cmdDaemon(_ []string) {
	dir := whittleHome()
	venvUvicorn := filepath.Join(dir, "venv", "bin", "uvicorn")
	if _, err := os.Stat(venvUvicorn); err == nil {
		os.Setenv("WHITTLE_MODEL_URL", "http://"+modelAddr)
		go superviseSidecar(dir, venvUvicorn)
	}
	if err := server.ListenAndServe(":" + strings.Split(routerAddr, ":")[1]); err != nil {
		fmt.Fprintln(os.Stderr, "whittle daemon:", err)
		os.Exit(1)
	}
}

func superviseSidecar(dir, uvicorn string) {
	logf, _ := os.OpenFile(filepath.Join(dir, "logs", "model.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	for {
		cmd := exec.Command(uvicorn, "app:app", "--host", "127.0.0.1", "--port", strings.Split(modelAddr, ":")[1])
		cmd.Dir = filepath.Join(dir, "model")
		cmd.Stdout, cmd.Stderr = logf, logf
		cmd.Env = append(os.Environ(), "MAX_CHARS=300000")
		if err := cmd.Run(); err != nil {
			fmt.Fprintln(logf, "sidecar exited:", err)
		}
		time.Sleep(3 * time.Second) // restart backoff; launchd keeps US alive
	}
}

func launchPlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", agentLabel+".plist")
}

func installLaunchAgent(dir string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key><array><string>%s</string><string>daemon</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>%s/logs/daemon.log</string>
  <key>StandardErrorPath</key><string>%s/logs/daemon.log</string>
</dict></plist>
`, agentLabel, self, dir, dir)
	if err := os.WriteFile(launchPlistPath(), []byte(plist), 0o644); err != nil {
		return err
	}
	_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), agentLabel)).Run() // idempotent re-setup
	return exec.Command("launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), launchPlistPath()).Run()
}

func cmdStop(_ []string) {
	if runtime.GOOS == "darwin" {
		if err := exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), agentLabel)).Run(); err != nil {
			fmt.Println("whittle: nothing to stop (launchd agent not loaded)")
			return
		}
		fmt.Println("whittle: stopped (hook still installed — outputs pass through uncompressed; `whittle cleanup` removes it)")
		return
	}
	fmt.Println("whittle: stop your supervisor's whittle unit (launchd is macOS-only)")
}

func cmdCleanup(_ []string) {
	cmdStop(nil)
	if err := removeClaudeHook(); err != nil {
		fmt.Println("whittle: hook removal:", err)
	} else {
		fmt.Println("whittle: Claude Code hook removed")
	}
	_ = exec.Command("claude", "mcp", "remove", "--scope", "user", "whittle").Run()
	_ = os.Remove(launchPlistPath())
	fmt.Println("whittle: launchd agent unregistered. (~/.whittle kept; delete it to remove the venv/model)")
}

func cmdStatus(_ []string) {
	check := func(name, url string) {
		c := http.Client{Timeout: 2 * time.Second}
		if r, err := c.Get(url); err == nil && r.StatusCode == 200 {
			fmt.Printf("  ✓ %s healthy (%s)\n", name, url)
		} else {
			fmt.Printf("  ✗ %s not responding (%s)\n", name, url)
		}
	}
	check("router ", "http://"+routerAddr+"/health")
	check("sidecar", "http://"+modelAddr+"/health")
	if hookInstalled() {
		fmt.Println("  ✓ Claude Code hook installed")
	} else {
		fmt.Println("  ✗ Claude Code hook not installed (run `whittle setup`)")
	}
}

func waitHealthy(url string, d time.Duration, name string) {
	c := http.Client{Timeout: time.Second}
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if r, err := c.Get(url); err == nil && r.StatusCode == 200 {
			fmt.Printf("  ✓ %s healthy\n", name)
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	fmt.Printf("  ! %s not healthy yet — check ~/.whittle/logs\n", name)
}

// --- Claude Code hook management (user-level ~/.claude/settings.json) ---

func claudeSettingsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

func hookCommand() string {
	self, _ := os.Executable()
	return self + " hook"
}

func loadSettings() (map[string]any, error) {
	s := map[string]any{}
	b, err := os.ReadFile(claudeSettingsPath())
	if err == nil {
		if err := json.Unmarshal(b, &s); err != nil {
			return nil, fmt.Errorf("~/.claude/settings.json is not valid JSON: %w", err)
		}
	}
	return s, nil
}

func saveSettings(s map[string]any) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	must(os.MkdirAll(filepath.Dir(claudeSettingsPath()), 0o755))
	return os.WriteFile(claudeSettingsPath(), b, 0o644)
}

// installClaudeHook merges (never clobbers) a PostToolUse entry into the user's
// existing hooks. Marker: the command contains "whittle hook".
func installClaudeHook() error {
	s, err := loadSettings()
	if err != nil {
		return err
	}
	hooks, _ := s["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	entries, _ := hooks["PostToolUse"].([]any)
	kept := entries[:0] // migrate: drop any older whittle entry (command-type era)
	for _, e := range entries {
		if !strings.Contains(fmt.Sprint(e), "/hook") && !strings.Contains(fmt.Sprint(e), "whittle") {
			kept = append(kept, e)
		}
	}
	entries = append(kept, map[string]any{
		"matcher": "*",
		"hooks": []any{map[string]any{
			"type": "http", "url": "http://127.0.0.1:45871/hook", "timeout": 10,
			"statusMessage": "whittle: compressing tool output…",
		}},
	})
	hooks["PostToolUse"] = entries
	s["hooks"] = hooks
	return saveSettings(s)
}

func removeClaudeHook() error {
	s, err := loadSettings()
	if err != nil {
		return err
	}
	hooks, _ := s["hooks"].(map[string]any)
	entries, _ := hooks["PostToolUse"].([]any)
	if entries == nil {
		return nil
	}
	kept := entries[:0]
	for _, e := range entries {
		if !strings.Contains(fmt.Sprint(e), "/hook") && !strings.Contains(fmt.Sprint(e), "whittle") {
			kept = append(kept, e)
		}
	}
	hooks["PostToolUse"] = kept
	return saveSettings(s)
}

func hookInstalled() bool {
	s, err := loadSettings()
	if err != nil {
		return false
	}
	hooks, _ := s["hooks"].(map[string]any)
	return strings.Contains(fmt.Sprint(hooks["PostToolUse"]), "/hook")
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "whittle:", err)
		os.Exit(1)
	}
}

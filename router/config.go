package router

import (
	"fmt"
	"os"
)

// LoadPolicyFile reads and validates a policy from a file path. It returns the
// policy, non-fatal warnings, and an error (a read or validation failure). The
// daemon uses a nil policy — from a missing/invalid file — to run in transparent
// passthrough rather than refuse to start (bricking Claude Code, R3).
func LoadPolicyFile(path string) (*Policy, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read policy %s: %w", path, err)
	}
	return Load(data)
}

// ReloadFile hot-swaps the active policy from a file. On any error it KEEPS the
// currently-running policy (never break a live proxy on a bad edit) and returns
// the error for the caller to log. On success the new policy takes effect
// atomically for the next request.
func (p *Proxy) ReloadFile(path string) ([]string, error) {
	pol, warns, err := LoadPolicyFile(path)
	if err != nil {
		return nil, err // keep the old policy
	}
	p.SetPolicy(pol)
	return warns, nil
}

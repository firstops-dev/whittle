package whittle

import (
	"io/fs"
	"testing"
)

// Every Python module app.py imports must be embedded — a missing one breaks the
// whole sidecar on `whittle setup` (app.py fails at `import route`), taking
// compression down with it. This guards the //go:embed list against drift.
func TestSidecarFS_HasAllModules(t *testing.T) {
	for _, f := range []string{
		"model/app.py",
		"model/route.py",
		"model/gate.py",
		"model/fidelity.py",
		"model/requirements.txt",
	} {
		if _, err := fs.Stat(SidecarFS, f); err != nil {
			t.Errorf("SidecarFS missing %s (setup would install a broken sidecar): %v", f, err)
		}
	}
}

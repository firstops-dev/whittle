package whittle

import "embed"

// SidecarFS carries the Python model sidecar inside the binary so that
// `go install .../cmd/whittle@latest` is a complete installation: `whittle
// setup` materializes these files to ~/.whittle/model and builds a venv there.
//
//go:embed model/app.py model/gate.py model/fidelity.py model/requirements.txt
var SidecarFS embed.FS

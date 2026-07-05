package whittle_test

// Pins the README "as a library" snippet: this Example is compiled and RUN by
// `go test`, so the documented API surface can never silently drift.

import (
	"context"
	"fmt"
	"strings"

	"github.com/firstops-dev/whittle"
)

func ExampleNew() {
	eng := whittle.New(whittle.Options{MinTokens: 0})
	toolOutput := "ERROR boot failed\n" + strings.Repeat("2026 INFO tick handler ok\n", 80)
	res := eng.Compress(context.Background(), toolOutput)
	fmt.Println(res.Action, res.Detected, strings.Contains(res.Output, "ERROR boot failed"))
	// Output: compressed log true
}

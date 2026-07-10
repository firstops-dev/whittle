package main

// whittle watch — a live, unified view of both event streams: the model
// router's per-request verdicts (~/.whittle/logs/router.log, written by the
// background service) and the compression hook's per-carve records
// (~/.whittle/stats.jsonl). One feed, chips per event, the same visual
// vocabulary as `whittle stats`.
//
// The follower is poll-based (no fsnotify dependency), tolerant of files that
// do not exist yet (it waits for them), and rotation-safe (a shrunk or replaced
// file is reopened from the start). Foreground `whittle route` prints to the
// terminal rather than the log file; watch shows the background service.

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func cmdWatch(args []string) {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	backlog := fs.Int("n", 8, "recent events to show before following")
	plain := fs.Bool("plain", false, "no color (also honors NO_COLOR)")
	dir := fs.String("dir", whittleHome(), "whittle home to watch")
	_ = fs.Parse(args)

	pal := newPalette(*plain || os.Getenv("NO_COLOR") != "")
	routerLog := filepath.Join(*dir, "logs", "router.log")
	statsLog := filepath.Join(*dir, "stats.jsonl")

	fmt.Printf("%s🪓 whittle%s %s· watching routes + carves · ^C to stop%s\n", pal.bold, pal.reset, pal.dim, pal.reset)
	for _, p := range []string{routerLog, statsLog} {
		if _, err := os.Stat(p); err != nil {
			fmt.Printf("%s  waiting for %s (created on first event)%s\n", pal.dim, p, pal.reset)
		}
	}

	rf := newFollower(routerLog)
	sf := newFollower(statsLog)
	for _, ev := range backlogEvents(rf, sf, *backlog, pal) {
		fmt.Println(ev)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-sig:
			return
		case <-tick.C:
			for _, l := range rf.readNew() {
				if out := renderRouterLine(l, pal); out != "" {
					fmt.Println(out)
				}
			}
			for _, l := range sf.readNew() {
				if out := renderCarveLine(l, pal); out != "" {
					fmt.Println(out)
				}
			}
		}
	}
}

// ---- palette -----------------------------------------------------------------

type palette struct {
	bold, dim, italic, green, cyan, blue, red, reset string
}

func newPalette(plain bool) palette {
	if plain {
		return palette{}
	}
	return palette{
		bold: "\x1b[1m", dim: "\x1b[2m", italic: "\x1b[3m",
		green: "\x1b[32m", cyan: "\x1b[36m", blue: "\x1b[34m", red: "\x1b[31m",
		reset: "\x1b[0m",
	}
}

// ---- follower ------------------------------------------------------------------

// follower tails one file: readNew returns complete new lines since the last
// call. A missing file yields nothing until it appears; truncation or
// replacement (rotation) reopens from the start. A trailing partial line is
// buffered until its newline arrives.
type follower struct {
	path    string
	offset  int64
	partial string
	primed  bool // first successful open seeks to end unless backlog consumed it
}

func newFollower(path string) *follower { return &follower{path: path} }

func (f *follower) readNew() []string {
	st, err := os.Stat(f.path)
	if err != nil {
		return nil
	}
	if !f.primed {
		// First sighting and no backlog was taken: start at the end, live-only.
		f.offset = st.Size()
		f.primed = true
		return nil
	}
	if st.Size() < f.offset {
		f.offset = 0 // rotated or truncated: start over
		f.partial = ""
	}
	if st.Size() == f.offset {
		return nil
	}
	file, err := os.Open(f.path)
	if err != nil {
		return nil
	}
	defer file.Close()
	if _, err := file.Seek(f.offset, io.SeekStart); err != nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(file, 4<<20))
	if err != nil {
		return nil
	}
	f.offset += int64(len(data))
	text := f.partial + string(data)
	lines := strings.Split(text, "\n")
	f.partial = lines[len(lines)-1] // "" when data ended in \n
	return lines[:len(lines)-1]
}

// prime marks the follower as started at its current end minus consumed backlog.
func (f *follower) prime(offset int64) { f.offset = offset; f.primed = true }

// backlogEvents renders the last n events across both files (by timestamp when
// both are available) and primes the followers at end-of-file.
func backlogEvents(rf, sf *follower, n int, pal palette) []string {
	type ev struct {
		ts   int64
		text string
	}
	var events []ev
	if lines, size := lastLines(rf.path, n); true {
		rf.prime(size)
		for _, l := range lines {
			if out := renderRouterLine(l, pal); out != "" {
				events = append(events, ev{routerLineTS(l), out})
			}
		}
	}
	if lines, size := lastLines(sf.path, n); true {
		sf.prime(size)
		for _, l := range lines {
			if out := renderCarveLine(l, pal); out != "" {
				events = append(events, ev{carveLineTS(l), out})
			}
		}
	}
	// insertion sort by ts (n is small); zero-ts lines keep relative order at front
	for i := 1; i < len(events); i++ {
		for j := i; j > 0 && events[j].ts < events[j-1].ts; j-- {
			events[j], events[j-1] = events[j-1], events[j]
		}
	}
	if len(events) > n {
		events = events[len(events)-n:]
	}
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.text
	}
	return out
}

// lastLines returns up to n final complete lines of path and the file size.
func lastLines(path string, n int) ([]string, int64) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	var size int64
	for sc.Scan() {
		lines = append(lines, sc.Text())
		size += int64(len(sc.Bytes())) + 1
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, size
}

// ---- router line rendering ------------------------------------------------------

type routerEvent struct {
	Tier      string `json:"tier"`
	Requested string `json:"requested"`
	Model     string `json:"model"`
	Reason    string `json:"reason"`
	Signals   string `json:"signals"`
	Status    int    `json:"status"`
	LatencyMS int64  `json:"latency_ms"`
	CtxTokens int    `json:"ctx_tokens"`
	InTokens  int    `json:"in_tokens"`
	OutTokens int    `json:"out_tokens"`
}

// splitRouterLine separates the stdlib log prefix ("2026/07/10 12:54:33 ") from
// the JSON payload. Returns clock ("12:54:33", may be empty) and the rest.
func splitRouterLine(line string) (clock, rest string) {
	rest = line
	if len(line) > 20 && line[4] == '/' && line[7] == '/' && line[10] == ' ' && line[19] == ' ' {
		return line[11:19], line[20:]
	}
	return "", rest
}

func renderRouterLine(line string, pal palette) string {
	if strings.TrimSpace(line) == "" {
		return ""
	}
	clock, rest := splitRouterLine(line)
	if !strings.HasPrefix(rest, "{") {
		// startup / warning lines: pass through dim
		return fmt.Sprintf("%s%s · %s%s", pal.dim, clock, rest, pal.reset)
	}
	var e routerEvent
	if json.Unmarshal([]byte(rest), &e) != nil {
		return fmt.Sprintf("%s%s · %s%s", pal.dim, clock, rest, pal.reset)
	}

	chipColor := pal.dim
	switch {
	case e.Tier == "-" || e.Model == e.Requested: // no-op / passthrough
		chipColor = pal.dim
	case strings.Contains(e.Model, "haiku"):
		chipColor = pal.green
	case strings.Contains(e.Model, "sonnet"):
		chipColor = pal.cyan
	case strings.Contains(e.Model, "opus"):
		chipColor = pal.blue
	}
	move := fmt.Sprintf("%s%s%s", pal.dim, family(e.Requested), pal.reset)
	if family(e.Model) != family(e.Requested) {
		move = fmt.Sprintf("%s%s%s→%s%s%s%s", pal.dim, family(e.Requested), pal.reset, pal.bold, chipColor, family(e.Model), pal.reset)
	} else {
		move += fmt.Sprintf("%s · kept%s", pal.dim, pal.reset)
	}
	status := ""
	if e.Status != 200 {
		status = fmt.Sprintf(" %s%d%s", pal.red, e.Status, pal.reset)
	}
	sig := ""
	if e.Signals != "" {
		sig = fmt.Sprintf("  %s%s%s", pal.dim, e.Signals, pal.reset)
	}
	return fmt.Sprintf("%s%s%s %s▸%s %-18s %s%s  %s%s tok · %dms%s%s",
		pal.dim, clock, pal.reset,
		chipColor, pal.reset,
		strings.TrimPrefix(strings.SplitN(e.Reason, " ", 2)[0], "route:"),
		move, status,
		pal.dim, fmtInt(e.CtxTokens), e.LatencyMS, pal.reset, sig)
}

// routerLineTS extracts a sortable timestamp (unix-ish) from the log prefix.
func routerLineTS(line string) int64 {
	if len(line) > 19 {
		if t, err := time.ParseInLocation("2006/01/02 15:04:05", line[:19], time.Local); err == nil {
			return t.Unix()
		}
	}
	return 0
}

func family(model string) string {
	for _, f := range []string{"haiku", "sonnet", "opus"} {
		if strings.Contains(model, f) {
			return f
		}
	}
	if model == "" || model == "-" {
		return "-"
	}
	return model
}

// ---- carve line rendering ---------------------------------------------------------

type carveEvent struct {
	TS       int64  `json:"ts"`
	Tool     string `json:"tool"`
	Strategy string `json:"strategy"`
	In       int    `json:"in_tokens"`
	Out      int    `json:"out_tokens"`
}

func renderCarveLine(line string, pal palette) string {
	if strings.TrimSpace(line) == "" {
		return ""
	}
	var e carveEvent
	if json.Unmarshal([]byte(line), &e) != nil || e.Strategy == "" {
		return ""
	}
	clock := time.Unix(e.TS, 0).Format("15:04:05")
	if e.Strategy == "retrieve" {
		return fmt.Sprintf("%s%s ↩ retrieve · original served back on demand%s", pal.dim, clock, pal.reset)
	}
	pct := 0
	if e.In > 0 {
		pct = int((1 - float64(e.Out)/float64(e.In)) * 100)
	}
	tool := e.Tool
	if tool == "" {
		tool = "output"
	}
	return fmt.Sprintf("%s%s%s 🪓 %-18s %s%s%s → %s%s%s tok %s%s−%d%%%s  %s%s%s",
		pal.dim, clock, pal.reset,
		tool,
		pal.dim, fmtInt(e.In), pal.reset,
		pal.green, fmtInt(e.Out), pal.reset,
		pal.bold, pal.green, pct, pal.reset,
		pal.dim, e.Strategy, pal.reset)
}

func carveLineTS(line string) int64 {
	var e carveEvent
	if json.Unmarshal([]byte(line), &e) == nil {
		return e.TS
	}
	return 0
}

func fmtInt(n int) string {
	s := fmt.Sprintf("%d", n)
	if n < 0 || len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

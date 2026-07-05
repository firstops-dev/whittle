package main

// whittle stats - the local savings dashboard. Reads ~/.whittle/stats.jsonl
// (local-only, never transmitted). Designed to answer "is whittle worth it?"
// in one glance and to be screenshot-clean: headline number, 14-day carve
// chart, strategy split, and the honesty metric (retrieval rate).

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type statEvent struct {
	TS       int64  `json:"ts"`
	Session  string `json:"session"`
	Tool     string `json:"tool"`
	Strategy string `json:"strategy"`
	In       int    `json:"in_tokens"`
	Out      int    `json:"out_tokens"`
}

func cmdStats(args []string) {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	days := fs.Int("days", 14, "window in days")
	price := fs.Float64("price", 3.0, "your input price per 1M tokens (USD)")
	share := fs.Bool("share", false, "plain-text block for pasting (no color)")
	_ = fs.Parse(args)

	f, err := os.Open(filepath.Join(whittleHome(), "stats.jsonl"))
	if err != nil {
		fmt.Println("whittle stats: no events yet - the hook records savings as your agent works")
		return
	}
	defer f.Close()

	cutoff := time.Now().AddDate(0, 0, -*days)
	daily := make([]int, *days) // tokens saved per day, oldest first
	byStrategy := map[string]int{}
	sessions := map[string]bool{}
	events, saved, retrievals := 0, 0, 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		var e statEvent
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		t := time.Unix(e.TS, 0)
		if t.Before(cutoff) {
			continue
		}
		if e.Strategy == "retrieve" {
			retrievals++
			continue
		}
		events++
		saved += e.In - e.Out
		byStrategy[e.Strategy] += e.In - e.Out
		if e.Session != "" {
			sessions[e.Session] = true
		}
		if d := *days - 1 - int(time.Since(t).Hours()/24); d >= 0 && d < *days {
			daily[d] += e.In - e.Out
		}
	}
	if events == 0 {
		fmt.Printf("whittle stats: no events in the last %d days\n", *days)
		return
	}

	bold, dim, green, reset := "\x1b[1m", "\x1b[2m", "\x1b[32m", "\x1b[0m"
	if *share {
		bold, dim, green, reset = "", "", "", ""
	}
	fmt.Printf("\n%s🪓 whittle%s %s- last %d days%s\n\n", bold, reset, dim, *days, reset)
	fmt.Printf("  %s%s%s tokens carved away  %s(~$%.2f at $%.2f/M · %d outputs · %d sessions)%s\n\n",
		green+bold, humanInt(saved), reset, dim, float64(saved)/1e6**price, *price, events, max(1, len(sessions)), reset)
	fmt.Printf("  %s\n", sparkline(daily))
	fmt.Printf("  %s%s%s\n\n", dim, timeAxis(*days), reset)

	type kv struct {
		k string
		v int
	}
	var top []kv
	for k, v := range byStrategy {
		top = append(top, kv{shortName(k), v})
	}
	sort.Slice(top, func(a, b int) bool { return top[a].v > top[b].v })
	maxV := top[0].v
	for i, e := range top {
		if i == 4 {
			break
		}
		bar := strings.Repeat("█", 1+max(0, e.v)*22/max(1, maxV))
		fmt.Printf("  %-12s %s%s%s %s\n", e.k, green, bar, reset, humanInt(e.v))
	}
	rate := 0.0
	if events > 0 {
		rate = 100 * float64(retrievals) / float64(events)
	}
	fmt.Printf("\n  %soriginals retrieved by the agent: %d (%.1f%%) - everything else was enough as carved%s\n", dim, retrievals, rate, reset)
	fmt.Printf("  %snever cuts what doesn't come back · github.com/firstops-dev/whittle%s\n\n", dim, reset)
}

func sparkline(daily []int) string {
	blocks := []rune("▁▂▃▄▅▆▇█")
	maxV := 1
	for _, v := range daily {
		if v > maxV {
			maxV = v
		}
	}
	var b strings.Builder
	for _, v := range daily {
		b.WriteRune(blocks[max(0, v)*(len(blocks)-1)/maxV])
		b.WriteRune(' ')
	}
	return b.String()
}

func timeAxis(days int) string {
	return fmt.Sprintf("%-*s%s", days*2-5, fmt.Sprintf("-%dd", days), "today")
}

func shortName(strategy string) string {
	s := strings.TrimPrefix(strategy, "ansi_strip+")
	s = strings.TrimSuffix(s, "_compressor")
	s = strings.TrimSuffix(s, "_crusher")
	if s == "" {
		s = "ansi_strip"
	}
	return s
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func humanInt(n int) string {
	switch {
	case n >= 1e6:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

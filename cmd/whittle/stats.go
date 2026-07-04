package main

// whittle stats — the local savings report. Reads ~/.whittle/stats.jsonl (written
// by the hook; never transmitted anywhere) and aggregates tokens whittled.

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

func cmdStats(args []string) {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	days := fs.Int("days", 7, "window in days")
	price := fs.Float64("price", 3.0, "your input price per 1M tokens (USD), for the $ estimate")
	_ = fs.Parse(args)

	f, err := os.Open(filepath.Join(whittleHome(), "stats.jsonl"))
	if err != nil {
		fmt.Println("whittle stats: no events yet (the hook records compressions as your agent works)")
		return
	}
	defer f.Close()

	cutoff := time.Now().AddDate(0, 0, -*days).Unix()
	type ev struct {
		TS       int64  `json:"ts"`
		Tool     string `json:"tool"`
		Strategy string `json:"strategy"`
		In       int    `json:"in_tokens"`
		Out      int    `json:"out_tokens"`
	}
	events, saved := 0, 0
	byStrategy := map[string]int{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		var e ev
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.TS < cutoff {
			continue
		}
		events++
		saved += e.In - e.Out
		byStrategy[e.Strategy] += e.In - e.Out
	}
	if events == 0 {
		fmt.Printf("whittle stats: no events in the last %d days\n", *days)
		return
	}
	fmt.Printf("whittle — last %d days\n", *days)
	fmt.Printf("  %d tool outputs whittled\n", events)
	fmt.Printf("  %s tokens saved (~$%.2f at $%.2f/M — and savings compound across every later turn)\n",
		humanInt(saved), float64(saved)/1e6**price, *price)
	type kv struct {
		k string
		v int
	}
	var top []kv
	for k, v := range byStrategy {
		top = append(top, kv{k, v})
	}
	sort.Slice(top, func(a, b int) bool { return top[a].v > top[b].v })
	for i, e := range top {
		if i == 3 {
			break
		}
		fmt.Printf("  %-34s %s tokens\n", e.k, humanInt(e.v))
	}
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

package compress

import (
	"strings"
	"testing"
)

func TestDetect(t *testing.T) {
	jsonArr := `[{"id":1,"name":"a"},{"id":2,"name":"b"},{"id":3,"name":"c"}]`

	diff := `diff --git a/foo.go b/foo.go
index 1234567..89abcde 100644
--- a/foo.go
+++ b/foo.go
@@ -1,3 +1,3 @@
-old line
+new line
 context`

	html := `<!doctype html><html><head><title>x</title></head><body><div><p>hi</p></div></body></html>`

	search := `src/foo.go:12: func main() {
src/bar.go:44: return nil
internal/baz.go:7: package baz
cmd/x.go:99: log.Println("x")`

	logTxt := `2024-01-01 INFO starting up
2024-01-01 INFO loaded config
2024-01-01 ERROR failed to connect
2024-01-01 WARN retrying
2024-01-01 INFO done`

	csv := `id,name,score
1,alice,90
2,bob,85
3,carol,77`

	md := `| id | name |
| --- | --- |
| 1 | a |
| 2 | b |`

	code := `package main

func main() {
	for i := 0; i < 10; i++ {
		if i > 5 {
			return
		}
	}
}`

	prose := strings.Repeat("the quick brown fox jumps over the lazy dog and keeps running along the riverbank. ", 8)

	tests := []struct {
		name string
		in   string
		want ContentType
	}{
		{"json", jsonArr, TypeJSON},
		{"diff", diff, TypeDiff},
		{"html", html, TypeHTML},
		{"search", search, TypeSearch},
		{"log", logTxt, TypeLog},
		{"csv", csv, TypeTabular},
		{"markdown_table", md, TypeTabular},
		{"code", code, TypeCode},
		{"prose", prose, TypeProse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, conf := Detect(tt.in)
			if got != tt.want {
				t.Fatalf("Detect()=%q (conf %.2f), want %q", got, conf, tt.want)
			}
		})
	}
}

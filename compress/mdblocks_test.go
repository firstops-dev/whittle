package compress

import (
	"strings"
	"testing"
)

func segKinds(t *testing.T, doc string) ([]MDSegment, MDStats) {
	t.Helper()
	return SegmentMarkdown(strings.Split(doc, "\n"))
}

// verbatimText concatenates all verbatim segment text for containment checks.
func verbatimText(segs []MDSegment) string {
	var b strings.Builder
	for _, s := range segs {
		if s.Verbatim {
			b.WriteString(s.Text)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func proseText(segs []MDSegment) string {
	var b strings.Builder
	for _, s := range segs {
		if !s.Verbatim {
			b.WriteString(s.Text)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// TestSegmentMarkdown_VerbatimClasses: every construct that must never reach the
// model lands in a verbatim segment, byte-exact; surrounding prose stays prose.
func TestSegmentMarkdown_VerbatimClasses(t *testing.T) {
	cases := map[string]struct {
		doc          string
		mustVerbatim []string // substrings that MUST be in verbatim segments
		mustProse    []string // substrings that MUST be in prose segments
	}{
		"backtick_fence": {
			doc:          "Intro prose sentence for the document here.\n```go\nfunc main() {}\n```\nMore prose after the fence follows here.",
			mustVerbatim: []string{"```go", "func main() {}", "```"},
			mustProse:    []string{"Intro prose", "More prose after"},
		},
		"tilde_fence": {
			doc:          "Before the fence some text.\n~~~python\nimport os\n~~~\nAfter the fence more text.",
			mustVerbatim: []string{"~~~python", "import os"},
			mustProse:    []string{"Before the fence", "After the fence"},
		},
		"unclosed_fence_rest_verbatim": {
			doc:          "Prose first paragraph here.\n```\ncode line one\ncode line two\nnever closed prose-looking line stays code.",
			mustVerbatim: []string{"code line one", "never closed prose-looking line stays code."},
			mustProse:    []string{"Prose first paragraph here."},
		},
		"fence_len_must_match": {
			doc:          "Some prose sits here first.\n````\ninner ``` not a closer\n````\nAfter the fence prose resumes.",
			mustVerbatim: []string{"inner ``` not a closer"},
			mustProse:    []string{"After the fence prose resumes."},
		},
		"indented_code": {
			doc:          "Paragraph before the example follows now.\n\n    indented code line\n\tTAB indented line\n\nAnd prose resumes right after that block.",
			mustVerbatim: []string{"    indented code line", "\tTAB indented line"},
			mustProse:    []string{"Paragraph before", "prose resumes"},
		},
		"atx_and_setext_headings": {
			doc:          "# Top\nProse line one is a real sentence here.\nSubtitle Text\n=====\nMore prose.",
			mustVerbatim: []string{"# Top", "Subtitle Text", "====="},
			mustProse:    []string{"Prose line one"},
		},
		"thematic_breaks": {
			doc:          "Above the break sits this sentence now.\n---\n***\n___\nBelow the break sits this sentence too.",
			mustVerbatim: []string{"---", "***", "___"},
			mustProse:    []string{"Above the break", "Below the break"},
		},
		"table": {
			doc:          "The table below lists the fields used.\n| name | type |\n|------|------|\n| id   | int  |\nTrailing prose after the table is here.",
			mustVerbatim: []string{"| name | type |", "| id   | int  |"},
			mustProse:    []string{"table below lists", "Trailing prose"},
		},
		"blockquote_list_linkdef_html_image": {
			doc:          "Prose paragraph starts the document off.\n> quoted error: connection refused\n- bullet item with `flag`\n1. numbered item\n[ref]: https://example.com\n<div class=\"x\">\n![alt](img.png)\nFinal prose sentence closes the doc here.",
			mustVerbatim: []string{"> quoted error", "- bullet item", "1. numbered item", "[ref]:", "<div", "![alt]"},
			mustProse:    []string{"Prose paragraph starts", "Final prose sentence"},
		},
		// Reviewer B1: unfenced code that dodges the straggler regexes must still
		// be VERBATIM — prose is opt-in, and these fail the isProseLine allow-list.
		"unfenced_sql": {
			doc:          "The migration below removes stale sessions from the table.\nDELETE FROM sessions WHERE last_seen < 1700000000 AND tenant_id = 42;\nSELECT count(*) FROM sessions ORDER BY name ASC\nRun it during the maintenance window only please.",
			mustVerbatim: []string{"DELETE FROM sessions", "SELECT count(*)"},
			mustProse:    []string{"migration below removes", "maintenance window"},
		},
		"unfenced_c_and_lisp": {
			doc:          "These declarations appear in the public header file today.\nvoid (*handler)(int sig);\nstruct node *next;\nint main(void);\n(defun foo (x) (+ x 1))\nAnd the document continues with plain prose here.",
			mustVerbatim: []string{"void (*handler)(int sig);", "struct node *next;", "(defun foo"},
			mustProse:    []string{"declarations appear in", "document continues with"},
		},
		"two_space_indented_code": {
			doc:          "The haskell snippet is reproduced with light indentation.\n  putStrLn hello\n  pure unit\nAfter which the prose resumes for the reader again.",
			mustVerbatim: []string{"  putStrLn hello"},
			mustProse:    []string{"haskell snippet is", "prose resumes for"},
		},
		"config_stragglers_verbatim": {
			doc:          "The magic number is documented below for reference.\nMagic: 0xE85250D6 in the header\nCXX := clang++\nAnd the doc continues with normal prose text.",
			mustVerbatim: []string{"Magic: 0xE85250D6", "CXX := clang++"},
			mustProse:    []string{"magic number is documented", "doc continues with"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			segs, _ := segKinds(t, tc.doc)
			v, p := verbatimText(segs), proseText(segs)
			for _, want := range tc.mustVerbatim {
				if !strings.Contains(v, want) {
					t.Errorf("%q must be VERBATIM; verbatim=%q", want, v)
				}
				if strings.Contains(p, want) {
					t.Errorf("%q leaked into PROSE (would reach the model)", want)
				}
			}
			for _, want := range tc.mustProse {
				if !strings.Contains(p, want) {
					t.Errorf("%q must be PROSE; prose=%q", want, p)
				}
			}
			// reassembling all segments must reproduce the input byte-exact
			var all []string
			for _, s := range segs {
				all = append(all, s.Text)
			}
			if strings.Join(all, "\n") != tc.doc {
				t.Errorf("segments do not reassemble to the input")
			}
		})
	}
}

// TestIsMarkdownDoc_V2: fenced technical docs now qualify; configs/scripts still
// veto; thin docs still fail the evidence floors.
func TestIsMarkdownDoc_V2(t *testing.T) {
	richDoc := `# Service Guide

This document explains how the ingestion service processes each event today.

## Usage

Run the service with the standard flags and watch the queue drain steadily.

` + "```go\nfunc main() { run() }\n```" + `

## Notes

Operators should always drain the queue before restarting anything in prod.`
	if !isMarkdownDoc(strings.Split(richDoc, "\n")) {
		t.Fatal("fenced technical doc with rich prose must now qualify")
	}

	ansible := strings.Repeat(`# Playbook for provisioning the production web tier.
- name: Ensure nginx is installed on every production host in the fleet.
  apt:
    name: nginx
    state: present
  become: yes
`, 4)
	if isMarkdownDoc(strings.Split(ansible, "\n")) {
		t.Fatal("kv-dominated YAML must still veto (budget)")
	}

	script := "#!/bin/bash\n# Deploy helper for the team to use every day now.\n# It restarts the service and verifies the health endpoint.\necho hi"
	if isMarkdownDoc(strings.Split(script, "\n")) {
		t.Fatal("shebang must remain an absolute veto")
	}

	thin := "# A\n\nshort.\n\n# B\n\nalso short."
	if isMarkdownDoc(strings.Split(thin, "\n")) {
		t.Fatal("prose-mass floor must reject thin docs")
	}
}

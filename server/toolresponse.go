package server

// toolresponse.go - locating the compressible text inside a PostToolUse
// tool_response, and rebuilding a replacement of the SAME shape.
//
// Shape preservation is load-bearing: Claude Code validates
// hookSpecificOutput.updatedToolOutput against the tool's output schema and
// silently keeps the ORIGINAL output on mismatch (verified live on Claude Code
// 2.1.203: a bare-string replacement for Bash is rejected with "does not match
// Bash's output shape"; the same payload wrapped as {"stdout": ...} lands).
// So the rebuild swaps only the text field the content came from and passes
// every sibling field through byte-exact (json.RawMessage, never re-decoded).

import "encoding/json"

// identity is the rebuild for shapeless text: the replacement IS the string.
func identity(c string) any { return c }

// ExtractHookText is the one extraction entry point for both hook paths (HTTP
// /hook and the `whittle hook` command). It prefers tool_response - the field
// that carries the tool's output SHAPE, which the replacement must reproduce -
// and falls back to the legacy string-only tool_output field (real Claude Code
// 2.1.203 events omit it; kept for SDK/older producers).
func ExtractHookText(toolResponse json.RawMessage, toolOutput string) (string, func(string) any, bool) {
	if text, rebuild, ok := ExtractToolText(toolResponse); ok {
		return text, rebuild, true
	}
	if toolOutput != "" {
		return toolOutput, identity, true
	}
	return "", nil, false
}

// ExtractToolText finds the compressible text in a tool_response and returns a
// rebuild function that produces an updatedToolOutput of the same shape with
// that text replaced.
//
// Recognized shapes, in order (captured from real PostToolUse events):
//   - bare non-empty string                    -> replaced as a bare string
//   - {"stdout"|"output"|"result": "text"}     -> that key swapped (Bash, ...)
//   - {"file": {"content": "text"}}            -> file.content swapped (Read)
//
// MCP content arrays ({"content":[{"type":"text",...}]}) are NOT handled (a
// pre-existing gap): unknown shapes fail closed - no replacement, original
// preserved - never a guessed rebuild.
//
// Sibling fields (stderr, interrupted, file.numLines, unknown future fields)
// are preserved verbatim: they describe the original tool run and keep the
// replacement schema-valid by construction. (file.numLines/totalLines
// deliberately keep describing the file on disk, so follow-up Read
// offset/limit math stays correct against the real file.) The text-carrying
// entry is nilled as soon as it is decoded so the large original bytes are
// not pinned in memory across the compress round-trip; rebuild is single-use
// and mutates the maps decoded here.
func ExtractToolText(raw json.RawMessage) (string, func(string) any, bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		// JSON null and "" decode successfully but carry no text: report !ok so
		// the tool_output fallback can fire instead of shadowing it.
		return s, identity, s != ""
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return "", nil, false
	}
	for _, k := range []string{"stdout", "output", "result"} {
		if v, ok := obj[k]; ok && json.Unmarshal(v, &s) == nil && s != "" {
			key := k
			obj[key] = nil // release the original bytes now; rebuild overwrites
			return s, func(c string) any {
				obj[key], _ = json.Marshal(c)
				return obj
			}, true
		}
	}
	if f, ok := obj["file"]; ok {
		var file map[string]json.RawMessage
		if json.Unmarshal(f, &file) == nil {
			if v, ok := file["content"]; ok && json.Unmarshal(v, &s) == nil && s != "" {
				obj["file"], file["content"] = nil, nil // ditto: rebuild refills both
				return s, func(c string) any {
					file["content"], _ = json.Marshal(c)
					obj["file"], _ = json.Marshal(file)
					return obj
				}, true
			}
		}
	}
	return "", nil, false
}

// HookReply wraps a rebuilt updatedToolOutput in the PostToolUse hook
// envelope - the single definition of the wire contract for both delivery
// paths (HTTP body and command-hook stdout).
func HookReply(updated any) map[string]any {
	return map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "PostToolUse",
			"updatedToolOutput": updated,
		},
	}
}

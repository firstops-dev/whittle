"""Production gate: decide whether content is SAFE to compress, and at what rate.

Metadata-first (most reliable), content-heuristic as fallback. The gate is fail-safe:
when unsure it returns SKIP, which means passthrough (original returned unchanged) so
it can never break the task, only forgo savings.

Classes:
  prose            -> safe to compress (web/search/articles/plain text)
  code_structured  -> SKIP (code, JSON/YAML/CSV, configs, file reads): deleting tokens
                      corrupts syntax/fields (LLMLingua weakness, LongCodeZip 2025)
"""
import json as _json
import re

# ---- structural veto (content is ground truth; runs BEFORE metadata) ----
# A tool name like "..._search" is a weak hint; a body that is literally a JSON
# object is proof. Trust the body. This block exists because enrichment/search
# MCP tools (Apollo, HubSpot) return JSON with no MIME, and the old
# tool_name="search"->prose path compressed and corrupted them.
_MARKUP = re.compile(r"^\s*<(\?xml|!doctype|html|svg|[a-zA-Z][\w:-]*[\s/>])", re.I)


def looks_structured(text):
    """Strong, cheap structural evidence that `text` is JSON/markup, not prose."""
    if not text:
        return False
    s = text.lstrip()
    if not s:
        return False
    c = s[0]
    if c in "{[":
        try:
            _json.loads(s)            # full parse: definitive
            return True
        except Exception:
            # truncated/partial but unmistakably JSON-shaped
            if re.match(r'[{\[]\s*["{\[]', s) or s.count('":') >= 3:
                return True
    if c == "<" and _MARKUP.search(s):
        return True
    return False


def prose_ratio(text):
    """Fraction of letters+spaces in a sample; structured data is symbol-heavy."""
    if not text:
        return 1.0
    sample = text[:4000]
    alpha = sum(ch.isalpha() or ch.isspace() for ch in sample)
    return alpha / max(len(sample), 1)


_MIN_PROSE_RATIO = 0.55   # below this, treat as structured (skip) — fail-safe

# ---- content heuristic (fallback only), ported from the experiment ----
_CODE_FENCE = re.compile(r"```")
_DIFF = re.compile(r"(?m)^(@@ |diff --git |index [0-9a-f]+\.\.|[+\-]{3} [ab]/)")
_SHELL = re.compile(r"(?m)^\s*(\$ |sudo |npm |pip |go (run|build|test|mod)|git |make |cd |ls |cat |grep |docker |kubectl )")
_PATHEXT = re.compile(r"[\w./\-]+\.(go|py|ts|tsx|js|jsx|java|rs|c|cpp|h|hpp|rb|php|sh|sql|yaml|yml|json|toml|proto|css|html)\b")
_CODE_SYNTAX = re.compile(
    r"(?m)(^\s*(func |def |class |import |package |from \w+ import|const |let |var |public |private |return |if \(|for \(|while \()"
    r"|[{};]\s*$|=>|::|\bself\.|\b#include\b)")
# Definitive JS/JSX markers (design tools like Figma get_design_context / get_jsx
# emit `const x = ...` assignments and `<div style={{...}}>` JSX, which the generic
# heuristic under-weights).
_JS_ASSIGN = re.compile(r"(?m)^\s*(const|let|var)\s+[\w$]+\s*=")
_JSX = re.compile(r"(=\{\{|style=\{|className=|</[A-Za-z]|<[A-Za-z]\w*\s+\w+=)")

_MIME_STRUCT = ("json", "xml", "yaml", "csv", "x-python", "javascript", "typescript",
                "x-sh", "sql", "octet-stream", "x-c", "x-java")
# NOTE: no prose allowlist for MIME or tool_name. Metadata can only vote SKIP;
# only the content body can earn a "prose" (compress) verdict. Search/fetch tools
# return structured data, so a tool_name->prose promotion is unsafe.
_TOOL_STRUCT = ("read", "edit", "write", "bash", "grep", "glob", "notebook", "exec",
                "shell", "sql", "file", "cat")


def code_signal(text):
    if not text:
        return 0
    s = 0
    if _CODE_FENCE.search(text): s += 2
    if _DIFF.search(text): s += 2
    if _SHELL.search(text): s += 1
    if _PATHEXT.search(text): s += 1
    if _CODE_SYNTAX.search(text): s += 1
    if _JS_ASSIGN.search(text): s += 2
    if _JSX.search(text): s += 2
    return s


def classify(content, tool_name=None, content_type=None):
    """Return (klass, signal_source).

    Order is deliberate and fail-safe — every rule can only push toward SKIP:
      1. structural veto on the CONTENT (strongest signal: the body IS json/markup)
      2. MIME, but only the structured side (a prose MIME is not trusted to override
         the body — search tools send no MIME anyway)
      3. tool_name, SKIP-direction only (never promotes to prose: search/fetch tools
         routinely return structured data)
      4. code heuristic
      5. prose-ratio floor (symbol-heavy bodies skip)
    Default prose only after surviving all five.
    """
    content = content or ""
    # 1. content structural veto — runs first; the body is ground truth.
    if looks_structured(content):
        return "code_structured", "content_structural"
    # 2. MIME: structured side only.
    if content_type and any(x in content_type.lower() for x in _MIME_STRUCT):
        return "code_structured", "mime"
    # 3. tool_name: SKIP-direction only (B: no prose promotion).
    if tool_name and any(k in tool_name.lower() for k in _TOOL_STRUCT):
        return "code_structured", "tool_name"
    # 4. code heuristic.
    if code_signal(content) >= 2:
        return "code_structured", "heuristic"
    # 5. prose-ratio floor (C).
    if prose_ratio(content) < _MIN_PROSE_RATIO:
        return "code_structured", "low_prose_ratio"
    return "prose", "default"


def decide(content, n_tokens, tool_name=None, content_type=None, content_class=None, min_tokens=64):
    """Return (action, klass, signal, reason). action in {compress, skip}."""
    if content_class in ("prose", "code_structured"):
        klass, signal = content_class, "override"
    else:
        klass, signal = classify(content, tool_name, content_type)
    if n_tokens < min_tokens:
        return "skip", klass, signal, "too_short"
    if klass == "code_structured":
        return "skip", klass, signal, "code_structured"
    return "compress", klass, signal, None

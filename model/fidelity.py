"""Fidelity guards for the prose path — deterministic pre/post passes around
LLMLingua-2 that make its deletions safe on identifier-dense agent output.

Root cause being fixed: the model deletes at sub-word granularity and its
word boundaries include punctuation, so `session_id` can become `_id` and
`backend/compressor/gate.go` can lose a path segment. Audited impact: ~13.6%
of its deletions are load-bearing (identifiers, numbers, negations).

Three guards, all deterministic and fail-open:
  1. protect():  swap identifier-shaped whitespace-tokens for a sentinel the
     model is forced to keep; restore after. Token-level only (a limitation:
     multi-token quoted spans are not protected — the whole-token guard still
     applies to them).
  2. whole_tokens(): reconstruct output so every original whitespace-token
     either survives WHOLE or is dropped WHOLE — never a fragment. Alignment
     is greedy in-order (extractive output preserves token order).
  3. NEGATIONS: small static force-keep list (joined into force_tokens by the
     caller) so dropped negations cannot invert meaning.

Any anomaly (sentinel lost, alignment failure) returns None → caller must
fail open to the original content. A wrong identifier is worse than an
uncompressed document.
"""
import re
from collections import Counter

SENTINEL = "FKEEPX"  # ascii, single wordpiece-friendly, never in real output

# MD_BLOCK_SENTINEL stands in for a masked verbatim markdown block (fenced code,
# headings, tables...) in the Go router's structure-aware doc path. It is force-
# kept (FORCE_TOKENS) and additionally entity-masked by protect() below like any
# ALL-CAPS token, but EXCLUDED from identifier-density accounting: a technical
# doc can carry dozens of block sentinels and must not skip as identifier_dense
# because of them.
MD_BLOCK_SENTINEL = "MDBLKX"

NEGATIONS = ["not", "no", "never", "without", "except", "unless", "cannot",
             "failed", "failure", "only", "none"]

# Identifier-shaped whitespace tokens whose loss/mangling misleads an agent.
_ENTITY_RES = [
    re.compile(r".*[/\\][\w.-]+"),                # paths
    re.compile(r".*\w_\w.*"),                     # snake_case
    re.compile(r".*[a-z][A-Z].*"),                # camelCase
    re.compile(r".*\d.*"),                        # anything numeric (ids, versions, counts, units)
    re.compile(r"https?://\S+"),                  # urls
    re.compile(r"`[^`]+`"),                       # backticked
    re.compile(r".*[A-Z]{2,}.*"),                 # acronyms / ALL_CAPS flags
]

_WS_TOKEN = re.compile(r"(\S+)(\s*)")


def _tokens(text):
    """(token, trailing_whitespace) pairs, preserving original spacing."""
    return [(m.group(1), m.group(2)) for m in _WS_TOKEN.finditer(text)]


def _is_entity(tok):
    t = tok.strip(".,;:!?")
    if len(t) < 2:
        return False
    return any(r.fullmatch(t) for r in _ENTITY_RES)


def protect(content):
    """Replace entity tokens with SENTINEL. Returns (masked, spans,
    protected_ratio) where spans is the ordered list of original tokens and
    protected_ratio is the char fraction protected (early-skip signal)."""
    parts, spans, prot_chars = [], [], 0
    for tok, ws in _tokens(content):
        if SENTINEL in tok:  # content collides with sentinel: cannot protect safely
            return content, [], 0.0
        if _is_entity(tok):
            spans.append(tok)
            # Bare short numerics ("3", "12", "87%") are protected but do NOT
            # count toward identifier density — number-mentioning prose is
            # normal prose, not identifier-dense content (reviewer P2).
            if (not re.fullmatch(r"\d{1,4}([.,]\d+)?%?", tok.strip(".,;:!?"))
                    and tok.strip(".,;:!?") != MD_BLOCK_SENTINEL):
                prot_chars += len(tok)
            parts.append(SENTINEL + ws)
        else:
            parts.append(tok + ws)
    ratio = prot_chars / max(1, len(content))
    return "".join(parts), spans, ratio


def restore(compressed, spans, masked=None):
    """Positional sentinel restore. LOAD-BEARING ASSUMPTION: extractive
    output preserves token order (LLMLingua-2 deletes, never reorders), so
    the i-th surviving sentinel is spans[i]. Count mismatch (loss/dup) fails
    open; reordering is impossible by construction for an extractive model.
    Returns None if any sentinel was lost or duplicated — caller fails open."""
    if not spans:
        return compressed
    # The model's detokenizer can glue a sentinel to adjacent surviving words
    # ("updatedFKEEPX"). Re-separate every occurrence so counting, adjacency
    # checks, and the restored spans all operate on standalone tokens.
    compressed = re.sub(r"(?<=\S)" + re.escape(SENTINEL), " " + SENTINEL, compressed)
    compressed = re.sub(re.escape(SENTINEL) + r"(?=\S)", SENTINEL + " ", compressed)
    if compressed.count(SENTINEL) != len(spans):
        return None
    # NOTE on drop-masked-by-duplication (count-preserving tamper): identical
    # sentinels cannot be distinguished here, and extractive DELETION
    # legitimately creates newly-adjacent sentinels (dropping the words
    # between two entities), so an adjacency heuristic false-fires on normal
    # output (measured: ~all real docs). Enforcement is LAYERED instead: the
    # count check above catches loss or duplication alone, and a wrong
    # positional mapping places spans out of original order, which
    # whole_tokens() (always run downstream) rejects as unaligned leftovers.
    out, idx, pos = [], 0, 0
    while True:
        j = compressed.find(SENTINEL, pos)
        if j < 0:
            out.append(compressed[pos:])
            break
        out.append(compressed[pos:j])
        out.append(spans[idx])
        idx += 1
        pos = j + len(SENTINEL)
    return "".join(out)


_WORD = re.compile(r"\w+")


def whole_tokens(original, compressed, min_coverage=0.6):
    """Rebuild compressed so each ORIGINAL whitespace-token is kept whole or
    dropped whole. Alignment is on \\w+ WORD-PIECES, not whole tokens:
    llmlingua's detokenizer both (a) deletes sub-word pieces ("session_id" ->
    "_id") and (b) glues force-kept punctuation across token boundaries, so one
    compressed token can span pieces of several original tokens ('args"body',
    '1..notion') or be a fragment of one. Splitting on \\w+ makes interior
    punctuation irrelevant on BOTH sides: the piece streams align in order
    regardless of how the detokenizer re-punctuated them.

    Each original whitespace-token owns a contiguous run of pieces; it is KEPT
    whole (emitted verbatim, with its original punctuation/whitespace — output
    is always a subset of original tokens, C1) iff >= min_coverage of its piece
    characters survive in the compressed stream, else dropped whole. Pure-
    punctuation original tokens (no pieces) are dropped — never resurrected
    (reviewer P2-a). A compressed piece that aligns to nothing (real token the
    original never had, or out-of-order) leaves leftovers -> None (anomaly).

    A compressed piece is treated as a sub-word FRAGMENT of the current original
    piece only when it is not itself an exact original piece still waiting ahead
    (remaining[cj] == 0). Without this guard a kept negation ("not" kept, "notes"
    dropped) is stolen as a fragment of the preceding container word, credited to
    the wrong token, and the real "not" is dropped whole -> silent meaning
    inversion. The exact-match-ahead interpretation is always preferred so the
    load-bearing later token survives (reviewer B1)."""
    orig = _tokens(original)
    pieces = []                          # (piece, orig_token_index)
    tok_chars = [0] * len(orig)          # total piece chars per original token
    for idx, (tok, _ws) in enumerate(orig):
        for p in _WORD.findall(tok):
            pieces.append((p, idx))
            tok_chars[idx] += len(p)
    comp = [p for t, _ in _tokens(compressed) for p in _WORD.findall(t)]
    remaining = Counter(p for p, _ in pieces)   # exact original pieces not yet passed
    covered = [0] * len(orig)            # matched piece chars per original token
    i, j = 0, 0
    while i < len(pieces) and j < len(comp):
        op, tidx = pieces[i]
        cj = comp[j]
        if cj == op:
            covered[tidx] += len(op)
            remaining[op] -= 1
            i += 1
            j += 1
            continue
        # Fragment only if cj is NOT an exact original piece waiting ahead —
        # else prefer that exact match (protects kept negations, reviewer B1).
        if cj in op and remaining[cj] == 0:
            run = 0                       # consume the run of true sub-word frags
            while (j < len(comp) and comp[j] != op
                   and comp[j] in op and remaining[comp[j]] == 0):
                run += len(comp[j])
                j += 1
            covered[tidx] += min(run, len(op))
            remaining[op] -= 1
            i += 1
            continue
        remaining[op] -= 1               # original piece dropped
        i += 1
    if j < len(comp):
        return None                      # unaligned real piece — anomaly
    kept = [k for k in range(len(orig))
            if tok_chars[k] and covered[k] >= min_coverage * tok_chars[k]]
    return "".join(orig[k][0] + orig[k][1] for k in kept).rstrip() or None

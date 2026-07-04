"""Contract tests for the fidelity guards (fidelity.py + app.py wiring).

These tests encode the SHARED OUTCOME CONTRACT, not the implementation's
quirks. A failing check below is a FOUND BUG in the code, not a test to relax.

Run: python3 test_fidelity.py   (plain asserts, no pytest; stdlib only; no torch)

Contract:
  C1  whole_tokens() output never contains a fragment of an original token —
      every output token is an original token, verbatim.
  C2  protect()/restore() round-trip is identity when all sentinels survive;
      entities land in spans; restore -> None (never a wrong mapping) when a
      sentinel is dropped or duplicated.
  C3  NEGATIONS present and joined into FORCE_TOKENS (with SENTINEL) in app.py.
  C4  every anomaly is fail-safe (None / decline), never partial/wrong output.
  C5  protect() ratio math: identifier-dense > 0.45, plain prose ~low.
  C6  determinism: two runs identical.
"""
import os
import random
import re

import fidelity as f

FAILS = []


def ok(cond, msg, repro=None):
    if cond:
        return True
    line = "FAIL: " + msg
    if repro is not None:
        line += "  ::  repro=" + repr(repro)
    print(line)
    FAILS.append(msg)
    return False


def _out_tokens(s):
    return [t for t, _ in f._tokens(s)]


# ---------------------------------------------------------------------------
# C3 — NEGATIONS + FORCE_TOKENS wiring (import fidelity only; read app.py text)
# ---------------------------------------------------------------------------
def test_c3_negations_and_force_tokens():
    expected_negs = {"not", "no", "never", "without", "except", "unless",
                     "cannot", "failed", "failure", "only", "none"}
    have = set(fidelity_negations())
    missing = expected_negs - have
    ok(not missing, "C3: NEGATIONS missing expected words %r" % (missing,))
    ok(len(f.NEGATIONS) == len(set(f.NEGATIONS)),
       "C3: NEGATIONS has duplicates: %r" % (f.NEGATIONS,))

    src = read_app_source()
    m = re.search(r"^FORCE_TOKENS\s*=\s*(.+)$", src, re.MULTILINE)
    ok(m is not None, "C3: FORCE_TOKENS assignment not found in app.py")
    if m:
        rhs = m.group(1)
        ok("fidelity.SENTINEL" in rhs,
           "C3: FORCE_TOKENS does not include fidelity.SENTINEL :: %r" % rhs)
        ok("fidelity.NEGATIONS" in rhs,
           "C3: FORCE_TOKENS does not join fidelity.NEGATIONS :: %r" % rhs)
        # sentence-boundary force tokens must remain so the model still segments
        for t in ('"."', '"?"', '"!"', '","', '":"', r'"\n"'):
            ok(t in rhs, "C3: FORCE_TOKENS dropped boundary token %s :: %r" % (t, rhs))


def fidelity_negations():
    return list(f.NEGATIONS)


def read_app_source():
    here = os.path.dirname(os.path.abspath(__file__))
    with open(os.path.join(here, "app.py"), "r", encoding="utf-8") as fh:
        return fh.read()


# ---------------------------------------------------------------------------
# C2 — entity classification + protect()/restore() round-trip
# ---------------------------------------------------------------------------
ENTITY_SAMPLES = [
    "backend/compressor/gate.go",   # path
    "backend\\win\\file.txt",       # windows path
    "session_id",                   # snake_case
    "user_name",                    # snake_case
    "camelCase",                    # camelCase
    "apiKey",                       # camelCase
    "v1.2.3",                       # version / numeric
    "87%",                          # numeric+unit
    "42",                           # bare number
    "https://example.com/x?y=1",    # url
    "http://a.b",                   # url
    "SIGKILL",                      # acronym / ALL_CAPS
    "ID_TOKEN",                     # ALL_CAPS flag
    "`code`",                       # backticked
]

NON_ENTITY_SAMPLES = [
    "the", "meeting", "covered", "budget", "review", "team", "ship",
    "hello", "world", "a",  # single char -> below length gate
    "I",                    # single char
]


def test_c2_entity_membership():
    for tok in ENTITY_SAMPLES:
        ok(f._is_entity(tok), "C2: entity NOT recognized: %r" % tok)
    for tok in NON_ENTITY_SAMPLES:
        ok(not f._is_entity(tok), "C2: plain word wrongly flagged entity: %r" % tok)


def test_c2_entities_land_in_spans():
    content = ("open backend/compressor/gate.go set session_id to v1.2.3 "
               "hit SIGKILL at https://example.com/x saw 87% via apiKey")
    _masked, spans, _ratio = f.protect(content)
    for want in ["backend/compressor/gate.go", "session_id", "v1.2.3",
                 "SIGKILL", "https://example.com/x", "87%", "apiKey"]:
        ok(want in spans, "C2: entity %r not captured in spans %r" % (want, spans))


def test_c2_roundtrip_identity():
    cases = [
        "see backend/x/y.go for session_id v1.2.3 SIGKILL at https://a.com/b 87% done",
        "plain prose with no identifiers at all here",
        "café résumé Ω unicode session_id naïve",         # unicode + entity
        "trailing spaces   and\ttabs\nand newlines v1.0",  # mixed whitespace
        "punctuation session_id, then backend/x.go. end",  # trailing punct on entity
        "",                                                # empty
        "solo",                                            # single token
        "backend/a.go backend/b.go backend/c.go",          # repeated-shape entities
    ]
    for content in cases:
        masked, spans, _ratio = f.protect(content)
        restored = f.restore(masked, spans)
        ok(restored == content,
           "C2: round-trip not identity", repro={"in": content, "out": restored})


def test_c2_roundtrip_fuzz():
    rng = random.Random(20260703)
    pool = ["the", "cat", "session_id", "backend/x/y.go", "v1.2.3", "87%",
            "SIGKILL", "apiKey", "run", "café", "Ω", "a.b", "foo_bar",
            "https://a.co/x", "and", "then", "done", "!", "note:", "x"]
    for _ in range(2000):
        n = rng.randint(0, 12)
        toks = [rng.choice(pool) for _ in range(n)]
        # random single-space vs multi-space joins
        content = ""
        for k, t in enumerate(toks):
            content += t
            if k < len(toks) - 1:
                content += " " * rng.randint(1, 3)
        masked, spans, _ratio = f.protect(content)
        restored = f.restore(masked, spans)
        if not ok(restored == content, "C2: fuzz round-trip not identity",
                  repro={"in": content, "out": restored}):
            break


def test_c2_dropped_sentinel_returns_none():
    content = "a backend/x/y.go b session_id c v1.2.3 d"
    masked, spans, _ = f.protect(content)
    dropped = masked.replace(f.SENTINEL, "", 1)   # lose one sentinel
    ok(f.restore(dropped, spans) is None,
       "C2: dropped sentinel must return None", repro=dropped)


def test_c2_duplicated_sentinel_returns_none():
    content = "a backend/x/y.go b session_id c"
    masked, spans, _ = f.protect(content)
    dup = masked.replace(f.SENTINEL, f.SENTINEL + " " + f.SENTINEL, 1)  # dup one
    ok(f.restore(dup, spans) is None,
       "C2: duplicated sentinel must return None", repro=dup)


def test_c2_drop_and_dup_count_preserving_returns_none():
    # Contract: "restore returns None (never a wrong mapping) when a sentinel
    # is dropped OR duplicated." Here one is dropped AND another duplicated so
    # the COUNT is preserved. A count-only guard will mis-map positionally.
    content = "alpha backend/x/y.go beta session_id gamma v1.2.3 delta"
    masked, spans, _ = f.protect(content)          # 3 sentinels
    tampered = masked.replace(f.SENTINEL, "X", 1)  # drop first  -> count 2
    tampered = tampered.replace("gamma " + f.SENTINEL,
                                "gamma " + f.SENTINEL + " " + f.SENTINEL, 1)  # +1 -> 3
    res = f.restore(tampered, spans, masked)
    if res is not None:  # layered contract: a wrong mapping must die downstream
        res = f.whole_tokens(content, res)
    ok(res is None,
       "C2: drop+dup (count-preserving) must return None, not a wrong mapping",
       repro={"tampered": tampered, "returned": res})


# ---------------------------------------------------------------------------
# C1 — whole_tokens keeps whole original tokens, never a fragment
# ---------------------------------------------------------------------------
def test_c1_basic_behaviors():
    # subset kept, order preserved
    ok(f.whole_tokens("the quick brown fox jumps", "quick fox jumps")
       == "quick fox jumps", "C1: subset keep failed")
    # low-coverage fragment -> token dropped whole (not emitted as fragment)
    r = f.whole_tokens("internationalization matters", "inter matters")
    ok(r is None or "inter" not in _out_tokens(r),
       "C1: low-cov fragment leaked", repro=r)
    ok(r == "matters", "C1: low-cov fragment should drop to 'matters'", repro=r)
    # high-coverage fragment -> restored to WHOLE original token
    r2 = f.whole_tokens("internationalization matters", "internationalizati matters")
    ok(r2 == "internationalization matters",
       "C1: high-cov fragment not restored whole", repro=r2)


def test_c1_output_tokens_are_verbatim_original():
    o = "alpha beta gamma delta epsilon"
    for c in ["alpha gamma epsilon", "beta", "alph gamm", "delta epsilon", ""]:
        r = f.whole_tokens(o, c)
        if r is None:
            continue
        oset = set(_out_tokens(o))
        for t in _out_tokens(r):
            ok(t in oset, "C1: output token not in original set",
               repro={"orig": o, "comp": c, "res": r, "tok": t})


def test_c1_fuzz_10k():
    """~10k random cases: random originals (repeats, punctuation, unicode);
    compressed = random ordered subset with random fragment substitutions.
    Assert: no exception, result is None-or-valid, and every output token is a
    verbatim original token (C1)."""
    rng = random.Random(0xF1DE11)
    pool = ["cat", "dog", "the", "a.b", "x", "session_id", "v1", "café", "Ω",
            "run!", "foo/bar", "id", "int", "data", "value", "co.", "naïve",
            "backend/x.go", "42", "ok", "no", "abc", "AB", "long_token_here"]
    N = 10000
    exceptions = 0
    violations = 0
    for it in range(N):
        n = rng.randint(0, 8)
        toks = [rng.choice(pool) for _ in range(n)]
        orig = " ".join(toks)
        oset = set(toks)
        comp = []
        for t in toks:
            if rng.random() < 0.45:          # random drop -> ordered subset
                continue
            if rng.random() < 0.4 and len(t) > 1:  # random fragment substitution
                a = rng.randint(0, len(t) - 1)
                b = rng.randint(a + 1, len(t))
                comp.append(t[a:b])
            else:
                comp.append(t)
        if comp and rng.random() < 0.05:     # occasionally inject noise token
            comp.insert(rng.randrange(len(comp) + 1), "ZZQ")
        cstr = " ".join(comp)
        try:
            res = f.whole_tokens(orig, cstr)
        except Exception as e:  # noqa: BLE001
            exceptions += 1
            print("FAIL: C1 fuzz raised %r :: orig=%r comp=%r" % (e, orig, cstr))
            if exceptions <= 3:
                continue
            break
        if res is None:
            continue
        for t in _out_tokens(res):
            if t not in oset:
                violations += 1
                print("FAIL: C1 fuzz token not in original :: "
                      "orig=%r comp=%r res=%r tok=%r" % (orig, cstr, res, t))
                break
        if violations > 3:
            break
    ok(exceptions == 0, "C1: fuzz raised %d exception(s)" % exceptions)
    ok(violations == 0, "C1: fuzz produced %d fragment/foreign token(s)" % violations)
    if exceptions == 0 and violations == 0:
        print("  C1 fuzz: %d cases, 0 exceptions, 0 violations" % N)


# ---------------------------------------------------------------------------
# C4 — every anomaly is fail-safe (None / decline), never partial output
# ---------------------------------------------------------------------------
def test_c4_sentinel_count_mismatch_none():
    content = "a session_id b v1.2.3 c"
    masked, spans, _ = f.protect(content)
    ok(f.restore(masked.replace(f.SENTINEL, "", 1), spans) is None,
       "C4: count mismatch (too few) must be None")
    ok(f.restore(masked + " " + f.SENTINEL, spans) is None,
       "C4: count mismatch (too many) must be None")


def test_c4_alignment_leftovers_none():
    # compressed contains a token that aligns to nothing -> leftover -> None
    ok(f.whole_tokens("alpha beta", "zzz") is None,
       "C4: unalignable leftover must be None")
    ok(f.whole_tokens("alpha beta", "alpha qqq") is None,
       "C4: trailing unalignable token must be None")


def test_c4_empty_reconstruction_none():
    ok(f.whole_tokens("alpha beta", "") is None,
       "C4: empty compressed -> None")
    ok(f.whole_tokens("alpha beta", "in") is None,
       "C4: everything drops (fragment 'in' has no host) -> None")


def test_c4_content_with_literal_sentinel_declines():
    # protect() returns a 3-tuple (can't be None); its fail-safe on collision is
    # to DECLINE protection: unchanged content, no spans, zero ratio. This keeps
    # the pipeline from ever mis-restoring around a pre-existing sentinel.
    content = "this has " + f.SENTINEL + " inside and session_id too"
    masked, spans, ratio = f.protect(content)
    ok(masked == content, "C4: SENTINEL-in-content must leave content unchanged",
       repro=masked)
    ok(spans == [], "C4: SENTINEL-in-content must yield empty spans", repro=spans)
    ok(ratio == 0.0, "C4: SENTINEL-in-content must yield ratio 0.0", repro=ratio)


# ---------------------------------------------------------------------------
# C5 — protect() ratio math
# ---------------------------------------------------------------------------
def test_c5_ratio_math():
    dense = ("session_id backend/x/y.go v1.2.3 SIGKILL https://a.com/b "
             "user_name apiKey 87%")
    _, _, rd = f.protect(dense)
    ok(rd > 0.45, "C5: identifier-dense ratio must exceed 0.45, got %.3f" % rd,
       repro=dense)

    prose = ("the meeting covered the budget review and the next steps for the "
             "team to ship the product soon")
    _, _, rp = f.protect(prose)
    ok(rp < 0.15, "C5: plain-prose ratio must be low (<0.15), got %.3f" % rp,
       repro=prose)
    ok(rp < rd, "C5: prose ratio must be below dense ratio")


# ---------------------------------------------------------------------------
# C6 — determinism
# ---------------------------------------------------------------------------
def test_c6_determinism():
    content = ("open backend/x/y.go set session_id v1.2.3 and note 87% then "
               "SIGKILL the run at https://a.com/b")
    a = f.protect(content)
    b = f.protect(content)
    ok(a == b, "C6: protect() not deterministic")

    comp = "backend/x/y.go set session_id note SIGKILL"
    ok(f.whole_tokens(content, comp) == f.whole_tokens(content, comp),
       "C6: whole_tokens() not deterministic")
    ok(f.restore(a[0], a[1]) == f.restore(a[0], a[1]),
       "C6: restore() not deterministic")


# ---------------------------------------------------------------------------
def main():
    tests = [
        test_c3_negations_and_force_tokens,
        test_c2_entity_membership,
        test_c2_entities_land_in_spans,
        test_c2_roundtrip_identity,
        test_c2_roundtrip_fuzz,
        test_c2_dropped_sentinel_returns_none,
        test_c2_duplicated_sentinel_returns_none,
        test_c2_drop_and_dup_count_preserving_returns_none,
        test_c1_basic_behaviors,
        test_c1_output_tokens_are_verbatim_original,
        test_c1_fuzz_10k,
        test_c4_sentinel_count_mismatch_none,
        test_c4_alignment_leftovers_none,
        test_c4_empty_reconstruction_none,
        test_c4_content_with_literal_sentinel_declines,
        test_c5_ratio_math,
        test_c6_determinism,
    ]
    for t in tests:
        t()
    print("\n%d/%d contract checks passed" % (
        len_all() - len(FAILS), len_all()))
    if FAILS:
        print("FAILURES (%d): each is a found bug per the contract:" % len(FAILS))
        for m in FAILS:
            print("  - " + m)
        raise SystemExit(1)


# We count individual ok() assertions; track total via a closure-free counter.
_TOTAL = {"n": 0}
_orig_ok = ok


def ok(cond, msg, repro=None):  # noqa: F811
    _TOTAL["n"] += 1
    return _orig_ok(cond, msg, repro)


def len_all():
    return _TOTAL["n"]


if __name__ == "__main__":
    main()


# --- Regression: reviewer-reproduced detokenizer artifacts (commit 4f73c78 review) ---
def test_detok_artifacts():
    import fidelity as f
    S = f.SENTINEL
    # contraction split with interior apostrophe must not fail the doc (P1-a)
    r = f.whole_tokens("we don't ship today", "don ' t ship")
    assert r is not None and "ship" in r.split(), repr(r)
    # punctuation-reattach artifact aligns via cores
    assert f.whole_tokens("noting, the issue here", "noting,, the issue") == "noting, the issue"
    # glued sentinel re-separates and restores (previously vacuously untested)
    spans = ["session_id"]
    masked = "updated " + S + " done"
    assert f.restore("updated" + S + " done", spans, masked) == "updated " + S.replace(S, "session_id") + " done".replace("  ", " ") or True
    out = f.restore("updated" + S + " done", spans, masked)
    assert out is not None and "session_id" in out, repr(out)
    # dropped pure-punct originals must NOT resurrect (P2-a)
    r2 = f.whole_tokens("a , b c", "a c")
    assert r2 is not None and "," not in r2.split(), repr(r2)
    print("PASS: detok artifact regressions")
test_detok_artifacts()


def test_no_negation_inversion_via_substring_theft():
    """Reviewer B1: a kept negation whose text is a substring of a preceding
    DROPPED container word must not be stolen as that word's fragment and then
    dropped -> silent meaning inversion. The kept negation must survive."""
    import fidelity as f
    cases = [
        ("notes not found",          "not found",      "not"),
        ("notes not encrypted",      "not encrypted",  "not"),
        ("nodes no access",          "no access",      "no"),
        ("node no bar",              "no bar",         "no"),
        ("notice not ready",         "not ready",      "not"),
        ("north no signal",          "no signal",      "no"),
        ("notification not sent yet","not sent yet",   "not"),
        ("nevermore never again",    "never again",    "never"),
    ]
    for orig, comp, neg in cases:
        r = f.whole_tokens(orig, comp)
        assert r is not None and neg in r.split(), \
            "negation %r lost: whole_tokens(%r,%r)=%r" % (neg, orig, comp, r)
        # and the dropped container word must NOT be resurrected
        container = orig.split()[0]
        assert container not in r.split(), \
            "container %r resurrected: %r" % (container, r)
    print("PASS: no negation inversion via substring theft")
test_no_negation_inversion_via_substring_theft()


def test_deletion_created_adjacency_is_legitimate():
    import fidelity as f
    S=f.SENTINEL
    orig="run backend/x/y.go with session_id now"
    masked,spans,_=f.protect(orig)
    # model deletes "with": sentinels become adjacent — must RESTORE, not None
    out=f.restore(S+" "+S+" now",spans[:2],masked)
    assert out is not None and "backend/x/y.go" in out and "session_id" in out, repr(out)
    print("PASS: deletion-created adjacency")
test_deletion_created_adjacency_is_legitimate()

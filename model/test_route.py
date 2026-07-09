"""Unit tests for the few-shot classify logic, with a stub embedder so no torch or
model download is needed. The stub embeds text as an L2-normalized multi-hot over
its word tokens, so token-overlapping strings have higher cosine — enough to make
nearest-example selection meaningful and deterministic."""
import math

from route import Classifier


class StubEmbedder:
    def __init__(self):
        self.calls = 0          # number of batches embedded
        self.embedded = []      # every text ever embedded (to assert caching)

    def __call__(self, texts):
        self.calls += 1
        out = []
        for t in texts:
            self.embedded.append(t)
            toks = t.lower().split()
            vocab = sorted(set(toks))
            vec = [1.0 if v in toks else 0.0 for v in _VOCAB]
            n = math.sqrt(sum(x * x for x in vec)) or 1.0
            out.append([x / n for x in vec])
        return out


# Fixed shared vocab so all vectors live in the same space.
_VOCAB = ["design", "architect", "a", "system", "api", "say", "hi", "hello",
          "fix", "the", "failing", "test", "why", "is", "this"]


def _clf():
    emb = StubEmbedder()
    return Classifier(emb, "stub-v1"), emb


def test_nearest_example_picks_semantically_closest_tier():
    clf, _ = _clf()
    examples = {
        "fast": ["say hi", "hello"],
        "smart": ["architect a system", "design api"],
    }
    tier, conf = clf.classify("design a system", examples)
    assert tier == "smart", (tier, conf)
    assert conf > 0.0


def test_empty_text_returns_zero():
    clf, _ = _clf()
    tier, conf = clf.classify("", {"fast": ["hi"]})
    assert (tier, conf) == ("", 0.0)


def test_no_examples_returns_zero():
    clf, _ = _clf()
    assert clf.classify("design a system", {}) == ("", 0.0)
    assert clf.classify("design a system", {"fast": [], "smart": [""]}) == ("", 0.0)


def test_examples_are_cached_across_calls():
    clf, emb = _clf()
    examples = {"fast": ["say hi"], "smart": ["design api"]}
    clf.classify("why is this", examples)
    n_examples_first = [e for e in emb.embedded if e in ("say hi", "design api")]
    clf.classify("fix the test", examples)  # same examples, new query
    n_examples_after = [e for e in emb.embedded if e in ("say hi", "design api")]
    # Each static example embedded exactly once total, despite two classify calls.
    assert sorted(n_examples_first) == sorted(n_examples_after) == ["design api", "say hi"]


def test_query_is_embedded_every_call_not_cached():
    clf, emb = _clf()
    examples = {"fast": ["say hi"]}
    clf.classify("why is this", examples)
    clf.classify("why is this", examples)  # identical query again
    # The dynamic query is re-embedded each call (it is never cached).
    assert emb.embedded.count("why is this") == 2


def test_confidence_clamped_non_negative():
    clf, _ = _clf()
    # Query shares no tokens with the example → cosine 0 (orthogonal), not negative.
    tier, conf = clf.classify("design", {"fast": ["say hi"]})
    assert conf >= 0.0

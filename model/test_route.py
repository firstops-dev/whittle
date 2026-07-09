"""Unit tests for the vSR routing-signal logic, with stubs so no torch or model
download is needed.

- StubEmbedder embeds text as an L2-normalized multi-hot over its word tokens, so
  token-overlapping strings have higher cosine — enough to exercise the cache and the
  text-level Scorer deterministically.
- The exact bank-score blend math is tested on hand-built vectors (not via the stub)
  so the arithmetic is pinned precisely.
- Domain selection is tested via an injected predict fn (no transformers).
"""
import math

from pytest import approx

from route import (Scorer, bank_score, complexity_margin, domain_distribution, domain_label,
                   embedding_score, DomainClassifier)


# Fixed shared vocab so all stub vectors live in the same space.
_VOCAB = ["design", "architect", "a", "system", "api", "say", "hi", "hello",
          "fix", "the", "failing", "test", "why", "is", "this"]


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
            vec = [1.0 if v in toks else 0.0 for v in _VOCAB]
            n = math.sqrt(sum(x * x for x in vec)) or 1.0
            out.append([x / n for x in vec])
        return out


def _scorer():
    emb = StubEmbedder()
    return Scorer(emb, "stub-v1"), emb


# ---- bank_score blend math (best/support with TopM) -------------------------

def test_bank_score_blends_best_and_top_m_support():
    q = [1.0, 0.0]
    # cosines (dot, since normalized) are 1.0, 0.8, 0.0.
    cands = [[1.0, 0.0], [0.8, 0.6], [0.0, 1.0]]
    # sorted desc = [1.0, 0.8, 0.0]; best=1.0; support=mean(top2)=0.9;
    # score = 0.75*1.0 + 0.25*0.9 = 0.975.
    assert bank_score(q, cands) == approx(0.975)


def test_bank_score_single_candidate_equals_its_cosine():
    # Only one candidate → best == support → the blend collapses to the raw cosine.
    assert bank_score([1.0, 0.0], [[0.6, 0.8]]) == approx(0.6)


def test_bank_score_top_m_larger_than_list_uses_all():
    q = [1.0, 0.0]
    cands = [[1.0, 0.0], [0.0, 1.0]]  # cosines 1.0, 0.0
    # top_m=5 but only 2 candidates: support = mean(1.0, 0.0) = 0.5;
    # score = 0.75*1.0 + 0.25*0.5 = 0.875.
    assert bank_score(q, cands, top_m=5) == approx(0.875)


def test_bank_score_empty_candidates_is_zero():
    assert bank_score([1.0, 0.0], []) == 0.0


def test_bank_score_respects_best_weight():
    q = [1.0, 0.0]
    cands = [[1.0, 0.0], [0.0, 1.0]]  # best 1.0, support mean(1.0,0.0)=0.5
    # best_weight=1.0 → pure best; best_weight=0.0 → pure support.
    assert bank_score(q, cands, best_weight=1.0) == approx(1.0)
    assert bank_score(q, cands, best_weight=0.0) == approx(0.5)


# ---- embedding_score == bank_score ------------------------------------------

def test_embedding_score_is_bank_score():
    q = [1.0, 0.0]
    cands = [[1.0, 0.0], [0.8, 0.6], [0.0, 1.0]]
    assert embedding_score(q, cands) == approx(bank_score(q, cands))


# ---- complexity_margin sign -------------------------------------------------

def test_complexity_margin_hard_leaning_is_positive():
    q = [1.0, 0.0]
    hard = [[1.0, 0.0]]   # cosine 1.0
    easy = [[0.0, 1.0]]   # cosine 0.0
    assert complexity_margin(q, hard, easy) == approx(1.0)


def test_complexity_margin_easy_leaning_is_negative():
    q = [1.0, 0.0]
    hard = [[0.0, 1.0]]   # cosine 0.0
    easy = [[1.0, 0.0]]   # cosine 1.0
    assert complexity_margin(q, hard, easy) == approx(-1.0)


def test_complexity_margin_empty_banks_is_zero():
    assert complexity_margin([1.0, 0.0], [], []) == 0.0


# ---- domain argmax ----------------------------------------------------------

def test_domain_label_picks_argmax_with_softmax_confidence():
    labels = ["biology", "law", "math"]
    label, conf = domain_label([0.1, 3.0, 0.2], labels)
    assert label == "law"
    # Confidence is the softmax prob of the argmax class: in (0, 1) and the max.
    exps = [math.exp(x) for x in [0.1, 3.0, 0.2]]
    assert conf == approx(exps[1] / sum(exps))
    assert 0.0 < conf < 1.0


def test_domain_label_empty_logits_is_zero():
    assert domain_label([], []) == ("", 0.0)


def test_domain_classifier_uses_injected_predict():
    # Inject a predict fn so no transformers/torch is needed; classify wires argmax.
    predict = lambda text: ([0.0, 5.0], ["easy_domain", "hard_domain"])
    clf = DomainClassifier(predict=predict)
    label, conf, probs = clf.classify("anything")
    assert label == "hard_domain"
    assert conf > 0.9


def test_domain_classifier_empty_text_short_circuits():
    called = []
    clf = DomainClassifier(predict=lambda t: called.append(t) or ([1.0], ["x"]))
    assert clf.classify("") == ("", 0.0, {})
    assert called == []  # model is never consulted for empty text


# ---- cache behavior: candidates once, query fresh ---------------------------

def test_candidates_embedded_once_across_calls():
    scorer, emb = _scorer()
    cands = ["design api", "architect a system"]
    scorer.score_embedding("why is this", cands)
    scorer.score_embedding("fix the test", cands)  # same candidates, new query
    for c in cands:
        assert emb.embedded.count(c) == 1, (c, emb.embedded)


def test_query_embedded_every_call_not_cached():
    scorer, emb = _scorer()
    scorer.score_embedding("why is this", ["design api"])
    scorer.score_embedding("why is this", ["design api"])  # identical query again
    assert emb.embedded.count("why is this") == 2


def test_duplicate_candidate_embedded_once():
    scorer, emb = _scorer()
    scorer.score_embedding("why is this", ["say hi", "say hi", "say hi"])
    assert emb.embedded.count("say hi") == 1


def test_scorer_embedding_matches_pure_function():
    # The text-level Scorer must produce the same number as the pure vector path.
    emb = StubEmbedder()
    scorer = Scorer(emb, "stub-v1")
    q = emb(["design a system"])[0]
    cvecs = emb(["design api", "architect a system"])
    assert scorer.score_embedding("design a system",
                                  ["design api", "architect a system"]) == approx(
        embedding_score(q, cvecs))


def test_scorer_complexity_sign_via_token_overlap():
    scorer, _ = _scorer()
    # Query shares tokens with the hard bank, none with the easy bank → positive.
    margin = scorer.score_complexity(
        "architect a system", hard=["architect a system"], easy=["say hi"])
    assert margin > 0.0


# ---- degenerate inputs ------------------------------------------------------

def test_embedding_empty_text_or_candidates_is_zero():
    scorer, emb = _scorer()
    assert scorer.score_embedding("", ["design api"]) == 0.0
    assert scorer.score_embedding("design api", []) == 0.0
    assert scorer.score_embedding("design api", ["", ""]) == 0.0  # empties filtered
    assert emb.embedded == []  # nothing ever reached the model


def test_complexity_empty_text_or_banks_is_zero():
    scorer, _ = _scorer()
    assert scorer.score_complexity("", ["hard"], ["easy"]) == 0.0
    assert scorer.score_complexity("design a system", [], []) == 0.0


def test_complexity_one_sided_bank_still_scores():
    scorer, _ = _scorer()
    # Only an easy bank present → hard bank scores 0.0 → margin is negative.
    margin = scorer.score_complexity("say hi", hard=[], easy=["say hi"])
    assert margin < 0.0


def test_domain_distribution_sums_to_one_and_matches_argmax():
    labels = ["biology", "law", "math"]
    probs = domain_distribution([0.1, 3.0, 0.2], labels)
    assert abs(sum(probs.values()) - 1.0) < 1e-9
    label, conf = domain_label([0.1, 3.0, 0.2], labels)
    assert max(probs, key=probs.get) == label == "law"
    assert probs["law"] == conf


def test_domain_distribution_empty():
    assert domain_distribution([], []) == {}


def test_classify_returns_full_distribution():
    predict = lambda text: ([0.0, 1.0, 2.0], ["a", "b", "c"])
    clf = DomainClassifier(predict=predict)
    label, conf, probs = clf.classify("x")
    assert label == "c" and set(probs) == {"a", "b", "c"}
    assert abs(sum(probs.values()) - 1.0) < 1e-9

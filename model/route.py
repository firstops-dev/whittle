"""Router smart-mode: vLLM Semantic Router signal computation.

The router (Go daemon) asks this sidecar for three routing signals, each computed by
one of vSR's two mmBERT models — hosted here, where the models live (compute where
the data lives). The Go client owns the thresholds; we only return raw scores.

  POST /v1/route/domain      -> MMLU-Pro domain label + softmax confidence, from the
                                14-class ModernBERT intent classifier.
  POST /v1/route/embedding   -> bank score of the query against a candidate list.
  POST /v1/route/complexity  -> margin between the query's bank score over a "hard"
                                bank and its bank score over an "easy" bank.

Both embedding signals reuse vSR's exact bank-score blend (0.75*best + 0.25*support,
support = mean of the top-2 cosines) — replicated, not invented. The engine applies
the >=threshold / margin tests; a non-200 makes the Go client fail open.

The candidate/bank lists are the caller's policy (static per policy version), so their
embeddings are cached by content hash + model version. The query text is dynamic and
embedded fresh each call. Both the embedder and the classifier are injected so all
scoring logic is unit-testable without torch; the real models are lazy-loaded on first
use (like the compressor) and provisioned at `whittle setup`.
"""
import hashlib
import math
import threading
from typing import Callable, Dict, List, Optional, Sequence, Tuple

# EmbedFn maps a batch of strings to a list of L2-NORMALIZED vectors (so cosine
# similarity is a plain dot product). One row per input, order-aligned.
EmbedFn = Callable[[Sequence[str]], List[List[float]]]

# PredictFn maps a query to (logits, labels): the raw class logits and the label
# strings (order-aligned) from the model config. Injected so domain selection is
# testable without torch.
PredictFn = Callable[[str], Tuple[Sequence[float], Sequence[str]]]

# vSR's two mmBERT models, loaded by HuggingFace id so `whittle setup` fetches them.
# Domain: ModernBertForSequenceClassification, 14 MMLU-Pro labels in its config
# id2label. Embed: sentence-transformers-compatible, 32k ctx, 768-dim 2D matryoshka.
DOMAIN_MODEL = "llm-semantic-router/mmbert32k-intent-classifier-merged"
EMBED_MODEL = "llm-semantic-router/mmbert-embed-32k-2d-matryoshka"

# vSR bank-score blend constants (verified from upstream). The single best cosine
# carries 0.75; the top-M support mean carries the rest, with M = 2 (or the list
# length when shorter).
BEST_WEIGHT = 0.75
TOP_M = 2


# ---- pure scoring (vector in, score out — no torch, no model) ---------------

def _dot(a: Sequence[float], b: Sequence[float]) -> float:
    return sum(x * y for x, y in zip(a, b))


def bank_score(query_vec: Sequence[float], cand_vecs: Sequence[Sequence[float]],
               best_weight: float = BEST_WEIGHT, top_m: int = TOP_M) -> float:
    """vSR bank score: blend the single best cosine with the mean of the top-M.

    Vectors are L2-normalized, so cosine is a plain dot product. Empty bank -> 0.0
    (never crash; the Go client reads a below-threshold score).

    NOTE: vSR optionally medoid-clusters the candidates (cosine>=0.9, cap 8) before
    scoring to down-weight near-duplicates. Skipped for v1 — the best/support blend
    is the load-bearing part and is replicated exactly here; clustering can layer in
    later without changing this contract.
    """
    if not cand_vecs:
        return 0.0
    sims = sorted((_dot(query_vec, c) for c in cand_vecs), reverse=True)
    best = sims[0]
    m = min(top_m, len(sims)) or len(sims)
    support = sum(sims[:m]) / m
    return best_weight * best + (1.0 - best_weight) * support


def embedding_score(query_vec: Sequence[float], cand_vecs: Sequence[Sequence[float]],
                    best_weight: float = BEST_WEIGHT, top_m: int = TOP_M) -> float:
    """Embedding signal = the query's bank score over the candidate list. The engine
    applies the >=threshold test; we only return the score."""
    return bank_score(query_vec, cand_vecs, best_weight, top_m)


def complexity_margin(query_vec: Sequence[float],
                      hard_vecs: Sequence[Sequence[float]],
                      easy_vecs: Sequence[Sequence[float]],
                      best_weight: float = BEST_WEIGHT, top_m: int = TOP_M) -> float:
    """Complexity signal = bank_score(query, hard) - bank_score(query, easy). Positive
    -> hard-leaning, negative -> easy-leaning. The engine maps the margin to
    hard/medium/easy against its own threshold; we only return the margin."""
    return (bank_score(query_vec, hard_vecs, best_weight, top_m)
            - bank_score(query_vec, easy_vecs, best_weight, top_m))


def domain_distribution(logits: Sequence[float], labels: Sequence[str]) -> Dict[str, float]:
    """Full softmax distribution as {label: prob}. Pure (no torch). The router
    thresholds PROBABILITY MASS over a policy-defined category set — a single
    scalar that subsumes entropy laddering: mass concentrates only when the
    classifier is confidently in-set, and an ambiguous/flat distribution simply
    fails the threshold (fail to the safe middle tier, cost-first). Empty -> {}."""
    if not logits:
        return {}
    hi = max(logits)
    exps = [math.exp(x - hi) for x in logits]  # shift for numerical stability
    total = sum(exps)
    return {labels[i]: exps[i] / total for i in range(len(logits))}


def domain_label(logits: Sequence[float], labels: Sequence[str]) -> Tuple[str, float]:
    """argmax label + its softmax probability. Empty logits -> ("", 0.0)."""
    probs = domain_distribution(logits, labels)
    if not probs:
        return "", 0.0
    label = max(probs, key=probs.get)
    return label, probs[label]


# ---- lazy real models (injected in tests) -----------------------------------

class Embedder:
    """Lazy, thread-safe wrapper over the sentence-transformers embedding model. The
    heavy import and model load happen on first call, not at construction, so importing
    this module never requires torch to be present (fail-open until actually used)."""

    def __init__(self, model_name: str = EMBED_MODEL):
        self.model_name = model_name
        self._model = None
        self._lock = threading.Lock()

    def version(self) -> str:
        return self.model_name

    def __call__(self, texts: Sequence[str]) -> List[List[float]]:
        if self._model is None:
            with self._lock:
                if self._model is None:
                    from sentence_transformers import SentenceTransformer

                    self._model = SentenceTransformer(self.model_name)
        return self._model.encode(
            list(texts), normalize_embeddings=True, convert_to_numpy=True
        ).tolist()


class DomainClassifier:
    """vSR's mmBERT intent classifier (14-class ModernBertForSequenceClassification):
    classify(text) -> (argmax MMLU-Pro label, softmax confidence). The label set comes
    from the model config's id2label — never hardcoded. Lazy: the transformers model
    loads on the first classify, so importing this module never needs torch. Inject
    `predict` (text -> (logits, labels)) to replace the model in tests."""

    def __init__(self, model_name: str = DOMAIN_MODEL, predict: Optional[PredictFn] = None):
        self.model_name = model_name
        self._predict = predict
        self._model = None
        self._tokenizer = None
        self._lock = threading.Lock()

    def version(self) -> str:
        return self.model_name

    def _predict_real(self, text: str) -> Tuple[List[float], List[str]]:
        if self._model is None:
            with self._lock:                      # double-checked: avoid double-load
                if self._model is None:
                    from transformers import (
                        AutoModelForSequenceClassification, AutoTokenizer)

                    self._tokenizer = AutoTokenizer.from_pretrained(self.model_name)
                    model = AutoModelForSequenceClassification.from_pretrained(self.model_name)
                    model.eval()
                    self._model = model
        import torch

        with torch.no_grad():
            enc = self._tokenizer(text, return_tensors="pt", truncation=True)
            logits = self._model(**enc).logits[0]
        id2label = self._model.config.id2label
        # transformers normalizes id2label keys to int, but be tolerant of str keys.
        labels = [id2label.get(i, id2label.get(str(i), "")) for i in range(logits.shape[-1])]
        return logits.tolist(), labels

    def classify(self, text: str) -> Tuple[str, float, Dict[str, float]]:
        """(argmax label, its prob, the FULL distribution). The distribution is the
        payload — the router thresholds mass over category sets; argmax is kept for
        logging/back-compat."""
        if not text:
            return "", 0.0, {}
        predict = self._predict or self._predict_real
        logits, labels = predict(text)
        label, conf = domain_label(logits, labels)
        return label, conf, domain_distribution(logits, labels)


# ---- embedding-signal engine (embed + cache + score) ------------------------

class Scorer:
    """Computes the two embedding-based signals from text. Owns the embedder and a
    per-candidate embedding cache (content hash + model version): the caller's bank
    lists are static per policy version, so they are embedded once; the query text is
    dynamic and embedded fresh each call."""

    def __init__(self, embed: EmbedFn, version: str):
        self._embed = embed
        self._version = version
        self._cache: Dict[str, List[float]] = {}
        self._lock = threading.Lock()

    def _key(self, text: str) -> str:
        h = hashlib.sha256()
        h.update(self._version.encode("utf-8"))
        h.update(b"\x00")  # domain-separate version from text
        h.update(text.encode("utf-8"))
        return h.hexdigest()

    def _embed_cached(self, texts: List[str]) -> List[List[float]]:
        """Embed with the cache: only genuinely-unseen candidate texts hit the model."""
        if not texts:
            return []
        uncached = list(dict.fromkeys(t for t in texts if self._key(t) not in self._cache))
        if uncached:
            vecs = self._embed(uncached)
            with self._lock:
                for t, v in zip(uncached, vecs):
                    self._cache[self._key(t)] = v
        return [self._cache[self._key(t)] for t in texts]

    def score_embedding(self, text: str, candidates: List[str]) -> float:
        cands = [c for c in candidates if c]
        if not text or not cands:
            return 0.0
        qvec = self._embed([text])[0]  # dynamic -> never cached
        return embedding_score(qvec, self._embed_cached(cands))

    def score_complexity(self, text: str, hard: List[str], easy: List[str]) -> float:
        h = [c for c in hard if c]
        e = [c for c in easy if c]
        if not text or (not h and not e):
            return 0.0
        qvec = self._embed([text])[0]  # dynamic -> never cached
        return complexity_margin(qvec, self._embed_cached(h), self._embed_cached(e))

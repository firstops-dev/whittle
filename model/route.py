"""Router smart-mode: few-shot embedding classify.

The router (Go daemon) calls POST /v1/route/classify with the request text and the
policy's per-tier examples. We embed the text and the examples with a sentence
model, take the NEAREST example per tier (cosine), and return the best tier plus
that similarity as confidence. The router then accepts it only if confidence clears
the policy's min_confidence, else falls to the static default.

Why the examples ride on every call: they are the user's policy, static per policy
version, so we cache their embeddings by content hash + model version here — where
the model lives (compute where the data lives). The query text is dynamic, embedded
fresh each call. Re-embedding the static examples is thus amortized to once.

Fail-open is the caller's job: any exception propagates, and the Go client turns a
non-200 into a fall-through to the default. We never fabricate a tier — an empty
text or empty examples returns ("", 0.0), which the router reads as low-confidence.

The embedder is injected so the selection + cache logic is testable without torch;
the real sentence model is lazy-loaded on first use (like the compressor's model)
and provisioned at `whittle setup`.
"""
import hashlib
import threading
from typing import Callable, Dict, List, Sequence, Tuple

# EmbedFn maps a batch of strings to a list of L2-NORMALIZED vectors (so cosine
# similarity is a plain dot product). One row per input, order-aligned.
EmbedFn = Callable[[Sequence[str]], List[List[float]]]

# all-MiniLM-L6-v2: 384-dim, ~90MB, strong few-shot semantic similarity, fast on
# CPU. It runs on the sidecar's existing torch stack (llmlingua already pulls
# torch), so smart mode adds one model download, not a new runtime.
MODEL_NAME = "sentence-transformers/all-MiniLM-L6-v2"


class Embedder:
    """Lazy, thread-safe wrapper over a sentence-transformers model. The heavy
    import and model load happen on first call, not at construction, so importing
    this module never requires torch to be present (fail-open until actually used)."""

    def __init__(self, model_name: str = MODEL_NAME):
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


class Classifier:
    """Few-shot nearest-example tier selector with a per-example embedding cache."""

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

    def _embed_examples(self, texts: List[str]) -> List[List[float]]:
        """Embed with the cache: only genuinely-unseen example texts hit the model."""
        uncached = list(dict.fromkeys(t for t in texts if self._key(t) not in self._cache))
        if uncached:
            vecs = self._embed(uncached)
            with self._lock:
                for t, v in zip(uncached, vecs):
                    self._cache[self._key(t)] = v
        return [self._cache[self._key(t)] for t in texts]

    def classify(self, text: str, examples: Dict[str, List[str]]) -> Tuple[str, float]:
        tiers = [(t, [e for e in exs if e]) for t, exs in examples.items()]
        tiers = [(t, exs) for t, exs in tiers if exs]
        if not text or not tiers:
            return "", 0.0

        qvec = self._embed([text])[0]  # dynamic → never cached
        best_tier, best_sim = "", -1.0
        for tier, exs in tiers:
            sim = max(_dot(qvec, ev) for ev in self._embed_examples(exs))
            if sim > best_sim:
                best_tier, best_sim = tier, sim
        # Normalized vectors give cosine ∈ [-1, 1]; clamp to a [0, 1] confidence.
        return best_tier, max(0.0, best_sim)


def _dot(a: Sequence[float], b: Sequence[float]) -> float:
    return sum(x * y for x, y in zip(a, b))

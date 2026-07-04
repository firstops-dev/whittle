"""Prompt Compression Service — productionized Exp3 baseline.

Stateless HTTP API: metadata-first gate -> LLMLingua-2 (extractive) -> expansion
guardrail. Extractive means the model can only delete tokens, so it cannot hijack
(execute embedded instructions) or hallucinate (invent content); the gate protects
code/structured content it would otherwise corrupt; the guardrail guarantees output
is never longer than input. Returns the original unchanged on any skip or failure
(fail-open: the service must never break the caller's task).
"""
import logging
import os
import threading
import time
from typing import Optional

import tiktoken
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel, Field

import gate
import fidelity

VERSION = "1.0.3"
MODEL_NAME = os.environ.get("COMPRESSOR_MODEL", "microsoft/llmlingua-2-xlm-roberta-large-meetingbank")
DEFAULT_RATE = {"prose": float(os.environ.get("RATE_PROSE", "0.6"))}
MAX_CHARS = int(os.environ.get("MAX_CHARS", "60000"))
FORCE_TOKENS = ["\n", ".", "?", "!", ",", ":", fidelity.SENTINEL, fidelity.MD_BLOCK_SENTINEL] + fidelity.NEGATIONS

ENC = tiktoken.get_encoding("o200k_base")
def ntok(s: str) -> int:
    return len(ENC.encode(s, disallowed_special=())) if s else 0

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s %(message)s")
log = logging.getLogger("compressor")

# Pin intra-op threads to the allocated vCPUs. Without this, torch/OpenMP default to
# the HOST's core count on a cgroup-limited container and thrash at the sync barrier,
# producing a large FIXED per-request overhead (measured ~12s on a high-core Spot host,
# independent of input size). OMP_NUM_THREADS is also set in the task def env so the
# native libs read it before import; this call covers torch's own intra-op pool.
import torch  # noqa: E402
_NUM_THREADS = max(1, int(os.environ.get("OMP_NUM_THREADS", "1") or "1"))
torch.set_num_threads(_NUM_THREADS)

app = FastAPI(title="FirstOps Prompt Compressor", version=VERSION)
_compressor = None
_lock = threading.Lock()

# Bound concurrent inference. Each call grabs all OMP threads, so 2+ in flight
# oversubscribe the vCPUs (every call then crosses the client's timeout) and the
# memory of stacked inferences can get the process OOM-killed (SIGKILL, no traceback)
# — which is what silently killed the sidecar under the comparison-run load. Excess
# load sheds fast as a clean "busy" skip (fail-open). MAX_CONCURRENCY=1 serializes at
# full thread width; raise only with more vCPUs.
_INFER_SEM = threading.Semaphore(max(1, int(os.environ.get("MAX_CONCURRENCY", "1") or "1")))
_INFER_WAIT_S = float(os.environ.get("INFER_WAIT_S", "0.25") or "0.25")


class _FidelitySkip(Exception):
    """Control flow: fidelity pre/post pass declined; fail open."""


INT8 = os.environ.get("COMPRESSOR_INT8", "").lower() in ("1", "true", "yes")


# Device selection, overridable via COMPRESSOR_DEVICE. Auto: CUDA (GPU host) >
# MPS (Apple Silicon, local experiments only) > CPU. Note MPS is a DIRECTIONAL
# probe — different silicon than the deployed T4, so its latency does not
# transfer to production.
def _pick_device():
    want = os.environ.get("COMPRESSOR_DEVICE", "").strip()
    if want:
        return want
    if torch.cuda.is_available():
        return "cuda"
    if getattr(torch.backends, "mps", None) is not None and torch.backends.mps.is_available():
        return "mps"
    return "cpu"


_DEVICE = _pick_device()


def get_compressor():
    global _compressor
    if _compressor is None:
        with _lock:                       # double-checked lock: avoid concurrent double-load -> OOM
            if _compressor is None:
                from llmlingua import PromptCompressor
                pc = PromptCompressor(model_name=MODEL_NAME, use_llmlingua2=True, device_map=_DEVICE)
                if _DEVICE.startswith("cuda"):
                    # GPU: half precision. ~2x the throughput of fp32 on the T4's
                    # tensor cores and the 560M model fits 16GB many times over.
                    pc.model = pc.model.half()
                    log.info("model on GPU (fp16); device=%s cuda=%s", _DEVICE, torch.cuda.get_device_name(0))
                elif _DEVICE == "mps":
                    # Apple GPU: fp32 (MPS lacks the fbgemm INT8 path and fp16 is
                    # flaky on some ops). llmlingua moves inputs to model.device,
                    # so device_map="mps" is enough; no quant.
                    log.info("model on Apple GPU (mps, fp32)")
                elif INT8:
                    # Dynamic INT8 quantization of the Linear layers. Validated quality-safe
                    # vs fp32 on our eval harness (D2/tool-output catastrophic stays 0%). Speedup
                    # is x86/fbgemm-only (slower under Apple qnnpack) — measured on x86 containers.
                    pc.model = torch.quantization.quantize_dynamic(pc.model, {torch.nn.Linear}, dtype=torch.qint8)
                    log.info("applied INT8 dynamic quantization (nn.Linear); engine=%s",
                             torch.backends.quantized.engine)
                _compressor = pc
    return _compressor


@app.on_event("startup")
def _warm_model():
    # Load eagerly so /health is not "ok" before the model is ready (readiness),
    # then run one inference to prime the CPU kernels — the first real call is
    # otherwise materially slower (kernel/oneDNN JIT, allocator warm-up).
    try:
        pc = get_compressor()
        pc.compress_prompt("warm up the inference kernels " * 40, rate=0.5, force_tokens=FORCE_TOKENS)
        log.info("model loaded + warmed at startup: %s (intra-op threads=%d)", MODEL_NAME, _NUM_THREADS)
    except Exception:
        log.exception("startup warmup failed")


class CompressRequest(BaseModel):
    content: str = Field(..., description="Text to compress (tool output or prompt)")
    tool_name: Optional[str] = Field(None, description="Tool that produced content (gating signal)")
    content_type: Optional[str] = Field(None, description="MIME of content (gating signal)")
    content_class: Optional[str] = Field(None, description="Override: 'prose' | 'code_structured'")
    intent: Optional[str] = Field(None, description="Optional user intent (context only)")
    rate: Optional[float] = Field(None, description="Target keep-rate (clamped to 0.1-1.0); default 0.6")
    min_tokens: int = Field(64, description="Skip if input shorter than this (clamped >= 0)")
    # NOTE: rate/min_tokens intentionally unconstrained so out-of-range values are clamped,
    # not rejected with 422 (a 422 would violate the never-break-the-caller contract).


class GateInfo(BaseModel):
    klass: str
    signal: str


class CompressResponse(BaseModel):
    compressed: str
    original_tokens: int
    compressed_tokens: int
    reduction: float
    action: str            # "compressed" | "skipped"
    skip_reason: Optional[str]
    gate: GateInfo
    latency_ms: int
    model: str
    version: str


@app.get("/health")
def health():
    # 503 until the model is loaded so the Docker HEALTHCHECK + ECS healthCheck
    # gate readiness; 200 only once /v1/compress can actually serve.
    if _compressor is None:
        raise HTTPException(status_code=503, detail="model not loaded")
    return {"status": "ok", "model_loaded": True, "version": VERSION}


@app.get("/v1/info")
def info():
    return {"service": "firstops-prompt-compressor", "version": VERSION, "model": MODEL_NAME,
            "method": "extractive (LLMLingua-2) + metadata-first gate + expansion guardrail",
            "default_rate": DEFAULT_RATE["prose"], "max_chars": MAX_CHARS,
            "int8": INT8, "intra_op_threads": _NUM_THREADS}


@app.post("/v1/compress", response_model=CompressResponse)
def compress(req: CompressRequest):
    t0 = time.time()
    content = req.content or ""
    ot = ntok(content)
    min_tokens = max(req.min_tokens, 0)
    action, klass, signal, reason = gate.decide(
        content, ot, req.tool_name, req.content_type, req.content_class, min_tokens)

    compressed, ct, skip_reason = content, ot, reason
    # Never compress only part of an oversized input: doing so would silently drop the
    # tail and report success. Skip (passthrough the full original) instead.
    if action == "compress" and len(content) > MAX_CHARS:
        action, skip_reason = "skip", "too_large"
    if action == "compress":
        # Shed load fast rather than oversubscribe the vCPUs / stack inference memory.
        # Fidelity pre-pass BEFORE the semaphore: pure CPU, and a guaranteed
        # identifier_dense skip must not occupy the single inference slot.
        masked, spans, prot_ratio = fidelity.protect(content)
        if prot_ratio > 0.60:
            action, skip_reason = "skip", "identifier_dense"
        elif not _INFER_SEM.acquire(timeout=_INFER_WAIT_S):
            action, skip_reason = "skip", "busy"
        else:
            try:
                rate = min(max(req.rate if req.rate is not None else DEFAULT_RATE["prose"], 0.1), 1.0)
                res = get_compressor().compress_prompt(masked, rate=rate, force_tokens=FORCE_TOKENS)
                cand = res.get("compressed_prompt", "") if isinstance(res, dict) else str(res)
                # Fidelity post-pass: restore protected tokens, then enforce
                # whole-token deletion. Any anomaly -> fail open (passthrough).
                cand = fidelity.restore(cand, spans, masked)
                if cand is not None:
                    cand = fidelity.whole_tokens(content, cand)
                if cand is None:
                    action, skip_reason = "skip", "fidelity_guard"
                    raise _FidelitySkip()
                cct = ntok(cand)
                if cand.strip() and cct < ot:       # expansion guardrail (never longer)
                    compressed, ct = cand, cct
                else:
                    action, skip_reason = "skip", "guardrail_expansion"
            except _FidelitySkip:
                pass
            except Exception:                       # fail-open: never break the caller
                log.exception("compress failed (len=%d tool=%s ctype=%s)", len(content), req.tool_name, req.content_type)
                action, skip_reason = "skip", "error"
            finally:
                _INFER_SEM.release()

    lat = int((time.time() - t0) * 1000)
    log.info("action=%s reason=%s in=%d out=%d gate=%s/%s lat_ms=%d",
             "compressed" if action == "compress" else "skipped", skip_reason, ot, ct, klass, signal, lat)
    return CompressResponse(
        compressed=compressed, original_tokens=ot, compressed_tokens=ct,
        reduction=round((ot - ct) / ot, 4) if ot else 0.0,
        action="compressed" if action == "compress" else "skipped",
        skip_reason=skip_reason, gate=GateInfo(klass=klass, signal=signal),
        latency_ms=lat, model=MODEL_NAME, version=VERSION)

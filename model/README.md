# whittle model sidecar (optional)

The ML prose path. Whittle's deterministic compressors need nothing; this
sidecar adds extractive prose compression (LLMLingua-2) with whittle's fidelity
guards (entity protection, whole-token deletion, negation preservation,
identifier-density skip). Everything fails open: if this service is down, slow,
or declines, whittle passes the original text through untouched.

## Run

    python -m venv .venv && .venv/bin/pip install -r requirements.txt
    .venv/bin/uvicorn app:app --port 45872

Then point whittle at it:

    export WHITTLE_MODEL_URL=http://127.0.0.1:45872

First start downloads the model (~1.6 GB, microsoft/llmlingua-2-xlm-roberta-large).
GPU: set COMPRESSOR_DEVICE=cuda (fp16) or mps (Apple). CPU works (set
OMP_NUM_THREADS to your core count; INT8 via COMPRESSOR_INT8=1 on x86).

## API

POST /v1/compress {"content": "...", "rate": 0.6} ->
{"compressed": "...", "action": "compressed"|"skipped", "skip_reason": ...}

GET /health returns 503 until the model is loaded (readiness-gate friendly).

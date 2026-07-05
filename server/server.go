// Package server is whittle's HTTP front door. It replaces the Python
// compressor's POST /v1/compress with a content-aware router + structural
// compressors (and delegates the prose path back to the Python service). The
// response shape matches the Python service closely enough that the edge-server
// caller — which reads only `compressed` + `action` — is unchanged.
package server

import (
	"path/filepath"

	"encoding/json"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/firstops-dev/whittle/compress"
	"github.com/firstops-dev/whittle/compress/compressors"
)

const (
	version = "0.2.1"
	model   = "whittle-router"
)

// ListenAndServe builds the pipeline from the environment and serves the HTTP
// API on addr (":45871" if empty). Env: WHITTLE_MODEL_URL (enables the ML prose
// path), WHITTLE_MAX_CHARS (global ceiling), WHITTLE_PROSE_MAX_CHARS (prose
// latency ceiling).
func ListenAndServe(addr string) error {
	gateCfg := compress.DefaultGateConfig()
	if v := os.Getenv("WHITTLE_PROSE_MAX_CHARS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			gateCfg.ProseMaxChars = n
			log.Printf("gate: WHITTLE_PROSE_MAX_CHARS override = %d", n)
		}
	}
	if v := os.Getenv("WHITTLE_MAX_CHARS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			gateCfg.MaxChars = n
			log.Printf("gate: WHITTLE_MAX_CHARS override = %d", n)
		}
	}
	p := compress.NewPipeline(
		compress.NewRegistry(compressors.DefaultChains()),
		gateCfg,
		log.Default(),
	)
	if addr == "" {
		addr = ":45871"
	}
	store, _ := OpenStore(storeDir(), 256<<20, 24*time.Hour) // nil on error: hints just don't emit
	srv := &http.Server{
		Addr:              addr,
		Handler:           NewMuxWithStore(p, store),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("whittle %s listening on %s", version, addr)
	return srv.ListenAndServe()
}

func NewMux(p *compress.Pipeline) http.Handler { return NewMuxWithStore(p, nil) }

func NewMuxWithStore(p *compress.Pipeline, store *Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/compress", compressHandler(p))
	mux.HandleFunc("/hook", hookHandler(p, store))
	mux.HandleFunc("/get", getHandler(store))
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/v1/info", infoHandler)
	return mux
}

// compressRequest mirrors the Python service body. Rate / MinTokens are pointers
// so an absent field falls back to defaults rather than to a zero override.
type compressRequest struct {
	Content      string   `json:"content"`
	ToolName     string   `json:"tool_name"`
	ContentType  string   `json:"content_type"`
	ContentClass string   `json:"content_class"`
	Intent       string   `json:"intent"`
	Rate         *float64 `json:"rate"`
	MinTokens    *int     `json:"min_tokens"`
}

type gateInfo struct {
	Klass  string `json:"klass"`
	Signal string `json:"signal"`
}

// compressResponse is a superset of the Python CompressResponse plus `strategy`.
// skip_reason is a pointer to serialize null on the compressed path (parity).
type compressResponse struct {
	Compressed       string   `json:"compressed"`
	OriginalTokens   int      `json:"original_tokens"`
	CompressedTokens int      `json:"compressed_tokens"`
	Reduction        float64  `json:"reduction"`
	Action           string   `json:"action"`
	SkipReason       *string  `json:"skip_reason"`
	Gate             gateInfo `json:"gate"`
	Detected         string   `json:"detected"`
	Strategy         string   `json:"strategy"`
	LatencyMs        int64    `json:"latency_ms"`
	Model            string   `json:"model"`
	Version          string   `json:"version"`
}

func compressHandler(p *compress.Pipeline) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		start := time.Now()

		var req compressRequest
		// Fail-open at the edge: any panic below writes the original content with
		// action=skipped rather than a 500, preserving the response contract.
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("textcompress: handler recovered panic: %v", rec)
				reason := "error"
				writeJSON(w, http.StatusOK, compressResponse{
					Compressed: req.Content,
					Action:     "skipped",
					SkipReason: &reason,
					Model:      model,
					Version:    version,
				})
			}
		}()

		// Bound the request body (~2x MaxChars): an oversized body is rejected by
		// the reader before it is fully buffered, not after.
		r.Body = http.MaxBytesReader(w, r.Body, int64(2*compress.DefaultMaxChars))
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}

		in := compress.Input{
			Content:      req.Content,
			ToolName:     req.ToolName,
			MIME:         req.ContentType,
			ContentClass: req.ContentClass,
			Rate:         0.6,
			MinTokens:    compress.DefaultMinTokens,
		}
		if req.Rate != nil {
			in.Rate = clampRate(*req.Rate)
		}
		if req.MinTokens != nil && *req.MinTokens >= 0 {
			in.MinTokens = *req.MinTokens
		}

		out := p.Compress(r.Context(), in)

		// Token counts via the calibrated estimator (MAE ~8% vs tiktoken o200k),
		// not chars/4 (which was off -27..-48% on structured content).
		ot := compress.EstimateTokens(req.Content)
		ct := compress.EstimateTokens(out.Output)
		reduction := 0.0
		if ot > 0 {
			reduction = round4(float64(ot-ct) / float64(ot))
		}
		var skip *string
		if out.SkipReason != "" {
			s := out.SkipReason
			skip = &s
		}

		writeJSON(w, http.StatusOK, compressResponse{
			Compressed:       out.Output,
			OriginalTokens:   ot,
			CompressedTokens: ct,
			Reduction:        reduction,
			Action:           out.Action,
			SkipReason:       skip,
			Gate:             gateInfo{Klass: out.GateKlass, Signal: out.GateSignal},
			Detected:         string(out.Detected),
			Strategy:         out.Strategy,
			LatencyMs:        time.Since(start).Milliseconds(),
			Model:            model,
			Version:          version,
		})
	}
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": version})
}

func infoHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"service": "whittle",
		"version": version,
		"model":   model,
		"method":  "content-aware router + structural compressors + prose delegation",
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func clampRate(r float64) float64 {
	if r < 0.1 {
		return 0.1
	}
	if r > 1.0 {
		return 1.0
	}
	return r
}

func round4(f float64) float64 { return math.Round(f*10000) / 10000 }

func storeDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".whittle", "cache")
}

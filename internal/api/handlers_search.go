package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"awesomeProject/internal/embed"
	"awesomeProject/internal/llm"
	"awesomeProject/internal/qdrant"
	"awesomeProject/internal/semantic"

	"github.com/danielgtaylor/huma/v2"
)

const (
	defaultModelFile  = "data/vectors/model.json"
	defaultEngineMeta = "data/vectors/engine.json"
	defaultQdrantURL  = "http://127.0.0.1:6333"
	defaultCollection = "undip_eprints"

	// defaultMinScore adalah floor skor cosine minimum untuk menampilkan
	// hit sebagai "relevan". Bila di-override lewat request field `min_score`,
	// nilainya dipakai. Tujuannya: quality > quantity — hit dengan skor di bawah
	// floor dianggap tidak relevan dan dibuang, frontend tidak menerima hasil
	// yang tidak bagus.
	defaultMinScore = 0.4
)

// engineMeta mirrors cli.engineMeta (kept local to avoid import cycle).
type engineMeta struct {
	Engine   string `json:"engine"`
	ModelDir string `json:"model_dir"`
	Dim      int    `json:"dim"`
}

func readEngineMeta(path string) (*engineMeta, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m engineMeta
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func payloadString(p map[string]any, key string) string {
	v, ok := p[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%g", t)
	case bool:
		return fmt.Sprintf("%v", t)
	case []any:
		parts := make([]string, 0, len(t))
		for _, e := range t {
			parts = append(parts, fmt.Sprintf("%v", e))
		}
		return strings.Join(parts, ", ")
	case []string:
		return strings.Join(t, ", ")
	default:
		return fmt.Sprintf("%v", t)
	}
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// ctxStatus mengubah pembatalan context menjadi status yang tepat, bukan 500:
// 499 bila klien sudah pergi (abort/timeout browser), 504 bila batas waktu
// server habis. ok=false berarti context masih hidup — error berasal dari
// kegagalan nyata di server.
func ctxStatus(ctx context.Context) (huma.StatusError, bool) {
	switch ctx.Err() {
	case context.Canceled:
		return huma.NewError(499, "client closed request"), true
	case context.DeadlineExceeded:
		return huma.Error504GatewayTimeout("request timeout"), true
	}
	return nil, false
}

func encodeQuery(ctx context.Context, engineFlag, modelDir, query, modelPath string) ([]float32, error) {
	// Cek lebih awal: request yang sudah dibatalkan tidak perlu memuat
	// sesi ONNX (~1,5 dtk) hanya untuk gagal di cek berikutnya.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	eng := strings.ToLower(strings.TrimSpace(engineFlag))
	if eng == "" || eng == "auto" {
		if meta, err := readEngineMeta(defaultEngineMeta); err == nil && meta != nil {
			if m := strings.ToLower(strings.TrimSpace(meta.Engine)); m != "" {
				eng = m
				if m == semantic.EncoderName && strings.TrimSpace(meta.ModelDir) != "" {
					modelDir = meta.ModelDir
				}
			}
		}
	}
	switch eng {
	case semantic.EncoderName:
		e, err := semantic.Open(modelDir)
		if err != nil {
			return nil, fmt.Errorf("muat encoder ONNX: %w", err)
		}
		vecs, err := e.Encode(ctx, []string{query})
		if err != nil {
			_ = e.Close()
			return nil, fmt.Errorf("encode query via ONNX: %w", err)
		}
		v := vecs[0]
		_ = e.Close()
		return v, nil
	case "tfidf":
		m, err := embed.LoadModel(modelPath)
		if err != nil {
			return nil, fmt.Errorf("muat model: %w", err)
		}
		return m.QueryVector(query), nil
	default:
		return nil, fmt.Errorf("engine tidak dikenal %q (pilih: tfidf | onnx | auto)", eng)
	}
}

func (s *Server) registerSearch() {
	huma.Register(s.api, huma.Operation{
		OperationID: "search",
		Method:      http.MethodPost,
		Path:        "/api/v1/search",
		Summary:     "Pencarian vektor (TF-IDF / ONNX)",
		Description: "Cari dokumen di Qdrant berdasarkan query teks. Engine dipilih otomatis dari engine.json (atau override).",
		Tags:        []string{"Search"},
	}, func(ctx context.Context, input *struct{ Body SearchRequest }) (*struct{ Body SearchResponse }, error) {
		req := input.Body
		if strings.TrimSpace(req.Query) == "" {
			return nil, huma.Error400BadRequest("query wajib diisi")
		}
		if req.N <= 0 {
			req.N = 10
		}
		modelDir := req.ModelDir
		if strings.TrimSpace(modelDir) == "" {
			modelDir = semantic.DefaultModelDir
		}
		vec, err := encodeQuery(ctx, req.Engine, modelDir, req.Query, defaultModelFile)
		if err != nil {
			if e, ok := ctxStatus(ctx); ok {
				return nil, e
			}
			return nil, huma.Error500InternalServerError(fmt.Sprintf("encode query: %v", err))
		}

		baseURL := envOr("QDRANT_URL", defaultQdrantURL)
		collection := defaultCollection
		client := qdrant.NewClient(baseURL, "")
		hits, err := client.Search(ctx, collection, vec, qdrant.SearchOptions{Limit: req.N, ScoreThreshold: 0})
		if err != nil {
			if e, ok := ctxStatus(ctx); ok {
				return nil, e
			}
			return nil, huma.Error502BadGateway(fmt.Sprintf("qdrant search: %v", err))
		}

		if strings.TrimSpace(req.Set) != "" {
			filtered := hits[:0]
			for _, h := range hits {
				if strings.Contains(payloadString(h.Payload, "sets"), req.Set) {
					filtered = append(filtered, h)
				}
			}
			hits = filtered
		}
		// Quality > quantity: pakai floor skor minimum (default bila tidak
		// di-set lewat `min_score`) lalu filtrasi. Hit di bawah floor dianggap
		// tidak relevan dan dibuang.
		minScore := req.MinScore
		if minScore <= 0 {
			minScore = defaultMinScore
		}
		if minScore > 0 {
			filtered := hits[:0]
			for _, h := range hits {
				if float64(h.Score) >= minScore {
					filtered = append(filtered, h)
				}
			}
			hits = filtered
		}

		outHits := make([]SearchHit, 0, len(hits))
		for _, h := range hits {
			outHits = append(outHits, SearchHit{
				Score:    h.Score,
				Title:    payloadString(h.Payload, "title"),
				Authors:  toStringSlice(h.Payload["authors"]),
				Year:     toInt(h.Payload["year"]),
				Subjects: toStringSlice(h.Payload["subjects"]),
				Types:    toStringSlice(h.Payload["types"]),
				URL:      payloadString(h.Payload, "url"),
				Payload:  h.Payload,
			})
		}

		resp := &struct{ Body SearchResponse }{Body: SearchResponse{
			Query: req.Query,
			Engine: func() string {
				if e := strings.TrimSpace(req.Engine); e != "" && e != "auto" {
					return e
				}
				if meta, err := readEngineMeta(defaultEngineMeta); err == nil && meta != nil {
					return meta.Engine
				}
				return "tfidf"
			}(),
			Hits:  outHits,
			Count: len(outHits),
		}}

		if req.Summarize && len(hits) > 0 {
			payloads := make([]map[string]any, 0, len(hits))
			for i := 0; i < len(hits) && i < llm.SummaryTopK; i++ {
				payloads = append(payloads, map[string]any{
					"title":    payloadString(hits[i].Payload, "title"),
					"abstract": payloadString(hits[i].Payload, "abstract"),
				})
			}
			summary, err := llm.SummarizePayloads(ctx, req.Query, payloads, envOr("GEMINI_MODEL", "gemini-2.5-flash"))
			if err != nil {
				resp.Body.Summary = "ringkasan AI tidak tersedia: " + err.Error()
			} else {
				resp.Body.Summary = summary
			}
		}
		if len(hits) == 0 {
			// Fallback: tidak ada hit relevan (di bawah floor / kosong).
			// Quality > quantity: jangan kembalikan hasil tidak relevan.
			resp.Body.Hits = []SearchHit{}
			if req.Summarize {
				resp.Body.Summary = fmt.Sprintf("Tidak ditemukan dokumen relevan untuk query %q (skor minimum: %s). Coba kata kunci yang lebih spesifik.", req.Query, strconv.FormatFloat(minScore, 'f', -1, 64))
			}
		}
		return resp, nil
	})
}

func toStringSlice(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			out = append(out, fmt.Sprintf("%v", e))
		}
		return out
	}
	return nil
}

func toInt(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	}
	return 0
}

package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"awesomeProject/internal/embed"
	"awesomeProject/internal/qdrant"
	"awesomeProject/internal/semantic"
	"os"
)

func CmdSearch(ctx context.Context, args []string) error {
	var query, modelPath, baseURL, collection string
	var limit int
	var minScore float64
	var showSet string
	var engine, modelDir string
	var summarize bool
	var llmModel string

	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Penggunaan: go run . search -q \"kata kunci\" [flags]")
		fs.PrintDefaults()
	}
	fs.StringVar(&query, "q", "", "query teks (wajib)")
	fs.StringVar(&modelPath, "model", DefaultModelFile, "file model TF-IDF")
	fs.StringVar(&baseURL, "url", EnvOr("QDRANT_URL", DefaultQdrantURL), "URL Qdrant (env: QDRANT_URL)")
	fs.StringVar(&collection, "collection", DefaultCollection, "nama collection")
	fs.IntVar(&limit, "n", 10, "jumlah hasil")
	fs.Float64Var(&minScore, "min-score", 0, "batas skor minimum (cosine)")
	fs.StringVar(&showSet, "set", "", "filter payload set (contains), contoh: \"Status = Published\"")
	fs.StringVar(&engine, "engine", "", "engine vektorisasi: tfidf | onnx | auto (default: dari engine.json / auto)")
	fs.StringVar(&modelDir, "model-dir", semantic.DefaultModelDir, "direktori model ONNX (untuk engine onnx)")
	fs.BoolVar(&summarize, "summarize", false, "tampilkan ringkasan AI (Gemini) dari hasil pencarian")
	fs.StringVar(&llmModel, "llm-model", EnvOr("GEMINI_MODEL", "gemini-2.5-flash"), "model Gemini untuk ringkasan (env: GEMINI_MODEL)")
	if err := fs.Parse(args); err != nil {
		return ErrUsage
	}
	if strings.TrimSpace(query) == "" {
		fmt.Fprintln(fs.Output(), "query -q wajib diisi")
		return ErrUsage
	}

	vec, err := encodeQueryViaEngine(ctx, engine, modelDir, query, modelPath)
	if err != nil {
		return err
	}

	client := qdrant.NewClient(baseURL, os.Getenv("QDRANT_API_KEY"))
	if err := client.Health(ctx); err != nil {
		return err
	}
	opt := qdrant.SearchOptions{Limit: limit, ScoreThreshold: float32(minScore)}
	if showSet != "" {
		opt.Filter = map[string]any{
			"must": []any{
				map[string]any{
					"key":   "sets",
					"match": map[string]any{"text": showSet},
				},
			},
		}
	}

	hits, err := client.Search(ctx, collection, vec, opt)
	if err != nil {
		return err
	}
	if len(hits) == 0 {
		Printf("tidak ada hasil untuk %q (periksa collection/indexing)", query)
		return nil
	}
	Printf("hasil untuk: %q (%d dari %s/%s)", query, len(hits), baseURL, collection)
	for i, h := range hits {
		fmt.Printf("\n%2d. [%.4f] %s\n", i+1, h.Score, payloadString(h.Payload, "title"))
		if a := payloadString(h.Payload, "authors"); a != "" {
			fmt.Printf("    penulis : %s\n", a)
		}
		meta := []string{}
		if y := payloadString(h.Payload, "year"); y != "" {
			meta = append(meta, "tahun "+y)
		}
		if t := payloadString(h.Payload, "types"); t != "" {
			meta = append(meta, t)
		}
		if s := payloadString(h.Payload, "sets"); s != "" {
			meta = append(meta, s)
		}
		if len(meta) > 0 {
			fmt.Printf("    meta    : %s\n", strings.Join(meta, " | "))
		}
		if s := payloadString(h.Payload, "subjects"); s != "" {
			fmt.Printf("    subjek  : %s\n", s)
		}
		if u := payloadString(h.Payload, "url"); u != "" {
			fmt.Printf("    url     : %s\n", u)
		}
	}
	if summarize {
		summary, err := SummarizeGemini(ctx, query, hits, llmModel)
		if err != nil {
			Printf("ringkasan AI tidak tersedia: %v", err)
			Printf("coba: go run . search -q \"%s\" tanpa -summarize", query)
			return nil
		}
		fmt.Printf("ringkasan AI\n%s\n", summary)
	}
	return nil
}

// encodeQueryViaEngine menghasilkan vektor query sesuai engine aktif:
// - "-engine onnx|auto" (atau engine.json menunjuk ONNX) -> encoder ONNX;
// - "-engine tfidf" -> model TF-IDF.
func encodeQueryViaEngine(ctx context.Context, engineFlag, modelDir, query, modelPath string) ([]float32, error) {
	eng := strings.ToLower(strings.TrimSpace(engineFlag))
	if eng == "" || eng == "auto" {
		if meta, err := readEngineMeta(DefaultEngineMeta); err == nil && meta != nil {
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

// PayloadString mengekspos payloadString untuk dipakai paket lain (mis. API).
func PayloadString(p map[string]any, key string) string { return payloadString(p, key) }
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
			parts = append(parts, payloadString(map[string]any{"v": e}, "v"))
		}
		return strings.Join(parts, ", ")
	case []string:
		return strings.Join(t, ", ")
	default:
		return fmt.Sprintf("%v", t)
	}
}

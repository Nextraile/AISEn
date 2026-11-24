// Package llm menyediakan ringkasan AI via Google Gemini REST API
// (stdlib only). Dipakai oleh CLI (search -summarize) dan REST API.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

const SummaryTopK = 5

var citationRe = regexp.MustCompile(`\[(\d+(?:\s*,\s*\d+)*)\]`)

// Summarize menghasilkan ringkasan singkat (bahasa Indonesia) dari hasil
// pencarian via Gemini API. Membutuhkan env GEMINI_API_KEY (atau .env).
func Summarize(ctx context.Context, query string, hits []map[string]any, llmModel string) (string, error) {
	// hits disederhanakan: caller mengirim slice payload (title/abstract).
	return summarize(ctx, query, hits, llmModel)
}

// SummarizeHits adalah varian yang menerima qdrant.Hit-like payload.
// Untuk menjaga decoupling, caller mengirim []map[string]any berisi
// {"title":..., "abstract":...}.
func SummarizePayloads(ctx context.Context, query string, payloads []map[string]any, llmModel string) (string, error) {
	return summarize(ctx, query, payloads, llmModel)
}

func summarize(ctx context.Context, query string, payloads []map[string]any, llmModel string) (string, error) {
	apiKey := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	if apiKey == "" {
		return "", fmt.Errorf("GEMINI_API_KEY belum diatur — set env var / .env")
	}
	if strings.TrimSpace(llmModel) == "" {
		llmModel = os.Getenv("GEMINI_MODEL")
	}
	if strings.TrimSpace(llmModel) == "" {
		llmModel = "gemini-2.5-flash"
	}

	srcs := make([]string, 0, len(payloads))
	valid := map[int]bool{}
	limit := len(payloads)
	if limit > SummaryTopK {
		limit = SummaryTopK
	}
	for i := 0; i < limit; i++ {
		n := i + 1
		valid[n] = true
		title := fmt.Sprintf("%v", payloads[i]["title"])
		abstract := fmt.Sprintf("%v", payloads[i]["abstract"])
		srcs = append(srcs, fmt.Sprintf("[%d] %s\n%s", n, title, abstract))
	}
	sources := ""
	if len(srcs) > 0 {
		sources = strings.Join(srcs, "\n\n")
	}
	prompt := "Anda adalah asisten riset. Tulis ringkasan singkat dalam bahasa Indonesia tentang isi, temuan utama, atau pola yang muncul dari jurnal yang disajikan. Gunakan HANYA sumber di bawah ini; jangan menambah fakta atau angka. Panjang maksimal 3 paragraf dan 180 kata. Utamakan poin paling relevan, hindari pengantar panjang dan jangan mengulang judul jurnal. Setiap klaim faktual wajib memiliki sitasi [n] sesuai nomor sumber. Tulis sitasi satu per satu, misalnya [1] [2] [3], jangan digabung menjadi satu kurung seperti [1, 2, 3]. Gunakan Markdown sederhana hanya untuk penekanan penting (**teks**) bila perlu. Jika sumber tidak cukup, katakan secara singkat.\n\nPertanyaan pengguna: " + query + "\n\nSumber jurnal:\n" + sources

	body, err := json.Marshal(map[string]any{
		"contents": []map[string]any{
			{"parts": []map[string]any{{"text": prompt}}},
		},
	})
	if err != nil {
		return "", err
	}
	endpoint := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent", url.PathEscape(llmModel))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"?key="+url.QueryEscape(apiKey), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("panggil Gemini: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("Gemini HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("parse respons Gemini: %w", err)
	}
	if len(out.Candidates) == 0 || len(out.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("Gemini tidak mengembalikan teks ringkasan")
	}
	summary := out.Candidates[0].Content.Parts[0].Text
	return sanitize(summary, valid), nil
}

func sanitize(text string, valid map[int]bool) string {
	return citationRe.ReplaceAllStringFunc(text, func(m string) string {
		inner := strings.TrimSpace(m[1 : len(m)-1])
		nums := []int{}
		for _, p := range strings.Split(inner, ",") {
			var n int
			if _, err := fmt.Sscanf(strings.TrimSpace(p), "%d", &n); err == nil && n > 0 {
				nums = append(nums, n)
			}
		}
		kept := make([]string, 0, len(nums))
		for _, n := range nums {
			if valid[n] {
				kept = append(kept, fmt.Sprintf("[%d]", n))
			}
		}
		return strings.Join(kept, " ")
	})
}

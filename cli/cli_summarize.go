package cli

import (
	"context"
	"fmt"
	"strings"

	"awesomeProject/internal/llm"
	"awesomeProject/internal/qdrant"
)

// SummarizeGemini adalah wrapper (backward-compat) di atas internal/llm.
// Kontrak sama dengan proyek legacy semantic-search-undip (backend.py).
func SummarizeGemini(ctx context.Context, query string, hits []qdrant.Hit, llmModel string) (string, error) {
	payloads := make([]map[string]any, 0, len(hits))
	for i := 0; i < len(hits) && i < llm.SummaryTopK; i++ {
		h := hits[i]
		payloads = append(payloads, map[string]any{
			"title":    payloadString(h.Payload, "title"),
			"abstract": payloadString(h.Payload, "abstract"),
		})
	}
	return llm.SummarizePayloads(ctx, query, payloads, llmModel)
}

// buildSummaryPrompt & sanitizeCitations tetap di internal/llm.
var _ = strings.TrimSpace
var _ = fmt.Sprintf

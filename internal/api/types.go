package api

import (
	"awesomeProject/internal/qdrant"
)

// SearchRequest adalah body POST /api/v1/search.
type SearchRequest struct {
	Query     string  `json:"query" doc:"Query teks (wajib)" minLength:"1"`
	N         int     `json:"n,omitempty" doc:"Jumlah hasil" default:"10"`
	MinScore  float64 `json:"min_score,omitempty" doc:"Batas skor minimum (cosine)" default:"0"`
	Set       string  `json:"set,omitempty" doc:"Filter payload set (contains)"`
	Engine    string  `json:"engine,omitempty" doc:"Engine: tfidf | onnx | auto (default auto)"`
	ModelDir  string  `json:"model_dir,omitempty" doc:"Direktori model ONNX"`
	Summarize bool    `json:"summarize,omitempty" doc:"Tambahkan ringkasan AI (Gemini)"`
}

// SearchHit adalah satu hasil pencarian.
type SearchHit struct {
	Score    float32        `json:"score"`
	Title    string         `json:"title"`
	Authors  []string       `json:"authors,omitempty"`
	Year     int            `json:"year,omitempty"`
	Subjects []string       `json:"subjects,omitempty"`
	Types    []string       `json:"types,omitempty"`
	URL      string         `json:"url,omitempty"`
	Payload  map[string]any `json:"payload,omitempty"`
}

// SearchResponse adalah body respons search.
type SearchResponse struct {
	Query   string      `json:"query"`
	Engine  string      `json:"engine"`
	Hits    []SearchHit `json:"hits"`
	Summary string      `json:"summary,omitempty"`
	Count   int         `json:"count"`
}

// HealthResponse adalah body /health.
type HealthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

// JobCreateRequest adalah body POST /api/v1/jobs.
type JobCreateRequest struct {
	Type string `json:"type" doc:"Jenis job: harvest | clean | normalize | vectorize | index | pipeline" enum:"harvest,clean,normalize,vectorize,index,pipeline"`
	// Params opsional per tipe (JSON bebas).
	Params map[string]any `json:"params,omitempty"`
}

// JobCreateResponse adalah respons pembuatan job.
type JobCreateResponse struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
}

// JobListResponse adalah respons GET /api/v1/jobs.
type JobListResponse struct {
	Jobs []*Job `json:"jobs"`
}

// JobGetResponse adalah respons GET /api/v1/jobs/{id}.
type JobGetResponse struct {
	Job *Job `json:"job"`
}

// alias qdrant hit (untuk handler)
var _ = qdrant.Hit{}

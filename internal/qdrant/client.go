// Package qdrant adalah klien REST minimal untuk Qdrant (tanpa dependensi eksternal).
// Mendukung: pembuatan collection, upsert point, pencarian vektor, dan info collection.
package qdrant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultURL adalah Qdrant lokal via Docker.
const DefaultURL = "http://127.0.0.1:6333"

// Distance metric yang dipakai tool ini.
const DistanceCosine = "Cosine"

// Client adalah klien Qdrant berbasis HTTP.
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// NewClient membuat klien dengan konfigurasi dasar.
func NewClient(baseURL, apiKey string) *Client {
	if baseURL == "" {
		baseURL = DefaultURL
	}
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}
}

// Point adalah satu vektor + payload siap upsert.
type Point struct {
	ID      uint64         `json:"id"`
	Vector  []float32      `json:"vector"`
	Payload map[string]any `json:"payload,omitempty"`
}

// Hit adalah hasil pencarian.
type Hit struct {
	ID      uint64         `json:"id"`
	Score   float32        `json:"score"`
	Payload map[string]any `json:"payload,omitempty"`
}

// apiResponse menampung envelope response Qdrant. Field status/error
// dibiarkan RawMessage karena format-nya berubah antar versi Qdrant:
//   - status: "ok" (string) ATAU {"code":404,"message":"..."} (objek)
//   - error : string ATAU objek {"message":"..."}
type apiResponse struct {
	Status json.RawMessage `json:"status"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
	Time   float64         `json:"time"`
}

// apiErrorMessage mengekstrak pesan error dari envelope response Qdrant.
func (a apiResponse) apiErrorMessage() string {
	var parts []string
	if len(a.Error) > 0 {
		var s string
		if err := json.Unmarshal(a.Error, &s); err == nil && s != "" {
			parts = append(parts, s)
		} else {
			var o struct {
				Message string `json:"message"`
				Reason  string `json:"reason"`
				Code    any    `json:"code"`
			}
			if err := json.Unmarshal(a.Error, &o); err == nil {
				if o.Message != "" {
					parts = append(parts, o.Message)
				}
				if o.Reason != "" {
					parts = append(parts, o.Reason)
				}
				if o.Code != nil {
					parts = append(parts, fmt.Sprintf("code=%v", o.Code))
				}
			}
		}
	}
	if len(a.Status) > 0 {
		var s string
		if err := json.Unmarshal(a.Status, &s); err == nil && s != "" && s != "ok" {
			parts = append(parts, "status="+s)
		} else {
			var o struct {
				Message string `json:"message"`
				Error   string `json:"error"`
				Code    any    `json:"code"`
			}
			if err := json.Unmarshal(a.Status, &o); err == nil {
				if o.Message != "" {
					parts = append(parts, o.Message)
				}
				if o.Error != "" {
					parts = append(parts, o.Error)
				}
				if o.Code != nil {
					parts = append(parts, fmt.Sprintf("code=%v", o.Code))
				}
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " | ")
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.APIKey != "" {
		req.Header.Set("api-key", c.APIKey)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("qdrant: %w (pastikan Qdrant berjalan di %s)", err, c.BaseURL)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}

	// Balaban bukan JSON (mis. proxy) -> laporkan apa adanya.
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		if resp.StatusCode >= 400 {
			return fmt.Errorf("qdrant: HTTP %d (body kosong) untuk %s %s", resp.StatusCode, method, path)
		}
		return nil
	}

	var ar apiResponse
	if err := json.Unmarshal(trimmed, &ar); err != nil {
		if resp.StatusCode >= 400 {
			return fmt.Errorf("qdrant: HTTP %d untuk %s %s: %s", resp.StatusCode, method, path, truncate(string(trimmed), 400))
		}
		// Bukan response envelope Qdrant (mis. /readyz) -> loloskan ke out.
		if out != nil {
			return json.Unmarshal(trimmed, out)
		}
		return nil
	}
	if msg := ar.apiErrorMessage(); msg != "" && resp.StatusCode >= 400 {
		return fmt.Errorf("qdrant: %s (HTTP %d)", msg, resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("qdrant: HTTP %d untuk %s %s: %s", resp.StatusCode, method, path, truncate(string(trimmed), 400))
	}
	if out != nil && len(ar.Result) > 0 {
		if err := json.Unmarshal(ar.Result, out); err != nil {
			return fmt.Errorf("qdrant: gagal decode result: %w", err)
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// Health memeriksa kesiapan Qdrant (GET /readyz, fallback ke /).
func (c *Client) Health(ctx context.Context) error {
	err := c.do(ctx, http.MethodGet, "/readyz", nil, nil)
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	// fallback: root endpoint
	var welcome map[string]any
	if err2 := c.do(ctx, http.MethodGet, "/", nil, &welcome); err2 != nil {
		return fmt.Errorf("qdrant tidak sehat: %v (fallback: %v)", err, err2)
	}
	return nil
}

// CollectionInfo berisi metadata collection.
type CollectionInfo struct {
	Name           string
	VectorSize     int
	VectorDistance string
	PointsCount    int64
	Status         string
}

// collectionInfoEnvelope adalah isi "result" pada GET /collections/{name}.
type collectionInfoEnvelope struct {
	Status              string `json:"status"`
	PointsCount         int64  `json:"points_count"`
	IndexedVectorsCount int64  `json:"indexed_vectors_count"`
	Config              struct {
		Params struct {
			Vectors struct {
				Size     int    `json:"size"`
				Distance string `json:"distance"`
			} `json:"vectors"`
		} `json:"params"`
	} `json:"config"`
}

// CollectionInfo mengambil info collection; error bila collection tidak ada.
func (c *Client) CollectionInfo(ctx context.Context, name string) (*CollectionInfo, error) {
	var env collectionInfoEnvelope
	path := "/collections/" + url.PathEscape(name)
	if err := c.do(ctx, http.MethodGet, path, nil, &env); err != nil {
		return nil, err
	}
	ci := &CollectionInfo{
		Name:           name,
		PointsCount:    env.PointsCount,
		Status:         env.Status,
		VectorSize:     env.Config.Params.Vectors.Size,
		VectorDistance: env.Config.Params.Vectors.Distance,
	}
	return ci, nil
}

// EnsureCollection membuat collection (bila belum ada) dengan skema dense
// Cosine berukuran dim. Bila sudah ada dengan dim berbeda -> error.
// Mengembalikan true bila collection baru dibuat.
func (c *Client) EnsureCollection(ctx context.Context, name string, dim int) (bool, error) {
	if name == "" {
		return false, fmt.Errorf("nama collection kosong")
	}
	if dim <= 0 {
		return false, fmt.Errorf("dim harus > 0")
	}
	existing, err := c.CollectionInfo(ctx, name)
	if err == nil {
		if existing.VectorSize != dim {
			return false, fmt.Errorf("collection %q punya dimensi %d, model saat ini %d "+
				"(hapus collection atau pakai model/dim yang sama)",
				name, existing.VectorSize, dim)
		}
		return false, nil
	}
	// Bukan "not found" -> error lain harus diteruskan.
	if !strings.Contains(strings.ToLower(err.Error()), "not found") &&
		!strings.Contains(strings.ToLower(err.Error()), "doesn't exist") &&
		!strings.Contains(strings.ToLower(err.Error()), "404") {
		return false, fmt.Errorf("gagal mengecek collection %q: %w", name, err)
	}

	body := map[string]any{
		"vectors": map[string]any{
			"size":     dim,
			"distance": DistanceCosine,
		},
		"on_disk_payload": false,
	}
	path := "/collections/" + url.PathEscape(name)
	if err := c.do(ctx, http.MethodPut, path, body, nil); err != nil {
		return false, fmt.Errorf("gagal membuat collection %q: %w", name, err)
	}
	return true, nil
}

// Upsert mengirim batch point dengan wait=true (idempotent berdasarkan ID).
func (c *Client) Upsert(ctx context.Context, name string, points []Point) error {
	if len(points) == 0 {
		return nil
	}
	body := map[string]any{"points": points}
	path := "/collections/" + url.PathEscape(name) + "/points?wait=true"
	return c.do(ctx, http.MethodPut, path, body, nil)
}

// SearchOptions opsi pencarian.
type SearchOptions struct {
	Limit          int
	ScoreThreshold float32
	Filter         map[string]any
}

// Search mencari tetangga terdekat dari vektor query.
func (c *Client) Search(ctx context.Context, name string, vector []float32, opt SearchOptions) ([]Hit, error) {
	if opt.Limit <= 0 {
		opt.Limit = 10
	}
	body := map[string]any{
		"vector":       vector,
		"limit":        opt.Limit,
		"with_payload": true,
		"with_vector":  false,
	}
	if opt.ScoreThreshold > 0 {
		body["score_threshold"] = opt.ScoreThreshold
	}
	if opt.Filter != nil {
		body["filter"] = opt.Filter
	}
	path := "/collections/" + url.PathEscape(name) + "/points/search"
	var hits []Hit
	if err := c.do(ctx, http.MethodPost, path, body, &hits); err != nil {
		return nil, err
	}
	return hits, nil
}

// DeleteCollection menghapus collection (untuk reset percobaan).
func (c *Client) DeleteCollection(ctx context.Context, name string) error {
	path := "/collections/" + url.PathEscape(name)
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

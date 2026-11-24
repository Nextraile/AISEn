package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Regression: klien yang membatalkan request (abort browser / pindah query)
// dulu dilaporkan sebagai 500 "encode query: ... context canceled". Status itu
// salah (server tidak gagal) dan memicu noise di access log.
func TestSearchCanceledClientReturns499(t *testing.T) {
	srv := New(Config{Addr: ":0"})

	// model_dir sengaja tidak valid: kalau handler tetap mencoba memuat model,
	// hasilnya 500 — jadi test ini sekaligus menjaga cek ctx di awal encodeQuery.
	body := `{"query":"machine learning","n":3,"engine":"onnx","model_dir":"/tidak/ada"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/search", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx, cancel := context.WithCancel(req.Context())
	cancel() // klien sudah pergi sebelum handler mulai
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != 499 {
		t.Fatalf("status = %d, want 499 (body: %s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "context canceled") {
		t.Errorf("body masih membocorkan context canceled: %s", rec.Body.String())
	}
}

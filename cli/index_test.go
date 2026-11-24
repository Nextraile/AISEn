package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"awesomeProject/internal/normalize"
	"awesomeProject/internal/store"
)

const (
	mockReadyBody = "all shards are ready"
	mockNotFound  = `{"status":{"code":404,"message":"Collection not found"},"time":0.001}`
	mockOK        = `{"result":{"status":"ok"},"status":"ok","time":0.001}`
)

// qdrantMock adalah server Qdrant tiruan yang mencatat jumlah upsert
// konkuren dan total point yang diterima.
type qdrantMock struct {
	t           *testing.T
	inflight    atomic.Int32
	maxInflight atomic.Int32
	totalPoints atomic.Int64
	upserts     atomic.Int32
	deleted     atomic.Int32
	failPoints  bool
	sleep       time.Duration
}

func (m *qdrantMock) track() {
	cur := m.inflight.Add(1)
	for {
		old := m.maxInflight.Load()
		if cur <= old || m.maxInflight.CompareAndSwap(old, cur) {
			break
		}
	}
	time.Sleep(m.sleep)
	m.inflight.Add(-1)
}

func (m *qdrantMock) handler(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/readyz":
		_, _ = io.WriteString(w, mockReadyBody)

	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/collections/"):
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, mockNotFound)

	case r.Method == http.MethodPut && r.URL.Path == "/collections/undip_eprints":
		_, _ = io.WriteString(w, mockOK)

	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/collections/"):
		m.deleted.Add(1)
		_, _ = io.WriteString(w, mockOK)

	case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/points"):
		if m.failPoints {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, "boom")
			return
		}
		var body struct {
			Points []struct {
				ID uint64 `json:"id"`
			} `json:"points"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			m.t.Errorf("decode upsert: %v", err)
		}
		m.totalPoints.Add(int64(len(body.Points)))
		m.upserts.Add(1)
		m.track()
		_, _ = io.WriteString(w, mockOK)

	default:
		m.t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	}
}

// writeFixtures membuat file dokumen + file vektor yang sinkron.
func writeFixtures(t *testing.T, dir string, n, dim int) (docsPath, vecPath string) {
	t.Helper()
	docsPath = dir + "/docs.jsonl"
	vecPath = dir + "/vectors.bin"

	docs := make([]normalize.Doc, n)
	for i := range docs {
		docs[i] = normalize.Doc{
			ID:       fmt.Sprintf("oai:eprints2.undip.ac.id:%d", i+1),
			EprintID: int64(i + 1),
			Title:    fmt.Sprintf("Judul Dokumen %d", i+1),
			Text:     "teks embedding dokumen uji",
		}
	}
	if err := store.WriteJSONL(docsPath, docs); err != nil {
		t.Fatalf("WriteJSONL: %v", err)
	}

	w, err := store.NewVectorWriter(vecPath, dim)
	if err != nil {
		t.Fatalf("NewVectorWriter: %v", err)
	}
	for i, d := range docs {
		idx := []uint32{uint32(i % dim)}
		val := []float32{1}
		if err := w.Write(d.ID, idx, val); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return docsPath, vecPath
}

func TestRunIndexParallelWorkers(t *testing.T) {
	mock := &qdrantMock{t: t, sleep: 25 * time.Millisecond}
	srv := httptest.NewServer(http.HandlerFunc(mock.handler))
	defer srv.Close()

	docsPath, vecPath := writeFixtures(t, t.TempDir(), 80, 16)

	// batch=8 -> 10 batch upsert; workers=4 harus berjalan konkuren.
	err := runIndex(context.Background(), docsPath, vecPath, srv.URL,
		"undip_eprints", 8, 4, false)
	if err != nil {
		t.Fatalf("runIndex: %v", err)
	}

	if got := mock.totalPoints.Load(); got != 80 {
		t.Errorf("total point = %d, want 80", got)
	}
	max := mock.maxInflight.Load()
	if max < 2 {
		t.Errorf("upsert tidak paralel: max inflight = %d (harus >= 2)", max)
	}
	if max > 4 {
		t.Errorf("max inflight = %d, melebihi -workers 4", max)
	}
	if up := mock.upserts.Load(); up != 10 {
		t.Errorf("jumlah batch upsert = %d, want 10", up)
	}
}

func TestRunIndexSingleWorkerStillWorks(t *testing.T) {
	mock := &qdrantMock{t: t, sleep: 0}
	srv := httptest.NewServer(http.HandlerFunc(mock.handler))
	defer srv.Close()

	docsPath, vecPath := writeFixtures(t, t.TempDir(), 10, 8)
	if err := runIndex(context.Background(), docsPath, vecPath, srv.URL,
		"undip_eprints", 5, 1, false); err != nil {
		t.Fatalf("runIndex: %v", err)
	}
	if got := mock.totalPoints.Load(); got != 10 {
		t.Errorf("total point = %d, want 10", got)
	}
	if mock.maxInflight.Load() != 1 {
		t.Errorf("workers=1 seharusnya tidak konkuren, max = %d", mock.maxInflight.Load())
	}
}

func TestRunIndexPropagatesUpsertError(t *testing.T) {
	mock := &qdrantMock{t: t, failPoints: true, sleep: 5 * time.Millisecond}
	srv := httptest.NewServer(http.HandlerFunc(mock.handler))
	defer srv.Close()

	docsPath, vecPath := writeFixtures(t, t.TempDir(), 30, 8)
	err := runIndex(context.Background(), docsPath, vecPath, srv.URL,
		"undip_eprints", 8, 4, false)
	if err == nil {
		t.Fatal("seharusnya error saat upsert gagal")
	}
	if !strings.Contains(err.Error(), "upsert") {
		t.Errorf("error tidak menjelaskan kegagalan upsert: %v", err)
	}
}

// writePartialFixtures membuat n dokumen tetapi hanya m vektor (m < n),
// mensimulasikan vectorize yang terpotong.
func writePartialFixtures(t *testing.T, dir string, n, m, dim int) (docsPath, vecPath string) {
	t.Helper()
	docsPath, vecPath = writeFixtures(t, dir, n, dim)

	// Tulis ulang file vektor dengan m record saja.
	w, err := store.NewVectorWriter(vecPath, dim)
	if err != nil {
		t.Fatalf("NewVectorWriter: %v", err)
	}
	for i := 0; i < m; i++ {
		if err := w.Write(fmt.Sprintf("oai:eprints2.undip.ac.id:%d", i+1), []uint32{uint32(i % dim)}, []float32{1}); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return docsPath, vecPath
}

// Regression: dulu `index -recreate` menghapus collection SEBELUM tahu file
// vektor bisa dibaca sampai selesai. File vektor terpotong -> collection lama
// hilang, lalu index gagal di tengah (insiden 152 point).
func TestRunIndexRecreateDoesNotDeleteOnBrokenVectors(t *testing.T) {
	mock := &qdrantMock{t: t}
	srv := httptest.NewServer(http.HandlerFunc(mock.handler))
	defer srv.Close()

	docsPath, vecPath := writePartialFixtures(t, t.TempDir(), 20, 5, 8)

	err := runIndex(context.Background(), docsPath, vecPath, srv.URL,
		"undip_eprints", 8, 2, true)
	if err == nil {
		t.Fatal("seharusnya error karena file vektor tidak lengkap")
	}
	if !strings.Contains(err.Error(), "vektor") {
		t.Errorf("error tidak menjelaskan file vektor: %v", err)
	}
	if got := mock.deleted.Load(); got != 0 {
		t.Errorf("collection dihapus %d kali padahal input rusak (harus 0)", got)
	}
}

func TestRunIndexRecreateDeletesAfterValidInput(t *testing.T) {
	mock := &qdrantMock{t: t}
	srv := httptest.NewServer(http.HandlerFunc(mock.handler))
	defer srv.Close()

	docsPath, vecPath := writeFixtures(t, t.TempDir(), 12, 8)
	if err := runIndex(context.Background(), docsPath, vecPath, srv.URL,
		"undip_eprints", 4, 2, true); err != nil {
		t.Fatalf("runIndex: %v", err)
	}
	if got := mock.deleted.Load(); got != 1 {
		t.Errorf("delete collection = %d, want 1", got)
	}
	if got := mock.totalPoints.Load(); got != 12 {
		t.Errorf("total point = %d, want 12", got)
	}
}

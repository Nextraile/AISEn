package qdrant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const notFoundEnvelope = `{"status":{"code":404,"message":"Collection ` + "`undip_eprints`" + ` doesn't exist!"},"time":0.001}`

const infoEnvelope = `{"result":{"status":"green","points_count":1469,
  "config":{"params":{"vectors":{"size":1024,"distance":"Cosine"}}}},
  "status":"ok","time":0.001}`

func TestEnsureCollectionCreatesWhenMissing(t *testing.T) {
	var putCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/collections/"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, notFoundEnvelope)
		case r.Method == http.MethodPut && r.URL.Path == "/collections/undip_eprints":
			putCalled = true
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("bad body: %v", err)
			}
			vec := body["vectors"].(map[string]any)
			if int(vec["size"].(float64)) != 1024 || vec["distance"] != DistanceCosine {
				t.Errorf("vectors = %#v", vec)
			}
			_, _ = io.WriteString(w, `{"result":{"status":"ok"},"status":"ok","time":0.001}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	defer srv.Close()

	created, err := NewClient(srv.URL, "").EnsureCollection(context.Background(), "undip_eprints", 1024)
	if err != nil {
		t.Fatalf("EnsureCollection: %v", err)
	}
	if !created {
		t.Error("seharusnya melaporkan collection baru dibuat")
	}
	if !putCalled {
		t.Error("PUT collection tidak dipanggil")
	}
}

func TestEnsureCollectionRejectsDimensionMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, infoEnvelope) // existing dim 1024
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, "").EnsureCollection(context.Background(), "undip_eprints", 512)
	if err == nil || !strings.Contains(err.Error(), "dimensi 1024") {
		t.Fatalf("err = %v, want mismatch dimensi", err)
	}
}

func TestEnsureCollectionReusesWhenDimMatches(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, infoEnvelope)
	}))
	defer srv.Close()

	created, err := NewClient(srv.URL, "").EnsureCollection(context.Background(), "undip_eprints", 1024)
	if err != nil {
		t.Fatalf("EnsureCollection: %v", err)
	}
	if created {
		t.Error("collection sudah ada, seharusnya created=false")
	}
}

func TestCollectionInfoParsesEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, infoEnvelope)
	}))
	defer srv.Close()

	info, err := NewClient(srv.URL, "").CollectionInfo(context.Background(), "undip_eprints")
	if err != nil {
		t.Fatalf("CollectionInfo: %v", err)
	}
	if info.VectorSize != 1024 || info.VectorDistance != DistanceCosine {
		t.Errorf("vector = %d/%s", info.VectorSize, info.VectorDistance)
	}
	if info.PointsCount != 1469 || info.Status != "green" {
		t.Errorf("info = %#v", info)
	}
}

func TestUpsertAndSearch(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		switch r.Method {
		case http.MethodPut:
			var body struct {
				Points []Point `json:"points"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode upsert: %v", err)
			}
			if len(body.Points) != 1 || body.Points[0].ID != 42 {
				t.Errorf("points = %#v", body.Points)
			}
			_, _ = io.WriteString(w, `{"result":{"status":"ok"},"status":"ok","time":0.001}`)
		case http.MethodPost:
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode search: %v", err)
			}
			if body["limit"].(float64) != 5 {
				t.Errorf("limit = %v", body["limit"])
			}
			_, _ = io.WriteString(w, `{"result":[{"id":42,"score":0.87,
			  "payload":{"title":"Judul Uji"}}],"status":"ok","time":0.001}`)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "secret-key")
	err := c.Upsert(context.Background(), "coll", []Point{{
		ID: 42, Vector: []float32{0.1, 0.2}, Payload: map[string]any{"title": "Judul Uji"},
	}})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if !strings.Contains(gotPath, "wait=true") {
		t.Errorf("upsert path = %q, want wait=true", gotPath)
	}

	hits, err := c.Search(context.Background(), "coll", []float32{0.1, 0.2}, SearchOptions{Limit: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != 42 || hits[0].Score < 0.8 {
		t.Errorf("hits = %#v", hits)
	}
	if hits[0].Payload["title"] != "Judul Uji" {
		t.Errorf("payload = %#v", hits[0].Payload)
	}
}

func TestAPIKeyHeaderSent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("api-key") != "k-123" {
			t.Errorf("api-key = %q", r.Header.Get("api-key"))
		}
		_, _ = io.WriteString(w, infoEnvelope)
	}))
	defer srv.Close()
	if _, err := NewClient(srv.URL, "k-123").CollectionInfo(context.Background(), "c"); err != nil {
		t.Fatalf("CollectionInfo: %v", err)
	}
}

func TestAPIErrorMessageVariants(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"status objek", `{"status":{"code":404,"message":"Collection x doesn't exist!"}}`, "doesn't exist"},
		{"error string", `{"status":"error","error":"Wrong input"}`, "Wrong input"},
		{"status error string", `{"status":{"error":"Bad request"}}`, "Bad request"},
		{"tanpa pesan", `{"status":"ok"}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var ar apiResponse
			if err := json.Unmarshal([]byte(c.body), &ar); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got := ar.apiErrorMessage()
			if c.want == "" {
				if got != "" {
					t.Errorf("got %q, want kosong", got)
				}
				return
			}
			if !strings.Contains(got, c.want) {
				t.Errorf("got %q, want mengandung %q", got, c.want)
			}
		})
	}
}

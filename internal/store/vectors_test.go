package store

import (
	"io"
	"math"
	"path/filepath"
	"testing"
)

func TestJSONLWriteReadRoundTrip(t *testing.T) {
	type line struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	path := filepath.Join(t.TempDir(), "x.jsonl")
	in := []line{{"a", "Alpha"}, {"b", "Beta"}}
	if err := WriteJSONL(path, in); err != nil {
		t.Fatalf("WriteJSONL: %v", err)
	}
	var out []line
	n, err := ReadJSONL[line](path, func(v line) error {
		out = append(out, v)
		return nil
	})
	if err != nil || n != len(in) {
		t.Fatalf("ReadJSONL n=%d err=%v", n, err)
	}
	if out[1].Name != "Beta" {
		t.Errorf("out = %#v", out)
	}
}

func TestJSONLWriterAppendCount(t *testing.T) {
	type line struct {
		N int `json:"n"`
	}
	path := filepath.Join(t.TempDir(), "w.jsonl")
	w, err := NewJSONLWriter[line](path)
	if err != nil {
		t.Fatalf("NewJSONLWriter: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := w.Write(line{i}); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if w.Count() != 5 {
		t.Errorf("Count = %d", w.Count())
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	n, err := CountJSONL(path)
	if err != nil || n != 5 {
		t.Fatalf("CountJSONL = %d, err=%v", n, err)
	}
}

func TestVectorBinRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.bin")
	const dim = 32

	w, err := NewVectorWriter(path, dim)
	if err != nil {
		t.Fatalf("NewVectorWriter: %v", err)
	}
	// Kasus: vektor normal, vektor kosong (nnz=0), dan id panjang.
	cases := []struct {
		id  string
		idx []uint32
		val []float32
	}{
		{"oai:eprints2.undip.ac.id:1", []uint32{1, 5, 31}, []float32{0.5, -0.5, 0.7071}},
		{"oai:eprints2.undip.ac.id:2", nil, nil},
		{"oai:eprints2.undip.ac.id:1000000", []uint32{0}, []float32{-1}},
	}
	for _, c := range cases {
		if err := w.Write(c.id, c.idx, c.val); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if w.Count() != len(cases) {
		t.Errorf("Count = %d", w.Count())
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r, err := OpenVectors(path)
	if err != nil {
		t.Fatalf("OpenVectors: %v", err)
	}
	defer r.Close()
	if r.Dim() != dim {
		t.Errorf("Dim = %d, want %d", r.Dim(), dim)
	}
	if r.Count() != len(cases) {
		t.Errorf("header Count = %d, want %d", r.Count(), len(cases))
	}
	for i, c := range cases {
		id, vec, err := r.Next()
		if err != nil {
			t.Fatalf("Next[%d]: %v", i, err)
		}
		if id != c.id {
			t.Errorf("id[%d] = %q, want %q", i, id, c.id)
		}
		if len(vec) != dim {
			t.Fatalf("len(vec[%d]) = %d", i, len(vec))
		}
		for _, j := range c.idx {
			if vec[j] == 0 {
				t.Errorf("vec[%d][%d] seharusnya terisi", i, j)
			}
		}
		for j := 0; j < dim; j++ {
			want := float32(0)
			for k, idx := range c.idx {
				if uint32(j) == idx {
					want = c.val[k]
				}
			}
			if math.Abs(float64(vec[j]-want)) > 1e-6 {
				t.Errorf("vec[%d][%d] = %v, want %v", i, j, vec[j], want)
			}
		}
	}
	if _, _, err := r.Next(); err != io.EOF {
		t.Errorf("Next() akhir = %v, want io.EOF", err)
	}
}

func TestNewVectorSinkRejectsUnknownFormat(t *testing.T) {
	if _, err := NewVectorSink("x.bin", "parquet", 8); err == nil {
		t.Error("format tak dikenal harus error")
	}
}

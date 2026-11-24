package embed

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
)

func TestParallelMapPreservesOrder(t *testing.T) {
	in := make([]int, 2000)
	for i := range in {
		in[i] = i
	}
	out := ParallelMap(in, func(x int) int { return x * x })
	if len(out) != len(in) {
		t.Fatalf("len = %d, want %d", len(out), len(in))
	}
	for i, v := range out {
		if v != i*i {
			t.Fatalf("out[%d] = %d, want %d (urutan tidak terjaga)", i, v, i*i)
		}
	}
}

// corpus lintas BatchSize (512): 1300 dokumen -> minimal 3 batch paralel.
func bigCorpus(n int) []string {
	docs := make([]string, n)
	for i := range docs {
		docs[i] = fmt.Sprintf(
			"Analisis rantai pasokan logistik industri dokumen ke-%d di Jawa Tengah "+
				"manajemen kinerja supply chain penelitian ini mengkaji pengaruh "+
				"variasi unik%04d terhadap hasil akhir studi kasus manufaktur", i, i)
	}
	return docs
}

// TestFitParallelMatchesNaive memastikan penghitungan df paralel per-batch
// identik dengan referensi sekuensial (tidak ada hitungan hilang/ganda).
func TestFitParallelMatchesNaive(t *testing.T) {
	docs := bigCorpus(1300) // > 2× BatchSize
	p := DefaultParams()
	p.MinDF = 1
	p.MaxFeatures = 1 << 20

	// Referensi sekuensial: df per term (unik per dokumen).
	naiveDF := make(map[string]int)
	var chars int64
	for _, text := range docs {
		chars += int64(len(text))
		seen := map[string]struct{}{}
		for _, tok := range Tokens(text, p.Ngram) {
			if _, dup := seen[tok]; dup {
				continue
			}
			seen[tok] = struct{}{}
			naiveDF[tok]++
		}
	}

	idx := 0
	m, err := Fit(func(yield func(string) error) error {
		for idx < len(docs) {
			if err := yield(docs[idx]); err != nil {
				return err
			}
			idx++
		}
		return nil
	}, p, nil)
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}

	if m.DocCount != len(docs) {
		t.Errorf("DocCount = %d, want %d", m.DocCount, len(docs))
	}
	if m.TextChars != chars {
		t.Errorf("TextChars = %d, want %d", m.TextChars, chars)
	}
	if len(m.IDF) != len(naiveDF) {
		t.Errorf("kosakata = %d, want %d (df hilang/berlebih)", len(m.IDF), len(naiveDF))
	}
	lnN := math.Log(float64(1 + len(docs)))
	for term, df := range naiveDF {
		want := lnN - math.Log(float64(1+df)) + 1
		got, ok := m.IDF[term]
		if !ok {
			t.Errorf("term %q hilang dari model", term)
			continue
		}
		if math.Abs(got-want) > 1e-12 {
			t.Errorf("IDF(%q) = %v, want %v (df=%d)", term, got, want, df)
		}
	}
}

// TestTransformOrderAndEquivalence memastikan Transform paralel menulis
// urutan id identik dengan input dan tiap vektor sama dengan Sparse().
func TestTransformOrderAndEquivalence(t *testing.T) {
	base := corpus()
	idx := 0
	m, err := Fit(func(yield func(string) error) error {
		for idx < len(base) {
			if err := yield(base[idx]); err != nil {
				return err
			}
			idx++
		}
		return nil
	}, DefaultParams(), nil)
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}

	// 1300 dokumen melintasi beberapa batch paralel.
	const n = 1300
	type got struct {
		id  string
		idx []uint32
		val []float32
	}
	var collected []got
	count, empty, err := m.Transform(context.Background(),
		func(yield func(id, text string) error) error {
			for i := 0; i < n; i++ {
				if err := yield(fmt.Sprintf("doc_%d", i), base[i%len(base)]); err != nil {
					return err
				}
			}
			return nil
		},
		func(id string, idx []uint32, val []float32) error {
			collected = append(collected, got{id: id, idx: idx, val: val})
			return nil
		},
		nil)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if count != n || len(collected) != n {
		t.Fatalf("count=%d collected=%d, want %d", count, len(collected), n)
	}
	if empty != 0 {
		t.Errorf("empty = %d, want 0", empty)
	}
	for i, g := range collected {
		if want := fmt.Sprintf("doc_%d", i); g.id != want {
			t.Fatalf("id[%d] = %q, want %q (urutan tidak terjaga)", i, g.id, want)
		}
		wantIdx, wantVal := m.Sparse(base[i%len(base)])
		if len(g.idx) != len(wantIdx) {
			t.Fatalf("doc_%d: nnz = %d, want %d", i, len(g.idx), len(wantIdx))
		}
		for k := range g.idx {
			if g.idx[k] != wantIdx[k] || g.val[k] != wantVal[k] {
				t.Fatalf("doc_%d[%d]: got (%d,%v), want (%d,%v)",
					i, k, g.idx[k], g.val[k], wantIdx[k], wantVal[k])
			}
		}
	}
}

func TestTransformRespectsContext(t *testing.T) {
	m, err := Fit(func(yield func(string) error) error {
		return yield("dokumen uji konteks pembatalan")
	}, DefaultParams(), nil)
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = m.Transform(ctx,
		func(yield func(id, text string) error) error {
			return yield("id1", "teks")
		},
		func(string, []uint32, []float32) error { return nil },
		nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

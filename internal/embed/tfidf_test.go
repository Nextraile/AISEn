package embed

import (
	"math"
	"testing"
)

func corpus() []string {
	return []string{
		"Analisis rantai pasokan logistik industri di Jawa Tengah",
		"Manajemen rantai pasokan dan kinerja bisnis UKM",
		"Mesin pembuat kapal nelayan berbahan kayu laminasi",
		"Pengujian tegangan tarik bambu laminasi untuk alat tangkap ikan",
	}
}

func TestTokenizeFiltersStopwordsAndShortTokens(t *testing.T) {
	got := Tokenize("dan di teknik industri yang analysis of 12 ab", 3)
	want := map[string]bool{"teknik": true, "industri": true, "analysis": true}
	if len(got) != len(want) {
		t.Fatalf("Tokenize() = %#v", got)
	}
	for _, tok := range got {
		if !want[tok] {
			t.Errorf("token tak terduga: %q", tok)
		}
	}
}

func TestTokensAddsBigrams(t *testing.T) {
	got := Tokens("rantai pasokan logistik", 2)
	found := false
	for _, tok := range got {
		if tok == "rantai_pasokan" {
			found = true
		}
	}
	if !found {
		t.Fatalf("bigram rantai_pasokan tidak ada: %#v", got)
	}
}

func TestFitAndVectorProperties(t *testing.T) {
	docs := corpus()
	idx := 0
	texts := func(yield func(string) error) error {
		for idx < len(docs) {
			if err := yield(docs[idx]); err != nil {
				return err
			}
			idx++
		}
		return nil
	}
	p := DefaultParams()
	p.Dim = 128
	m, err := Fit(texts, p, nil)
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	if m.DocCount != len(docs) {
		t.Errorf("DocCount = %d, want %d", m.DocCount, len(docs))
	}
	if len(m.IDF) == 0 {
		t.Fatal("kosakata kosong")
	}

	v := m.Vector(docs[0])
	if len(v) != p.Dim {
		t.Fatalf("dim = %d, want %d", len(v), p.Dim)
	}
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	if math.Abs(norm-1) > 1e-4 {
		t.Errorf("vektor tidak L2-normalisasi: |v|^2 = %f", norm)
	}

	// Vektor dokumen mirip harus lebih dekat daripada yang jauh.
	simA := cosine(v, m.Vector(docs[1]))
	simB := cosine(v, m.Vector(docs[2]))
	if simA <= simB {
		t.Errorf("dokumen terkait harus lebih mirip: A=%f B=%f", simA, simB)
	}

	// Deterministik: transformasi yang sama menghasilkan vektor sama.
	v2 := m.Vector(docs[0])
	for i := range v {
		if v[i] != v2[i] {
			t.Fatalf("vektor tidak deterministik pada index %d", i)
		}
	}
}

func TestSparseMatchesVector(t *testing.T) {
	docs := corpus()
	idx := 0
	m, err := Fit(func(yield func(string) error) error {
		for idx < len(docs) {
			if err := yield(docs[idx]); err != nil {
				return err
			}
			idx++
		}
		return nil
	}, DefaultParams(), nil)
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}

	text := docs[0]
	sIdx, sVal := m.Sparse(text)
	dense := m.Vector(text)
	for i, id := range sIdx {
		if dense[id] != sVal[i] {
			t.Fatalf("sparse[%d] tidak konsisten dengan dense", i)
		}
	}
	var sumSparse, sumDense float64
	for _, x := range sVal {
		sumSparse += float64(x) * float64(x)
	}
	for _, x := range dense {
		sumDense += float64(x) * float64(x)
	}
	if math.Abs(sumSparse-sumDense) > 1e-5 {
		t.Errorf("norm sparse %f != dense %f", sumSparse, sumDense)
	}
}

func TestModelSaveLoadRoundTrip(t *testing.T) {
	docs := corpus()
	idx := 0
	m, err := Fit(func(yield func(string) error) error {
		for idx < len(docs) {
			_ = yield(docs[idx])
			idx++
		}
		return nil
	}, DefaultParams(), nil)
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	path := t.TempDir() + "/model.json"
	if err := m.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	m2, err := LoadModel(path)
	if err != nil {
		t.Fatalf("LoadModel: %v", err)
	}
	if m2.DocCount != m.DocCount || len(m2.IDF) != len(m.IDF) {
		t.Errorf("model tidak identik setelah round-trip")
	}
	a, b := m.Vector(docs[0]), m2.Vector(docs[0])
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("vektor berbeda setelah load model pada index %d", i)
		}
	}
}

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

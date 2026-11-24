package embed

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"awesomeProject/internal/store"
)

// Method adalah identifikasi algoritma embedding pada file model.
const Method = "tfidf-hashing-v1"

// MinFeatures minimal dimensi hash.
const MinFeatures = 64

// Params parameter pembangunan model TF-IDF.
type Params struct {
	// Dim adalah ukuran vektor padat hasil feature hashing.
	Dim int `json:"dim"`
	// MinDF minimal jumlah dokumen yang memuat sebuah term agar masuk kosakata.
	MinDF int `json:"min_df"`
	// MaxFeatures batas ukuran kosakata (term paling sering dipakai yang lolos).
	MaxFeatures int `json:"max_features"`
	// Ngram 1 = unigram; 2 = unigram + bigram.
	Ngram int `json:"ngram"`
	// SublinearTF memakai 1+ln(tf) alih-alih tf mentah.
	SublinearTF bool `json:"sublinear_tf"`
}

// DefaultParams mengembalikan parameter yang wajar untuk korpus metadata akademik.
func DefaultParams() Params {
	return Params{
		Dim:         1024,
		MinDF:       2,
		MaxFeatures: 50_000,
		Ngram:       2,
		SublinearTF: true,
	}
}

// Validate memastikan parameter masuk akal.
func (p *Params) Validate() error {
	if p.Dim == 0 {
		p.Dim = DefaultParams().Dim
	}
	if p.Dim < MinFeatures {
		return fmt.Errorf("dim harus >= %d, dapat %d", MinFeatures, p.Dim)
	}
	if p.MinDF < 1 {
		p.MinDF = 1
	}
	if p.MaxFeatures <= 0 {
		p.MaxFeatures = DefaultParams().MaxFeatures
	}
	if p.Ngram < 1 {
		p.Ngram = 1
	}
	if p.Ngram > 2 {
		return fmt.Errorf("ngram hanya mendukung 1 atau 2, dapat %d", p.Ngram)
	}
	return nil
}

// Model adalah artefak TF-IDF: parameter + IDF per term kosakata.
type Model struct {
	Version   int                `json:"version"`
	Method    string             `json:"method"`
	Params    Params             `json:"params"`
	DocCount  int                `json:"doc_count"`
	IDF       map[string]float64 `json:"idf"`
	BuiltAt   time.Time          `json:"built_at"`
	TextChars int64              `json:"total_text_chars"`
}

// TextSource menyediakan teks embedding satu per satu (streaming).
type TextSource func(yield func(text string) error) error

// LogFunc untuk progres opsional.
type LogFunc func(format string, args ...any)

// Fit membangun model TF-IDF dari iterable teks (sekali iterasi penuh).
//   - Pass 1: menghitung df per term dan jumlah dokumen
//   - IDF     : ln((1+N)/(1+df)) + 1 (smooth, seperti sklearn)
func Fit(texts TextSource, p Params, logf LogFunc) (*Model, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}

	df := make(map[string]int, 1<<16)
	n := 0
	var chars int64

	// Pass 1 paralel per-batch: tokenisasi dijalankan GOMAXPROCS goroutine,
	// hasil unik per dokumen di-merge ke df (merge komutatif -> deterministik).
	batch := make([]string, 0, BatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		perDoc := ParallelMap(batch, func(text string) []string {
			seen := make(map[string]struct{}, 64)
			uniq := make([]string, 0, 64)
			for _, tok := range Tokens(text, p.Ngram) {
				if _, dup := seen[tok]; dup {
					continue
				}
				seen[tok] = struct{}{}
				uniq = append(uniq, tok)
			}
			return uniq
		})
		for _, uniq := range perDoc {
			for _, tok := range uniq {
				df[tok]++
			}
		}
		batch = batch[:0]
	}

	err := texts(func(text string) error {
		chars += int64(len(text))
		n++
		batch = append(batch, text)
		if len(batch) >= BatchSize {
			flush()
		}
		if n%10000 == 0 {
			logf("  fit: %d dokumen, %d term unik", n, len(df))
		}
		return nil
	})
	if err == nil {
		flush()
	}
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, errors.New("vektorisasi: tidak ada dokumen untuk di-fit")
	}

	// Filter df + urutkan berdasarkan df turun (lalu alfabetis untuk determinisme).
	type entry struct {
		term string
		df   int
	}
	all := make([]entry, 0, len(df))
	for t, d := range df {
		if d < p.MinDF {
			continue
		}
		all = append(all, entry{t, d})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].df != all[j].df {
			return all[i].df > all[j].df
		}
		return all[i].term < all[j].term
	})
	if len(all) > p.MaxFeatures {
		all = all[:p.MaxFeatures]
	}

	m := &Model{
		Version:   1,
		Method:    Method,
		Params:    p,
		DocCount:  n,
		IDF:       make(map[string]float64, len(all)),
		BuiltAt:   time.Now().UTC(),
		TextChars: chars,
	}
	lnN := math.Log(float64(1 + n))
	for _, e := range all {
		m.IDF[e.term] = lnN - math.Log(float64(1+e.df)) + 1
	}
	logf("model TF-IDF siap: dokumen=%d, kosakata=%d (dari %d kandidat), dim=%d, teks=%d char",
		n, len(m.IDF), len(df), p.Dim, chars)
	return m, nil
}

// defaultIDF adalah IDF untuk term yang tidak ada di korpus training
// (df dianggap 0): ln((1+N)/1) + 1.
func (m *Model) defaultIDF() float64 {
	return math.Log(float64(1+m.DocCount)) + 1
}

// hashTerm memetakan term ke (indeks, tanda) memakai dua seed FNV-1a berbeda
// sehingga tanda tidak berkorelasi dengan indeks (mengurangi bias tabrakan).
func hashTerm(term string, dim int) (uint32, float32) {
	idx := fnv32aSeed(term, 2166136261) % uint32(dim)
	if fnv32aSeed(term, 0x9e3779b9)&1 == 1 {
		return idx, -1
	}
	return idx, 1
}

func fnv32aSeed(s string, seed uint32) uint32 {
	h := seed
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// weightedTerms menghitung bobot TF-IDF per term unik dari satu teks:
//   - tf mentah dihitung per term
//   - bobot = (1+ln(tf)) * idf bila SublinearTF, selain itu tf * idf
//
// Hasil berupa bobot per term (belum di-hash) sehingga tabrakan hash tidak
// mencampur bobot term yang berbeda IDF.
func (m *Model) weightedTerms(text string) map[string]float64 {
	terms := Tokens(text, m.Params.Ngram)
	if len(terms) == 0 {
		return nil
	}
	tf := make(map[string]int, len(terms)*2)
	for _, t := range terms {
		tf[t]++
	}
	out := make(map[string]float64, len(tf))
	defIDF := m.defaultIDF()
	for t, c := range tf {
		idf, ok := m.IDF[t]
		if !ok {
			idf = defIDF
		}
		w := float64(c)
		if m.Params.SublinearTF && c > 1 {
			w = 1 + math.Log(float64(c))
		}
		out[t] = w * idf
	}
	return out
}

// Vector mengubah satu teks menjadi vektor padat L2-normalisasi:
// bobot tiap term di-hash ke [0,Dim) dengan tanda ±1 (signed feature hashing)
// lalu dinormalisasi agar cos-similarity siap pakai untuk Qdrant.
func (m *Model) Vector(text string) []float32 {
	wts := m.weightedTerms(text)
	dim := m.Params.Dim
	out := make([]float32, dim)
	if len(wts) == 0 {
		return out
	}
	for term, w := range wts {
		idx, sign := hashTerm(term, dim)
		out[idx] += float32(w) * sign
	}
	var norm float64
	for _, v := range out {
		norm += float64(v) * float64(v)
	}
	if norm == 0 {
		return out
	}
	inv := float32(1 / math.Sqrt(norm))
	for i, v := range out {
		out[i] = v * inv
	}
	return out
}

// Sparse mengubah teks menjadi pasangan (indeks, bobot) terurut indeks naik,
// tanpa membangun slice padat penuh - dipakai untuk menulis file biner.
func (m *Model) Sparse(text string) ([]uint32, []float32) {
	wts := m.weightedTerms(text)
	if len(wts) == 0 {
		return nil, nil
	}
	dim := m.Params.Dim
	acc := make(map[uint32]float32, len(wts))
	for term, w := range wts {
		idx, sign := hashTerm(term, dim)
		acc[idx] += float32(w) * sign
	}
	idx := make([]uint32, 0, len(acc))
	val := make([]float32, 0, len(acc))
	var norm float64
	for i, w := range acc {
		idx = append(idx, i)
		val = append(val, w)
		norm += float64(w) * float64(w)
	}
	if norm == 0 {
		return nil, nil
	}
	// Urutkan berdasarkan indeks agar file biner deterministik.
	order := make([]int, len(idx))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return idx[order[a]] < idx[order[b]] })
	inv := float32(1 / math.Sqrt(norm))
	sIdx := make([]uint32, len(idx))
	sVal := make([]float32, len(val))
	for i, o := range order {
		sIdx[i] = idx[o]
		sVal[i] = val[o] * inv
	}
	return sIdx, sVal
}

// QueryVector adalah alias Vector, ditekankan untuk sisi pencarian.
func (m *Model) QueryVector(text string) []float32 { return m.Vector(text) }

// Save menyimpan model sebagai JSON (atomik).
func (m *Model) Save(path string) error {
	if err := store.EnsureDir(path); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadModel memuat model dari file JSON dan memvalidasi kompatibilitasnya.
func LoadModel(path string) (*Model, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Model
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("model rusak (%s): %w", path, err)
	}
	if m.Method != Method {
		return nil, fmt.Errorf("model %s memakai metode %q, tool ini mendukung %q",
			filepath.Base(path), m.Method, Method)
	}
	if err := m.Params.Validate(); err != nil {
		return nil, err
	}
	if m.DocCount <= 0 {
		return nil, fmt.Errorf("model %s tidak punya doc_count", path)
	}
	return &m, nil
}

// Dim mendapatkan dimensi vektor model.
func (m *Model) Dim() int { return m.Params.Dim }

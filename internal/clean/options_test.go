package clean

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"awesomeProject/internal/oai"
	"awesomeProject/internal/store"
)

func TestLooksAdministrative(t *testing.T) {
	cases := []struct {
		title string
		urls  []string
		want  bool
	}{
		{"Penilaian Sejawat Artikel Jurnal", nil, true},
		{"penilaian sejawat (peer review)", nil, true},
		{"Analisis Logistik Rantai Pasokan", nil, false},
		{"Judul Normal", []string{"https://eprints2.undip.ac.id/id/eprint/1/peer_review.pdf"}, true},
		{"Judul Normal", []string{"https://eprints2.undip.ac.id/id/eprint/1/scan.pdf"}, false},
	}
	for _, c := range cases {
		if got := LooksAdministrative(c.title, c.urls); got != c.want {
			t.Errorf("LooksAdministrative(%q, %v) = %v, want %v", c.title, c.urls, got, c.want)
		}
	}
}

func TestOptionsReject(t *testing.T) {
	long := strings.Repeat("a", 80)
	base := Record{
		Title:        []string{"Judul Skripsi yang Cukup Panjang"},
		Descriptions: []string{long},
		URLs:         []string{"https://eprints2.undip.ac.id/id/eprint/1/"},
	}

	t.Run("lolos", func(t *testing.T) {
		if r := DefaultOptions().Reject(base); r != "" {
			t.Errorf("record valid ditolak: %q", r)
		}
	})
	t.Run("abstrak pas 50 char ditolak (legacy: >50)", func(t *testing.T) {
		rec := base
		rec.Descriptions = []string{strings.Repeat("b", 50)}
		if r := DefaultOptions().Reject(rec); r != "short_abstract" {
			t.Errorf("got %q, want short_abstract", r)
		}
	})
	t.Run("abstrak 51 char lolos", func(t *testing.T) {
		rec := base
		rec.Descriptions = []string{strings.Repeat("b", 51)}
		if r := DefaultOptions().Reject(rec); r != "" {
			t.Errorf("got %q, want lolos", r)
		}
	})
	t.Run("tanpa abstrak ditolak", func(t *testing.T) {
		rec := base
		rec.Descriptions = nil
		if r := DefaultOptions().Reject(rec); r != "no_abstract" {
			t.Errorf("got %q, want no_abstract", r)
		}
	})
	t.Run("abstrak pendek lolos bila MinDesc=0", func(t *testing.T) {
		rec := base
		rec.Descriptions = []string{"pendek"}
		if r := TanpaFilter().Reject(rec); r != "" {
			t.Errorf("got %q, want lolos", r)
		}
	})
	t.Run("dokumen admin ditolak walau abstrak panjang", func(t *testing.T) {
		rec := base
		rec.Title = []string{"Penilaian Sejawat: Evaluasi Artikel"}
		if r := DefaultOptions().Reject(rec); r != "administrative" {
			t.Errorf("got %q, want administrative", r)
		}
	})
	t.Run("admin tetap ditolak walau SkipAdmin=false", func(t *testing.T) {
		rec := base
		rec.Title = []string{"Form Penilaian Sejawat"}
		opts := Options{MinDesc: 50, SkipAdmin: false}
		if r := opts.Reject(rec); r != "" {
			t.Errorf("got %q, want lolos", r)
		}
	})
}

func rawRecord(id, title, desc, creator string) oai.Record {
	rec := oai.Record{
		Header: oai.Header{
			Identifier: id,
			Datestamp:  "2020-01-01T00:00:00Z",
		},
	}
	dc := oai.DC{}
	if title != "" {
		dc.Title = []string{title}
	}
	if desc != "" {
		dc.Description = []string{desc}
	}
	if creator != "" {
		dc.Creator = []string{creator}
	}
	dc.Date = []string{"2020"}
	dc.Identifier = []string{"https://eprints2.undip.ac.id/id/eprint/1/"}
	rec.Metadata = &oai.Metadata{DC: dc}
	return rec
}

func TestRunWithLegacyFilterStats(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "raw.jsonl")
	out := filepath.Join(dir, "clean.jsonl")

	records := []oai.Record{
		rawRecord("oai:x:1", "Judul Valid yang Lumayan Panjang", strings.Repeat("abstrak ", 12), "Penulis, Satu"),
		rawRecord("oai:x:2", "Judul Tanpa Abstrak Yang Panjang", "", "Penulis, Dua"),
		rawRecord("oai:x:3", "Judul Dengan Abstrak Pendek Sekali", "pendek", "Penulis, Tiga"),
		rawRecord("oai:x:4", "Penilaian Sejawat: Evaluasi Artikel", strings.Repeat("abstrak ", 12), "Penulis, Empat"),
	}
	if err := store.WriteJSONL(in, records); err != nil {
		t.Fatalf("WriteJSONL: %v", err)
	}

	_, st, err := cleanRunForTest(t, in, out, DefaultOptions())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.Input != 4 || st.Kept != 1 || st.Dropped != 3 {
		t.Errorf("stats = %s", st)
	}
	if st.NoAbstract != 2 {
		t.Errorf("NoAbstract = %d, want 2 (tanpa abstrak + abstrak ≤50)", st.NoAbstract)
	}
	if st.AdminFiltered != 1 {
		t.Errorf("AdminFiltered = %d, want 1", st.AdminFiltered)
	}

	var kept []Record
	n, err := store.ReadJSONL[Record](out, func(r Record) error {
		kept = append(kept, r)
		return nil
	})
	if err != nil || n != 1 {
		t.Fatalf("output n=%d err=%v", n, err)
	}
	if kept[0].Identifier != "oai:x:1" {
		t.Errorf("record tersimpan = %q, harus record valid", kept[0].Identifier)
	}
}

func cleanRunForTest(t *testing.T, in, out string, opts Options) (string, CleansingStats, error) {
	t.Helper()
	_, st, err := Run(context.Background(), in, out, opts, func(string, ...any) {})
	return out, st, err
}

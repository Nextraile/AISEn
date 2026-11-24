package clean

import (
	"strings"
	"unicode/utf8"
)

// Options menentukan penyaringan record berbasis metadata.
//
// Nilai default meniru persis proyek legacy semantic-search-undip
// (data_harvesting.py): hanya record yang punya abstrak (dc:description
// pertama) lebih dari 50 karakter DAN bukan dokumen administratif.
type Options struct {
	// MinDesc adalah panjang minimal deskripsi/abstrak pertama dalam
	// karakter Unicode (setelah trim). 0 berarti tanpa syarat panjang.
	MinDesc int

	// SkipAdmin membuang dokumen administratif: judul mengandung
	// "penilaian sejawat" atau URL mengandung "peer_review".
	SkipAdmin bool
}

// DefaultOptions mengembalikan filter metadata legacy (abstrak > 50 char,
// bukan dokumen administratif) — sama seperti proyek FastAPI sebelumnya.
func DefaultOptions() Options {
	return Options{MinDesc: 50, SkipAdmin: true}
}

// TanpaFilter menonaktifkan seluruh penyaringan metadata
// (semua record lolos; hanya dedup & record rusak yang dibuang).
func TanpaFilter() Options { return Options{} }

// LooksAdministrative meniru aturan legacy:
//
//	"penilaian sejawat" in title.lower() or "peer_review" in url.lower()
func LooksAdministrative(title string, urls []string) bool {
	if strings.Contains(strings.ToLower(title), "penilaian sejawat") {
		return true
	}
	for _, u := range urls {
		if strings.Contains(strings.ToLower(u), "peer_review") {
			return true
		}
	}
	return false
}

// Reject mengembalikan alasan record ditolak filter metadata, atau ""
// bila lolos. Aturan (port data_harvesting.py):
//   - SkipAdmin: buang dokumen administratif
//   - MinDesc  : wajib punya abstrak pertama > MinDesc karakter
func (o Options) Reject(rec Record) string {
	if o.SkipAdmin && len(rec.Title) > 0 && LooksAdministrative(rec.Title[0], rec.URLs) {
		return "administrative"
	}
	if o.MinDesc > 0 {
		if len(rec.Descriptions) == 0 {
			return "no_abstract"
		}
		// Legacy: len(description.strip()) > 50 (Python str = rune).
		if utf8.RuneCountInString(strings.TrimSpace(rec.Descriptions[0])) <= o.MinDesc {
			return "short_abstract"
		}
	}
	return ""
}

// Allow memutuskan apakah record lolos filter metadata.
func (o Options) Allow(rec Record) bool { return o.Reject(rec) == "" }

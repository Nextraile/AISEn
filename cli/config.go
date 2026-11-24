// Package cli berisi perintah-perintah CLI untuk pipeline
// harvest -> clean -> normalize -> vectorize -> index -> search.
//
// Package ini hanya boleh di-import oleh package main (entry point);
// semua dependensi lain berada di awesomeProject/internal/* sehingga
// tidak ada siklus import.
package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"awesomeProject/internal/progress"
)

// Lokasi data & endpoint default. Path bersifat relatif terhadap working
// directory (di dalam container: /app, dengan /app/data sebagai volume).
const (
	DefaultRawFile     = "data/raw/records.jsonl"
	DefaultStateFile   = "data/harvest_state.json"
	DefaultCleanFile   = "data/clean/records.jsonl"
	DefaultNormFile    = "data/normalized/records.jsonl"
	DefaultVectorFile  = "data/vectors/vectors.bin"
	DefaultModelFile   = "data/vectors/model.json"
	DefaultEngineMeta  = "data/vectors/engine.json"
	DefaultSetsFile    = "data/sets.jsonl"
	DefaultQdrantURL   = "http://127.0.0.1:6333"
	DefaultCollection  = "undip_eprints"
	DefaultOAIEndpoint = "https://eprints2.undip.ac.id/cgi/oai2"
)

// ErrUsage dipakai package main untuk keluar dengan exit code 2
// ketika flag/perintah salah.
var ErrUsage = errors.New("usage")

// Printf menulis log progres ke stdout — dipakai seluruh perintah CLI.
// Setelah ditulis, pesan juga dikirim ke progress.Hook bila di-set
// (dipakai REST server untuk update field Progress pada Job).
func Printf(format string, args ...any) {
	msg := fmt.Sprintf(format+"\n", args...)
	fmt.Print(msg)
	progress.Notify(strings.TrimSpace(msg))
}

// EnvOr mengembalikan nilai environment key, atau def bila kosong/tidak ada.
// Dipakai mis. untuk QDRANT_URL di lingkungan container.
func EnvOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// LoadDotEnv memuat variabel dari file .env di direktori kerja (jika ada)
// ke environment, TANPA menimpa env var yang sudah di-set sebelumnya
// (env nyata selalu lebih prioritas). Format sederhana: KEY=VALUE, baris
// kosong / "#" diabaikan, tanda kutip tunggal/ganda dihapus.
// Dipakai agar user bisa menyimpan GEMINI_API_KEY dll. di file .env
// (stdlib-only, tanpa dependensi eksternal).
func LoadDotEnv(path string) {
	if path == "" {
		path = ".env"
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		if key == "" {
			continue
		}
		// buang komentar trailing " # ..." (opsional)
		// buang tanda kutip
		if len(val) >= 2 && ((val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'')) {
			val = val[1 : len(val)-1]
		}
		if os.Getenv(key) == "" {
			_ = os.Setenv(key, val)
		}
	}
}

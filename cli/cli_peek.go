package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"awesomeProject/internal/store"
)

// CmdPeek menampilkan isi file pipeline untuk inspeksi cepat:
// JSONL ditalan-kan (pretty print), file vektor biner ditampilkan
// berupa daftar id + jumlah dimensi non-nol.
func CmdPeek(ctx context.Context, args []string) error {
	var file string
	var n int
	var countOnly bool

	fs := flag.NewFlagSet("peek", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Penggunaan: go run . peek -file data/raw/records.jsonl [-n 3]")
		fs.PrintDefaults()
	}
	fs.StringVar(&file, "file", DefaultRawFile, "file yang mau dilihat (jsonl/bin)")
	fs.IntVar(&n, "n", 3, "jumlah record yang ditampilkan")
	fs.BoolVar(&countOnly, "count", false, "hanya tampilkan jumlah record")
	if err := fs.Parse(args); err != nil {
		return ErrUsage
	}
	if n < 0 {
		n = 0
	}

	if isBinaryVectorFile(file) {
		return peekVectors(ctx, file, n, countOnly)
	}

	shown := 0
	count, err := store.ReadJSONL[json.RawMessage](file, func(raw json.RawMessage) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if countOnly || shown >= n {
			return nil
		}
		var buf bytes.Buffer
		if err := json.Indent(&buf, raw, "  ", "  "); err != nil {
			return err
		}
		fmt.Printf("# record %d\n%s\n", shown+1, buf.String())
		shown++
		return nil
	})
	if err != nil {
		return err
	}
	if countOnly {
		Printf("%s: %d record", file, count)
		return nil
	}
	Printf("\n%s: %d record total (menampilkan %d, ukuran %.1f KB)",
		file, count, shown, float64(store.FileSize(file))/1024)
	return nil
}

func peekVectors(ctx context.Context, file string, n int, countOnly bool) error {
	src, err := store.OpenVectors(file)
	if err != nil {
		return err
	}
	defer src.Close()

	shown, total, nnzTotal := 0, 0, 0
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		id, vec, err := src.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		nnz := 0
		for _, v := range vec {
			if v != 0 {
				nnz++
			}
		}
		nnzTotal += nnz
		total++
		if !countOnly && shown < n {
			Printf("row %d: id=%s dim=%d nnz=%d", shown, id, len(vec), nnz)
			shown++
		}
	}
	if countOnly {
		Printf("%s: %d record (dim=%d)", file, total, src.Dim())
		return nil
	}
	avg := 0.0
	if total > 0 {
		avg = float64(nnzTotal) / float64(total)
	}
	Printf("\n%s: %d record, dim=%d, nnz rata-rata=%.1f, ukuran %.1f KB",
		file, total, src.Dim(), avg, float64(store.FileSize(file))/1024)
	return nil
}

func isBinaryVectorFile(path string) bool {
	switch ext := extensionOf(path); ext {
	case ".bin":
		return true
	default:
		return false
	}
}

func extensionOf(path string) string {
	for i := len(path) - 1; i >= 0 && path[i] != '/' && path[i] != '\\'; i-- {
		if path[i] == '.' {
			return path[i:]
		}
	}
	return ""
}

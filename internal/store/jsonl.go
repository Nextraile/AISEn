// Package store menyediakan persistensi file untuk pipeline:
// JSONL untuk data teks, dan format biner ringkas untuk vektor.
package store

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// EnsureDir membuat direktori induk dari path file.
func EnsureDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}

// WriteJSONL menulis items ke path (menimpa), satu objek per baris.
func WriteJSONL[T any](path string, items []T) error {
	if err := EnsureDir(path); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 1<<20)
	enc := json.NewEncoder(w)
	for _, it := range items {
		if err := enc.Encode(it); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return f.Close()
}

// AppendJSONL menambahkan items ke akhir file (dibuat bila belum ada).
func AppendJSONL[T any](path string, items []T) error {
	if err := EnsureDir(path); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 1<<20)
	enc := json.NewEncoder(w)
	for _, it := range items {
		if err := enc.Encode(it); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return f.Close()
}

// JSONLWriter menulis JSONL secara streaming (satu file, banyak baris).
// Lebih efisien daripada AppendJSONL per record karena file tidak dibuka-tutup.
type JSONLWriter[T any] struct {
	f     *os.File
	w     *bufio.Writer
	enc   *json.Encoder
	count int
}

// NewJSONLWriter membuat/menimpa file JSONL untuk ditulis berurutan.
func NewJSONLWriter[T any](path string) (*JSONLWriter[T], error) {
	if err := EnsureDir(path); err != nil {
		return nil, err
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	return &JSONLWriter[T]{f: f, w: w, enc: json.NewEncoder(w)}, nil
}

// Write menulis satu record.
func (j *JSONLWriter[T]) Write(v T) error {
	if err := j.enc.Encode(v); err != nil {
		return err
	}
	j.count++
	return nil
}

// Count jumlah record yang sudah ditulis.
func (j *JSONLWriter[T]) Count() int { return j.count }

// Close flush buffer dan tutup file.
func (j *JSONLWriter[T]) Close() error {
	if err := j.w.Flush(); err != nil {
		j.f.Close()
		return err
	}
	return j.f.Close()
}

// ReadJSONL membaca JSONL secara streaming (hemat memori) dan meneruskan
// tiap record ke fn. Mengembalikan jumlah record yang dibaca.
func ReadJSONL[T any](path string, fn func(T) error) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	r := bufio.NewReaderSize(f, 1<<20)
	dec := json.NewDecoder(r)
	count := 0
	for {
		var v T
		err := dec.Decode(&v)
		if errors.Is(err, io.EOF) {
			return count, nil
		}
		if err != nil {
			return count, fmt.Errorf("%s: baris ke-%d: %w", path, count+1, err)
		}
		if err := fn(v); err != nil {
			return count, err
		}
		count++
	}
}

// CountJSONL menghitung jumlah baris objek di file JSONL.
func CountJSONL(path string) (int, error) {
	return ReadJSONL[json.RawMessage](path, func(json.RawMessage) error { return nil })
}

// FileSize mengembalikan ukuran file dalam byte (0 bila tidak ada).
func FileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

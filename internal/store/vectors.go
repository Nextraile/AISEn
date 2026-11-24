package store

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// Format vektor: JSONL (mudah diinspeksi) atau biner (ringkas, untuk korpus besar).
const (
	FormatJSONL = "jsonl"
	FormatBin   = "bin"
)

// vectorMagic mengidentifikasi file biner vektor buatan tool ini.
var vectorMagic = [4]byte{'U', 'D', 'V', '1'}

const vectorHeaderSize = 4 + 4 + 4 + 4 + 4 // magic + version + dim + count + reserved

// VectorWriter menulis baris vektor berformat biner:
//
//	header : magic(4) | version(u32) | dim(u32) | count(u32) | reserved(u32)
//	record : idLen(u16) | id(idLen byte) | nnz(u32) | nnz * {idx(u32), value(f32)}
//
// Penyimpanan memakai entri non-nol saja karena vektor TF-IDF jarang (sparse).
type VectorWriter struct {
	f      *os.File
	w      *bufio.Writer
	dim    uint32
	count  uint32
	closed bool
}

// NewVectorWriter membuat file biner baru dan menulis header awal
// (count diperbarui saat Close()).
func NewVectorWriter(path string, dim int) (*VectorWriter, error) {
	if dim <= 0 {
		return nil, fmt.Errorf("dim harus > 0, dapat %d", dim)
	}
	if err := EnsureDir(path); err != nil {
		return nil, err
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	vw := &VectorWriter{f: f, w: bufio.NewWriterSize(f, 1<<20), dim: uint32(dim)}
	if err := vw.writeHeader(0); err != nil {
		f.Close()
		return nil, err
	}
	// writeHeader memakai WriteAt (tidak menggeser offset tulis),
	// jadi posisi tulis harus dipindahkan manual ke akhir header.
	if _, err := f.Seek(vectorHeaderSize, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	return vw, nil
}

func (v *VectorWriter) writeHeader(count uint32) error {
	buf := make([]byte, vectorHeaderSize)
	copy(buf[0:4], vectorMagic[:])
	binary.LittleEndian.PutUint32(buf[4:8], 1) // version
	binary.LittleEndian.PutUint32(buf[8:12], v.dim)
	binary.LittleEndian.PutUint32(buf[12:16], count)
	// reserved[16:20] tetap nol
	_, err := v.f.WriteAt(buf, 0)
	return err
}

// Write menambah satu baris vektor (idx harus unik dan < dim).
func (v *VectorWriter) Write(id string, idx []uint32, val []float32) error {
	if v.closed {
		return errors.New("VectorWriter sudah ditutup")
	}
	if len(id) > math.MaxUint16 {
		return fmt.Errorf("id terlalu panjang: %d", len(id))
	}
	if len(idx) != len(val) {
		return fmt.Errorf("idx/val tidak sama panjang: %d vs %d", len(idx), len(val))
	}
	if uint32(len(idx)) > v.dim {
		return fmt.Errorf("nnz melebihi dim: %d > %d", len(idx), v.dim)
	}

	var hdr [6]byte
	binary.LittleEndian.PutUint16(hdr[0:2], uint16(len(id)))
	binary.LittleEndian.PutUint32(hdr[2:6], uint32(len(idx)))
	if _, err := v.w.Write(hdr[:]); err != nil {
		return err
	}
	if _, err := v.w.WriteString(id); err != nil {
		return err
	}
	scratch := make([]byte, 8)
	for i := range idx {
		if idx[i] >= v.dim {
			return fmt.Errorf("idx %d >= dim %d", idx[i], v.dim)
		}
		binary.LittleEndian.PutUint32(scratch[0:4], idx[i])
		binary.LittleEndian.PutUint32(scratch[4:8], math.Float32bits(val[i]))
		if _, err := v.w.Write(scratch); err != nil {
			return err
		}
	}
	v.count++
	return nil
}

// Close menulis ulang jumlah record pada header lalu menutup file.
func (v *VectorWriter) Close() error {
	if v.closed {
		return nil
	}
	v.closed = true
	if err := v.w.Flush(); err != nil {
		v.f.Close()
		return err
	}
	if err := v.writeHeader(v.count); err != nil {
		v.f.Close()
		return err
	}
	return v.f.Close()
}

// Count mengembalikan jumlah record yang sudah ditulis.
func (v *VectorWriter) Count() int { return int(v.count) }

// VectorSource membaca baris vektor secara streaming dan
// mengembalikannya sebagai vektor padat (dense) siap kirim ke Qdrant.
type VectorSource interface {
	// Next mengembalikan (id, dense vector, nil) atau io.EOF di akhir.
	Next() (string, []float32, error)
	Dim() int
	Count() int // 0 bila tidak diketahui sebelum selesai dibaca
	Close() error
}

type binVectorSource struct {
	f     *os.File
	r     *bufio.Reader
	dim   int
	count int
	read  int
	buf   []byte
}

// OpenVectors membuka file vektor (.bin atau .jsonl) berdasarkan ekstensi.
func OpenVectors(path string) (VectorSource, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jsonl", ".json":
		return openJSONLVectors(path)
	default:
		return openBinVectors(path)
	}
}

func openBinVectors(path string) (VectorSource, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	r := bufio.NewReaderSize(f, 1<<20)
	hdr := make([]byte, vectorHeaderSize)
	if _, err := io.ReadFull(r, hdr); err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: header tidak terbaca: %w", path, err)
	}
	if string(hdr[0:4]) != string(vectorMagic[:]) {
		f.Close()
		return nil, fmt.Errorf("%s: bukan file vektor biner yang dikenal", path)
	}
	dim := int(binary.LittleEndian.Uint32(hdr[8:12]))
	count := int(binary.LittleEndian.Uint32(hdr[12:16]))
	return &binVectorSource{f: f, r: r, dim: dim, count: count, buf: make([]byte, 8)}, nil
}

func (b *binVectorSource) Dim() int   { return b.dim }
func (b *binVectorSource) Count() int { return b.count }

func (b *binVectorSource) Next() (string, []float32, error) {
	var idLenHdr [6]byte
	if _, err := io.ReadFull(b.r, idLenHdr[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return "", nil, io.EOF
		}
		return "", nil, err
	}
	idLen := int(binary.LittleEndian.Uint16(idLenHdr[0:2]))
	nnz := int(binary.LittleEndian.Uint32(idLenHdr[2:6]))
	if nnz < 0 || nnz > b.dim {
		return "", nil, fmt.Errorf("nnz tidak wajar: %d (dim %d)", nnz, b.dim)
	}
	idBytes := make([]byte, idLen)
	if _, err := io.ReadFull(b.r, idBytes); err != nil {
		return "", nil, fmt.Errorf("gagal baca id: %w", err)
	}
	vec := make([]float32, b.dim)
	for i := 0; i < nnz; i++ {
		if _, err := io.ReadFull(b.r, b.buf); err != nil {
			return "", nil, fmt.Errorf("gagal baca entri vektor %d/%d: %w", i, nnz, err)
		}
		idx := binary.LittleEndian.Uint32(b.buf[0:4])
		if int(idx) >= b.dim {
			return "", nil, fmt.Errorf("idx %d >= dim %d", idx, b.dim)
		}
		vec[idx] = math.Float32frombits(binary.LittleEndian.Uint32(b.buf[4:8]))
	}
	b.read++
	return string(idBytes), vec, nil
}

func (b *binVectorSource) Close() error { return b.f.Close() }

type jsonlVectorLine struct {
	ID     string    `json:"id"`
	Vector []float32 `json:"vector"`
}

type jsonlVectorSource struct {
	f     *os.File
	dec   *json.Decoder
	dim   int
	read  int
	count int
}

func openJSONLVectors(path string) (VectorSource, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return &jsonlVectorSource{f: f, dec: json.NewDecoder(bufio.NewReaderSize(f, 1<<20))}, nil
}

func (j *jsonlVectorSource) Dim() int   { return j.dim }
func (j *jsonlVectorSource) Count() int { return j.count }

func (j *jsonlVectorSource) Next() (string, []float32, error) {
	var line jsonlVectorLine
	if err := j.dec.Decode(&line); err != nil {
		if errors.Is(err, io.EOF) {
			return "", nil, io.EOF
		}
		return "", nil, err
	}
	if j.dim == 0 {
		j.dim = len(line.Vector)
	} else if len(line.Vector) != j.dim {
		return "", nil, fmt.Errorf("dimensi tidak konsisten pada %q: %d != %d", line.ID, len(line.Vector), j.dim)
	}
	j.read++
	return line.ID, line.Vector, nil
}

func (j *jsonlVectorSource) Close() error { return j.f.Close() }

// JSONLVectorWriter menulis vektor padat ke JSONL (untuk inspeksi/debug).
type JSONLVectorWriter struct {
	f     *os.File
	w     *bufio.Writer
	count int
}

func NewJSONLVectorWriter(path string) (*JSONLVectorWriter, error) {
	if err := EnsureDir(path); err != nil {
		return nil, err
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &JSONLVectorWriter{f: f, w: bufio.NewWriterSize(f, 1<<20)}, nil
}

func (j *JSONLVectorWriter) Write(id string, _ []uint32, vec []float32) error {
	line := jsonlVectorLine{ID: id, Vector: vec}
	b, err := json.Marshal(line)
	if err != nil {
		return err
	}
	if _, err := j.w.Write(append(b, '\n')); err != nil {
		return err
	}
	j.count++
	return nil
}

func (j *JSONLVectorWriter) Close() error {
	if err := j.w.Flush(); err != nil {
		j.f.Close()
		return err
	}
	return j.f.Close()
}

func (j *JSONLVectorWriter) Count() int { return j.count }

// VectorSink adalah interface umum penulis vektor (biner maupun JSONL).
type VectorSink interface {
	Write(id string, idx []uint32, val []float32) error
	Close() error
	Count() int
}

// NewVectorSink membuat sink sesuai format ("bin" atau "jsonl").
func NewVectorSink(path, format string, dim int) (VectorSink, error) {
	switch strings.ToLower(format) {
	case FormatJSONL:
		return NewJSONLVectorWriter(path)
	case FormatBin, "":
		return NewVectorWriter(path, dim)
	default:
		return nil, fmt.Errorf("format vektor tidak dikenal %q (pakai bin|jsonl)", format)
	}
}

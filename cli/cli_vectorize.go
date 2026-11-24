package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"awesomeProject/internal/embed"
	"awesomeProject/internal/normalize"
	"awesomeProject/internal/semantic"
	"awesomeProject/internal/store"
)

// modelParamsFromFlags membaca flag parameter TF-IDF.
type modelParamsFromFlags struct {
	dim         int
	minDF       int
	maxFeatures int
	ngram       int
	noSublinear bool
}

func addModelFlags(fs *flag.FlagSet, m *modelParamsFromFlags) {
	d := embed.DefaultParams()
	fs.IntVar(&m.dim, "dim", d.Dim, "dimensi vektor (feature hashing)")
	fs.IntVar(&m.minDF, "min-df", d.MinDF, "minimal document frequency sebuah term")
	fs.IntVar(&m.maxFeatures, "max-features", d.MaxFeatures, "maksimal ukuran kosakata")
	fs.IntVar(&m.ngram, "ngram", d.Ngram, "1 = unigram, 2 = unigram+bigram")
	fs.BoolVar(&m.noSublinear, "no-sublinear", false, "pakai tf mentah, bukan 1+ln(tf)")
}

func (m modelParamsFromFlags) params() embed.Params {
	return embed.Params{
		Dim:         m.dim,
		MinDF:       m.minDF,
		MaxFeatures: m.maxFeatures,
		Ngram:       m.ngram,
		SublinearTF: !m.noSublinear,
	}
}

// engineMeta adalah metadata engine embedding yang dipakai vectorize.
type engineMeta struct {
	Engine   string `json:"engine"`
	ModelDir string `json:"model_dir"`
	Dim      int    `json:"dim"`
	BuiltAt  string `json:"built_at"`
}

func writeEngineMeta(path, engine, modelDir string, dim int) error {
	m := engineMeta{Engine: engine, ModelDir: modelDir, Dim: dim, BuiltAt: time.Now().Format(time.RFC3339)}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func ReadEngineMeta(path string) (*engineMeta, error) { return readEngineMeta(path) }

func readEngineMeta(path string) (*engineMeta, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m engineMeta
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func filesExist(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// resolveEngine mengembalikan engine yang dipakai: flag eksplisit
// ("tfidf"|"onnx") atau "auto" (onnx bila model tersedia, else tfidf).
func resolveEngine(flagVal, modelDir string) string {
	switch strings.ToLower(strings.TrimSpace(flagVal)) {
	case "", "auto":
		if filesExist(modelDir+"/tokenizer.json") && filesExist(modelDir+"/model_optimized.onnx") {
			return semantic.EncoderName
		}
		return "tfidf"
	case "tfidf", "onnx":
		return strings.ToLower(flagVal)
	}
	return strings.ToLower(flagVal)
}

// textStreamer mengembalikan fungsi iterasi teks embedding dari file dokumen.
func textStreamer(path string) embed.TextSource {
	return func(yield func(string) error) error {
		_, err := store.ReadJSONL[normalize.Doc](path, func(d normalize.Doc) error {
			if d.Text == "" {
				return nil
			}
			return yield(d.Text)
		})
		return err
	}
}

// docStream mengalirkan (id, teks) tiap dokumen untuk Transform.
// Semua dokumen ikut (termasuk teks kosong) agar jumlah baris vektor
// selalu sama dengan jumlah dokumen — prasyarat pasangan di tahap index.
func docStream(path string) embed.DocStream {
	return func(yield func(id, text string) error) error {
		_, err := store.ReadJSONL[normalize.Doc](path, func(d normalize.Doc) error {
			return yield(d.ID, d.Text)
		})
		return err
	}
}

// writeVectors transformasi dokumen -> file vektor (paralel per-batch).
func writeVectors(ctx context.Context, m *embed.Model, in, out, format string) error {
	sink, err := store.NewVectorSink(out, format, m.Dim())
	if err != nil {
		return err
	}
	defer sink.Close()

	total, _ := store.CountJSONL(in)
	Printf("vektorisasi TF-IDF: %d dokumen, dim=%d, batch=%d, workers=%d",
		total, m.Dim(), embed.BatchSize, runtime.GOMAXPROCS(0))

	count, empty, err := m.Transform(ctx, docStream(in), sink.Write, func(c int) {
		fmt.Print(progressBar(c, total, 30))
	})
	if err != nil {
		return fmt.Errorf("gagal menulis vektor: %w", err)
	}
	fmt.Println()
	if err := sink.Close(); err != nil {
		return err
	}
	Printf("vektor ditulis: %d record (%d kosong) -> %s [format=%s, %.1f KB]",
		count, empty, out, format, float64(store.FileSize(out))/1024)
	return nil
}

// denseToSparse mengubah vektor dense (384-dim) menjadi sparse (idx/val)
// untuk penulisan ke file vektor biner/JSONL.
func denseToSparse(vec []float32) ([]uint32, []float32) {
	idx := make([]uint32, 0, len(vec))
	val := make([]float32, 0, len(vec))
	for i, v := range vec {
		if v != 0 {
			idx = append(idx, uint32(i))
			val = append(val, v)
		}
	}
	return idx, val
}

// writeSemanticVectors menulis file vektor dari encoder ONNX (paralel
// batch + ordered write).
func writeSemanticVectors(ctx context.Context, e *semantic.Encoder, in, out, format string) error {
	sink, err := store.NewVectorSink(out, format, e.Dim())
	if err != nil {
		return err
	}
	defer sink.Close()

	total, _ := store.CountJSONL(in)
	Printf("vektorisasi ONNX: %d dokumen, dim=%d, batch=%d, workers=%d",
		total, e.Dim(), semantic.EncodeBatchSize, runtime.GOMAXPROCS(0))

	var buf []normalize.Doc
	done := 0
	flush := func() error {
		if len(buf) == 0 {
			return nil
		}
		texts := make([]string, len(buf))
		for i, d := range buf {
			texts[i] = d.Text
		}
		vecs, err := e.Encode(ctx, texts)
		if err != nil {
			return err
		}
		for i, d := range buf {
			idx, val := denseToSparse(vecs[i])
			if err := sink.Write(d.ID, idx, val); err != nil {
				return err
			}
		}
		done += len(buf)
		buf = buf[:0]
		fmt.Print(progressBar(done, total, 30))
		return nil
	}

	_, err = store.ReadJSONL[normalize.Doc](in, func(d normalize.Doc) error {
		buf = append(buf, d)
		if len(buf) >= semantic.EncodeBatchSize {
			return flush()
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("baca dokumen untuk vektor: %w", err)
	}
	if err := flush(); err != nil {
		return err
	}
	if err := sink.Close(); err != nil {
		return err
	}
	fmt.Println()
	Printf("vektor ditulis: %d record -> %s [format=%s, %.1f KB]",
		sink.Count(), out, format, float64(store.FileSize(out))/1024)
	return nil
}

// runVectorize adalah inti perintah vectorize (dipakai juga oleh pipeline).
// Engine: tfidf -> fit TF-IDF dari korpus; onnx -> encode via ONNX model.
func runVectorize(ctx context.Context, in, out, modelPath, format string, p embed.Params, engine, modelDir string) error {
	start := time.Now()
	switch resolveEngine(engine, modelDir) {
	case semantic.EncoderName:
		return runVectorizeOnnx(ctx, in, out, modelDir, format)
	case "tfidf":
		if err := p.Validate(); err != nil {
			return err
		}

		// Tahap 1: fit model TF-IDF dari korpus teks (streaming, paralel per-batch).
		Printf("vektorisasi: fit model dari %s ...", in)
		model, err := embed.Fit(textStreamer(in), p, Printf)
		if err != nil {
			return err
		}
		if err := model.Save(modelPath); err != nil {
			return fmt.Errorf("gagal simpan model: %w", err)
		}
		Printf("model disimpan: %s (%d dokumen, %d term, dim=%d)",
			modelPath, model.DocCount, len(model.IDF), model.Dim())

		// Tahap 2: transformasi tiap dokumen menjadi vektor (goroutine per-batch).
		if err := writeVectors(ctx, model, in, out, format); err != nil {
			return err
		}
		Printf("vektorisasi selesai dalam %s", time.Since(start).Round(time.Millisecond))
		return nil
	default:
		return fmt.Errorf("engine tidak dikenal %q (pilih: tfidf | onnx | auto)", engine)
	}
}

// runVectorizeOnnx adalah jalur encoder ONNX: buka model, encode dokumen,
// tulis file vektor (dim=384) + engine.json metadata.
func runVectorizeOnnx(ctx context.Context, in, out, modelDir, format string) error {
	start := time.Now()
	e, err := semantic.Open(modelDir)
	if err != nil {
		return err
	}
	defer e.Close()
	if err := writeSemanticVectors(ctx, e, in, out, format); err != nil {
		return err
	}
	if err := writeEngineMeta(filepath.Join(filepath.Dir(out), "engine.json"), semantic.EncoderName, modelDir, e.Dim()); err != nil {
		return err
	}
	Printf("vektorisasi selesai dalam %s", time.Since(start).Round(time.Millisecond))
	return nil
}

func CmdVectorize(ctx context.Context, args []string) error {
	var in, out, modelPath, format string
	var mp modelParamsFromFlags
	var rebuildModel bool
	var engine, modelDir string

	fs := flag.NewFlagSet("vectorize", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Penggunaan: go run . vectorize [flags]")
		fs.PrintDefaults()
	}
	fs.StringVar(&in, "in", DefaultNormFile, "file JSONL dokumen ternormalisasi")
	fs.StringVar(&out, "out", DefaultVectorFile, "file output vektor")
	fs.StringVar(&modelPath, "model", DefaultModelFile, "file model TF-IDF (JSON)")
	fs.StringVar(&format, "format", store.FormatBin, "format vektor: bin | jsonl")
	fs.BoolVar(&rebuildModel, "rebuild-model", false, "fit ulang model walau file sudah ada")
	fs.StringVar(&engine, "engine", "auto", "engine vektorisasi: tfidf | onnx | auto (default auto -> onnx bila model tersedia)")
	fs.StringVar(&modelDir, "model-dir", semantic.DefaultModelDir, "direktori model ONNX (untuk engine onnx)")
	addModelFlags(fs, &mp)
	if err := fs.Parse(args); err != nil {
		return ErrUsage
	}
	if format != store.FormatBin && format != store.FormatJSONL {
		return fmt.Errorf("-format harus bin atau jsonl, dapat %q", format)
	}

	eng := resolveEngine(engine, modelDir)
	switch eng {
	case semantic.EncoderName:
		return runVectorizeOnnx(ctx, in, out, modelDir, format)
	case "tfidf":
		p := mp.params()
		if !rebuildModel {
			if m, err := embed.LoadModel(modelPath); err == nil {
				Printf("memakai model lama: %s (dibuat %s, dokumen=%d, dim=%d)",
					modelPath, m.BuiltAt.Format(time.RFC3339), m.DocCount, m.Dim())
				if m.Params != p {
					Printf("catatan: flag parameter berbeda dari model -> memakai parameter model")
				}
				return reuseModelVectorize(ctx, m, in, out, format)
			} else if !errors.Is(err, os.ErrNotExist) {
				Printf("model lama tidak bisa dipakai (%v) -> fit ulang", err)
			}
		}
		return runVectorize(ctx, in, out, modelPath, format, p, "tfidf", modelDir)
	default:
		return fmt.Errorf("engine tidak dikenal %q (pilih: tfidf | onnx | auto)", eng)
	}
}

// reuseModelVectorize melewati tahap fit dan langsung transformasi (paralel).
func reuseModelVectorize(ctx context.Context, m *embed.Model, in, out, format string) error {
	return writeVectors(ctx, m, in, out, format)
}

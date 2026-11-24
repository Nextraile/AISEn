// Package semantic menyediakan encoder vektor berbasis ONNX Runtime
// (model multilingual MiniLM-L12 dari Qdrant, ONNX int8) + tokenizer
// HuggingFace (sugarme/tokenizer, pure Go).
//
// Ini adalah pengganti TF-IDF untuk tahap vektorisasi, meniru perilaku
// model sentence-transformers/paraphrase-multilingual-MiniLM-L12-v2 yang
// dipakai proyek legacy semantic-search-undip. Encoder menghasilkan vektor
// 384-dim (mean pooling + L2 normalize).
package semantic

import (
	"context"
	"fmt"
	"math"
	"os"
	"runtime"
	"strings"
	"sync"

	"github.com/sugarme/tokenizer"
	"github.com/sugarme/tokenizer/pretrained"
	ort "github.com/yalue/onnxruntime_go"
)

const (
	// DefaultModelDir lokasi model ONNX + tokenizer di dalam repo.
	DefaultModelDir = "data/models/multilingual-minilm-l12"

	// EncoderName identifikasi engine embedding yang dipakai.
	EncoderName = "onnx"

	// HiddenSize dimensi output model multilingual MiniLM-L12 (termasuk
	// versi int8) — konsisten dengan model legacy.
	HiddenSize = 384

	// DefaultMaxLen batas panjang urutan token (sesuai truncation
	// tokenizer.json: max_length=128).
	DefaultMaxLen = 128

	// EncodeBatchSize ukuran batch inference ke ONNX Runtime.
	EncodeBatchSize = 8
)

var (
	ortInitOnce sync.Once
	ortInitErr  error
)

// Encoder adalah wrapper ONNX Runtime + tokenizer HuggingFace.
type Encoder struct {
	modelDir string
	tok      *tokenizer.Tokenizer
	sess     *ort.DynamicAdvancedSession
	dim      int
	maxLen   int
}

var errEncoderClosed = fmt.Errorf("encoder ONNX sudah ditutup")

// Open membuka tokenizer dan session ONNX dari sebuah direktori model.
// Direktori harus berisi tokenizer.json dan model_optimized.onnx.
func Open(modelDir string) (*Encoder, error) {
	if modelDir == "" {
		modelDir = DefaultModelDir
	}
	lib := os.Getenv("ONNXRUNTIME_SHARED_LIBRARY_PATH")
	if lib == "" {
		for _, cand := range []string{"lib/libonnxruntime.so", "/usr/local/lib/libonnxruntime.so", "/usr/lib/libonnxruntime.so", "onnxruntime.so"} {
			if _, err := os.Stat(cand); err == nil {
				lib = cand
				break
			}
		}
	}
	ort.SetSharedLibraryPath(lib)
	ortInitOnce.Do(func() {
		ortInitErr = ort.InitializeEnvironment()
	})
	if ortInitErr != nil {
		return nil, fmt.Errorf("inialisasi ONNX Runtime: %w", ortInitErr)
	}
	sess, err := ort.NewDynamicAdvancedSession(
		modelDir+"/model_optimized.onnx",
		[]string{"input_ids", "attention_mask", "token_type_ids"},
		[]string{"last_hidden_state"},
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("buka session ONNX dari %s: %w", modelDir, err)
	}
	tk, err := pretrained.FromFile(modelDir + "/tokenizer.json")
	if err != nil {
		_ = sess.Destroy()
		return nil, fmt.Errorf("buka tokenizer dari %s: %w", modelDir, err)
	}
	return &Encoder{
		modelDir: modelDir,
		tok:      tk,
		sess:     sess,
		dim:      HiddenSize,
		maxLen:   DefaultMaxLen,
	}, nil
}

// Close merusak session & tokenizer.
func (e *Encoder) Close() error {
	if e.sess != nil {
		if err := e.sess.Destroy(); err != nil {
			return err
		}
		e.sess = nil
	}
	return nil
}

// Dim mengembalikan dimensi vektor hasil encode.
func (e *Encoder) Dim() int { return e.dim }

// MaxLen mengembalikan batas panjang token setelah truncasi.
func (e *Encoder) MaxLen() int { return e.maxLen }

// Encode mengubah sekumpulan teks menjadi vektor 384-dim L2-normalized,
// konsisten dengan model sentence-transformers/paraphrase-multilingual-MiniLM-L12-v2.
//
// Vektor disusun per teks: vektor[i] adalah vektor untuk texts[i].
// Encode dilakukan batch-per-batch lalu diproses paralel (worker per batch,
// ONNX Runtime session thread-safe) dengan hasil dikumpulkan berurutan.
// Teks kosong / tanpa token valid menghasilkan vektor nol.
func (e *Encoder) Encode(ctx context.Context, texts []string) ([][]float32, error) {
	if e.sess == nil {
		return nil, errEncoderClosed
	}
	if len(texts) == 0 {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	batches := make([][]string, 0, (len(texts)+EncodeBatchSize-1)/EncodeBatchSize)
	for i := 0; i < len(texts); i += EncodeBatchSize {
		end := i + EncodeBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		batches = append(batches, texts[i:end])
	}

	res := make([][]float32, len(texts))
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		firstErr error
	)
	jobs := make(chan int, len(batches))
	workers := runtime.GOMAXPROCS(0)
	if workers > len(batches) {
		workers = len(batches)
	}
	if workers < 1 {
		workers = 1
	}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for bi := range jobs {
				vecs, err := e.encodeBatch(ctx, batches[bi])
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					return
				}
				mu.Lock()
				start := bi * EncodeBatchSize
				for i, v := range vecs {
					res[start+i] = v
				}
				mu.Unlock()
			}
		}()
	}
	for i := range batches {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return res, nil
}

// encodeBatch mengubah satu batch teks menjadi vektor (mean pooling dari
// output ONNX + L2 normalize).
func (e *Encoder) encodeBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	encs, err := e.tok.EncodeBatch(toEncodeInputs(texts), true)
	if err != nil {
		return nil, fmt.Errorf("tokenisasi batch: %w", err)
	}
	if len(encs) != len(texts) {
		return nil, fmt.Errorf("tokenisasi: jumlah encoding %d, ingin %d", len(encs), len(texts))
	}
	batch := len(encs)
	seqLen := int64(0)
	for _, enc := range encs {
		if int64(len(enc.Ids)) > seqLen {
			seqLen = int64(len(enc.Ids))
		}
	}
	if seqLen == 0 {
		return nil, nil
	}

	idFlat := make([]int64, 0, batch*int(seqLen))
	maskFlat := make([]int64, 0, batch*int(seqLen))
	typeFlat := make([]int64, 0, batch*int(seqLen))
	for _, enc := range encs {
		encIds := enc.Ids
		encMask := enc.AttentionMask
		if len(encIds) < int(seqLen) {
			encIds = append(append([]int{}, encIds...), make([]int, int(seqLen)-len(encIds))...)
		}
		if len(encMask) < int(seqLen) {
			encMask = append(append([]int{}, encMask...), make([]int, int(seqLen)-len(encMask))...)
		}
		for s := int64(0); s < seqLen; s++ {
			id := int64(0)
			mask := int64(0)
			ty := int64(0)
			if int(s) < len(encIds) {
				id = int64(encIds[s])
			}
			if int(s) < len(encMask) {
				mask = int64(encMask[int(s)])
			}
			idFlat = append(idFlat, id)
			maskFlat = append(maskFlat, mask)
			typeFlat = append(typeFlat, ty)
		}
	}

	inputIDs, err := newInt64Tensor(ort.NewShape(int64(batch), seqLen), idFlat)
	if err != nil {
		return nil, err
	}
	attentionMask, err := newInt64Tensor(ort.NewShape(int64(batch), seqLen), maskFlat)
	if err != nil {
		_ = inputIDs.Destroy()
		return nil, err
	}
	tokenTypeIDs, err := newInt64Tensor(ort.NewShape(int64(batch), seqLen), typeFlat)
	if err != nil {
		_ = inputIDs.Destroy()
		_ = attentionMask.Destroy()
		return nil, err
	}
	out, err := ort.NewEmptyTensor[float32](ort.NewShape(int64(batch), seqLen, int64(e.dim)))
	if err != nil {
		_ = inputIDs.Destroy()
		_ = attentionMask.Destroy()
		_ = tokenTypeIDs.Destroy()
		return nil, fmt.Errorf("alokasi tensor output ONNX: %w", err)
	}
	outputs := []ort.Value{out}
	if err := e.sess.Run([]ort.Value{inputIDs, attentionMask, tokenTypeIDs}, outputs); err != nil {
		_ = out.Destroy()
		_ = inputIDs.Destroy()
		_ = attentionMask.Destroy()
		_ = tokenTypeIDs.Destroy()
		return nil, fmt.Errorf("inference ONNX: %w", err)
	}
	data := out.GetData()
	if len(data) != int(batch)*int(seqLen)*int(e.dim) {
		_ = out.Destroy()
		_ = inputIDs.Destroy()
		_ = attentionMask.Destroy()
		_ = tokenTypeIDs.Destroy()
		return nil, fmt.Errorf("output ONNX: panjang data %d, ingin %d", len(data), batch*int(seqLen)*int(e.dim))
	}
	vectors := make([][]float32, 0, batch)
	dim := int(e.dim)
	for i := 0; i < batch; i++ {
		vec := make([]float32, dim)
		sum := make([]float32, dim)
		cnt := 0
		for s := int64(0); s < seqLen; s++ {
			if maskFlat[(i*int(seqLen))+int(s)] == 0 {
				continue
			}
			base := (i*int(seqLen) + int(s)) * dim
			cnt++
			for d := 0; d < dim; d++ {
				sum[d] += data[base+int(d)]
			}
		}
		if cnt > 0 {
			for d := 0; d < dim; d++ {
				vec[d] = sum[d] / float32(cnt)
			}
			l2normalize(vec)
		}
		vectors = append(vectors, vec)
	}
	for j, t := range texts {
		if strings.TrimSpace(t) == "" {
			vectors[j] = make([]float32, e.dim)
		}
	}
	if err := out.Destroy(); err != nil {
		return nil, err
	}
	_ = inputIDs.Destroy()
	_ = attentionMask.Destroy()
	_ = tokenTypeIDs.Destroy()
	return vectors, nil
}

func newInt64Tensor(s ort.Shape, data []int64) (*ort.Tensor[int64], error) {
	t, err := ort.NewTensor[int64](s, data)
	if err != nil {
		return nil, fmt.Errorf("buat tensor int64: %w", err)
	}
	return t, nil
}

func toEncodeInputs(texts []string) []tokenizer.EncodeInput {
	inputs := make([]tokenizer.EncodeInput, len(texts))
	for i, t := range texts {
		inputs[i] = tokenizer.NewSingleEncodeInput(tokenizer.NewInputSequence(t))
	}
	return inputs
}

func l2normalize(v []float32) {
	var sum float32
	for _, x := range v {
		sum += x * x
	}
	norm := float32(math.Sqrt(float64(sum)))
	if norm > 0 {
		for i := range v {
			v[i] /= norm
		}
	}
}

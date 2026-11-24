package semantic

import (
	"context"
	"math"
	"os"
	"testing"
)

var sharedEncoder *Encoder

func requireModel(t *testing.T) *Encoder {
	t.Helper()
	if sharedEncoder != nil {
		return sharedEncoder
	}
	modelDir := "../../data/models/multilingual-minilm-l12"
	libPath := "../../lib/libonnxruntime.so"
	if _, err := os.Stat(modelDir + "/model_optimized.onnx"); err != nil {
		t.Skipf("model ONNX tidak ada di %s: %v", modelDir, err)
	}
	t.Setenv("ONNXRUNTIME_SHARED_LIBRARY_PATH", libPath)
	e, err := Open(modelDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	sharedEncoder = e
	return e
}

func TestEncodeDimAndNormalize(t *testing.T) {
	e := requireModel(t)
	vec, err := e.Encode(context.Background(), []string{"supply chain management rantai pasokan logistik"})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if len(vec) != 1 || len(vec[0]) != e.Dim() {
		t.Fatalf("vektor = %d x %d, want 1 x %d", len(vec), len(vec[0]), e.Dim())
	}
	var sum float32
	for _, x := range vec[0] {
		sum += x * x
	}
	if math.Abs(float64(math.Sqrt(float64(sum)))-1) > 0.001 {
		t.Errorf("vektor tidak L2-normal: norm=%v", math.Sqrt(float64(sum)))
	}
}

func TestEncodeEmptyTextZeroVector(t *testing.T) {
	e := requireModel(t)
	vec, err := e.Encode(context.Background(), []string{""})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if len(vec) != 1 || len(vec[0]) != e.Dim() {
		t.Fatalf("vektor kosong = %v", vec)
	}
	for _, x := range vec[0] {
		if x != 0 {
			t.Errorf("vektor kosong tidak nol: %v", x)
		}
	}
}

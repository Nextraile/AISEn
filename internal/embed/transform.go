package embed

import "context"

// BatchSize menentukan banyaknya dokumen yang diproses paralel per batch
// pada pass fit dan transformasi. Cukup besar untuk memanfaatkan semua core
// tapi tetap kecil supaya memori terkendali.
const BatchSize = 512

// DocStream mengalirkan dokumen (id, teks) satu per satu — dipakai Transform.
type DocStream func(yield func(id, text string) error) error

// sparseResult adalah hasil transformasi satu dokumen.
type sparseResult struct {
	idx []uint32
	val []float32
}

// Transform mengubah stream dokumen menjadi sparse vector SECARA PARALEL
// (per-batch, batas GOMAXPROCS) lalu menulis hasil BERURUTAN lewat write —
// urutan file output identik dengan versi sekuensial.
//
// Mengembalikan jumlah dokumen yang ditulis dan jumlah vektor kosong.
// Jika progress != nil, dipanggil setelah setiap batch dengan jumlah
// dokumen yang sudah ditulis (untuk pelacakan CLI).
func (m *Model) Transform(
	ctx context.Context,
	src DocStream,
	write func(id string, idx []uint32, val []float32) error,
	progress func(count int),
) (count, empty int, err error) {
	type pending struct {
		id   string
		text string
	}
	batch := make([]pending, 0, BatchSize)

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		results := ParallelMap(batch, func(p pending) sparseResult {
			idx, val := m.Sparse(p.text)
			return sparseResult{idx: idx, val: val}
		})
		for i := range batch {
			if len(results[i].idx) == 0 {
				empty++
			}
			if err := write(batch[i].id, results[i].idx, results[i].val); err != nil {
				return err
			}
			count++
		}
		batch = batch[:0]
		if progress != nil {
			progress(count)
		}
		return nil
	}

	err = src(func(id, text string) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		batch = append(batch, pending{id: id, text: text})
		if len(batch) >= BatchSize {
			return flush()
		}
		return nil
	})
	if err != nil {
		return count, empty, err
	}
	if err = flush(); err != nil {
		return count, empty, err
	}
	return count, empty, nil
}

package embed

import (
	"runtime"
	"sync"
)

// ParallelMap menerapkan fn ke setiap elemen in SECARA PARALEL
// (maksimal runtime.GOMAXPROCS goroutine) sambil menjaga urutan hasil
// identik dengan urutan input — aman untuk penulisan berurutan ke file.
//
// Pemakaian: fn harus murni/read-only terhadap data yang dibagi
// (mis. *Model.Sparse hanya membaca peta IDF sehingga aman konkuren).
func ParallelMap[S, T any](in []S, fn func(S) T) []T {
	out := make([]T, len(in))
	if len(in) == 0 {
		return out
	}
	workers := runtime.GOMAXPROCS(0)
	if workers > len(in) {
		workers = len(in)
	}
	if workers <= 1 {
		for i := range in {
			out[i] = fn(in[i])
		}
		return out
	}

	chunk := (len(in) + workers - 1) / workers
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		start := w * chunk
		if start >= len(in) {
			break
		}
		end := start + chunk
		if end > len(in) {
			end = len(in)
		}
		wg.Add(1)
		go func(start, end int) {
			defer wg.Done()
			for i := start; i < end; i++ {
				out[i] = fn(in[i])
			}
		}(start, end)
	}
	wg.Wait()
	return out
}

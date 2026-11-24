package cli

import (
	"context"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"awesomeProject/internal/normalize"
	"awesomeProject/internal/qdrant"
	"awesomeProject/internal/store"
)

// payloadAbstractMax membatasi panjang abstrak di payload Qdrant.
const payloadAbstractMax = 4000

// pointID membuat ID numerik deterministik untuk Qdrant:
// nomor eprint bila ada, selain itu hash FNV-1a dari identifier OAI
// yang dibatasi ke 53 bit agar aman dari presisi angka JSON.
func pointID(doc normalize.Doc) uint64 {
	if doc.EprintID > 0 {
		return uint64(doc.EprintID)
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(doc.ID))
	return h.Sum64() & ((1 << 53) - 1)
}

func buildPayload(d normalize.Doc) map[string]any {
	authors := make([]string, 0, len(d.Authors))
	for _, a := range d.Authors {
		authors = append(authors, a.Raw)
	}
	p := map[string]any{
		"id":            d.ID,
		"title":         d.Title,
		"source":        d.Source,
		"peer_reviewed": d.PeerReviewed,
	}
	if d.EprintID > 0 {
		p["eprint_id"] = d.EprintID
	}
	if len(authors) > 0 {
		p["authors"] = authors
	}
	if d.Year > 0 {
		p["year"] = d.Year
	}
	if d.Date != "" {
		p["date"] = d.Date
	}
	if d.Abstract != "" {
		p["abstract"] = truncateRunes(d.Abstract, payloadAbstractMax)
	}
	if len(d.Subjects) > 0 {
		p["subjects"] = d.Subjects
	}
	if len(d.Types) > 0 {
		types := make([]string, 0, len(d.Types))
		for _, t := range d.Types {
			if isPeerReviewFlag(t) {
				continue // flag peer-review, bukan tipe dokumen
			}
			types = append(types, t)
		}
		if len(types) > 0 {
			p["types"] = types
		}
	}
	if d.Publisher != "" {
		p["publisher"] = d.Publisher
	}
	if d.Language != "" {
		p["language"] = d.Language
	}
	if len(d.SetNames) > 0 {
		p["sets"] = d.SetNames
	}
	if len(d.SetSpecs) > 0 {
		p["set_specs"] = d.SetSpecs
	}
	if d.LandingURL != "" {
		p["url"] = d.LandingURL
	}
	if d.FileURL != "" {
		p["file_url"] = d.FileURL
	}
	if d.Citation != "" {
		p["citation"] = truncateRunes(d.Citation, 1000)
	}
	if len(d.QualityFlags) > 0 {
		p["quality_flags"] = d.QualityFlags
	}
	return p
}

// isPeerReviewFlag mengembalikan true bila token adalah flag peninjauan
// (mis. "PeerReviewed" / "NonPeerReviewed") yang sengaja disematkan ke dalam
// daftar tipe, bukan nama tipe dokumen. Token peer-review ditolak agar
// `types` di payload bersih (hanya tipe dokumen asli).
func isPeerReviewFlag(t string) bool {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "peerreviewed", "peer_reviewed", "peer-reviewed":
		return true
	case "nonpeerreviewed", "non_peer_reviewed", "non-peer-reviewed":
		return true
	}
	return false
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// validateVectorPair memastikan file vektor lengkap dan sinkron dengan file
// dokumen SEBELUM collection dihapus. Tanpa ini, `index -recreate` dengan file
// vektor terpotong (mis. vectorize yang dibunuh di tengah) menghapus collection
// lama lalu gagal setelah beberapa point — data lama hilang, data baru separuh.
func validateVectorPair(docsFile, vecFile string) error {
	vsrc, err := store.OpenVectors(vecFile)
	if err != nil {
		return fmt.Errorf("buka file vektor: %w", err)
	}
	defer vsrc.Close()

	docs, err := store.CountJSONL(docsFile)
	if err != nil {
		return fmt.Errorf("hitung dokumen: %w", err)
	}
	if c := vsrc.Count(); c > 0 && c != docs {
		return fmt.Errorf("file vektor tidak lengkap: %d vektor untuk %d dokumen (jalankan ulang vectorize)", c, docs)
	}

	read := 0
	_, err = store.ReadJSONL[normalize.Doc](docsFile, func(d normalize.Doc) error {
		id, _, err := vsrc.Next()
		if err == io.EOF {
			return fmt.Errorf("file vektor berhenti lebih awal (dokumen %q tanpa vektor)", d.ID)
		}
		if err != nil {
			return err
		}
		if id != d.ID {
			return fmt.Errorf("file vektor tidak sinkron dengan dokumen: vektor=%q dokumen=%q (jalankan ulang vectorize)", id, d.ID)
		}
		read++
		return nil
	})
	if err != nil {
		return err
	}
	if read != docs {
		return fmt.Errorf("dokumen terbaca %d, ingin %d", read, docs)
	}
	if id, _, err := vsrc.Next(); err != io.EOF {
		if err != nil {
			return fmt.Errorf("baca vektor berlebih: %w", err)
		}
		return fmt.Errorf("file vektor punya entri berlebih (mulai %q)", id)
	}
	return nil
}

// runIndex adalah inti perintah index (dipakai juga oleh pipeline).
// Pembacaan dokumen+vektor tetap sekuensial (sinkron per ID), tetapi
// request upsert dijalankan oleh -workers goroutine paralel.
func runIndex(ctx context.Context, docsFile, vecFile, baseURL, collection string, batch, workers int, recreate bool) error {
	start := time.Now()
	client := qdrant.NewClient(baseURL, os.Getenv("QDRANT_API_KEY"))
	if err := client.Health(ctx); err != nil {
		return err
	}
	// Validasi input dulu — jangan pernah menghapus collection lama sebelum
	// tahu file dokumen+vektor bisa dipakai sampai selesai.
	if err := validateVectorPair(docsFile, vecFile); err != nil {
		return fmt.Errorf("validasi input: %w", err)
	}
	if recreate {
		if err := client.DeleteCollection(ctx, collection); err != nil {
			return err
		}
		Printf("collection %q dihapus (recreate)", collection)
	}

	vsrc, err := store.OpenVectors(vecFile)
	if err != nil {
		return fmt.Errorf("buka file vektor: %w", err)
	}
	defer vsrc.Close()

	if batch <= 0 {
		batch = 64
	}
	if workers < 1 {
		workers = 1
	}

	created := false
	ensured := false
	skipped := 0
	var buf []qdrant.Point

	// --- Pool worker: tiap worker mengirim satu batch upsert ke Qdrant ---
	var (
		mu       sync.Mutex
		upserted int
		firstErr error
	)
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()

	fail := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
			cancel() // hentikan worker & pengirim lain
		}
		mu.Unlock()
	}
	currentErr := func() error {
		mu.Lock()
		defer mu.Unlock()
		return firstErr
	}

	jobs := make(chan []qdrant.Point, workers*2)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for pts := range jobs {
				if err := client.Upsert(wctx, collection, pts); err != nil {
					fail(fmt.Errorf("upsert gagal setelah %d point terkirim: %w", func() int {
						mu.Lock()
						defer mu.Unlock()
						return upserted
					}(), err))
					return
				}
				mu.Lock()
				upserted += len(pts)
				fmt.Print(progressBar(upserted, int(vsrc.Count()), 30))
				mu.Unlock()
			}
		}()
	}

	// flush mengirim batch penuh ke worker TANPA memakai ulang slice buf
	// (worker mungkin masih membacanya).
	flush := func() error {
		if len(buf) == 0 {
			return nil
		}
		if err := currentErr(); err != nil {
			return err
		}
		pts := buf
		buf = make([]qdrant.Point, 0, batch)
		select {
		case jobs <- pts:
			return nil
		case <-wctx.Done():
			if err := currentErr(); err != nil {
				return err
			}
			return wctx.Err()
		}
	}
	buf = make([]qdrant.Point, 0, batch)

	// Sinkronisasi streaming: tiap dokumen diambil satu-per-satu
	// dipasangkan dengan vektor berurutan dari file vektor.
	_, err = store.ReadJSONL[normalize.Doc](docsFile, func(d normalize.Doc) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := currentErr(); err != nil {
			return err
		}
		id, vec, err := vsrc.Next()
		if err == io.EOF {
			skipped++
			return fmt.Errorf("file vektor berhenti lebih awal (dokumen %q tanpa vektor)", d.ID)
		}
		if err != nil {
			return err
		}
		if id != d.ID {
			return fmt.Errorf("file vektor tidak sinkron dengan dokumen: vektor=%q dokumen=%q (jalankan ulang vectorize)",
				id, d.ID)
		}
		if !ensured {
			// Bila collection sudah ada tapi dimensinya != vektor baru
			// (mis. dari TF-IDF dim=1024 ke ONNX dim=384) -> hapus & buat ulang.
			if vdim := vsrc.Dim(); vdim > 0 {
				if info, cerr := client.CollectionInfo(ctx, collection); cerr == nil && info.VectorSize != vdim {
					if err := client.DeleteCollection(ctx, collection); err != nil {
						return fmt.Errorf("hapus collection %q: %w", collection, err)
					}
					Printf("collection %q dihapus (dimensi lama %d != vektor baru %d)", collection, info.VectorSize, vdim)
				}
			}
			created, err = client.EnsureCollection(ctx, collection, len(vec))
			if err != nil {
				return err
			}
			if created {
				Printf("collection %q dibuat (dim=%d, distance=%s)", collection, len(vec), qdrant.DistanceCosine)
			} else {
				Printf("memakai collection %q yang sudah ada (dim=%d)", collection, len(vec))
			}
			ensured = true
		}
		buf = append(buf, qdrant.Point{
			ID:      pointID(d),
			Vector:  vec,
			Payload: buildPayload(d),
		})
		if len(buf) >= batch {
			return flush()
		}
		return nil
	})

	// Rekap worker: kirim sisa buffer, tutup channel, tunggu seluruh worker.
	flushErr := flush()
	close(jobs)
	wg.Wait()
	if err == nil && flushErr != nil {
		err = flushErr
	}
	if err == nil {
		err = currentErr()
	}
	if err != nil {
		return fmt.Errorf("index: %w", err)
	}
	// Pastikan tidak ada sisa baris vektor.
	if _, _, err := vsrc.Next(); err == nil {
		return fmt.Errorf("file vektor punya baris lebih banyak daripada dokumen (jalankan ulang vectorize)")
	} else if err != io.EOF {
		return err
	}
	if !ensured {
		return fmt.Errorf("tidak ada dokumen untuk diindeks")
	}

	fmt.Println()
	mu.Lock()
	finalUpserted := upserted
	mu.Unlock()
	if info, err := client.CollectionInfo(ctx, collection); err == nil {
		Printf("collection %q: points=%d status=%s dim=%d",
			collection, info.PointsCount, info.Status, info.VectorSize)
	}
	Printf("index selesai: %d point (%d dilewati, %d worker) dalam %s",
		finalUpserted, skipped, workers, time.Since(start).Round(time.Millisecond))
	return nil
}

func CmdIndex(ctx context.Context, args []string) error {
	var docsFile, vecFile, baseURL, collection string
	var batch, workers int
	var recreate bool

	fs := flag.NewFlagSet("index", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Penggunaan: go run . index [flags]")
		fs.PrintDefaults()
	}
	fs.StringVar(&docsFile, "in", DefaultNormFile, "file JSONL dokumen (payload)")
	fs.StringVar(&vecFile, "vectors", DefaultVectorFile, "file vektor (bin/jsonl)")
	fs.StringVar(&baseURL, "url", EnvOr("QDRANT_URL", DefaultQdrantURL), "URL Qdrant (env: QDRANT_URL)")
	fs.StringVar(&collection, "collection", DefaultCollection, "nama collection Qdrant")
	fs.IntVar(&batch, "batch", 64, "jumlah point per request upsert")
	fs.IntVar(&workers, "workers", 4, "jumlah goroutine upsert paralel")
	fs.BoolVar(&recreate, "recreate", false, "hapus collection lama sebelum indexing")
	if err := fs.Parse(args); err != nil {
		return ErrUsage
	}
	return runIndex(ctx, docsFile, vecFile, baseURL, collection, batch, workers, recreate)
}

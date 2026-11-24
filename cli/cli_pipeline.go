package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"awesomeProject/internal/clean"
	"awesomeProject/internal/normalize"
	"awesomeProject/internal/oai"
	"awesomeProject/internal/semantic"
	"awesomeProject/internal/store"
)

// CmdPipeline menjalankan seluruh alur end-to-end:
// harvest -> clean -> normalize -> vectorize -> index.
func CmdPipeline(ctx context.Context, args []string) error {
	var (
		// harvest
		url, prefix, set, from, until string
		limit                         int
		delay                         time.Duration
		fresh                         bool
		setsFile                      string
		skipHarvest, skipIndex        bool
		// vectorize
		format string
		mp     modelParamsFromFlags
		// index
		qdrantURL, collection string
		batch                 int
		workers               int
		recreate              bool
		timingJSON            string
		// engine vektorisasi (tfidf|onnx|auto)
		engine   string
		modelDir string
		// clean (filter metadata legacy)
		cleanOpts clean.Options = clean.DefaultOptions()
	)

	fs := flag.NewFlagSet("pipeline", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Penggunaan: go run . pipeline [flags]")
		fs.PrintDefaults()
	}
	fs.StringVar(&url, "url", DefaultOAIEndpoint, "endpoint OAI-PMH")
	fs.StringVar(&prefix, "prefix", "oai_dc", "metadataPrefix OAI-PMH")
	fs.StringVar(&set, "set", "", "filter set (setSpec)")
	fs.StringVar(&from, "from", "", "filter datestamp minimal")
	fs.StringVar(&until, "until", "", "filter datestamp maksimal")
	fs.IntVar(&limit, "limit", 0, "batas record harvest (0 = semua)")
	fs.DurationVar(&delay, "delay", 500*time.Millisecond, "jeda antar request OAI")
	fs.BoolVar(&fresh, "fresh", false, "mulai harvest dari nol")
	fs.StringVar(&setsFile, "sets-out", DefaultSetsFile, "file daftar set")
	fs.BoolVar(&skipHarvest, "skip-harvest", false, "lewati tahap harvest (pakai data existing)")
	fs.BoolVar(&skipIndex, "skip-index", false, "lewati indexing Qdrant")
	fs.StringVar(&format, "format", store.FormatBin, "format file vektor: bin | jsonl")
	fs.StringVar(&qdrantURL, "qdrant-url", EnvOr("QDRANT_URL", DefaultQdrantURL), "URL Qdrant (env: QDRANT_URL)")
	fs.StringVar(&collection, "collection", DefaultCollection, "nama collection")
	fs.IntVar(&batch, "batch", 64, "jumlah point per upsert")
	fs.IntVar(&workers, "workers", 4, "jumlah goroutine upsert paralel (index)")
	fs.BoolVar(&recreate, "recreate", false, "hapus collection sebelum indexing")
	fs.StringVar(&timingJSON, "timing-json", "", "tulis ringkasan timing per tahap ke file JSON (opsional, untuk perbandingan)")
	fs.IntVar(&cleanOpts.MinDesc, "min-desc", cleanOpts.MinDesc,
		"panjang minimal abstrak di clean; 0 = tanpa syarat (legacy: 50)")
	fs.BoolVar(&cleanOpts.SkipAdmin, "skip-admin", cleanOpts.SkipAdmin,
		"clean: buang dokumen administratif")
	addModelFlags(fs, &mp)
	fs.StringVar(&engine, "engine", "auto", "engine vektorisasi: tfidf | onnx | auto (default auto -> onnx bila model tersedia)")
	fs.StringVar(&modelDir, "model-dir", semantic.DefaultModelDir, "direktori model ONNX (untuk engine onnx)")
	if err := fs.Parse(args); err != nil {
		return ErrUsage
	}
	if format != store.FormatBin && format != store.FormatJSONL {
		return fmt.Errorf("-format harus bin atau jsonl, dapat %q", format)
	}

	start := time.Now()
	type stageTime struct {
		name string
		dur  time.Duration
	}
	var stages []stageTime
	step := func(name string, t time.Time) {
		d := time.Since(t).Round(time.Millisecond)
		stages = append(stages, stageTime{name: name, dur: d})
		Printf("--- %s selesai dalam %s", name, d)
	}

	// 1. Harvest -----------------------------------------------------
	if !skipHarvest {
		t := time.Now()
		client := oai.NewClient(url, delay)
		client.Log = Printf
		if info, err := client.Identify(ctx); err == nil {
			Printf("repository: %s (OAI %s)", info.RepositoryName, info.ProtocolVersion)
		}
		hf := harvestFlags{url: url, delay: delay, setsFile: setsFile}
		if err := harvestSets(ctx, hf, Printf); err != nil {
			Printf("ListSets dilewati: %v", err)
		}
		res, err := oai.Run(ctx, oai.HarvestOptions{
			OutFile:        DefaultRawFile,
			StateFile:      DefaultStateFile,
			MetadataPrefix: prefix,
			Set:            set,
			From:           from,
			Until:          until,
			Limit:          limit,
			Fresh:          fresh,
			Client:         client,
		}, Printf)
		if err != nil {
			return err
		}
		step("harvest", t)
		if !res.Done && limit > 0 {
			Printf("harvest berhenti di -limit %d (bisa dilanjutkan tanpa -limit)", limit)
		}
	} else {
		Printf("--- harvest dilewati (--skip-harvest)")
	}

	// 2. Cleansing ---------------------------------------------------
	t := time.Now()
	if _, _, err := clean.Run(ctx, DefaultRawFile, DefaultCleanFile, cleanOpts, Printf); err != nil {
		return err
	}
	step("clean", t)

	// 3. Normalisasi -------------------------------------------------
	t = time.Now()
	setNames, err := normalize.LoadSetNames(setsFile)
	if err != nil {
		return err
	}
	if _, _, err := normalize.Run(ctx, DefaultCleanFile, DefaultNormFile, setNames, Printf); err != nil {
		return err
	}
	step("normalize", t)

	// 4. Vektorisasi (fit ulang model karena korpus bisa berubah) ----
	t = time.Now()
	if err := runVectorize(ctx, DefaultNormFile, DefaultVectorFile, DefaultModelFile, format, mp.params(), engine, modelDir); err != nil {
		return err
	}
	step("vectorize", t)

	// 5. Indexing Qdrant ---------------------------------------------
	if skipIndex {
		Printf("--- indexing dilewati (--skip-index)")
	} else {
		t = time.Now()
		if err := runIndex(ctx, DefaultNormFile, DefaultVectorFile, qdrantURL, collection, batch, workers, recreate); err != nil {
			return err
		}
		step("index", t)
	}

	// Ringkasan akhir ------------------------------------------------
	Printf("\n=== ringkasan ===")
	printFileStat("raw      ", DefaultRawFile)
	printFileStat("clean    ", DefaultCleanFile)
	printFileStat("normalized", DefaultNormFile)
	printFileStat("vectors  ", DefaultVectorFile)
	printFileStat("model    ", DefaultModelFile)
	if filesExist(DefaultEngineMeta) {
		printFileStat("engine   ", DefaultEngineMeta)
	}
	total := time.Since(start).Round(time.Millisecond)
	Printf("total waktu: %s", total)

	// Tabel timing per tahap (untuk perbandingan dengan implementasi lain).
	Printf("\n=== timing (per tahap) ===")
	for _, st := range stages {
		Printf("  %-12s : %s", st.name, st.dur)
	}
	if skipIndex {
		Printf("  %-12s : %s (dilewati)", "index", "-")
	}
	Printf("  %-12s : %s", "TOTAL", total)

	if timingJSON != "" {
		type timingEntry struct {
			Stage    string `json:"stage"`
			Duration string `json:"duration"`
		}
		entries := make([]timingEntry, 0, len(stages)+1)
		for _, st := range stages {
			entries = append(entries, timingEntry{Stage: st.name, Duration: st.dur.String()})
		}
		if skipIndex {
			entries = append(entries, timingEntry{Stage: "index", Duration: "skipped"})
		}
		entries = append(entries, timingEntry{Stage: "TOTAL", Duration: total.String()})
		b, err := json.MarshalIndent(entries, "", "  ")
		if err == nil {
			_ = os.WriteFile(timingJSON, b, 0o644)
			Printf("timing JSON ditulis: %s", timingJSON)
		}
	}

	if !skipIndex {
		Printf("\ncoba: go run . search -q \"supply chain management\"")
	}
	return nil
}

func printFileStat(label, path string) {
	size := store.FileSize(path)
	if size == 0 {
		Printf("%s : %s (tidak ada/kosong)", label, path)
		return
	}
	Printf("%s : %s (%.1f KB)", label, path, float64(size)/1024)
}

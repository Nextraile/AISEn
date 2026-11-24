package cli

import (
	"context"
	"flag"
	"fmt"
	"time"

	"awesomeProject/internal/oai"
	"awesomeProject/internal/store"
)

// harvestFlags adalah konfigurasi perintah harvest.
type harvestFlags struct {
	out      string
	state    string
	url      string
	prefix   string
	set      string
	from     string
	until    string
	limit    int
	delay    time.Duration
	fresh    bool
	setsFile string
	setsOnly bool
}

func newHarvestFlagSet(name string, fs *flag.FlagSet, h *harvestFlags) {
	fs.StringVar(&h.out, "out", DefaultRawFile, "file output JSONL (record mentah)")
	fs.StringVar(&h.state, "state", DefaultStateFile, "file checkpoint harvest")
	fs.StringVar(&h.url, "url", DefaultOAIEndpoint, "endpoint OAI-PMH")
	fs.StringVar(&h.prefix, "prefix", "oai_dc", "metadataPrefix OAI-PMH")
	fs.StringVar(&h.set, "set", "", "filter set (setSpec), kosong = semua set")
	fs.StringVar(&h.from, "from", "", "filter datestamp minimal (YYYY-MM-DDThh:mm:ssZ)")
	fs.StringVar(&h.until, "until", "", "filter datestamp maksimal")
	fs.IntVar(&h.limit, "limit", 0, "berhenti setelah N record (0 = semua, resumable)")
	fs.DurationVar(&h.delay, "delay", 500*time.Millisecond, "jeda sopan antar request")
	fs.BoolVar(&h.fresh, "fresh", false, "abaikan checkpoint, mulai dari nol (timpa file)")
	fs.StringVar(&h.setsFile, "sets-out", DefaultSetsFile, "file daftar set (ListSets) untuk label")
	fs.BoolVar(&h.setsOnly, "sets-only", false, "hanya ambil daftar set, lalu keluar")
	_ = name
}

// harvestSets mengambil ListSets dan menyimpannya sebagai JSONL
// (dipakai normalisasi untuk memberi nama human-readable pada setSpec hex).
func harvestSets(ctx context.Context, h harvestFlags, logf oai.LogFunc) error {
	c := oai.NewClient(h.url, h.delay)
	c.Log = logf
	sets, err := c.ListSets(ctx)
	if err != nil {
		return fmt.Errorf("ListSets gagal: %w", err)
	}
	type setLine struct {
		Spec string `json:"set_spec"`
		Name string `json:"set_name"`
	}
	lines := make([]setLine, 0, len(sets))
	for _, s := range sets {
		lines = append(lines, setLine{s.Spec, s.Name})
	}
	if err := store.WriteJSONL(h.setsFile, lines); err != nil {
		return err
	}
	logf("daftar set disimpan: %d set -> %s", len(lines), h.setsFile)
	return nil
}

func CmdHarvest(ctx context.Context, args []string) error {
	var h harvestFlags
	fs := flag.NewFlagSet("harvest", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Penggunaan: go run . harvest [flags]")
		fs.PrintDefaults()
	}
	newHarvestFlagSet("harvest", fs, &h)
	if err := fs.Parse(args); err != nil {
		return ErrUsage
	}

	logf := Printf
	client := oai.NewClient(h.url, h.delay)
	client.Log = logf

	// Ambil daftar set lebih dulu (untuk label), best-effort.
	if h.setsOnly {
		return harvestSets(ctx, h, logf)
	}
	if info, err := client.Identify(ctx); err == nil {
		logf("repository: %s | protocol OAI %s | granularity %s | earliest %s",
			info.RepositoryName, info.ProtocolVersion, info.Granularity, info.EarliestDatestamp)
	} else {
		logf("Identify gagal (lanjut tetap jalan): %v", err)
	}
	if _, err := store.CountJSONL(h.setsFile); err != nil {
		if err := harvestSets(ctx, h, logf); err != nil {
			logf("ListSets dilewati: %v", err)
		}
	}

	res, err := oai.Run(ctx, oai.HarvestOptions{
		OutFile:        h.out,
		StateFile:      h.state,
		MetadataPrefix: h.prefix,
		Set:            h.set,
		From:           h.from,
		Until:          h.until,
		Limit:          h.limit,
		Fresh:          h.fresh,
		Client:         client,
	}, logf)
	if err != nil {
		return err
	}
	logf("harvest selesai: %d record dari %d halaman (selesai=%v) -> %s",
		res.Records, res.Pages, res.Done, h.out)
	logf("ukuran file: %.1f KB", float64(store.FileSize(h.out))/1024)
	return nil
}

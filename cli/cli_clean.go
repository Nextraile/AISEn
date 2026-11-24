package cli

import (
	"context"
	"flag"
	"fmt"

	"awesomeProject/internal/clean"
)

func CmdClean(ctx context.Context, args []string) error {
	var in, out string
	var opts clean.Options = clean.DefaultOptions()
	fs := flag.NewFlagSet("clean", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Penggunaan: go run . clean [flags]")
		fs.PrintDefaults()
	}
	fs.StringVar(&in, "in", DefaultRawFile, "file JSONL hasil harvest")
	fs.StringVar(&out, "out", DefaultCleanFile, "file JSONL hasil cleansing")
	fs.IntVar(&opts.MinDesc, "min-desc", opts.MinDesc,
		"panjang minimal abstrak dalam karakter; 0 = tanpa syarat (legacy: 50)")
	fs.BoolVar(&opts.SkipAdmin, "skip-admin", opts.SkipAdmin,
		"buang dokumen administratif (judul 'penilaian sejawat' / URL 'peer_review')")
	if err := fs.Parse(args); err != nil {
		return ErrUsage
	}

	_, st, err := clean.Run(ctx, in, out, opts, Printf)
	if err != nil {
		return err
	}
	Printf("filter metadata: min-desc=%d skip-admin=%v", opts.MinDesc, opts.SkipAdmin)
	Printf("flags kualitas: %v", st.FlagCount)
	Printf("selanjutnya: go run . normalize")
	return nil
}

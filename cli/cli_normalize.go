package cli

import (
	"context"
	"flag"
	"fmt"

	"awesomeProject/internal/normalize"
)

func CmdNormalize(ctx context.Context, args []string) error {
	var in, out, sets string
	fs := flag.NewFlagSet("normalize", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Penggunaan: go run . normalize [flags]")
		fs.PrintDefaults()
	}
	fs.StringVar(&in, "in", DefaultCleanFile, "file JSONL hasil cleansing")
	fs.StringVar(&out, "out", DefaultNormFile, "file JSONL dokumen ternormalisasi")
	fs.StringVar(&sets, "sets", DefaultSetsFile, "file daftar set (untuk label setSpec)")
	if err := fs.Parse(args); err != nil {
		return ErrUsage
	}

	setNames, err := normalize.LoadSetNames(sets)
	if err != nil {
		return err
	}
	if len(setNames) == 0 {
		Printf("catatan: %s kosong/tidak ada -> label set memakai hasil decode hex", sets)
	} else {
		Printf("label set aktif: %d entri dari %s", len(setNames), sets)
	}

	if _, _, err := normalize.Run(ctx, in, out, setNames, Printf); err != nil {
		return err
	}
	Printf("selanjutnya: go run . vectorize")
	return nil
}

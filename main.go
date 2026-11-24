// CLI harvest metadata OAI-PMH eprints2.undip.ac.id
// -> cleansing -> normalisasi -> vektorisasi TF-IDF -> indexing Qdrant.
//
// Package main hanya berisi entry point + dispatch perintah; implementasi
// tiap perintah ada di package cli (awesomeProject/cli), dan logika data
// ada di awesomeProject/internal/*.
//
// Semua tahap menyimpan hasil ke file (JSONL/biner) sehingga tiap langkah
// bisa dijalankan, diinspeksi, dan diulang secara terpisah.
package main

import (
	"awesomeProject/cli"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
)

const appName = "undip-harvester"

// appVersion boleh dioverride saat build:
//
//	go build -ldflags "-X main.appVersion=1.2.3"
var appVersion = "0.1.0"

func main() {
	if err := run(os.Args[1:]); err != nil {
		switch {
		case errors.Is(err, cli.ErrUsage):
			os.Exit(2)
		case errors.Is(err, context.Canceled):
			fmt.Fprintln(os.Stderr, "\ndibatalkan oleh user")
			os.Exit(130)
		default:
			fmt.Fprintln(os.Stderr, "ERROR:", err)
			os.Exit(1)
		}
	}
}

func run(args []string) error {
	cli.LoadDotEnv(".env")
	if len(args) == 0 {
		printUsage()
		return cli.ErrUsage
	}
	cmd, cmdArgs := args[0], args[1:]

	switch cmd {
	case "help", "-h", "--help":
		printUsage()
		return nil
	case "version", "-v", "--version":
		fmt.Printf("%s %s\n", appName, appVersion)
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var err error
	switch cmd {
	case "harvest":
		err = cli.CmdHarvest(ctx, cmdArgs)
	case "clean":
		err = cli.CmdClean(ctx, cmdArgs)
	case "normalize":
		err = cli.CmdNormalize(ctx, cmdArgs)
	case "vectorize":
		err = cli.CmdVectorize(ctx, cmdArgs)
	case "index":
		err = cli.CmdIndex(ctx, cmdArgs)
	case "search":
		err = cli.CmdSearch(ctx, cmdArgs)
	case "pipeline":
		err = cli.CmdPipeline(ctx, cmdArgs)
	case "peek":
		err = cli.CmdPeek(ctx, cmdArgs)
	case "serve":
		err = cli.CmdServe(ctx, cmdArgs)
	default:
		fmt.Fprintf(os.Stderr, "perintah tidak dikenal: %q\n\n", cmd)
		printUsage()
		return cli.ErrUsage
	}
	return err
}

func printUsage() {
	fmt.Printf(`%s %s — pipeline data repository UNDIP (OAI-PMH -> Qdrant)

Penggunaan:
  ./undip-harvester <perintah> [flags]   # setelah: go build -o undip-harvester .
  atau: go run . <perintah> [flags]

Perintah (dijalankan berurutan):
  harvest     Ambil record mentah dari OAI-PMH, simpan ke JSONL (resumable)
  clean       Cleansing: whitespace/HTML/control char, dedup, penilaian kualitas
  normalize   Normalisasi: tahun, penulis, bahasa, set, teks embedding
  vectorize   Bangun model TF-IDF + file vektor (biner/jsonl)
  index       Kirim vektor + payload ke Qdrant (buat collection otomatis)
  search      Pencarian vektor dari query teks (uji kualitas indexing)
  serve       Jalankan REST API (Huma v2 + chi) + Scalar docs di /docs
  pipeline    Jalankan semua tahap di atas dalam satu perintah
  peek        Tampilkan isi file JSONL/biner untuk inspeksi
  version     Tampilkan versi

Alur file default:
  %s
  %s   (checkpoint harvest)
  %s
  %s
  %s / %s

Contoh:
  # 1) harvest (bisa dihentikan & dilanjutkan kapan saja)
  ./undip-harvester harvest -limit 2000

  # 2) seluruh pipeline sampai Qdrant (butuh Qdrant: docker compose up -d qdrant)
  ./undip-harvester pipeline

  # 3) uji pencarian
  ./undip-harvester search -q "supply chain management logistik"

Jalankan "./undip-harvester <perintah> -h" untuk daftar flag tiap perintah.
`, appName, appVersion, cli.DefaultRawFile, cli.DefaultStateFile,
		cli.DefaultCleanFile, cli.DefaultNormFile,
		cli.DefaultVectorFile, cli.DefaultModelFile)
}

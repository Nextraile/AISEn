package cli

import (
	"context"
	"flag"
	"fmt"

	"awesomeProject/internal/api"
)

// CmdServe menjalankan REST API (Huma v2 + chi) dengan Scalar docs.
func CmdServe(ctx context.Context, args []string) error {
	var host, port, apiKey string
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Penggunaan: go run . serve [flags]")
		fs.PrintDefaults()
	}
	fs.StringVar(&host, "host", "0.0.0.0", "alamat listen (default 0.0.0.0)")
	fs.StringVar(&port, "port", "8080", "port listen (default 8080; env: PORT)")
	fs.StringVar(&apiKey, "api-key", EnvOr("API_KEY", ""), "API key (opsional; env: API_KEY)")
	if err := fs.Parse(args); err != nil {
		return ErrUsage
	}
	if p := EnvOr("PORT", ""); p != "" {
		port = p
	}
	addr := host + ":" + port
	srv := api.New(api.Config{Addr: addr, Version: "0.1.0", APIKey: apiKey, RunJob: RunJob})
	Printf("REST API berjalan di http://%s", addr)
	Printf("  Scalar docs : http://%s/docs", addr)
	Printf("  OpenAPI     : http://%s/openapi.json", addr)
	Printf("  Health      : http://%s/health", addr)
	return srv.Start(ctx)
}

// Package api menyediakan REST API (Huma v2 + chi) di atas internal/*.
// Dokumentasi OpenAPI 3.1 di-generate otomatis oleh Huma; Scalar UI
// disajikan di /docs (config.DocsRenderer = huma.DocsRendererScalar).
package api

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Config adalah konfigurasi server.
type Config struct {
	Addr    string // alamat listen, mis. ":8080"
	Version string // versi aplikasi
	APIKey  string // jika non-kosong, wajib header X-API-Key (production)
	RunJob  func(ctx context.Context, typ string, args []string) error
}

// Server membungkus http.Server + router chi + Huma API.
type Server struct {
	cfg     Config
	router  *chi.Mux
	api     huma.API
	httpSrv *http.Server
	jobs    *JobStore
}

// New membangun server siap dijalankan.
func New(cfg Config) *Server {
	if cfg.Addr == "" {
		cfg.Addr = ":8080"
	}
	if cfg.Version == "" {
		cfg.Version = "dev"
	}
	router := chi.NewMux()
	router.Use(middleware.RequestID)
	router.Use(middleware.RealIP)
	router.Use(middleware.Recoverer)
	// Timeout dinaikkan (120s) — search + ringkasan AI (Gemini) bisa
	// memakan 10-90 detik, terlalu pendek untuk dianggap error.
	router.Use(middleware.Timeout(120 * time.Second))
	// Access log per request (method, path, status, latency) ke stdout.
	router.Use(middleware.Logger)

	if cfg.APIKey != "" {
		router.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				p := r.URL.Path
				if p == "/health" || p == "/docs" || p == "/openapi.json" || p == "/openapi.yaml" {
					next.ServeHTTP(w, r)
					return
				}
				key := r.Header.Get("X-API-Key")
				if key == "" {
					if v := r.Header.Get("Authorization"); strings.HasPrefix(v, "Bearer ") {
						key = strings.TrimPrefix(v, "Bearer ")
					}
				}
				if key != cfg.APIKey {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				next.ServeHTTP(w, r)
			})
		})
	}

	config := huma.DefaultConfig("undip-harvester API", cfg.Version)
	config.Info.Description = "REST API untuk pipeline data repository UNDIP (OAI-PMH -> cleansing -> normalisasi -> vektorisasi -> Qdrant) + pencarian semantik."
	config.DocsRenderer = huma.DocsRendererScalar
	config.DocsRendererConfig = map[string]any{
		"theme":      "mars",
		"hideModels": false,
	}

	api := humachi.New(router, config)

	s := &Server{cfg: cfg, router: router, api: api, jobs: NewJobStore()}
	s.registerRoutes()

	var handler http.Handler = router
	handler = corsMiddleware(handler)

	s.httpSrv = &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return s
}

// Start menjalankan server hingga ctx dibatalkan (graceful shutdown).
func (s *Server) Start(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		if err := s.httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return s.httpSrv.Shutdown(shutdownCtx)
	}
}

// API mengembalikan instance Huma (untuk registrasi operasi tambahan).
func (s *Server) API() huma.API { return s.api }

// Router mengembalikan chi router (untuk middleware/handler tambahan).
func (s *Server) Router() *chi.Mux { return s.router }

// corsMiddleware menambahkan header CORS dan menangani preflight OPTIONS.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		allowed := strings.TrimSpace(os.Getenv("CORS_ORIGINS"))
		allowOrigin := "*"
		if allowed != "" && allowed != "*" {
			for _, o := range strings.Split(allowed, ",") {
				if strings.TrimSpace(o) == origin {
					allowOrigin = origin
					break
				}
			}
			if allowOrigin == "*" && origin != "" {
				allowOrigin = ""
			}
		}
		if allowOrigin != "" {
			w.Header().Set("Access-Control-Allow-Origin", allowOrigin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key, Accept")
		w.Header().Set("Access-Control-Max-Age", "86400")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

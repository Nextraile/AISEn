package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// registerRoutes mendaftarkan semua operasi Huma.
func (s *Server) registerRoutes() {
	s.registerHealth()
	s.registerSearch()
	s.registerJobs()
	s.registerAdmin()
}

func (s *Server) registerHealth() {
	huma.Register(s.api, huma.Operation{
		OperationID: "health",
		Method:      http.MethodGet,
		Path:        "/health",
		Summary:     "Health check",
		Tags:        []string{"System"},
	}, func(ctx context.Context, _ *struct{}) (*struct{ Body HealthResponse }, error) {
		return &struct{ Body HealthResponse }{Body: HealthResponse{Status: "ok", Version: s.cfg.Version}}, nil
	})
}

package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

func (s *Server) registerAdmin() {
	// POST /api/v1/admin/{action} — convenience wrapper di atas /api/v1/jobs.
	huma.Register(s.api, huma.Operation{
		OperationID: "admin-action",
		Method:      http.MethodPost,
		Path:        "/api/v1/admin/{action}",
		Summary:     "Jalankan tahap pipeline (harvest/clean/normalize/vectorize/index/pipeline) sebagai job async",
		Tags:        []string{"Admin"},
	}, func(ctx context.Context, input *struct {
		Action string           `path:"action"`
		Body   JobCreateRequest `json:"body,omitempty"`
	}) (*struct{ Body JobCreateResponse }, error) {
		action := strings.ToLower(strings.TrimSpace(input.Action))
		switch action {
		case "harvest", "clean", "normalize", "vectorize", "index", "pipeline":
		default:
			return nil, huma.Error400BadRequest("action tidak valid: " + action)
		}
		req := input.Body
		if req.Type == "" {
			req.Type = action
		}
		if req.Type != action {
			return nil, huma.Error400BadRequest("type harus sama dengan action")
		}
		j := s.jobs.Create(action)
		args := paramsToArgs(req.Params)
		s.jobs.RunAsync(ctx, j, func(ctx context.Context, progress func(string)) (any, error) {
			progress("menjalankan " + action)
			if s.cfg.RunJob == nil {
				return nil, fmt.Errorf("job runner tidak terkonfigurasi")
			}
			err := s.cfg.RunJob(ctx, action, args)
			if err != nil {
				return nil, err
			}
			return map[string]string{"type": action, "status": "done"}, nil
		})
		return &struct{ Body JobCreateResponse }{Body: JobCreateResponse{JobID: j.ID, Status: string(j.Status)}}, nil
	})
}

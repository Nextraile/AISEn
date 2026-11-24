package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"awesomeProject/internal/progress"
)

func (s *Server) registerJobs() {
	// POST /api/v1/jobs
	huma.Register(s.api, huma.Operation{
		OperationID: "create-job",
		Method:      http.MethodPost,
		Path:        "/api/v1/jobs",
		Summary:     "Buat job asinkron (harvest/clean/normalize/vectorize/index/pipeline)",
		Tags:        []string{"Jobs"},
	}, func(ctx context.Context, input *struct{ Body JobCreateRequest }) (*struct{ Body JobCreateResponse }, error) {
		req := input.Body
		typ := strings.ToLower(strings.TrimSpace(req.Type))
		switch typ {
		case "harvest", "clean", "normalize", "vectorize", "index", "pipeline":
		default:
			return nil, huma.Error400BadRequest("type tidak valid: " + typ)
		}
		j := s.jobs.Create(typ)
		args := paramsToArgs(req.Params)
		s.jobs.RunAsync(ctx, j, func(ctx context.Context, emit func(string)) (any, error) {
			emit("menjalankan " + typ)
			if s.cfg.RunJob == nil {
				return nil, fmt.Errorf("job runner tidak terkonfigurasi")
			}
			// Redirect log CLI (Printf) ke progress callback supaya Job.Progress
			// ter-update real-time (dipakai frontend via polling GET /jobs/{id}).
			prev := progress.Hook
			progress.Hook = emit
			defer func() { progress.Hook = prev }()
			err := s.cfg.RunJob(ctx, typ, args)
			if err != nil {
				return nil, err
			}
			return map[string]string{"type": typ, "status": "done"}, nil
		})
		return &struct{ Body JobCreateResponse }{Body: JobCreateResponse{JobID: j.ID, Status: string(j.Status)}}, nil
	})

	// GET /api/v1/jobs
	huma.Register(s.api, huma.Operation{
		OperationID: "list-jobs",
		Method:      http.MethodGet,
		Path:        "/api/v1/jobs",
		Summary:     "Daftar job",
		Tags:        []string{"Jobs"},
	}, func(ctx context.Context, _ *struct{}) (*struct{ Body JobListResponse }, error) {
		return &struct{ Body JobListResponse }{Body: JobListResponse{Jobs: s.jobs.List()}}, nil
	})

	// GET /api/v1/jobs/{id}
	huma.Register(s.api, huma.Operation{
		OperationID: "get-job",
		Method:      http.MethodGet,
		Path:        "/api/v1/jobs/{id}",
		Summary:     "Status & hasil job",
		Tags:        []string{"Jobs"},
	}, func(ctx context.Context, input *struct {
		ID string `path:"id"`
	}) (*struct{ Body JobGetResponse }, error) {
		j, ok := s.jobs.Get(input.ID)
		if !ok {
			return nil, huma.Error404NotFound("job tidak ditemukan: " + input.ID)
		}
		return &struct{ Body JobGetResponse }{Body: JobGetResponse{Job: j}}, nil
	})
}

func paramsToArgs(params map[string]any) []string {
	if len(params) == 0 {
		return nil
	}
	args := make([]string, 0, len(params)*2)
	for k, v := range params {
		args = append(args, "-"+k)
		switch t := v.(type) {
		case bool:
			if t {
				args = append(args, "true")
			} else {
				args = append(args, "false")
			}
		case []any:
			for _, e := range t {
				args = append(args, fmt.Sprintf("%v", e))
			}
		default:
			args = append(args, fmt.Sprintf("%v", t))
		}
	}
	return args
}

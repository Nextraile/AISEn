package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// JobStatus adalah status pekerjaan asinkron.
type JobStatus string

const (
	JobQueued    JobStatus = "queued"
	JobRunning   JobStatus = "running"
	JobSucceeded JobStatus = "succeeded"
	JobFailed    JobStatus = "failed"
)

// Job merepresentasikan satu pekerjaan asinkron (harvest/clean/.../pipeline).
type Job struct {
	ID         string    `json:"id"`
	Type       string    `json:"type"`
	Status     JobStatus `json:"status"`
	Progress   string    `json:"progress,omitempty"`
	Result     any       `json:"result,omitempty"`
	Error      string    `json:"error,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

// JobStore menyimpan job in-memory (thread-safe) + persistent ke disk
// (JSON) agar status job bertahan walau server restart.
type JobStore struct {
	mu   sync.RWMutex
	jobs map[string]*Job
	busy bool
	file string
}

// defaultJobsFile adalah lokasi file penyimpanan job (bisa override lewat env).
const defaultJobsFile = "data/api/jobs.json"

func NewJobStore() *JobStore { return newJobStore(defaultJobsFile) }

// newJobStore memuat job dari disk. Job yang masih queued/running saat proses
// mati tidak akan pernah selesai, jadi ditandai gagal ("terputus") — kalau
// tidak, UI akan polling status "running" selamanya.
func newJobStore(file string) *JobStore {
	s := &JobStore{jobs: make(map[string]*Job), file: file}
	if b, err := os.ReadFile(file); err == nil {
		var m map[string]*Job
		if err := json.Unmarshal(b, &m); err == nil {
			for k, v := range m {
				if v == nil {
					continue
				}
				if v.Status == JobQueued || v.Status == JobRunning {
					v.Status = JobFailed
					v.Error = "job terputus: server berhenti saat job berjalan"
					v.FinishedAt = time.Now()
				}
				s.jobs[k] = v
			}
			s.persist() // simpan hasil pemulihan
		}
	}
	return s
}

// persist menulis seluruh job ke file (best-effort).
func (s *JobStore) persist() {
	b, err := json.MarshalIndent(s.jobs, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.file), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(s.file, b, 0o644)
}

func (s *JobStore) Create(typ string) *Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := &Job{ID: fmt.Sprintf("job-%d", time.Now().UnixNano()), Type: typ, Status: JobQueued, CreatedAt: time.Now()}
	s.jobs[j.ID] = j
	s.persist()
	return j
}

func (s *JobStore) Get(id string) (*Job, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.jobs[id]
	return j, ok
}

func (s *JobStore) List() []*Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		out = append(out, j)
	}
	return out
}

func (s *JobStore) Update(id string, fn func(*Job)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok {
		fn(j)
	}
	s.persist()
}

// RunAsync menjalankan fn di goroutine dan memperbarui status job.
// Catatan: ctx dari handler HTTP tidak dipakai as-is — akan dibuang cancel-
// nya (context.WithoutCancel) karena job background harus hidup independen
// dari request yang membuatnya.
// Single-flight: hanya satu job yang boleh berjalan sekaligus. Ini mencegah
// dua job pipeline (mis. dua tahap index) mengakses collection Qdrant yang
// sama secara bersamaan.
func (s *JobStore) RunAsync(ctx context.Context, j *Job, fn func(ctx context.Context, progress func(string)) (any, error)) {
	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		// Update memakai lock + persist: jangan ubah Job langsung dari sini
		// (dulu ditulis tanpa lock dan tidak tersimpan ke disk).
		s.Update(j.ID, func(j *Job) {
			j.Status = JobFailed
			j.Error = "job lain masih berjalan (single-flight); coba lagi setelah status kosong"
		})
		return
	}
	s.busy = true
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			s.busy = false
			s.mu.Unlock()
		}()
		ctx = context.WithoutCancel(ctx)
		s.Update(j.ID, func(j *Job) { j.Status = JobRunning; j.StartedAt = time.Now() })
		res, err := fn(ctx, func(p string) {
			s.Update(j.ID, func(j *Job) { j.Progress = p })
		})
		s.Update(j.ID, func(j *Job) {
			j.FinishedAt = time.Now()
			if err != nil {
				j.Status = JobFailed
				j.Error = err.Error()
			} else {
				j.Status = JobSucceeded
				j.Result = res
			}
		})
	}()
}

package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeJobsFile(t *testing.T, jobs map[string]*Job) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "jobs.json")
	b, err := json.Marshal(jobs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// Regression: job "running" yang tertinggal di disk (server mati di tengah job)
// dulu dimuat apa adanya sehingga status "running" bertahan selamanya.
func TestJobStoreRecoversInterruptedJobs(t *testing.T) {
	path := writeJobsFile(t, map[string]*Job{
		"job-lama": {ID: "job-lama", Type: "clean", Status: JobRunning, Progress: "menjalankan clean"},
		"job-ok":   {ID: "job-ok", Type: "harvest", Status: JobSucceeded},
	})

	s := newJobStore(path)

	j, ok := s.Get("job-lama")
	if !ok {
		t.Fatal("job hilang setelah load")
	}
	if j.Status != JobFailed {
		t.Errorf("status job terputus = %q, want %q", j.Status, JobFailed)
	}
	if j.FinishedAt.IsZero() {
		t.Error("finished_at job terputus masih kosong")
	}
	if done, _ := s.Get("job-ok"); done.Status != JobSucceeded {
		t.Errorf("job yang sudah selesai berubah status: %q", done.Status)
	}

	// Pemulihan harus ikut tersimpan ke disk.
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk map[string]*Job
	if err := json.Unmarshal(b, &onDisk); err != nil {
		t.Fatal(err)
	}
	if got := onDisk["job-lama"].Status; got != JobFailed {
		t.Errorf("status di disk = %q, want %q", got, JobFailed)
	}
}

// Saat single-flight menolak job, penolakan harus tersimpan (dulu hanya
// diubah di memori tanpa lock sehingga hilang setelah restart).
func TestJobStoreBusyRejectionPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	s := newJobStore(path)
	s.busy = true

	j := s.Create("pipeline")
	s.RunAsync(context.Background(), j, func(context.Context, func(string)) (any, error) {
		t.Error("fn tidak boleh dijalankan saat store busy")
		return nil, nil
	})

	got, _ := s.Get(j.ID)
	if got == nil || got.Status != JobFailed {
		t.Fatalf("status = %+v, want failed", got)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk map[string]*Job
	if err := json.Unmarshal(b, &onDisk); err != nil {
		t.Fatal(err)
	}
	if onDisk[j.ID] == nil || onDisk[j.ID].Status != JobFailed {
		t.Errorf("penolakan busy tidak tersimpan ke disk: %+v", onDisk[j.ID])
	}
}

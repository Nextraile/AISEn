package oai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"awesomeProject/internal/progress"
)

// ErrTokenExpired menandai resumptionToken sudah kedaluwarsa (butuh --fresh).
var ErrTokenExpired = errors.New("resumptionToken kedaluwarsa; jalankan ulang dengan --fresh")

// ErrTokenStuck menandai server mengembalikan token yang sama berulang kali.
var ErrTokenStuck = errors.New("resumptionToken tidak bergerak maju; berhenti untuk menghindari infinite loop")

// HarvestState adalah checkpoint progres harvest sehingga bisa dilanjutkan.
type HarvestState struct {
	BaseURL        string    `json:"base_url"`
	Verb           string    `json:"verb"`
	MetadataPrefix string    `json:"metadata_prefix"`
	Set            string    `json:"set,omitempty"`
	From           string    `json:"from,omitempty"`
	Until          string    `json:"until,omitempty"`
	OutFile        string    `json:"out_file"`
	Token          string    `json:"resumption_token"`
	TokenCursor    int64     `json:"token_cursor,omitempty"`
	TotalHint      int64     `json:"complete_list_size,omitempty"`
	Pages          int       `json:"pages"`
	Records        int       `json:"records"`
	Done           bool      `json:"done"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// HarvestOptions adalah konfigurasi satu kali proses harvest.
type HarvestOptions struct {
	OutFile        string
	StateFile      string
	MetadataPrefix string
	Set            string
	From           string
	Until          string
	Limit          int  // 0 = tanpa batas
	Fresh          bool // abaikan checkpoint, mulai dari nol
	Client         *Client
}

// HarvestResult merangkum hasil satu kali eksekusi harvest.
type HarvestResult struct {
	Pages   int
	Records int
	Done    bool
	Resumed bool
	Total   int64
}

// LoadState membaca checkpoint; mengembalikan (nil, nil) bila belum ada.
func LoadState(path string) (*HarvestState, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st HarvestState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("state file rusak (%s): %w", path, err)
	}
	return &st, nil
}

// SaveState menulis checkpoint secara atomik (temp + rename).
func SaveState(path string, st *HarvestState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	st.UpdatedAt = time.Now().UTC()
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Run melakukan harvest penuh (ListRecords beriterasi via resumptionToken)
// dan menyimpan record mentah sebagai JSONL ke opt.OutFile.
//
// Sifat: idempotent per halaman - state disimpan setiap halaman selesai,
// sehingga crash/interupsi bisa dilanjutkan dari token terakhir.
func Run(ctx context.Context, opt HarvestOptions, logf LogFunc) (HarvestResult, error) {
	res := HarvestResult{}
	start := time.Now()
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if opt.Client == nil {
		opt.Client = NewClient(DefaultBaseURL, 500*time.Millisecond)
	}
	if opt.MetadataPrefix == "" {
		opt.MetadataPrefix = "oai_dc"
	}
	if opt.OutFile == "" {
		return res, fmt.Errorf("oai: OutFile wajib diisi")
	}
	if opt.StateFile == "" {
		opt.StateFile = opt.OutFile + ".state.json"
	}

	// --- Tentukan titik awal (resume vs mulai baru) ---
	var st *HarvestState
	if !opt.Fresh {
		var err error
		st, err = LoadState(opt.StateFile)
		if err != nil {
			return res, err
		}
	}
	if st != nil {
		mismatch := st.OutFile != opt.OutFile ||
			st.MetadataPrefix != opt.MetadataPrefix ||
			st.Set != opt.Set ||
			st.From != opt.From ||
			st.Until != opt.Until ||
			st.BaseURL != opt.Client.BaseURL
		if mismatch {
			logf("checkpoint tidak cocok dengan flag saat ini -> memulai harvest baru (file lama ditimpa)")
			st = nil
		} else if st.Done {
			logf("harvest sudah sebelumnya selesai: %d record di %s (pakai --fresh untuk mengulang)",
				st.Records, opt.OutFile)
			return HarvestResult{Pages: st.Pages, Records: st.Records, Done: true, Total: st.TotalHint}, nil
		}
	}

	startPages, startRecords := 0, 0
	token := ""
	if st != nil {
		token = st.Token
		startPages, startRecords = st.Pages, st.Records
		res.Resumed = true
		logf("melanjutkan harvest dari halaman %d (%d record sudah ada), token=%q",
			startPages, startRecords, truncate(token, 60))
		// Pastikan file output tetap ada (append).
		if _, err := os.Stat(opt.OutFile); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return res, err
			}
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(opt.OutFile), 0o755); err != nil {
			return res, err
		}
		// Buang sisa file lama agar tidak bercampur.
		if err := os.WriteFile(opt.OutFile, nil, 0o644); err != nil {
			return res, err
		}
		st = &HarvestState{
			BaseURL:        opt.Client.BaseURL,
			Verb:           "ListRecords",
			MetadataPrefix: opt.MetadataPrefix,
			Set:            opt.Set,
			From:           opt.From,
			Until:          opt.Until,
			OutFile:        opt.OutFile,
		}
	}

	f, err := os.OpenFile(opt.OutFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return res, err
	}
	defer f.Close()
	enc := json.NewEncoder(f)

	done := false
	for !done {
		if err := ctx.Err(); err != nil {
			return res, err
		}

		lr, err := opt.Client.ListRecords(ctx, ListRecordsParams{
			MetadataPrefix: opt.MetadataPrefix,
			Set:            opt.Set,
			From:           opt.From,
			Until:          opt.Until,
			Token:          token,
		})
		if err != nil {
			// Simpan state dulu sebelum keluar agar bisa dilanjutkan.
			_ = SaveState(opt.StateFile, st)
			if oe, ok := err.(OAIError); ok && oe.Code == "badResumptionToken" {
				return res, fmt.Errorf("%w (%s)", ErrTokenExpired, oe.Value)
			}
			return res, err
		}

		now := time.Now().UTC()
		written := 0
		for i := range lr.Records {
			lr.Records[i].HarvestedAt = now
			if err := enc.Encode(lr.Records[i]); err != nil {
				_ = SaveState(opt.StateFile, st)
				return res, fmt.Errorf("gagal menulis record: %w", err)
			}
			written++
		}

		st.Pages++
		st.Records += written
		tok := lr.ResumptionToken
		if tok != nil && tok.CompleteListSize > 0 {
			st.TotalHint = tok.CompleteListSize
		}
		st.Token = ""
		if tok != nil {
			st.Token = tok.Value
			st.TokenCursor = tok.Cursor
		}

		res.Pages, res.Records, res.Total = st.Pages, st.Records, st.TotalHint
		if st.TotalHint > 0 {
			fmt.Print(progress.Bar(st.Records, int(st.TotalHint), 30))
		} else {
			// Server OAI tidak mengirim completeListSize -> counter dinamis
			// (halaman + record + laju + elapsed) di satu baris.
			fmt.Print(progress.Counter(st.Pages, st.Records, time.Since(start)))
		}

		// Batas pengujian.
		if opt.Limit > 0 && st.Records >= opt.Limit {
			fmt.Println()
			logf("mencapai --limit %d; state disimpan agar bisa dilanjutkan", opt.Limit)
			if err := SaveState(opt.StateFile, st); err != nil {
				return res, err
			}
			if err := f.Sync(); err != nil {
				return res, err
			}
			res.Records = st.Records
			return res, nil
		}

		if tok.IsEmpty() {
			done = true
			st.Done = true
			st.Token = ""
			break
		}
		// Token tidak boleh sama dengan token sebelumnya (loop proteksi).
		if st.Pages > 1 && tok.Value == token {
			_ = SaveState(opt.StateFile, st)
			return res, ErrTokenStuck
		}
		token = tok.Value

		if err := SaveState(opt.StateFile, st); err != nil {
			return res, err
		}
		if err := f.Sync(); err != nil {
			return res, err
		}
		// Jeda sopan antar permintaan.
		if opt.Client.Delay > 0 {
			select {
			case <-ctx.Done():
				return res, ctx.Err()
			case <-time.After(opt.Client.Delay):
			}
		}
	}

	fmt.Println()
	if err := SaveState(opt.StateFile, st); err != nil {
		return res, err
	}
	if err := f.Sync(); err != nil {
		return res, err
	}
	res.Done = true
	return res, nil
}

func progressSuffix(st *HarvestState) string {
	if st.TotalHint > 0 {
		pct := float64(st.Records) / float64(st.TotalHint) * 100
		if pct > 100 {
			pct = 100
		}
		return fmt.Sprintf(" (%.1f%%)", pct)
	}
	return ""
}

func formatTotal(n int64) string {
	if n <= 0 {
		return "?"
	}
	return fmt.Sprintf("%d", n)
}

// Package normalize mengubah record bersih menjadi dokumen kanonik
// (schema final) yang siap divektorisasi dan diindeks ke Qdrant:
// tahun, penulis terstruktur, bahasa, label set, dst.
package normalize

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"awesomeProject/internal/clean"
	"awesomeProject/internal/oai"
	"awesomeProject/internal/progress"
	"awesomeProject/internal/store"
)

// Author adalah penulis dalam bentuk terstruktur.
type Author struct {
	Raw    string `json:"raw"`
	Family string `json:"family,omitempty"`
	Given  string `json:"given,omitempty"`
}

// Doc adalah skema dokumen final hasil normalisasi.
type Doc struct {
	ID           string    `json:"id"`                    // identifier OAI
	EprintID     int64     `json:"eprint_id,omitempty"`   // nomor eprint (bila terdeteksi)
	LandingURL   string    `json:"landing_url,omitempty"` // halaman abstract
	FileURL      string    `json:"file_url,omitempty"`    // berkas PDF pertama
	Title        string    `json:"title"`
	Authors      []Author  `json:"authors,omitempty"`
	Subjects     []string  `json:"subjects,omitempty"`
	Abstract     string    `json:"abstract,omitempty"`
	Publisher    string    `json:"publisher,omitempty"`
	Citation     string    `json:"citation,omitempty"`
	Year         int       `json:"year,omitempty"`
	Date         string    `json:"date,omitempty"` // ISO-8601 bila bisa diparse
	Types        []string  `json:"types,omitempty"`
	PeerReviewed bool      `json:"peer_reviewed"`
	Language     string    `json:"language,omitempty"`      // nama bahasa ("English")
	LanguageCode string    `json:"language_code,omitempty"` // kode ("en")
	SetSpecs     []string  `json:"set_specs,omitempty"`
	SetNames     []string  `json:"set_names,omitempty"` // hasil decode hex setSpec
	QualityFlags []string  `json:"quality_flags,omitempty"`
	Text         string    `json:"embedding_text"` // teks yang divektorisasi
	Source       string    `json:"source"`         // "eprints2.undip.ac.id"
	SourceURL    string    `json:"source_url,omitempty"`
	HarvestedAt  time.Time `json:"harvested_at,omitempty"`
}

var (
	reEprintPath = regexp.MustCompile(`/id/eprint/(\d+)`)
	reOAIID      = regexp.MustCompile(`:(\d+)$`)
	reYear       = regexp.MustCompile(`\b(19|20)\d{2}\b`)
)

// Stats ringkasan tahap normalisasi.
type Stats struct {
	Input        int `json:"input"`
	Output       int `json:"output"`
	NoTitle      int `json:"missing_title"`
	WithYear     int `json:"with_year"`
	WithAbstract int `json:"with_abstract"`
}

// FromClean mengubah satu record bersih menjadi Doc kanonik.
// setNames (opsional) memetakan setSpec -> nama dari verb=ListSets;
// bila tidak ada, nama di-decode dari hex EPrints ("7374617475733D707562" -> "status=pub").
func FromClean(c clean.Record, setNames map[string]string) Doc {
	d := Doc{
		ID:           c.Identifier,
		Title:        firstNonEmpty(c.Title),
		Abstract:     firstNonEmpty(c.Descriptions),
		Publisher:    firstNonEmpty(c.Publishers),
		Citation:     c.Citation,
		Types:        lowerList(c.Types),
		Subjects:     c.Subjects,
		SetSpecs:     c.SetSpecs,
		QualityFlags: c.QualityFlags,
		Source:       "eprints2.undip.ac.id",
		HarvestedAt:  c.CleansedAt,
	}

	// --- Penulis ---
	for _, cr := range c.Creators {
		if a := parseAuthor(cr); a != nil {
			d.Authors = append(d.Authors, *a)
		}
	}
	// Dedup penulis berdasarkan nama kanonik.
	d.Authors = dedupAuthors(d.Authors)

	// --- Tahun & tanggal ---
	d.Date, d.Year = parseDate(c.Dates)
	if d.Year == 0 {
		if y := yearFromText(d.Title + " " + d.Abstract + " " + d.Citation); y != 0 {
			d.Year = y
		}
	}

	// --- Bahasa ---
	if code := firstNonEmpty(c.Languages); code != "" {
		d.LanguageCode = strings.ToLower(strings.TrimSpace(code))
		if name, ok := languages[d.LanguageCode]; ok {
			d.Language = name
		} else {
			d.Language = strings.TrimSpace(code)
		}
	}

	// --- Tipe & peer review ---
	for _, t := range d.Types {
		switch strings.ToLower(t) {
		case "peerreviewed", "peer_reviewed", "peer-reviewed":
			d.PeerReviewed = true
		}
	}
	// "nonpeerreviewed" harus menang bila keduanya ada
	for _, t := range d.Types {
		if strings.EqualFold(t, "nonpeerreviewed") {
			d.PeerReviewed = false
		}
	}

	// --- URL ---
	for _, u := range c.URLs {
		if d.LandingURL == "" && reEprintPath.MatchString(u) && !looksLikeFile(u) {
			d.LandingURL = u
			if m := reEprintPath.FindStringSubmatch(u); m != nil {
				d.EprintID, _ = strconv.ParseInt(m[1], 10, 64)
			}
		}
		if d.FileURL == "" && looksLikeFile(u) {
			d.FileURL = u
		}
	}
	if d.LandingURL == "" {
		for _, u := range c.URLs {
			if reEprintPath.MatchString(u) {
				d.LandingURL = u
				if m := reEprintPath.FindStringSubmatch(u); m != nil {
					d.EprintID, _ = strconv.ParseInt(m[1], 10, 64)
				}
				break
			}
		}
	}
	// Cadangan: nomor eprint dari identifier OAI "oai:...:<id>"
	if d.EprintID == 0 && d.ID != "" {
		if m := reOAIID.FindStringSubmatch(d.ID); m != nil {
			d.EprintID, _ = strconv.ParseInt(m[1], 10, 64)
		}
	}
	if d.SourceURL == "" {
		d.SourceURL = d.LandingURL
	}

	// --- Label set (dari ListSets, fallback decode hex EPrints) ---
	d.SetNames = labelSetSpecs(d.SetSpecs, setNames)

	// --- Teks embedding ---
	d.Text = BuildText(d)
	return d
}

// BuildText menyusun korpus teks untuk vektorisasi.
// Urutan penting hanya untuk keterbacaan; bobot ditentukan TF-IDF.
func BuildText(d Doc) string {
	parts := make([]string, 0, 8)
	if d.Title != "" {
		parts = append(parts, d.Title)
	}
	if len(d.Subjects) > 0 {
		parts = append(parts, strings.Join(d.Subjects, ", "))
	}
	if d.Abstract != "" {
		parts = append(parts, d.Abstract)
	}
	if len(d.Authors) > 0 {
		names := make([]string, 0, len(d.Authors))
		for _, a := range d.Authors {
			names = append(names, a.Raw)
		}
		parts = append(parts, "penulis: "+strings.Join(names, "; "))
	}
	if d.Publisher != "" {
		parts = append(parts, d.Publisher)
	}
	if len(d.SetNames) > 0 {
		parts = append(parts, strings.Join(d.SetNames, ", "))
	}
	return clean.Text(strings.Join(parts, ". "))
}

// Run menjalankan tahap normalisasi penuh: baca clean -> Doc -> tulis.
func Run(ctx context.Context, inPath, outPath string, setNames map[string]string, logf oai.LogFunc) (string, Stats, error) {
	var st Stats
	if logf == nil {
		logf = func(string, ...any) {}
	}
	total, _ := store.CountJSONL(inPath)
	out, err := store.NewJSONLWriter[Doc](outPath)
	if err != nil {
		return outPath, st, err
	}
	defer out.Close()

	_, err = store.ReadJSONL[clean.Record](inPath, func(c clean.Record) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		st.Input++
		if total > 0 {
			fmt.Print(progress.Bar(st.Input, total, 30))
		}
		if !c.Usable {
			return nil
		}
		d := FromClean(c, setNames)
		if d.Title == "" {
			st.NoTitle++
			return nil
		}
		if d.Year > 0 {
			st.WithYear++
		}
		if d.Abstract != "" {
			st.WithAbstract++
		}
		st.Output++
		return out.Write(d)
	})
	if err != nil {
		return outPath, st, fmt.Errorf("normalize: %w", err)
	}
	if err := out.Close(); err != nil {
		return outPath, st, err
	}
	fmt.Println()
	logf("normalize selesai: input=%d output=%d with_year=%d with_abstract=%d -> %s",
		st.Input, st.Output, st.WithYear, st.WithAbstract, outPath)
	return outPath, st, nil
}

// --- helpers ---

func firstNonEmpty(vs []string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func lowerList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		out = append(out, v)
	}
	return out
}

// parseAuthor memecah "Family, Given" menjadi struktur penulis.
// Bila tidak ada koma, seluruh nama dianggap family name.
func parseAuthor(raw string) *Author {
	raw = clean.Text(raw)
	if raw == "" {
		return nil
	}
	a := &Author{Raw: raw}
	if i := strings.Index(raw, ","); i >= 0 {
		a.Family = strings.TrimSpace(raw[:i])
		a.Given = strings.TrimSpace(raw[i+1:])
	} else {
		// "Given Family" -> family = kata terakhir
		fields := strings.Fields(raw)
		if len(fields) > 1 {
			a.Given = strings.Join(fields[:len(fields)-1], " ")
			a.Family = fields[len(fields)-1]
		} else {
			a.Family = raw
		}
	}
	return a
}

func dedupAuthors(in []Author) []Author {
	if len(in) <= 1 {
		return in
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]Author, 0, len(in))
	for _, a := range in {
		key := strings.ToLower(a.Family + "|" + a.Given)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, a)
	}
	return out
}

// parseDate mencoba beberapa format tanggal Dublin Core umum:
// "2019", "2019-12", "2019-12-30", "2019-12-30T01:04:29Z", "30/12/2019".
func parseDate(values []string) (string, int) {
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		// Timestamp penuh
		for _, layout := range []string{
			time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02 15:04:05",
		} {
			if t, err := time.Parse(layout, v); err == nil {
				return t.UTC().Format(time.RFC3339), t.Year()
			}
		}
		if y, ok := parseYearPrefix(v); ok {
			return v, y
		}
		if y := yearFromText(v); y != 0 {
			return v, y
		}
	}
	return "", 0
}

func parseYearPrefix(v string) (int, bool) {
	if len(v) < 4 {
		return 0, false
	}
	y, err := strconv.Atoi(v[:4])
	if err != nil || y < 1500 || y > time.Now().Year()+1 {
		return 0, false
	}
	// pastikan memang pola tahun-awal
	if !reYear.MatchString(v[:4]) {
		return 0, false
	}
	return y, true
}

func yearFromText(s string) int {
	m := reYear.FindString(s)
	if m == "" {
		return 0
	}
	y, err := strconv.Atoi(m)
	if err != nil {
		return 0
	}
	return y
}

func looksLikeFile(u string) bool {
	l := strings.ToLower(u)
	for _, ext := range []string{".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".jpg", ".png", ".zip"} {
		if strings.Contains(l, ext) {
			return true
		}
	}
	return false
}

// labelSetSpecs mengubah daftar setSpec menjadi label:
// utamakan nama dari ListSets; fallback decode hex EPrints
// ("7374617475733D707562" -> "status=pub").
func labelSetSpecs(specs []string, setNames map[string]string) []string {
	if len(specs) == 0 {
		return nil
	}
	out := make([]string, 0, len(specs))
	seen := make(map[string]struct{}, len(specs))
	for _, s := range specs {
		dec := s
		if name, ok := setNames[s]; ok && name != "" {
			dec = name
		} else {
			dec = decodeHexSet(s)
		}
		dec = clean.Text(dec)
		if dec == "" {
			continue
		}
		if _, ok := seen[dec]; ok {
			continue
		}
		seen[dec] = struct{}{}
		out = append(out, dec)
	}
	return out
}

// LoadSetNames memuat file daftar set (JSONL berisi set_spec/set_name)
// yang dihasilkan perintah harvest. File tidak ada -> peta kosong (bukan error).
func LoadSetNames(path string) (map[string]string, error) {
	type setLine struct {
		Spec string `json:"set_spec"`
		Name string `json:"set_name"`
	}
	names := map[string]string{}
	_, err := store.ReadJSONL[setLine](path, func(l setLine) error {
		if l.Spec != "" && l.Name != "" {
			names[l.Spec] = l.Name
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return names, nil
		}
		return nil, err
	}
	return names, nil
}

func decodeHexSet(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	// Hanya decode bila seluruh string heksadesimal genap dan menghasilkan teks printable.
	if len(s)%2 != 0 || !isHexString(s) {
		return s
	}
	b := make([]byte, 0, len(s)/2)
	for i := 0; i+1 < len(s); i += 2 {
		v, err := strconv.ParseUint(s[i:i+2], 16, 8)
		if err != nil {
			return s
		}
		b = append(b, byte(v))
	}
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			return s // bukan teks ASCII -> pertahankan spesifikasi asli
		}
	}
	return string(b)
}

func isHexString(s string) bool {
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return len(s) > 0
}

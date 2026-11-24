// Package clean menangani cleansing data mentah hasil harvest OAI-PMH:
// normalisasi whitespace, pembuangan karakter kontrol/HTML, deduplikasi,
// pemisahan URL vs kutipan, dan penilaian kualitas record.
package clean

import (
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var (
	reHTMLTag = regexp.MustCompile(`<[^>]*>`)
	reSpaces  = regexp.MustCompile(`\s+`)
	// Karakter zero-width (ZWSP/ZWNJ/ZWJ/WJ/BOM) sering terselip dari sumber XML.
	reZeroWidth = regexp.MustCompile("[\u200b-\u200d\u2060\ufeff]")
)

// QualityFlags adalah daftar penanda kualitas record.
const (
	FlagDeleted       = "deleted"
	FlagNoTitle       = "missing_title"
	FlagNoCreator     = "missing_creator"
	FlagNoDescription = "missing_description"
	FlagShortTitle    = "short_title"
	FlagBoilerplate   = "boilerplate_title"
	FlagNoDate        = "missing_date"
	FlagJunkCitation  = "empty_citation"
)

var boilerplatePrefixes = []string{
	"turnitin",
	"hasil scan plagiasi",
	"hasil penilaian",
	"lembar hasil penilaian",
	"hasil penilaian sebidang",
	"artikel ",
	"scan ",
	"snapshot",
	"similarity",
}

// Record adalah hasil cleansing: satu record bersih siap dinormalisasi.
type Record struct {
	Identifier   string    `json:"identifier"`
	Datestamp    string    `json:"datestamp"`
	SetSpecs     []string  `json:"set_specs,omitempty"`
	Deleted      bool      `json:"deleted"`
	Title        []string  `json:"title,omitempty"`
	Creators     []string  `json:"creators,omitempty"`
	Subjects     []string  `json:"subjects,omitempty"`
	Descriptions []string  `json:"descriptions,omitempty"`
	Publishers   []string  `json:"publishers,omitempty"`
	Dates        []string  `json:"dates,omitempty"`
	Types        []string  `json:"types,omitempty"`
	Languages    []string  `json:"languages,omitempty"`
	Rights       []string  `json:"rights,omitempty"`
	Relations    []string  `json:"relations,omitempty"`
	Formats      []string  `json:"formats,omitempty"`
	Contributors []string  `json:"contributors,omitempty"`
	Identifiers  []string  `json:"identifiers,omitempty"`
	URLs         []string  `json:"urls,omitempty"`
	Citation     string    `json:"citation,omitempty"`
	QualityFlags []string  `json:"quality_flags,omitempty"`
	Usable       bool      `json:"usable"`
	CleansedAt   time.Time `json:"cleansed_at"`
}

// Text membersihkan satu nilai teks:
//   - unescape entity HTML/XML (mis. &amp;, &#13;) bila masih tersisa
//   - buang tag HTML & karakter kontrol (termasuk CR dari entitas &#13;)
//   - buang zero-width / BOM
//   - rapikan spasi jadi satu lalu trim
func Text(s string) string {
	if s == "" {
		return ""
	}
	// Beberapa field EPrints menyimpan entity ganda (&amp;amp; / &#13;),
	// jadi di-unescape dulu sebelum tag dibuang.
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	s = reZeroWidth.ReplaceAllString(s, "")
	s = reHTMLTag.ReplaceAllString(s, " ")
	// Buang sisa karakter kontrol ASCII (kecuali spasi).
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		if r == '�' { // replacement char: buang
			return -1
		}
		return r
	}, s)
	s = reSpaces.ReplaceAllString(s, " ")
	s = dedupPunct(s)
	return strings.TrimSpace(s)
}

// dedupPunct merapikan tanda baca kembar berurutan (mis. ",," -> ",",
// "::" -> ":"). RE2 tidak mendukung backreference, jadi dilakukan manual.
func dedupPunct(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prev := rune(0)
	for _, r := range s {
		if (r == ',' || r == ';' || r == ':' || r == '.') && r == prev {
			continue
		}
		b.WriteRune(r)
		prev = r
	}
	return b.String()
}

// TextList membersihkan daftar nilai, membuang yang kosong,
// dan mendeduplikasi case-insensitive sambil mempertahankan urutan.
func TextList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = Text(v)
		if v == "" {
			continue
		}
		key := strings.ToLower(v)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// IsURL memeriksa apakah string adalah http(s) URL valid.
func IsURL(s string) bool {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

// LooksBoilerplate mengenali judul template/junk yang tidak bermakna
// untuk retrieval (mis. "Turnitin_Artikel 1").
func LooksBoilerplate(title string) bool {
	t := strings.ToLower(strings.TrimSpace(title))
	if t == "" {
		return false
	}
	for _, p := range boilerplatePrefixes {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

// CleansingStats merangkum hasil tahap cleansing.
type CleansingStats struct {
	Input   int `json:"input"`
	Kept    int `json:"kept"`
	Dropped int `json:"dropped"`
	Dupes   int `json:"duplicates"`
	Deleted int `json:"deleted_records"`
	// NoAbstract: dibuang karena tanpa abstrak / abstrak ≤ MinDesc.
	NoAbstract int `json:"filtered_no_abstract"`
	// AdminFiltered: dibuang karena dokumen administratif.
	AdminFiltered int            `json:"filtered_administrative"`
	FlagCount     map[string]int `json:"flag_count"`
}

// NewStats membuat stats dengan peta flag kosong.
func NewStats() CleansingStats {
	return CleansingStats{FlagCount: map[string]int{}}
}

func (s *CleansingStats) addFlag(f string) {
	if s.FlagCount == nil {
		s.FlagCount = map[string]int{}
	}
	s.FlagCount[f]++
}

func (s CleansingStats) String() string {
	return fmt.Sprintf("input=%d kept=%d dropped=%d dup=%d deleted=%d no_abstract=%d admin=%d flags=%v",
		s.Input, s.Kept, s.Dropped, s.Dupes, s.Deleted, s.NoAbstract, s.AdminFiltered, s.FlagCount)
}

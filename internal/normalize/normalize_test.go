package normalize

import (
	"strings"
	"testing"
)

func TestDecodeHexSet(t *testing.T) {
	cases := map[string]string{
		"7374617475733D707562":       "status=pub",
		"7375626A656374733D62":       "subjects=b",
		"74797065733D61727469636C65": "types=article",
		"not-hex-value":              "not-hex-value", // dipertahankan
		"":                           "",
	}
	for in, want := range cases {
		if got := decodeHexSet(in); got != want {
			t.Errorf("decodeHexSet(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLabelSetSpecsPrefersListSetsName(t *testing.T) {
	names := map[string]string{"7374617475733D707562": "Status = Published"}
	got := labelSetSpecs([]string{"7374617475733D707562", "74797065733D6F74686572"}, names)
	if len(got) != 2 {
		t.Fatalf("got %#v", got)
	}
	if got[0] != "Status = Published" {
		t.Errorf("harus memakai nama ListSets, dapat %q", got[0])
	}
	if got[1] != "types=other" {
		t.Errorf("fallback decode hex gagal: %q", got[1])
	}
}

func TestParseAuthor(t *testing.T) {
	a := parseAuthor("KUSUMAWARDHANI, Amie")
	if a == nil || a.Family != "KUSUMAWARDHANI" || a.Given != "Amie" {
		t.Fatalf("parseAuthor(koma) = %#v", a)
	}
	b := parseAuthor("Amie Kusumawardhani")
	if b == nil || b.Family != "Kusumawardhani" || b.Given != "Amie" {
		t.Fatalf("parseAuthor(spasi) = %#v", b)
	}
	if parseAuthor("   ") != nil {
		t.Error("nama kosong harus menghasilkan nil")
	}
}

func TestParseDate(t *testing.T) {
	cases := []struct {
		in   []string
		year int
	}{
		{[]string{"2015"}, 2015},
		{[]string{"2017-03"}, 2017},
		{[]string{"2018-12-30T01:04:29Z"}, 2018},
		{[]string{"2014-11-12"}, 2014},
		{[]string{"tanpa tanggal"}, 0},
	}
	for _, c := range cases {
		_, y := parseDate(c.in)
		if y != c.year {
			t.Errorf("parseDate(%v) year = %d, want %d", c.in, y, c.year)
		}
	}
}

func TestBuildTextIncludesKeyFields(t *testing.T) {
	d := Doc{
		Title:     "Judul Skripsi",
		Subjects:  []string{"Teknik Industri"},
		Abstract:  "Abstrak penelitian ini membahas ...",
		Authors:   []Author{{Raw: "Doe, John"}},
		Publisher: "UNDIP",
	}
	txt := BuildText(d)
	for _, want := range []string{"Judul Skripsi", "Teknik Industri", "Abstrak penelitian", "Doe, John", "UNDIP"} {
		if !strings.Contains(txt, want) {
			t.Errorf("BuildText tidak memuat %q: %s", want, txt)
		}
	}
}

package clean

import (
	"strings"
	"testing"
)

func TestTextCleansControlAndWhitespace(t *testing.T) {
	in := "  Judul\r\nDengan\tSpasi   Ganjil &#13;​ akhir  "
	got := Text(in)
	want := "Judul Dengan Spasi Ganjil akhir"
	if got != want {
		t.Fatalf("Text() = %q, want %q", got, want)
	}
}

func TestTextStripsHTML(t *testing.T) {
	got := Text("<b>Penting</b> &amp; <i>lain</i>")
	if strings.ContainsAny(got, "<>") {
		t.Fatalf("tag HTML masih ada: %q", got)
	}
	if got != "Penting & lain" {
		t.Fatalf("Text() = %q", got)
	}
}

func TestTextDedupPunct(t *testing.T) {
	if got := Text("Analisis,,;: kembar"); got != "Analisis,;: kembar" {
		t.Fatalf("dedupPunct gagal: %q", got)
	}
}

func TestTextListDedupCaseInsensitive(t *testing.T) {
	got := TextList([]string{" Teknik ", "teknik", "", "TEKNIK", "Mesin"})
	if len(got) != 2 || got[0] != "Teknik" || got[1] != "Mesin" {
		t.Fatalf("TextList() = %#v", got)
	}
}

func TestIsURL(t *testing.T) {
	cases := map[string]bool{
		"https://eprints2.undip.ac.id/id/eprint/4/": true,
		"http://example.org":                        true,
		"  https://x.id/a.pdf  ":                    true,
		"Kurniawan, Adi Judul. Penerbit.":           false,
		"":                                          false,
		"ftp://x":                                   false,
	}
	for in, want := range cases {
		if got := IsURL(in); got != want {
			t.Errorf("IsURL(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestLooksBoilerplate(t *testing.T) {
	if !LooksBoilerplate("Turnitin_Artikel 1") {
		t.Error("Turnitin_Artikel 1 seharusnya boilerplate")
	}
	if !LooksBoilerplate("Hasil Scan Plagiasi (TURNITIN) : Judul") {
		t.Error("Hasil Scan Plagiasi seharusnya boilerplate")
	}
	if LooksBoilerplate("Analisis Pengaruh Logistik terhadap Kinerja Bisnis") {
		t.Error("judul normal tidak boleh dianggap boilerplate")
	}
}

package oai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSanitizeXMLDropsIllegalRunes(t *testing.T) {
	// Reproduksi kasus nyata: U+FFFE di tengah teks (non-character XML).
	in := "<t>aman\xef\xbf\xbeilegal\xef\xbf\xbftambahan</t>"
	out, dropped := sanitizeXML([]byte(in))
	if dropped != 2 {
		t.Errorf("dropped = %d, want 2", dropped)
	}
	if string(out) != "<t>amanilegaltambahan</t>" {
		t.Errorf("out = %q", out)
	}

	// U+FFFF juga non-character dan harus dibuang.
	out, dropped = sanitizeXML([]byte("a\xff\xffb"))
	if dropped != 2 || string(out) != "ab" {
		t.Errorf("U+FFFF: out=%q dropped=%d", out, dropped)
	}

	// Byte UTF-8 rusak dibuang per byte (memicu "illegal UTF-8 encoding").
	out, dropped = sanitizeXML([]byte("ok\xff\xfd!"))
	if dropped != 2 || string(out) != "ok!" {
		t.Errorf("invalid UTF-8: out=%q dropped=%d", out, dropped)
	}
}

func TestSanitizeXMLKeepsValidBodyUnchanged(t *testing.T) {
	in := []byte(`<?xml version="1.0"?><r>teks biasa � (U+FFFD legal)</r>`)
	out, dropped := sanitizeXML(in)
	if dropped != 0 {
		t.Errorf("dropped = %d, want 0", dropped)
	}
	// Body bersih harus mengembalikan slice yang sama (tanpa penyalinan).
	if &out[0] != &in[0] {
		t.Error("body bersih seharusnya tidak disalin ulang")
	}

	// Kontrol yang valid: tab, LF, CR, CJK, emoji (U+10FFFF batas atas).
	if _, d := sanitizeXML([]byte("a\tb\nc\rd日本語\xf0\x9f\x98\x80")); d != 0 {
		t.Errorf("karakter valid ikut dibuang: dropped=%d", d)
	}
}

func TestDoSanitizesIllegalCharsBeforeDecode(t *testing.T) {
	// Body panjang dengan U+FFFE di "baris" tengah, seperti error produksi.
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` +
		`<OAI-PMH xmlns="http://www.openarchives.org/OAI/2.0/"><request verb="Identify">`)
	for i := 0; i < 600; i++ {
		sb.WriteString("x\n")
	}
	sb.WriteString("karakter\xef\xbf\xbeilegal")
	for i := 0; i < 50; i++ {
		sb.WriteString("y\n")
	}
	sb.WriteString("</request><Identify><repositoryName>Undip Repository</repositoryName>" +
		"<baseURL>https://eprints2.undip.ac.id/cgi/oai2</baseURL>" +
		"<protocolVersion>2.0</protocolVersion><adminEmail>a@b.c</adminEmail>" +
		"<earliestDatestamp>2019-12-30T01:04:29Z</earliestDatestamp>" +
		"<deletedRecord>persistent</deletedRecord><granularity>YYYY-MM-DDThh:mm:ssZ</granularity>" +
		"</Identify></OAI-PMH>")

	var logged []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(sb.String()))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 0)
	c.Log = func(format string, args ...any) {
		logged = append(logged, format)
	}
	id, err := c.Identify(context.Background())
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if id.RepositoryName != "Undip Repository" {
		t.Errorf("repositoryName = %q", id.RepositoryName)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "sanitasi XML") {
		t.Errorf("log sanitasi tidak muncul: %v", logged)
	}
}

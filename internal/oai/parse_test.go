package oai

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sampleListRecords = `<?xml version='1.0' encoding='UTF-8'?>
<OAI-PMH xmlns="http://www.openarchives.org/OAI/2.0/"
         xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"
         xsi:schemaLocation="http://www.openarchives.org/OAI/2.0/ http://www.openarchives.org/OAI/2.0/OAI-PMH.xsd">
  <responseDate>2026-11-20T02:00:33Z</responseDate>
  <request verb="ListRecords" metadataPrefix="oai_dc">https://eprints2.undip.ac.id/cgi/oai2</request>
  <ListRecords>
    <record>
      <header>
        <identifier>oai:eprints2.undip.ac.id:4</identifier>
        <datestamp>2019-12-30T01:04:29Z</datestamp>
        <setSpec>7374617475733D707562</setSpec>
      </header>
      <metadata>
        <oai_dc:dc xmlns:oai_dc="http://www.openarchives.org/OAI/2.0/oai_dc/"
                   xmlns:dc="http://purl.org/dc/elements/1.1/"
                   xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
          <dc:title>Judul Pertama&#13;
Baris Kedua</dc:title>
          <dc:creator>Kurniawan, Adi</dc:creator>
          <dc:description>Abstrak singkat.</dc:description>
          <dc:date>2019-12</dc:date>
          <dc:type>Article</dc:type>
          <dc:type>PeerReviewed</dc:type>
          <dc:identifier>https://eprints2.undip.ac.id/id/eprint/4/1/a.pdf</dc:identifier>
        </oai_dc:dc>
      </metadata>
    </record>
    <record>
      <header status="deleted">
        <identifier>oai:eprints2.undip.ac.id:99</identifier>
        <datestamp>2020-01-01T00:00:00Z</datestamp>
      </header>
    </record>
    <resumptionToken cursor="100" completeListSize="12345">metadataPrefix%3Doai_dc%26offset%3D100</resumptionToken>
  </ListRecords>
</OAI-PMH>`

func TestParseListRecords(t *testing.T) {
	resp, err := TolerantDecode(strings.NewReader(sampleListRecords))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ListRecords == nil {
		t.Fatal("ListRecords nil")
	}
	lr := resp.ListRecords
	if len(lr.Records) != 2 {
		t.Fatalf("jumlah record = %d, want 2", len(lr.Records))
	}

	rec := lr.Records[0]
	if rec.Header.Identifier != "oai:eprints2.undip.ac.id:4" {
		t.Errorf("identifier = %q", rec.Header.Identifier)
	}
	if len(rec.Header.SetSpecs) != 1 || rec.Header.SetSpecs[0] != "7374617475733D707562" {
		t.Errorf("setSpecs = %#v", rec.Header.SetSpecs)
	}
	if !rec.HasMetadata() {
		t.Fatal("record pertama seharusnya punya metadata")
	}
	dc := rec.DC()
	if len(dc.Title) != 1 || !strings.Contains(dc.Title[0], "Baris Kedua") {
		t.Errorf("title = %#v", dc.Title)
	}
	if len(dc.Type) != 2 || dc.Type[1] != "PeerReviewed" {
		t.Errorf("types = %#v", dc.Type)
	}
	if len(dc.Date) != 1 || dc.Date[0] != "2019-12" {
		t.Errorf("dates = %#v", dc.Date)
	}

	del := lr.Records[1]
	if !strings.EqualFold(del.Header.Status, "deleted") {
		t.Errorf("status = %q", del.Header.Status)
	}
	if del.HasMetadata() {
		t.Error("record terhapus tidak boleh punya metadata")
	}

	tok := lr.ResumptionToken
	if tok.IsEmpty() {
		t.Fatal("resumptionToken seharusnya terisi")
	}
	if tok.Cursor != 100 || tok.CompleteListSize != 12345 {
		t.Errorf("token attrs = cursor:%d size:%d", tok.Cursor, tok.CompleteListSize)
	}
}

func TestParseOAIError(t *testing.T) {
	const body = `<?xml version="1.0"?><OAI-PMH xmlns="http://www.openarchives.org/OAI/2.0/">
  <error code="badResumptionToken">The resumptionToken is expired</error></OAI-PMH>`
	resp, err := TolerantDecode(strings.NewReader(body))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Errors) != 1 || resp.Errors[0].Code != "badResumptionToken" {
		t.Fatalf("errors = %#v", resp.Errors)
	}
	if resp.Errors[0].Error() == "" {
		t.Error("OAIError.Error() kosong")
	}
}

func TestHarvestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "state.json")
	st := &HarvestState{
		BaseURL:        "https://example.org/oai",
		Verb:           "ListRecords",
		MetadataPrefix: "oai_dc",
		OutFile:        "data/raw/records.jsonl",
		Token:          "abc",
		Pages:          3,
		Records:        300,
		Done:           false,
	}
	if err := SaveState(path, st); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	got, err := LoadState(path)
	if err != nil || got == nil {
		t.Fatalf("LoadState got=%v err=%v", got, err)
	}
	if got.Token != "abc" || got.Records != 300 || got.Done {
		t.Errorf("state = %#v", got)
	}
	if got.UpdatedAt.IsZero() || time.Since(got.UpdatedAt) > time.Minute {
		t.Errorf("UpdatedAt tidak terisi: %v", got.UpdatedAt)
	}

	// State hilang -> (nil, nil)
	if got, err := LoadState(filepath.Join(dir, "nope.json")); err != nil || got != nil {
		t.Errorf("LoadState hilang: got=%v err=%v", got, err)
	}

	// State rusak -> error jelas
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(bad); err == nil {
		t.Error("state rusak harus menghasilkan error")
	}
}

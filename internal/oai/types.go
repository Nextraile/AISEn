// Package oai berisi klien dan tipe data untuk protokol OAI-PMH 2.0.
package oai

import (
	"encoding/xml"
	"time"
)

// Namespace protokol OAI-PMH. Dipakai bila parsing membutuhkan pencocokan namespace eksplisit.
const Namespace = "http://www.openarchives.org/OAI/2.0/"

// Header adalah <header> pada OAI-PMH: identitas record, datestamp, dan setSpec.
type Header struct {
	// Status berisi "deleted" bila record sudah dihapus oleh repository.
	Status     string   `xml:"status,attr" json:"status,omitempty"`
	Identifier string   `xml:"identifier" json:"identifier"`
	Datestamp  string   `xml:"datestamp" json:"datestamp"`
	SetSpecs   []string `xml:"setSpec" json:"set_specs,omitempty"`
}

// DC adalah elemen <oai_dc:dc> berisi metadata Dublin Core.
// Semua field disimpan sebagai daftar karena Dublin Core bersifat multi-valued.
type DC struct {
	Title       []string `xml:"title" json:"title,omitempty"`
	Creator     []string `xml:"creator" json:"creators,omitempty"`
	Subject     []string `xml:"subject" json:"subjects,omitempty"`
	Description []string `xml:"description" json:"descriptions,omitempty"`
	Publisher   []string `xml:"publisher" json:"publishers,omitempty"`
	Contributor []string `xml:"contributor" json:"contributors,omitempty"`
	Date        []string `xml:"date" json:"dates,omitempty"`
	Type        []string `xml:"type" json:"types,omitempty"`
	Format      []string `xml:"format" json:"formats,omitempty"`
	Identifier  []string `xml:"identifier" json:"identifiers,omitempty"`
	Source      []string `xml:"source" json:"sources,omitempty"`
	Language    []string `xml:"language" json:"languages,omitempty"`
	Relation    []string `xml:"relation" json:"relations,omitempty"`
	Rights      []string `xml:"rights" json:"rights,omitempty"`
	Coverage    []string `xml:"coverage" json:"coverage,omitempty"`
}

// Metadata memuat satu elemen <dc> di bawah <metadata>.
type Metadata struct {
	DC DC `xml:"dc" json:"dc"`
}

// Record adalah satu record OAI lengkap (header + metadata).
// Struct ini dipakai sekaligus sebagai bentuk persistensi JSON mentah (raw).
type Record struct {
	Header      Header    `xml:"header" json:"header"`
	Metadata    *Metadata `xml:"metadata" json:"metadata,omitempty"`
	HarvestedAt time.Time `xml:"-" json:"harvested_at"`
}

// HasMetadata melaporkan apakah record membawa metadata Dublin Core
// (record terhapus / deleted tidak memilikinya).
func (r Record) HasMetadata() bool {
	return r.Metadata != nil
}

// DC memudahkan akses metadata tanpa guard pointer.
func (r Record) DC() DC {
	if r.Metadata == nil {
		return DC{}
	}
	return r.Metadata.DC
}

// ResumptionToken adalah <resumptionToken> beserta atributnya.
type ResumptionToken struct {
	Value            string `xml:",chardata" json:"value"`
	Cursor           int64  `xml:"cursor,attr" json:"cursor,omitempty"`
	CompleteListSize int64  `xml:"completeListSize,attr" json:"complete_list_size,omitempty"`
	Expired          string `xml:"expired,attr" json:"expired,omitempty"`
}

// IsEmpty menandai halaman terakhir (token kosong).
func (t *ResumptionToken) IsEmpty() bool {
	return t == nil || t.Value == ""
}

// ListRecords adalah balasan untuk verb=ListRecords.
type ListRecords struct {
	Records         []Record         `xml:"record" json:"records"`
	ResumptionToken *ResumptionToken `xml:"resumptionToken" json:"resumption_token,omitempty"`
}

// ListIdentifiers adalah balasan untuk verb=ListIdentifiers.
type ListIdentifiers struct {
	Headers         []Header         `xml:"header" json:"headers"`
	ResumptionToken *ResumptionToken `xml:"resumptionToken" json:"resumption_token,omitempty"`
}

// Set adalah pasangan setSpec/setName dari verb=ListSets.
type Set struct {
	Spec string `xml:"setSpec" json:"set_spec"`
	Name string `xml:"setName" json:"set_name"`
}

// ListSets adalah balasan untuk verb=ListSets.
type ListSets struct {
	Sets []Set `xml:"set" json:"sets"`
}

// Identify memuat info dasar repository.
type Identify struct {
	RepositoryName    string `xml:"repositoryName" json:"repository_name"`
	BaseURL           string `xml:"baseURL" json:"base_url"`
	ProtocolVersion   string `xml:"protocolVersion" json:"protocol_version"`
	EarliestDatestamp string `xml:"earliestDatestamp" json:"earliest_datestamp"`
	DeletedRecord     string `xml:"deletedRecord" json:"deleted_record"`
	Granularity       string `xml:"granularity" json:"granularity"`
}

// Request adalah elemen <request> yang mengembalikan parameter yang dipakai server.
type Request struct {
	Value string `xml:",chardata" json:"url"`
}

// OAIError adalah elemen <error code="...">.
type OAIError struct {
	Code  string `xml:"code,attr" json:"code"`
	Value string `xml:",chardata" json:"message"`
}

// Error membuatnya memenuhi interface error.
func (e OAIError) Error() string {
	return "oai error [" + e.Code + "]: " + e.Value
}

// Response adalah root <OAI-PMH>.
type Response struct {
	XMLName         xml.Name         `xml:"OAI-PMH" json:"-"`
	Request         Request          `xml:"request" json:"request"`
	Errors          []OAIError       `xml:"error" json:"errors,omitempty"`
	Identify        *Identify        `xml:"Identify" json:"identify,omitempty"`
	ListRecords     *ListRecords     `xml:"ListRecords" json:"list_records,omitempty"`
	ListIdentifiers *ListIdentifiers `xml:"ListIdentifiers" json:"list_identifiers,omitempty"`
	ListSets        *ListSets        `xml:"ListSets" json:"list_sets,omitempty"`
}

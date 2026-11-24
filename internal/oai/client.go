package oai

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"time"
	"unicode/utf8"
)

// LogFunc menulis log progres; pakai fmt.Printf bila tidak butuh format khusus.
type LogFunc func(format string, args ...any)

// DefaultBaseURL adalah endpoint OAI-PMH repository UNDIP.
const DefaultBaseURL = "https://eprints2.undip.ac.id/cgi/oai2"

// Client adalah klien OAI-PMH sederhana dengan retry + jeda antar permintaan.
type Client struct {
	BaseURL   string
	HTTP      *http.Client
	Delay     time.Duration
	UserAgent string
	Retries   int
	Log       LogFunc
}

// NewClient membuat klien dengan nilai default yang wajar.
func NewClient(baseURL string, delay time.Duration) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if delay < 0 {
		delay = 0
	}
	return &Client{
		BaseURL:   baseURL,
		HTTP:      &http.Client{Timeout: 90 * time.Second},
		Delay:     delay,
		UserAgent: "undip-oai-harvester/1.0 (+cli; polite OAI-PMH client)",
		Retries:   5,
	}
}

func (c *Client) logf(format string, args ...any) {
	if c.Log != nil {
		c.Log(format, args...)
	}
}

// get melakukan GET dengan parameter, retry untuk 5xx/429/gagal jaringan.
func (c *Client) get(ctx context.Context, params url.Values) ([]byte, error) {
	endpoint := c.BaseURL
	if u, err := url.Parse(c.BaseURL); err == nil && u.RawQuery == "" {
		endpoint = c.BaseURL + "?" + params.Encode()
	}

	var lastErr error
	for attempt := 0; attempt <= c.Retries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<uint(attempt-1)) * time.Second
			if backoff > 20*time.Second {
				backoff = 20 * time.Second
			}
			backoff += time.Duration(rand.Intn(500)) * time.Millisecond
			c.logf("retry #%d dalam %s (%v)", attempt, backoff.Round(time.Millisecond), lastErr)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", c.UserAgent)
		req.Header.Set("Accept", "application/xml, text/xml;q=0.9, */*;q=0.8")

		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 128<<20))
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}

		switch {
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastErr = fmt.Errorf("status %d: %s", resp.StatusCode, truncate(string(body), 200))
			// Hormati Retry-After bila ada.
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if d, err := time.ParseDuration(ra + "s"); err == nil && d > 0 {
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-time.After(d):
					}
				}
			}
			continue
		case resp.StatusCode != http.StatusOK:
			return nil, fmt.Errorf("oai: status %d untuk %s: %s", resp.StatusCode, endpoint, truncate(string(body), 300))
		}
		return body, nil
	}
	return nil, fmt.Errorf("oai: menyerah setelah %d percobaan: %w", c.Retries+1, lastErr)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// xmlCharOK mengikuti produksi karakter XML 1.0 yang diterima encoding/xml.
// Di antaranya MENOLAK non-character U+FFFE/U+FFFF yang kadang dikirim
// repository EPrints di tengah teks (menyebabkan "illegal character code").
func xmlCharOK(r rune) bool {
	return r == 0x9 || r == 0xA || r == 0xD ||
		(r >= 0x20 && r <= 0xD7FF) ||
		(r >= 0xE000 && r <= 0xFFFD) ||
		(r >= 0x10000 && r <= 0x10FFFF)
}

// sanitizeXML membuang karakter ilegal dari body XML sebelum di-decode:
//   - non-character U+FFFE/U+FFFF (kasus nyata dari eprints2.undip.ac.id)
//   - surrogate UTF-16 (U+D800–U+DFFF)
//   - byte UTF-8 tidak valid (memicu "illegal UTF-8 encoding")
//
// Bila body sudah bersih, slice ASLI dikembalikan tanpa penyalinan.
// Mengembalikan juga jumlah karakter yang dibuang.
func sanitizeXML(b []byte) ([]byte, int) {
	if utf8.Valid(b) {
		clean := true
		for _, r := range string(b) {
			if !xmlCharOK(r) {
				clean = false
				break
			}
		}
		if clean {
			return b, 0
		}
	}
	out := make([]byte, 0, len(b))
	dropped := 0
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if (r == utf8.RuneError && size == 1) || !xmlCharOK(r) {
			dropped++
			i += size
			continue
		}
		out = append(out, b[i:i+size]...)
		i += size
	}
	return out, dropped
}

// do mengirim permintaan dan men-decode response OAI-PMH.
// Setiap <error> server dikembalikan sebagai error.
func (c *Client) do(ctx context.Context, params url.Values) (*Response, error) {
	body, err := c.get(ctx, params)
	if err != nil {
		return nil, err
	}
	// Buang karakter ilegal (mis. U+FFFE) yang membuat encoding/xml gagal.
	var dropped int
	body, dropped = sanitizeXML(body)
	if dropped > 0 {
		c.logf("sanitasi XML: membuang %d karakter ilegal dari response", dropped)
	}
	// Lewati PInstruction (xml-stylesheet) bila ada sebelum root element.
	body = bytes.TrimLeft(body, " \t\r\n")
	for bytes.HasPrefix(body, []byte("<?")) {
		end := bytes.Index(body, []byte("?>"))
		if end < 0 {
			break
		}
		body = bytes.TrimLeft(body[end+2:], " \t\r\n")
	}

	dec := xml.NewDecoder(bytes.NewReader(body))
	var resp Response
	if err := dec.Decode(&resp); err != nil {
		return nil, fmt.Errorf("oai: gagal parse XML: %w (prefix: %q)", err, truncate(string(body), 200))
	}
	if len(resp.Errors) > 0 {
		return &resp, resp.Errors[0]
	}
	return &resp, nil
}

// Identify menjalankan verb=Identify.
func (c *Client) Identify(ctx context.Context) (*Identify, error) {
	p := url.Values{"verb": {"Identify"}}
	resp, err := c.do(ctx, p)
	if err != nil {
		return nil, err
	}
	if resp.Identify == nil {
		return nil, fmt.Errorf("oai: response Identify kosong")
	}
	return resp.Identify, nil
}

// ListSets menjalankan verb=ListSets (dipakai untuk memetakan setSpec -> nama).
func (c *Client) ListSets(ctx context.Context) ([]Set, error) {
	p := url.Values{"verb": {"ListSets"}}
	resp, err := c.do(ctx, p)
	if err != nil {
		return nil, err
	}
	if resp.ListSets == nil {
		return nil, nil
	}
	return resp.ListSets.Sets, nil
}

// ListRecordsParams membawa parameter halaman pertama ListRecords.
type ListRecordsParams struct {
	MetadataPrefix string
	Set            string
	From           string
	Until          string
	// Token, bila terisi, dipakai untuk melanjutkan halaman berikutnya
	// (OAI-PMH melarang kombinasi token dengan parameter lain).
	Token string
}

// ListRecords menjalankan verb=ListRecords untuk satu halaman.
func (c *Client) ListRecords(ctx context.Context, prm ListRecordsParams) (*ListRecords, error) {
	p := url.Values{"verb": {"ListRecords"}}
	if prm.Token != "" {
		p.Set("resumptionToken", prm.Token)
	} else {
		if prm.MetadataPrefix == "" {
			return nil, fmt.Errorf("oai: metadataPrefix wajib untuk halaman pertama")
		}
		p.Set("metadataPrefix", prm.MetadataPrefix)
		if prm.Set != "" {
			p.Set("set", prm.Set)
		}
		if prm.From != "" {
			p.Set("from", prm.From)
		}
		if prm.Until != "" {
			p.Set("until", prm.Until)
		}
	}
	resp, err := c.do(ctx, p)
	if err != nil {
		return nil, err
	}
	if resp.ListRecords == nil {
		return nil, fmt.Errorf("oai: response ListRecords kosong")
	}
	return resp.ListRecords, nil
}

// TolerantDecode decode response OAI-PMH dari reader.
// encoding/xml bersifat lepas-kuat terhadap elemen tambahan dan namespace,
// sehingga cukup untuk mem-parse balasan server apa adanya.
func TolerantDecode(r io.Reader) (*Response, error) {
	var resp Response
	if err := xml.NewDecoder(r).Decode(&resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

package embed

import (
	"strings"
	"unicode"
)

// Tokenize memecah teks menjadi token siap pakai TF-IDF:
//   - huruf kecil semua, digit tetap dipertahankan (mis. "2019", "covid19")
//   - hanya token yang mengandung huruf dan panjang >= minLen
//   - stopword/bahasa noise dibuang
func Tokenize(text string, minLen int) []string {
	if minLen <= 0 {
		minLen = 3
	}
	var out []string
	var b strings.Builder
	flush := func() {
		if b.Len() == 0 {
			return
		}
		tok := b.String()
		b.Reset()
		if len(tok) < minLen {
			return
		}
		hasLetter := false
		for _, r := range tok {
			if unicode.IsLetter(r) {
				hasLetter = true
				break
			}
		}
		if !hasLetter {
			return // angka murni (tahun dll) tidak jadi fitur
		}
		if IsStopword(tok) {
			return
		}
		out = append(out, tok)
	}
	for _, r := range strings.ToLower(text) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r > 0x7f && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return out
}

// Tokens menghasilkan token unigram; bila ngram >= 2 juga menambahkan
// bigram dari pasangan token berurutan yang tersisa setelah filtering.
func Tokens(text string, ngram int) []string {
	unigrams := Tokenize(text, 3)
	if ngram < 2 {
		return unigrams
	}
	out := make([]string, 0, len(unigrams)*2)
	out = append(out, unigrams...)
	for i := 0; i+1 < len(unigrams); i++ {
		out = append(out, unigrams[i]+"_"+unigrams[i+1])
	}
	return out
}

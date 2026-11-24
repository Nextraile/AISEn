// Package embed menangani tokenisasi, TF-IDF, dan pembangunan vektor
// berbasis feature-hashing (dense, siap dikirim ke Qdrant).
package embed

import "strings"

// Locale berisi daftar stopword untuk bahasa Indonesia dan Inggris
// (dipakai oleh tahap vektorisasi; ditempatkan di clean agar bisa
// dibagikan bila nanti ditambah tahap pencarian teks).

// IndonesianStopwords adalah stopword bahasa Indonesia yang umum muncul
// pada abstrak skripsi/jurnal.
var IndonesianStopwords = toSet(strings.Fields(`
yang dan di ke dari untuk dengan pada adalah ini itu dalam tidak akan juga
dapat lebih atau serta oleh karena telah harus agar maka namun tetapi sudah
belum para mereka kita saya kami anda secara seperti antara lain jika yaitu
yakni menjadi memiliki dilakukan terhadap mengenai berbagai tersebut setiap
masih hanya pun ada tiap hal per sampai hingga guna antara tersebut bisa
harus akan sedang bisa agar terjadi bahwa inilah itulah siapa mana bagaimana
mengapa dimana dimanakah berkaitan berkaitan dengan dalam rangka guna demi
serta akan namun padahal walaupun meskipun sementara sehingga hingga
masing berada terletak berikut berikutnya pertama kedua ketiga tersebutlah
disebut disebutkan namanya yaitu yakni artinya ialah yakni
`))

// EnglishStopwords adalah stopword bahasa Inggris generik.
var EnglishStopwords = toSet(strings.Fields(`
a an the and or but if then else when while for to of in on at by with from
as is are was were be been being am do does did doing have has had having
it its this that these those i you he she they we our your his her their
me him them us which who whom whose what where why how not no nor so than
too very can will just should now about into over under again further more
most other some such only own same s t don now
`))

// CommonNoise adalah token teknis/akademik yang tidak membedakan dokumen.
var CommonNoise = toSet(strings.Fields(`
abstract abstrack introduction background methodology methods result results
conclusion conclusionthe keywords keyword references doi issn isbn vol volume
issue journal article paper ijirae e3s web of conferences conference
pppp p ppp figure table appendix skripsi thesis disertasi jurnal
`))

func toSet(fields []string) map[string]struct{} {
	m := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		if f == "" {
			continue
		}
		m[strings.ToLower(f)] = struct{}{}
	}
	return m
}

// IsStopword memeriksa token terhadap gabungan daftar stopword/noise.
func IsStopword(tok string) bool {
	if _, ok := IndonesianStopwords[tok]; ok {
		return true
	}
	if _, ok := EnglishStopwords[tok]; ok {
		return true
	}
	if _, ok := CommonNoise[tok]; ok {
		return true
	}
	return false
}

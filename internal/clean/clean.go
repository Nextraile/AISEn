package clean

import (
	"context"
	"fmt"
	"strings"
	"time"

	"awesomeProject/internal/oai"
	"awesomeProject/internal/progress"
	"awesomeProject/internal/store"
)

// FromRaw mengubah satu record mentah menjadi record bersih.
// Kedua nilai balikan: record bersih, dan apakah record layak dipakai
// (False berarti record dibuang, mis. record terhapus/tanpa judul).
func FromRaw(raw oai.Record) (Record, bool) {
	cr := Record{
		Identifier: strings.TrimSpace(raw.Header.Identifier),
		Datestamp:  strings.TrimSpace(raw.Header.Datestamp),
		SetSpecs:   TextList(raw.Header.SetSpecs),
		Deleted:    strings.EqualFold(raw.Header.Status, "deleted"),
		CleansedAt: time.Now().UTC(),
	}
	if cr.Identifier == "" {
		return cr, false
	}

	dc := raw.DC()
	cr.Title = TextList(dc.Title)
	cr.Creators = TextList(dc.Creator)
	cr.Subjects = TextList(dc.Subject)
	cr.Descriptions = TextList(dc.Description)
	cr.Publishers = TextList(dc.Publisher)
	cr.Dates = TextList(dc.Date)
	cr.Types = TextList(dc.Type)
	cr.Languages = TextList(dc.Language)
	cr.Rights = TextList(dc.Rights)
	cr.Relations = TextList(dc.Relation)
	cr.Formats = TextList(dc.Format)
	cr.Contributors = TextList(dc.Contributor)
	cr.Identifiers = TextList(dc.Identifier)

	// Pisahkan URL (landing page / file) dari string kutipan.
	for _, id := range cr.Identifiers {
		if IsURL(id) {
			cr.URLs = appendUniqueLower(cr.URLs, id)
		} else if cr.Citation == "" {
			cr.Citation = Text(id)
		}
	}
	for _, rel := range cr.Relations {
		if IsURL(rel) {
			cr.URLs = appendUniqueLower(cr.URLs, rel)
		}
	}

	// --- Penilaian kualitas ---
	if cr.Deleted {
		cr.QualityFlags = append(cr.QualityFlags, FlagDeleted)
	}
	if len(cr.Title) == 0 {
		cr.QualityFlags = append(cr.QualityFlags, FlagNoTitle)
	} else {
		if len(cr.Title[0]) < 15 {
			cr.QualityFlags = append(cr.QualityFlags, FlagShortTitle)
		}
		if LooksBoilerplate(cr.Title[0]) {
			cr.QualityFlags = append(cr.QualityFlags, FlagBoilerplate)
		}
	}
	if len(cr.Creators) == 0 {
		cr.QualityFlags = append(cr.QualityFlags, FlagNoCreator)
	}
	if len(cr.Descriptions) == 0 {
		cr.QualityFlags = append(cr.QualityFlags, FlagNoDescription)
	}
	if len(cr.Dates) == 0 {
		cr.QualityFlags = append(cr.QualityFlags, FlagNoDate)
	}
	if cr.Citation == "" && len(cr.URLs) == 0 {
		cr.QualityFlags = append(cr.QualityFlags, FlagJunkCitation)
	}

	// Record terhapus atau tanpa judul sama sekali tidak berguna untuk pipeline.
	cr.Usable = !cr.Deleted && len(cr.Title) > 0
	return cr, cr.Usable
}

func appendUniqueLower(dst []string, v string) []string {
	for _, x := range dst {
		if strings.EqualFold(x, v) {
			return dst
		}
	}
	return append(dst, v)
}

// Run menjalankan tahap cleansing penuh: baca raw -> bersih -> filter -> tulis.
// Deduplikasi dilakukan berdasarkan identifier OAI (record ganda dibuang),
// lalu Options menyaring record berbasis metadata (lihat options.go).
// Mengembalikan path output dan statistiknya.
func Run(ctx context.Context, inPath, outPath string, opts Options, logf oai.LogFunc) (string, CleansingStats, error) {
	st := NewStats()
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if err := store.EnsureDir(outPath); err != nil {
		return outPath, st, err
	}

	seen := make(map[string]struct{})
	total, _ := store.CountJSONL(inPath)
	out, err := store.NewJSONLWriter[Record](outPath)
	if err != nil {
		return outPath, st, err
	}
	defer out.Close()

	_, err = store.ReadJSONL[oai.Record](inPath, func(raw oai.Record) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		st.Input++
		if total > 0 {
			fmt.Print(progress.Bar(st.Input, total, 30))
		}
		if raw.Header.Identifier != "" {
			key := strings.ToLower(strings.TrimSpace(raw.Header.Identifier))
			if _, ok := seen[key]; ok {
				st.Dupes++
				st.Dropped++
				return nil
			}
			seen[key] = struct{}{}
		}
		rec, ok := FromRaw(raw)
		if !ok {
			st.Dropped++
			if rec.Deleted {
				st.Deleted++
			}
			for _, f := range rec.QualityFlags {
				st.addFlag(f)
			}
			return nil
		}
		// Penyaringan metadata (kriteria legacy: abstrak cukup panjang,
		// bukan dokumen administratif).
		if reason := opts.Reject(rec); reason != "" {
			st.Dropped++
			switch reason {
			case "administrative":
				st.AdminFiltered++
			default: // no_abstract | short_abstract
				st.NoAbstract++
			}
			return nil
		}
		for _, f := range rec.QualityFlags {
			st.addFlag(f)
		}
		st.Kept++
		return out.Write(rec)
	})
	if err != nil {
		return outPath, st, fmt.Errorf("clean: %w", err)
	}
	if err := out.Close(); err != nil {
		return outPath, st, err
	}
	fmt.Println()
	logf("clean selesai: %s -> %s", st, outPath)
	return outPath, st, nil
}

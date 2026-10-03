package release

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzParse exists because the corpus in parse_test.go is a list of names I
// thought of, and the names that break a parser are the ones nobody thought of.
// A release name arrives from an indexer, which got it from a stranger, so the
// only acceptable behaviour on any input at all is "returns".
//
// Run longer with: go test ./internal/release/ -run xxx -fuzz FuzzParse
func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		"The.Matrix.1999.1080p.BluRay.x264-SWTYBLZ",
		"Breaking.Bad.S05E14.1080p.BluRay.x264-DEMAND",
		"Blade.Runner.2049.2017.2160p.UHD.BluRay.x265-TERMiNAL",
		"Show.Name.S02E05-E07.1080p.BluRay.x264",
		"The.Daily.Show.2024.03.14.1080p.WEB.h264-GROUP",
		"Movie Title (2019) [1080p] [BluRay] [5.1]",
		"", ".", "-", "[", "S01E01", "1080p", "../../etc/passwd",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, name string) {
		got := Parse(name)

		// 1. A title must never carry a control character into a log line, a
		//    template or eventually a filename.
		if strings.ContainsAny(got.Title, "\x00\n\r\t") {
			t.Fatalf("control character survived into the title: %q from %q", got.Title, name)
		}

		// 2. The title is ALWAYS valid UTF-8, whatever went in. Invalid bytes
		//    are dropped rather than replaced, so nothing downstream — JSON
		//    encoding, the database, a template — has to cope with them.
		if !utf8.ValidString(got.Title) {
			t.Fatalf("invalid UTF-8 in the title: %q from %q", got.Title, name)
		}

		// 3. Sanitising must not make the string bigger. Replacing invalid
		//    bytes with U+FFFD would turn one byte into three, so a
		//    "sanitised" title could outgrow its input — which the fuzzer
		//    found in about a second.
		if len(got.Title) > len(name) {
			t.Fatalf("title %q (%d bytes) is longer than the input %q (%d bytes)",
				got.Title, len(got.Title), name, len(name))
		}

		// 4. Episode numbers must be sane. A season of 10^9 would be a
		//    denial-of-service against whatever allocates by season count.
		if got.Season > 10000 {
			t.Fatalf("implausible season %d from %q", got.Season, name)
		}
		if len(got.Episodes) > 200 {
			t.Fatalf("%d episodes from one name: %q", len(got.Episodes), name)
		}
		for _, e := range got.Episodes {
			if e < 0 {
				t.Fatalf("negative episode %d from %q", e, name)
			}
		}

		// 5. A year is a year.
		if got.Year != 0 && !plausibleYear(got.Year) {
			t.Fatalf("implausible year %d from %q", got.Year, name)
		}

		// 6. Classification must not contradict itself.
		if got.FullSeason && len(got.Episodes) > 0 {
			t.Fatalf("both a full season and individual episodes from %q", name)
		}

		// 7. Quality classification must never panic and must stay in the ladder.
		q := QualityOf(got)
		var found bool
		for _, known := range DefaultLadder {
			if known.Name == q.Name {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("QualityOf produced %q, which is not in the ladder, from %q", q.Name, name)
		}
	})
}

// FuzzProfileTerms checks that an operator cannot write a profile term that
// breaks the pipeline. Terms are compiled with the standard library, so they
// are RE2 and cannot backtrack — but they can still fail to compile, and that
// must be an error rather than a panic.
func FuzzProfileTerms(f *testing.F) {
	for _, seed := range []string{"Atmos", "/x26[45]/", "/(/", "5.1", "", "/", "//", "/(a+)+b/"} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, term string) {
		p := Profile{
			Name: "fuzz", Allowed: []string{"WEBDL-1080p"}, Cutoff: "WEBDL-1080p",
			Preferred: []ScoredTerm{{Term: term, Score: 1}},
		}
		if err := p.Compile(); err != nil {
			return // refusing a bad term is the correct outcome
		}
		// A term that compiled must be usable without panicking.
		p.Score(Parse("Film.2020.1080p.WEB-DL.x264-GROUP"))
		p.Accepts(Parse("Film.2020.1080p.WEB-DL.x264-GROUP"))
	})
}

package release

import (
	"strings"
	"testing"
	"time"
)

// want describes the fields a case asserts. Zero values are not checked, so a
// case states only what it is actually about — otherwise every new field added
// to Parsed would break every test in the corpus.
type want struct {
	title      string
	year       int
	season     int // 0 means "not asserted"; use -1 to assert "no season"
	episodes   []int
	fullSeason bool
	airDate    string
	resolution Resolution
	source     Source
	codec      Codec
	audio      Audio
	channels   string
	hdr        []string
	editions   []string
	revision   int
	proper     bool
	group      string
	container  string
	confidence Confidence
}

func check(t *testing.T, name string, w want) {
	t.Helper()
	got := Parse(name)

	if w.title != "" && got.Title != w.title {
		t.Errorf("title = %q, want %q", got.Title, w.title)
	}
	if w.year != 0 && got.Year != w.year {
		t.Errorf("year = %d, want %d", got.Year, w.year)
	}
	if w.season != 0 {
		expect := w.season
		if expect == -1 {
			expect = -1
		}
		if got.Season != expect {
			t.Errorf("season = %d, want %d", got.Season, expect)
		}
	}
	if w.episodes != nil && !equalInts(got.Episodes, w.episodes) {
		t.Errorf("episodes = %v, want %v", got.Episodes, w.episodes)
	}
	if w.fullSeason && !got.FullSeason {
		t.Error("full season was not detected")
	}
	if w.airDate != "" && got.AirDate != w.airDate {
		t.Errorf("air date = %q, want %q", got.AirDate, w.airDate)
	}
	if w.resolution != "" && got.Resolution != w.resolution {
		t.Errorf("resolution = %q, want %q", got.Resolution, w.resolution)
	}
	if w.source != "" && got.Source != w.source {
		t.Errorf("source = %q, want %q", got.Source, w.source)
	}
	if w.codec != "" && got.Codec != w.codec {
		t.Errorf("codec = %q, want %q", got.Codec, w.codec)
	}
	if w.audio != "" && got.Audio != w.audio {
		t.Errorf("audio = %q, want %q", got.Audio, w.audio)
	}
	if w.channels != "" && got.Channels != w.channels {
		t.Errorf("channels = %q, want %q", got.Channels, w.channels)
	}
	if w.hdr != nil && !equalStrings(got.HDR, w.hdr) {
		t.Errorf("hdr = %v, want %v", got.HDR, w.hdr)
	}
	if w.editions != nil && !equalStrings(got.Editions, w.editions) {
		t.Errorf("editions = %v, want %v", got.Editions, w.editions)
	}
	if w.revision != 0 && got.Revision != w.revision {
		t.Errorf("revision = %d, want %d", got.Revision, w.revision)
	}
	if w.proper && !got.Proper {
		t.Error("PROPER was not detected")
	}
	if w.group != "" && got.Group != w.group {
		t.Errorf("group = %q, want %q", got.Group, w.group)
	}
	if w.container != "" && got.Container != w.container {
		t.Errorf("container = %q, want %q", got.Container, w.container)
	}
	if w.confidence != "" && got.Confidence != w.confidence {
		t.Errorf("confidence = %q, want %q", got.Confidence, w.confidence)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Films
// ---------------------------------------------------------------------------

func TestParseMovies(t *testing.T) {
	cases := map[string]want{
		"The.Matrix.1999.1080p.BluRay.x264-SWTYBLZ": {
			title: "The Matrix", year: 1999, season: -1,
			resolution: Resolution1080p, source: SourceBluRay, codec: CodecH264,
			group: "SWTYBLZ", confidence: ConfidenceHigh,
		},
		"Arrival.2016.2160p.UHD.BluRay.x265.10bit.HDR.DTS-HD.MA.7.1-SWTYBLZ": {
			title: "Arrival", year: 2016,
			resolution: Resolution2160p, source: SourceBluRay, codec: CodecH265,
			audio: AudioDTSHD, channels: "7.1", hdr: []string{"HDR"},
			group: "SWTYBLZ",
		},
		"Dune.Part.Two.2024.1080p.WEB-DL.DDP5.1.Atmos.H.264-FLUX": {
			title: "Dune Part Two", year: 2024,
			resolution: Resolution1080p, source: SourceWEBDL, codec: CodecH264,
			audio: AudioEAC3, channels: "5.1", group: "FLUX",
		},
		"Oppenheimer.2023.REMUX.2160p.BluRay.HDR.DV.TrueHD.7.1.Atmos-FraMeSToR": {
			title: "Oppenheimer", year: 2023,
			resolution: Resolution2160p, source: SourceRemux,
			audio: AudioTrueHD, channels: "7.1", group: "FraMeSToR",
		},
		"Alien.1979.Directors.Cut.1080p.BluRay.DTS.x264-CtrlHD": {
			title: "Alien", year: 1979, editions: []string{"Director's Cut"},
			resolution: Resolution1080p, source: SourceBluRay, audio: AudioDTS,
			codec: CodecH264, group: "CtrlHD",
		},
		"The.Lord.of.the.Rings.The.Two.Towers.2002.EXTENDED.1080p.BluRay.x264-AMIABLE": {
			title: "The Lord of the Rings The Two Towers", year: 2002,
			editions: []string{"Extended"}, resolution: Resolution1080p,
			group: "AMIABLE",
		},
		"Some.Film.2019.PROPER.1080p.WEBRip.x265-RARBG": {
			title: "Some Film", year: 2019, revision: 1, proper: true,
			source: SourceWEBRip, codec: CodecH265, group: "RARBG",
		},
		"Another.Film.2021.REPACK2.1080p.BluRay.x264-GROUP": {
			title: "Another Film", year: 2021, revision: 2,
			source: SourceBluRay, group: "GROUP",
		},
		"Parasite.2019.LIMITED.1080p.BluRay.x264-CADAVER.mkv": {
			title: "Parasite", year: 2019, container: "mkv", group: "CADAVER",
		},
		"Movie Title (2019) [1080p] [BluRay] [5.1]": {
			title: "Movie Title", year: 2019,
			resolution: Resolution1080p, source: SourceBluRay, channels: "5.1",
		},
	}

	for name, w := range cases {
		t.Run(name, func(t *testing.T) { check(t, name, w) })
	}
}

// The case that breaks naive parsers: a title that ends in a year-shaped
// number. Picking the first match gives a film nobody released.
func TestTitlesContainingYears(t *testing.T) {
	cases := map[string]want{
		"Blade.Runner.2049.2017.2160p.UHD.BluRay.x265-TERMiNAL": {
			title: "Blade Runner 2049", year: 2017, resolution: Resolution2160p,
		},
		"2012.2009.1080p.BluRay.x264-REFiNED": {
			title: "2012", year: 2009, resolution: Resolution1080p,
		},
		"1917.2019.1080p.BluRay.x264-SPARKS": {
			title: "1917", year: 2019,
		},
		"Blade Runner 2049 (2017) 1080p BluRay": {
			title: "Blade Runner 2049", year: 2017,
		},
		// A bracketed year wins even when a bare one follows it.
		"Some.Title.(1999).2020.Remaster.1080p.BluRay": {
			year: 1999,
		},
	}
	for name, w := range cases {
		t.Run(name, func(t *testing.T) { check(t, name, w) })
	}
}

// ---------------------------------------------------------------------------
// Television
// ---------------------------------------------------------------------------

func TestParseEpisodes(t *testing.T) {
	cases := map[string]want{
		"Breaking.Bad.S05E14.Ozymandias.1080p.BluRay.x264-DEMAND": {
			title: "Breaking Bad", season: 5, episodes: []int{14},
			resolution: Resolution1080p, source: SourceBluRay, group: "DEMAND",
			confidence: ConfidenceHigh,
		},
		"The.Office.US.S03E12.720p.WEB-DL.AAC2.0.H.264-GROUP": {
			title: "The Office US", season: 3, episodes: []int{12},
			resolution: Resolution720p, source: SourceWEBDL, audio: AudioAAC,
		},
		"Some.Show.1x05.HDTV.XviD-LOL": {
			title: "Some Show", season: 1, episodes: []int{5},
			source: SourceHDTV, codec: CodecXviD, group: "LOL",
		},
		"Show.Name.S01E01E02.1080p.WEB-DL": {
			title: "Show Name", season: 1, episodes: []int{1, 2},
		},
		"Show.Name.S02E05-E07.1080p.BluRay.x264": {
			title: "Show Name", season: 2, episodes: []int{5, 6, 7},
		},
		"The.Office.US.S03.COMPLETE.1080p.BluRay.x265-RARBG": {
			title: "The Office US", season: 3, fullSeason: true, group: "RARBG",
		},
		"Some.Show.Season.2.1080p.WEB-DL": {
			title: "Some Show", season: 2, fullSeason: true,
		},
		"The.Daily.Show.2024.03.14.1080p.WEB.h264-GROUP": {
			title: "The Daily Show", airDate: "2024-03-14", year: 2024,
			resolution: Resolution1080p, codec: CodecH264,
		},
	}
	for name, w := range cases {
		t.Run(name, func(t *testing.T) { check(t, name, w) })
	}
}

// ---------------------------------------------------------------------------
// The ambiguities that matter
// ---------------------------------------------------------------------------

// "WEB-DL" ends in "-DL". Naive trailing-group extraction yields "DL" for every
// WEB-DL release on the planet.
func TestHyphenatedQualityTermsAreNotMistakenForGroups(t *testing.T) {
	for _, name := range []string{
		"Some.Movie.2020.1080p.WEB-DL",
		"Some.Movie.2020.1080p.Blu-Ray",
		"Some.Movie.2020.1080p.WEB-DL.DTS-HD",
		"Some.Show.S01E01.1080p.WEB-DL.DD-EX",
	} {
		t.Run(name, func(t *testing.T) {
			got := Parse(name)
			for _, bad := range []string{"DL", "Ray", "HD", "EX"} {
				if strings.EqualFold(got.Group, bad) {
					t.Errorf("group = %q, which is half of a quality term", got.Group)
				}
			}
		})
	}
}

// "Remux" and "BluRay" appear together; remux is the stronger claim and must
// win, because a remux and a re-encode are not the same quality at all.
func TestRemuxBeatsBluRay(t *testing.T) {
	got := Parse("Film.2020.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.7.1-GROUP")
	if got.Source != SourceRemux {
		t.Errorf("source = %q, want remux", got.Source)
	}
}

// "WEB-DL" and "WEBRip" are different qualities and both contain "WEB".
func TestWebVariantsAreDistinguished(t *testing.T) {
	for name, wantSource := range map[string]Source{
		"Film.2020.1080p.WEB-DL.x264":  SourceWEBDL,
		"Film.2020.1080p.WEBDL.x264":   SourceWEBDL,
		"Film.2020.1080p.WEBRip.x264":  SourceWEBRip,
		"Film.2020.1080p.WEB-Rip.x264": SourceWEBRip,
		"Film.2020.1080p.WEB.x264":     SourceWEBDL,
	} {
		if got := Parse(name); got.Source != wantSource {
			t.Errorf("%s: source = %q, want %q", name, got.Source, wantSource)
		}
	}
}

// DTS-HD MA, DTS-X, DTS-HD and DTS are four different things and three of them
// contain the fourth.
func TestAudioSpecificityOrdering(t *testing.T) {
	for name, wantAudio := range map[string]Audio{
		"Film.2020.1080p.BluRay.DTS-HD.MA.5.1-G": AudioDTSHD,
		"Film.2020.1080p.BluRay.DTS-X.7.1-G":     AudioDTSX,
		"Film.2020.1080p.BluRay.DTS-HD.5.1-G":    AudioDTSHD,
		"Film.2020.1080p.BluRay.DTS.5.1-G":       AudioDTS,
		"Film.2020.1080p.WEB-DL.DDP5.1-G":        AudioEAC3,
		"Film.2020.1080p.WEB-DL.DD5.1-G":         AudioAC3,
		"Film.2020.1080p.BluRay.TrueHD.7.1-G":    AudioTrueHD,
	} {
		if got := Parse(name); got.Audio != wantAudio {
			t.Errorf("%s: audio = %q, want %q", name, got.Audio, wantAudio)
		}
	}
}

// Separator-anchored patterns must not fire inside words. "TS" inside "GUTS"
// would turn a Blu-ray into a telesync, which is the difference between
// watching a film and watching somebody's shoulder.
func TestVocabularyDoesNotMatchInsideWords(t *testing.T) {
	cases := map[string]func(Parsed) string{
		"Guts.and.Glory.2019.1080p.BluRay.x264-G": func(p Parsed) string {
			if p.Source == SourceTelesync {
				return "the TS in Guts was read as a telesync"
			}
			return ""
		},
		"The.Cam.Chronicles.2019.1080p.BluRay-G": func(p Parsed) string {
			if p.Source == SourceCAM {
				return "the Cam in the title was read as a cam rip"
			}
			return ""
		},
		"Shadrick.2019.1080p.WEB-DL-G": func(p Parsed) string {
			if len(p.HDR) > 0 {
				return "HDR matched inside a word"
			}
			return ""
		},
		"Advice.2019.1080p.WEB-DL-G": func(p Parsed) string {
			if p.Codec == CodecH264 {
				return "AVC matched inside Advice"
			}
			return ""
		},
	}
	for name, assert := range cases {
		t.Run(name, func(t *testing.T) {
			if msg := assert(Parse(name)); msg != "" {
				t.Error(msg)
			}
		})
	}
}

// A tag word before the year or the episode marker is part of the title. Read
// as a tag, it cut the title short — "The.French.Connection.1971" was "The",
// "Russian.Doll.S01E01" was nothing at all — and no search, by a person or by
// automatic acquisition, could match those titles. After the year or the
// marker, the same words are tags as before; with neither, the vocabulary is
// read everywhere, as before.
func TestATagWordBeforeTheYearIsPartOfTheTitle(t *testing.T) {
	cases := []struct {
		name, title       string
		languages, extras []string
		resolution        Resolution
		source            Source
		season            int
	}{
		{"The.French.Connection.1971.1080p.BluRay.x264-GRP", "The French Connection", nil, nil, Resolution1080p, SourceBluRay, -1},
		{"The.Italian.Job.2003.1080p.BluRay.x264-GRP", "The Italian Job", nil, nil, Resolution1080p, SourceBluRay, -1},
		{"Uncut.Gems.2019.1080p.WEB-DL.DDP5.1.H.264-GRP", "Uncut Gems", nil, nil, Resolution1080p, SourceWEBDL, -1},
		{"Charlottes.Web.2006.1080p.BluRay.x264-GRP", "Charlottes Web", nil, nil, Resolution1080p, SourceBluRay, -1},
		{"Internal.Affairs.1990.1080p.BluRay.x264-GRP", "Internal Affairs", nil, nil, Resolution1080p, SourceBluRay, -1},
		{"Russian.Doll.S01E01.1080p.WEB.H264-GRP", "Russian Doll", nil, nil, Resolution1080p, SourceWEBDL, 1},
		// After the anchor, still tags.
		{"Dune.2021.TRUEFRENCH.1080p.BluRay.x264-GRP", "Dune", []string{"French"}, nil, Resolution1080p, SourceBluRay, -1},
		{"The.French.Connection.1971.FRENCH.1080p.BluRay.x264-GRP", "The French Connection", []string{"French"}, nil, Resolution1080p, SourceBluRay, -1},
		{"Die.Simpsons.S30E01.German.DL.1080p.WEB.x264-GRP", "Die Simpsons", []string{"German"}, nil, Resolution1080p, SourceWEBDL, 30},
		{"Uncut.Gems.2019.UNCUT.1080p.BluRay.x264-GRP", "Uncut Gems", nil, []string{"Uncut"}, Resolution1080p, SourceBluRay, -1},
		// No year and no marker: read everywhere, as before.
		{"Some.Film.1080p.BluRay.x264-GRP", "Some Film", nil, nil, Resolution1080p, SourceBluRay, -1},
	}
	for _, c := range cases {
		p := Parse(c.name)
		if p.Title != c.title || !equalStrings(p.Languages, c.languages) || !equalStrings(p.Editions, c.extras) ||
			p.Resolution != c.resolution || p.Source != c.source || p.Season != c.season {
			t.Errorf("%s:\n  title %q languages %v editions %v %s %s season %d\n  want  %q languages %v editions %v %s %s season %d",
				c.name, p.Title, p.Languages, p.Editions, p.Resolution, p.Source, p.Season,
				c.title, c.languages, c.extras, c.resolution, c.source, c.season)
		}
	}
}

func TestHDRFormatsAreAdditive(t *testing.T) {
	got := Parse("Film.2021.2160p.BluRay.REMUX.DV.HDR10.HEVC.TrueHD.7.1-G")
	if len(got.HDR) < 2 {
		t.Errorf("hdr = %v, want both Dolby Vision and HDR10", got.HDR)
	}
	var hasDV, hasHDR10 bool
	for _, h := range got.HDR {
		if h == "DV" {
			hasDV = true
		}
		if h == "HDR10" {
			hasHDR10 = true
		}
	}
	if !hasDV || !hasHDR10 {
		t.Errorf("hdr = %v, want DV and HDR10", got.HDR)
	}
}

// ---------------------------------------------------------------------------
// Hostile input
// ---------------------------------------------------------------------------

// A release name comes from an indexer, which got it from a stranger. None of
// these may panic, hang, or produce something that could become a path.
func TestHostileNamesAreSurvived(t *testing.T) {
	hostile := []string{
		"",
		".",
		"....................",
		strings.Repeat("S01E01.", 500),
		strings.Repeat("a", MaxNameLength*4),
		strings.Repeat("1080p.", 2000),
		"../../../../etc/passwd",
		"..\\..\\..\\windows\\system32\\config\\sam",
		"Film.2020\x00.1080p.BluRay",
		"Film\n2020\r\n1080p",
		"<script>alert(1)</script>.2020.1080p.BluRay-G",
		"'; DROP TABLE media; --.2020.1080p",
		"Ẅ̷̢̛̻͇̮̗͖̞̈́͊͠e̸̠͇͐i̴̺͐r̶̥̈d̷̰̈.2020.1080p.BluRay",
		strings.Repeat("[", 5000),
		strings.Repeat("(", 5000) + strings.Repeat(")", 5000),
		"S99999999999999999999E99999999999999999999",
		"Film.99999999.1080p",
	}

	for i, name := range hostile {
		done := make(chan Parsed, 1)
		go func() { done <- Parse(name) }()

		select {
		case got := <-done:
			// The title must never be usable as a path component. Import builds
			// paths from sanitised metadata, not from this, but a parser that
			// happily emits "../../etc" invites somebody to shortcut that.
			if strings.Contains(got.Title, "..") && strings.ContainsAny(got.Title, `/\`) {
				t.Errorf("case %d produced a traversal-shaped title: %q", i, got.Title)
			}
			if strings.ContainsAny(got.Title, "\x00\n\r") {
				t.Errorf("case %d left a control character in the title: %q", i, got.Title)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("case %d did not finish in 2s: %.60q", i, name)
		}
	}
}

// RE2 guarantees linear-time matching, which is the reason a crafted name
// cannot burn CPU here.
//
// The property is measured as a RATIO rather than as wall-clock. An absolute
// threshold is a test that fails on a slow machine and on every run under
// -race, which slows execution by roughly ten times — and a test that fails for
// reasons unrelated to the thing it guards gets deleted by whoever is trying to
// ship. Doubling the input on a backtracking engine multiplies the time by a
// large factor; on RE2 it roughly doubles it.
func TestPathologicalInputIsLinear(t *testing.T) {
	pathological := func(n int) string {
		return strings.Repeat("S01E01-E02.", n) + strings.Repeat("a", n*10)
	}

	measure := func(name string) time.Duration {
		// Warm, then measure, so the first call's lazy work is not counted.
		Parse(name)
		start := time.Now()
		for i := 0; i < 30; i++ {
			Parse(name)
		}
		return time.Since(start)
	}

	short := measure(pathological(20))
	long := measure(pathological(40))

	if short <= 0 {
		t.Skip("the clock is too coarse to measure this")
	}
	ratio := float64(long) / float64(short)

	// Linear would be ~2. Eight is generous room for measurement noise and
	// still nowhere near the blow-up a backtracking engine would show.
	if ratio > 8 {
		t.Errorf("doubling the input multiplied the time by %.1fx (%v -> %v); "+
			"that is not linear behaviour", ratio, short, long)
	}
}

func TestLongNamesAreTruncatedNotRejected(t *testing.T) {
	name := "Real.Title.2020.1080p.BluRay.x264-GROUP" + strings.Repeat(".padding", 2000)
	got := Parse(name)
	if got.Title != "Real Title" {
		t.Errorf("title = %q, want the real prefix", got.Title)
	}
	if len(got.Raw) != len(name) {
		t.Error("Raw should hold the full original, for the audit trail")
	}
}

// ---------------------------------------------------------------------------
// Honesty
// ---------------------------------------------------------------------------

// The parser must say when it is guessing. A caller automating a grab on a
// low-confidence parse is making a decision the parser did not support.
func TestConfidenceReflectsWhatWasActuallyFound(t *testing.T) {
	for name, wantConf := range map[string]Confidence{
		"The.Matrix.1999.1080p.BluRay.x264-G": ConfidenceHigh,
		"Breaking.Bad.S05E14.1080p.BluRay-G":  ConfidenceHigh,
		"Some.Film.2019":                      ConfidenceMedium,
		"Some.Film.1080p":                     ConfidenceMedium,
		"just some words here":                ConfidenceLow,
		"":                                    ConfidenceLow,
	} {
		if got := Parse(name); got.Confidence != wantConf {
			t.Errorf("%q: confidence = %q, want %q (parsed %+v)", name, got.Confidence, wantConf, got)
		}
	}
}

// Unrecognised tokens are reported rather than silently dropped, so an operator
// looking at a wrong decision can see what the parser did not understand.
func TestUnrecognisedTokensAreReported(t *testing.T) {
	got := Parse("Film.2020.1080p.BluRay.WEIRDFLAG.x264-GROUP")
	var found bool
	for _, u := range got.Unmatched {
		if strings.EqualFold(u, "WEIRDFLAG") {
			found = true
		}
	}
	if !found {
		t.Errorf("unmatched = %v, want it to mention WEIRDFLAG", got.Unmatched)
	}
}

// A film has no season. Reporting season 0 would make "season 0" —  a real
// thing, specials — indistinguishable from "not a series".
func TestFilmsHaveNoSeasonRatherThanSeasonZero(t *testing.T) {
	got := Parse("The.Matrix.1999.1080p.BluRay.x264-G")
	if got.Season != -1 {
		t.Errorf("season = %d, want -1", got.Season)
	}
	if got.IsEpisode() {
		t.Error("a film was classified as an episode")
	}
}

func TestEpisodesAreClassifiedAsEpisodes(t *testing.T) {
	for _, name := range []string{
		"Breaking.Bad.S05E14.1080p-G",
		"Some.Show.1x05.HDTV-G",
		"Some.Show.S03.COMPLETE.1080p-G",
		"The.Daily.Show.2024.03.14.1080p-G",
	} {
		if !Parse(name).IsEpisode() {
			t.Errorf("%q was not classified as an episode", name)
		}
	}
}

func TestParseIsDeterministic(t *testing.T) {
	name := "Film.2021.2160p.BluRay.REMUX.DV.HDR10.HEVC.TrueHD.7.1.Extended-GROUP"
	first := Parse(name)
	for i := 0; i < 50; i++ {
		got := Parse(name)
		if got.Title != first.Title || got.Source != first.Source ||
			got.Audio != first.Audio || !equalStrings(got.HDR, first.HDR) {
			t.Fatalf("parse %d differed from the first", i)
		}
	}
}

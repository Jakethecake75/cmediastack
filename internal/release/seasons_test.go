package release

import "testing"

// TestSeveralSeasonsAreRecognised pins what counts as a release of more than
// one season (ADR-0033, decision 2). The parser reads the first season marker
// and stops, so "Show.S01-S03" parses as a pack of season 1; this is what
// keeps a three-season download from being grabbed as one.
func TestSeveralSeasonsAreRecognised(t *testing.T) {
	several := []string{
		"Show.S01-S03.1080p.BluRay.x264-GRP",
		"Show.S01-03.720p.WEB-DL-GRP",
		"Show.S01.-.S03.1080p-GRP",
		"Show.Seasons.1-3.1080p-GRP",
		"Show.Season.1-3.1080p-GRP",
		"Show Season 1 - 3 1080p",
		"Show.S01.S02.1080p.WEB-DL-GRP",
		"Show.Complete.Series.1080p.BluRay-GRP",
		"Show.The.Complete.Series.720p-GRP",
	}
	for _, name := range several {
		if !NamesSeveralSeasons(name) {
			t.Errorf("%q names several seasons, and was not recognised as doing so", name)
		}
	}

	one := []string{
		"Show.S02.1080p.BluRay.x264-GRP",
		"Show.S02.COMPLETE.1080p.WEB-DL-GRP",
		"Show.Season.2.1080p-GRP",
		"Show.S02E01-E03.1080p-GRP",
		"Show.S02E01E02.1080p-GRP",
		"Show.S02E05.1080p-GRP",
		"Show.2019.S02.1080p-GRP",
		"Show.S02.5.1.1080p-GRP",
		"Show.S02.1080p.DDP5.1-GRP",
		"S.W.A.T.2017.S03.1080p-GRP",
		"Series.Complete.S02.1080p-GRP",
	}
	for _, name := range one {
		if NamesSeveralSeasons(name) {
			t.Errorf("%q names one season, and was taken for several", name)
		}
	}
}

// TestTheSeasonsOfAPackAreItsSpan pins ADR-0057, decision 2: the lowest and
// highest season a name gives, and a complete series to its last.
func TestTheSeasonsOfAPackAreItsSpan(t *testing.T) {
	for _, tc := range []struct {
		name        string
		first, last int
		several     bool
	}{
		{"Show.S01-S05.1080p.BluRay.x264-GRP", 1, 5, true},
		{"Show.S02-04.720p.WEB-DL-GRP", 2, 4, true},
		{"Show.Seasons.3-7.1080p-GRP", 3, 7, true},
		{"Show.S03.S01.S02.1080p-GRP", 1, 3, true},
		{"Show.S00-S02.1080p-GRP", 0, 2, true},
		{"Show.Complete.Series.1080p.BluRay-GRP", 1, 0, true},
		{"Show.The.Complete.Series.S01-S06.1080p-GRP", 1, 6, true},
		{"Show.S10-S12.1080p-GRP", 10, 12, true},
		{"Show.S02.1080p.BluRay-GRP", 0, 0, false},
		{"Show.S02E01-E03.1080p-GRP", 0, 0, false},
	} {
		first, last, several := SeasonSpan(tc.name)
		if first != tc.first || last != tc.last || several != tc.several {
			t.Errorf("%s: %d to %d (%v), want %d to %d (%v)", tc.name, first, last, several,
				tc.first, tc.last, tc.several)
		}
	}
}

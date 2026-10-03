package importer

import (
	"errors"
	"strings"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/release"
)

func parse(t *testing.T, name string) release.Parsed {
	t.Helper()
	return release.Parse(name)
}

// The layout every other media server already understands, because an
// operator's library outlives this software.
func TestAFilmIsLaidOutWhereAnyMediaServerWouldLookForIt(t *testing.T) {
	l, err := PlanLayout(parse(t, "Blade Runner 2049 2017 1080p BluRay x264-GROUP.mkv"), "Bluray-1080p")
	if err != nil {
		t.Fatal(err)
	}
	if l.Folder != "Blade Runner 2049 (2017)" {
		t.Errorf("folder = %q", l.Folder)
	}
	want := "Blade Runner 2049 (2017)/Blade Runner 2049 (2017) [Bluray-1080p].mkv"
	if l.RelPath != want {
		t.Errorf("path = %q, want %q", l.RelPath, want)
	}
	if l.IsTelevision {
		t.Error("a film was classified as television")
	}
}

func TestAnEpisodeGetsASeasonFolderAndACode(t *testing.T) {
	l, err := PlanLayout(parse(t, "The.Expanse.S02E05.1080p.WEB-DL.DD5.1.H264-GRP.mkv"), "WEBDL-1080p")
	if err != nil {
		t.Fatal(err)
	}
	if !l.IsTelevision {
		t.Fatal("an episode was not classified as television")
	}
	if !strings.Contains(l.RelPath, "/Season 02/") {
		t.Errorf("path = %q, want a zero-padded season folder", l.RelPath)
	}
	if !strings.Contains(l.RelPath, "S02E05") {
		t.Errorf("path = %q, want an episode code", l.RelPath)
	}
	if l.Season != 2 || l.Episode != 5 {
		t.Errorf("season/episode = %d/%d", l.Season, l.Episode)
	}
}

// A double-length pilot released as one file holds two episodes. A name
// claiming only the first makes the second look missing forever.
func TestAMultiEpisodeFileNamesItsWholeRange(t *testing.T) {
	l, err := PlanLayout(parse(t, "Show.S01E01E02.1080p.WEB-DL-GRP.mkv"), "WEBDL-1080p")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(l.RelPath, "S01E01-E02") {
		t.Errorf("path = %q, want the full range", l.RelPath)
	}
	if l.Episode != 1 || l.EpisodeLast != 2 {
		t.Errorf("range = %d..%d", l.Episode, l.EpisodeLast)
	}
}

// An import that lands under "Unknown (0)" is worse than one that did not
// happen: the file is in the library, the operator believes it worked, and
// finding it later means remembering which of fifty things it was.
func TestAThinParseIsRefusedRatherThanFiledBlind(t *testing.T) {
	for _, name := range []string{
		"", "   ", "....", "1080p.mkv", "[GRP].mkv",
	} {
		if l, err := PlanLayout(parse(t, name), "Bluray-1080p"); !errors.Is(err, ErrUnnameable) {
			t.Errorf("PlanLayout(%q) = %+v, %v — want a refusal", name, l, err)
		}
	}
}

// An episode number with no season could be absolute-numbered anime or a name
// half-read. Guessing a season files it where the operator will not look.
func TestAnEpisodeWithNoSeasonIsRefused(t *testing.T) {
	p := release.Parsed{Title: "Show", Season: -1, Episodes: []int{5}, Raw: "Show - 05.mkv"}
	if _, err := PlanLayout(p, "WEBDL-1080p"); !errors.Is(err, ErrUnnameable) {
		t.Errorf("err = %v, want a refusal", err)
	}
}

// A season pack reaching here means selection found one video file for a whole
// season, which is not something to file as an episode.
func TestASeasonPackIsRefusedAsASingleImport(t *testing.T) {
	p := release.Parsed{Title: "Show", Year: 2015, Season: 2, FullSeason: true, Raw: "Show.S02.1080p"}
	if _, err := PlanLayout(p, "WEBDL-1080p"); !errors.Is(err, ErrUnnameable) {
		t.Errorf("err = %v, want a refusal", err)
	}
}

// The title comes from a release name a stranger wrote. "../../etc" is a legal
// title, and every component goes through library.SafeComponent.
func TestAHostileTitleCannotEscapeTheLayout(t *testing.T) {
	for _, title := range []string{
		"../../etc/cron.d/x",
		"/etc/passwd",
		"..",
		`..\..\windows`,
		"CON",
		"film\x00name",
		strings.Repeat("a", 500),
	} {
		p := release.Parsed{Title: title, Year: 2019, Season: -1, Container: "mkv"}
		l, err := PlanLayout(p, "Bluray-1080p")
		if err != nil {
			continue // refusing is fine
		}
		if strings.Contains(l.Folder, "/") || strings.Contains(l.Folder, `\`) {
			t.Errorf("title %q produced a multi-component folder %q", title, l.Folder)
		}
		// A literal ".." INSIDE a longer component is harmless — ".._.._etc"
		// is one directory with an odd name, not a traversal. What matters is
		// that no COMPONENT is "." or "..", and that the shape is unchanged.
		for _, comp := range strings.Split(l.RelPath, "/") {
			if comp == "." || comp == ".." || comp == "" {
				t.Errorf("title %q produced component %q in %q", title, comp, l.RelPath)
			}
		}
		// A film's path is exactly folder/file — two components, never more.
		if got := strings.Count(l.RelPath, "/"); got != 1 {
			t.Errorf("title %q produced %d separators in %q", title, got, l.RelPath)
		}
	}
}

// Matching stems are what make a player find a sidecar. "Which of these four is
// English" is not a question to answer by discarding the answer.
func TestSubtitlesAreNamedToMatchTheirVideo(t *testing.T) {
	video := "Blade Runner 2049 (2017)/Blade Runner 2049 (2017) [Bluray-1080p].mkv"

	for original, wantSuffix := range map[string]string{
		"Subs/2_English.srt":      "[Bluray-1080p].en.srt",
		"Subs/3_Spanish.srt":      "[Bluray-1080p].es.srt",
		"movie.en.srt":            "[Bluray-1080p].en.srt",
		"movie.forced.srt":        "[Bluray-1080p].forced.srt",
		"subtitle.ass":            "[Bluray-1080p].ass",
		"Film.2019.1080p.fra.srt": "[Bluray-1080p].fr.srt",
	} {
		got, err := SubtitlePath(video, original)
		if err != nil {
			t.Fatalf("SubtitlePath(%q): %v", original, err)
		}
		if !strings.HasSuffix(got, wantSuffix) {
			t.Errorf("SubtitlePath(%q) = %q, want it to end %q", original, got, wantSuffix)
		}
		// It must sit beside the video, not somewhere else.
		if !strings.HasPrefix(got, "Blade Runner 2049 (2017)/") {
			t.Errorf("SubtitlePath(%q) = %q, not beside the video", original, got)
		}
	}
}

// Taking the first match from the front reads "En" as a language on a film
// called "En Route".
func TestALanguageTagIsReadFromTheEnd(t *testing.T) {
	if got := languageTag("En.Route.2019.1080p.BluRay.srt"); got == "en" {
		t.Errorf("the film title was read as a language tag: %q", got)
	}
	if got := languageTag("En.Route.2019.1080p.BluRay.eng.srt"); got != "en" {
		t.Errorf("a real trailing tag was missed: %q", got)
	}
	// A language and a modifier together: both are kept, and the language is
	// not lost to the modifier that follows it.
	if got := languageTag("movie.en.forced.srt"); got != "en.forced" {
		t.Errorf("languageTag(en.forced) = %q", got)
	}
	if got := languageTag("movie.english.sdh.srt"); got != "en.sdh" {
		t.Errorf("languageTag(english.sdh) = %q", got)
	}
}

// A subtitle whose name says nothing about language still lands beside its
// video rather than being dropped.
func TestAnUntaggedSubtitleStillMatchesItsVideo(t *testing.T) {
	video := "Film (2019)/Film (2019) [Bluray-1080p].mkv"
	got, err := SubtitlePath(video, "whatever.srt")
	if err != nil {
		t.Fatal(err)
	}
	if got != "Film (2019)/Film (2019) [Bluray-1080p].srt" {
		t.Errorf("got %q", got)
	}
}

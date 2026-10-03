package importer

import (
	"strings"
	"testing"
)

const (
	gib = 1 << 30
	mib = 1 << 20
	kib = 1 << 10
)

// A realistic torrent. Getting this wrong does not produce an error — it
// produces a library entry that plays a 48-megabyte sample, or an executable
// sitting in a media folder that gets synced to a phone.
func TestARealisticReleasePicksTheFeature(t *testing.T) {
	sel := Select([]Candidate{
		{"Movie.2019.1080p.BluRay.x264-GRP/Movie.2019.1080p.BluRay.x264-GRP.mkv", 18 * gib},
		{"Movie.2019.1080p.BluRay.x264-GRP/Sample/sample.mkv", 48 * mib},
		{"Movie.2019.1080p.BluRay.x264-GRP/Subs/2_English.srt", 71 * kib},
		{"Movie.2019.1080p.BluRay.x264-GRP/RARBG_DO_NOT_MIRROR.exe", 1 * kib},
		{"Movie.2019.1080p.BluRay.x264-GRP/movie.nfo", 3 * kib},
		{"Movie.2019.1080p.BluRay.x264-GRP/poster.jpg", 412 * kib},
	})

	if !strings.HasSuffix(sel.Video.Path, "GRP.mkv") {
		t.Fatalf("chose %q", sel.Video.Path)
	}
	if len(sel.Subtitles) != 1 {
		t.Errorf("subtitles = %d, want 1", len(sel.Subtitles))
	}
	// Every other file is accounted for with a reason, so "it downloaded and
	// then nothing happened" has an answer.
	if len(sel.Rejected) != 4 {
		t.Errorf("rejected = %d, want 4: %+v", len(sel.Rejected), sel.Rejected)
	}
}

// The rule that matters: an allowlist, not a denylist. Nothing that is not a
// video container can ever be chosen, whatever it is called or how large.
func TestOnlyAllowlistedContainersCanBeChosen(t *testing.T) {
	for _, name := range []string{
		"payload.exe", "setup.scr", "thing.lnk", "release.rar", "disc.iso",
		"archive.zip", "part.001", "notes.nfo", "cover.jpg", "script.sh",
		"movie.mkv.exe", "film.txt", "data.bin", "x.MKV.rar",
	} {
		sel := Select([]Candidate{{name, 20 * gib}})
		if sel.Video.Path != "" {
			t.Errorf("Select(%q) chose it as media", name)
		}
		if len(sel.Rejected) != 1 || sel.Rejected[0].Reason != ReasonNotAContainer {
			t.Errorf("Select(%q) rejected for %q", name, sel.Rejected[0].Reason)
		}
	}
}

// A disc image cannot be direct-played and cannot be probed without mounting
// it. Importing one produces a library entry nothing can play, so it is skipped
// with a reason rather than accepted and left broken.
func TestDiscImagesAreSkippedRatherThanImportedBroken(t *testing.T) {
	sel := Select([]Candidate{{"Film.2019.BluRay/Film.iso", 40 * gib}})
	if sel.Video.Path != "" {
		t.Fatalf("an ISO was imported: %q", sel.Video.Path)
	}
	if sel.Summary() == "" {
		t.Error("no explanation was produced")
	}
}

func TestSamplesAreRecognisedByPathAndBySize(t *testing.T) {
	// By path: under a Sample directory, however large.
	sel := Select([]Candidate{
		{"Film/Sample/film-sample.mkv", 19 * gib},
		{"Film/film.mkv", 18 * gib},
	})
	if !strings.HasSuffix(sel.Video.Path, "Film/film.mkv") {
		t.Errorf("a large sample beat the feature: %q", sel.Video.Path)
	}

	// By size: named innocuously, but a fiftieth of the feature.
	sel = Select([]Candidate{
		{"Film/film.mkv", 18 * gib},
		{"Film/film-preview.mkv", 300 * mib},
	})
	if len(sel.Rejected) != 1 || sel.Rejected[0].Reason != ReasonSampleBySize {
		t.Errorf("rejections = %+v", sel.Rejected)
	}
}

// Substring matching gets this wrong in the direction that silently discards an
// operator's film.
func TestAFilmWhoseTitleContainsASampleWordSurvives(t *testing.T) {
	for _, name := range []string{
		"Free.Samples.2012.1080p.BluRay-GRP/Free.Samples.2012.1080p.BluRay-GRP.mkv",
		"Resampled.2019.1080p/Resampled.2019.1080p.mkv",
		// Trailer Park Boys: The Movie is a real film, and the series is a real
		// series. A rule matching "trailer" anywhere in the name loses both.
		"Trailer.Park.Boys.The.Movie.2014.1080p.BluRay-GRP.mkv",
		"The.Trailer.Park.Boys.S01E01.mkv",
		"Extras.2005.S01E01.1080p.mkv",
		"Extraction.2020.2160p.mkv",
		"Proofread.2021.1080p.mkv",
		"Sample.This.2011.1080p.BluRay-GRP.mkv",
	} {
		sel := Select([]Candidate{{name, 18 * gib}})
		if sel.Video.Path == "" {
			t.Errorf("Select(%q) discarded a legitimate film: %+v", name, sel.Rejected)
		}
	}
}

// And the reverse: the markers that should match, do.
func TestSampleMarkersMatchWholeWords(t *testing.T) {
	for _, name := range []string{
		"Film/Sample/x.mkv",
		"Film/sample.mkv",
		"Film/Film-sample.mkv",
		"Film/Film.SAMPLE.mkv",
		"Film/Extras/deleted.mkv",
		"Film/Featurettes/making-of.mkv",
		"Film/Trailers/teaser.mkv",
		"Film/proof/screen.mkv",
	} {
		sel := Select([]Candidate{{name, 18 * gib}})
		if sel.Video.Path != "" {
			t.Errorf("Select(%q) chose a sample or extra as the feature", name)
		}
	}
}

// A 20-minute animated episode is genuinely small. Refusing an operator's
// legitimate file is worse than importing a large sample, which the sample
// rules catch anyway.
func TestASmallButLegitimateEpisodeIsKept(t *testing.T) {
	sel := Select([]Candidate{{"Show.S01E01.720p.mkv", 120 * mib}})
	if sel.Video.Path == "" {
		t.Fatalf("a 120 MiB episode was refused: %+v", sel.Rejected)
	}
	// But something genuinely tiny is not a feature.
	sel = Select([]Candidate{{"Show.S01E01.mkv", 2 * mib}})
	if sel.Video.Path != "" {
		t.Error("a 2 MiB file was treated as an episode")
	}
}

// A subtitle from an unrelated folder would be renamed to match the film and be
// wrong in a way nobody notices until they turn subtitles on.
func TestOnlyNearbySubtitlesRideAlong(t *testing.T) {
	sel := Select([]Candidate{
		{"Pack/Film.A/Film.A.mkv", 18 * gib},
		{"Pack/Film.A/Film.A.srt", 60 * kib},
		{"Pack/Film.A/Subs/eng.srt", 60 * kib},
		{"Pack/Film.B/Film.B.srt", 60 * kib},
		{"unrelated.srt", 60 * kib},
	})
	if len(sel.Subtitles) != 2 {
		t.Fatalf("subtitles = %d, want the 2 beside the video: %+v",
			len(sel.Subtitles), sel.Subtitles)
	}
	for _, s := range sel.Subtitles {
		if !strings.HasPrefix(s.Path, "Pack/Film.A/") {
			t.Errorf("a distant subtitle rode along: %q", s.Path)
		}
	}
}

// An empty or hopeless download is not an error: it is a selection with an
// explanation, which is what gets recorded for the operator to read.
func TestAnUnusableDownloadExplainsItself(t *testing.T) {
	if s := Select(nil); s.Video.Path != "" || s.Summary() == "" {
		t.Errorf("empty download: %+v %q", s, s.Summary())
	}

	s := Select([]Candidate{
		{"readme.txt", 1 * kib},
		{"keygen.exe", 40 * kib},
	})
	if s.Video.Path != "" {
		t.Fatal("something was chosen from a download with no media")
	}
	sum := s.Summary()
	if !strings.Contains(sum, "no media file was suitable") {
		t.Errorf("summary = %q", sum)
	}
	// It names the largest offender, which is the one an operator will look at.
	if !strings.Contains(sum, "keygen.exe") {
		t.Errorf("the summary does not name a file: %q", sum)
	}
}

// A season pack: many episodes, none dwarfing the others. Choosing the largest
// is right for a single import, and the caller handles packs separately — but
// selection must not mistake the other episodes for samples.
func TestASeasonPackDoesNotLookLikeSamples(t *testing.T) {
	var files []Candidate
	for _, ep := range []string{"E01", "E02", "E03", "E04"} {
		files = append(files, Candidate{"Show.S01/Show.S01" + ep + ".1080p.mkv", 2 * gib})
	}
	sel := Select(files)
	if sel.Video.Path == "" {
		t.Fatal("nothing was chosen from a season pack")
	}
	for _, r := range sel.Rejected {
		if r.Reason == ReasonSampleBySize {
			t.Errorf("an episode in a season pack was called a sample: %+v", r)
		}
	}
}

// Case must not decide anything: .MKV is .mkv, and Sample is sample.
func TestSelectionIsCaseInsensitive(t *testing.T) {
	sel := Select([]Candidate{{"Film/FILM.MKV", 18 * gib}})
	if sel.Video.Path == "" {
		t.Error("an uppercase extension was refused")
	}
	sel = Select([]Candidate{{"Film/SAMPLE/x.mkv", 18 * gib}})
	if sel.Video.Path != "" {
		t.Error("an uppercase Sample directory was not recognised")
	}
}

// The invariant that makes selection safe: whatever comes back, if anything
// does, is an allowlisted video container. The corpus above is what I thought
// to try; this is for what I did not.
func FuzzSelectNeverChoosesANonContainer(f *testing.F) {
	f.Add("Movie.2019.1080p.mkv", int64(18*gib), "Sample/sample.mkv", int64(48*mib))
	f.Add("payload.exe", int64(20*gib), "film.mkv", int64(1*gib))
	f.Add("a", int64(0), "b", int64(0))
	f.Add("../../etc/passwd", int64(1*gib), "x.mkv", int64(2*gib))
	f.Add("Free.Samples.2012.mkv", int64(9*gib), "trailer.mkv", int64(1*gib))

	f.Fuzz(func(t *testing.T, p1 string, b1 int64, p2 string, b2 int64) {
		sel := Select([]Candidate{{p1, b1}, {p2, b2}})
		if sel.Video.Path == "" {
			return
		}
		// 1. Always an allowlisted container. This is the one that keeps a
		//    .exe out of a media folder.
		if !sel.Video.IsVideo() {
			t.Fatalf("chose %q, which is not an allowlisted container", sel.Video.Path)
		}
		// 2. Always one of the files it was given, never something synthesised.
		if sel.Video.Path != p1 && sel.Video.Path != p2 {
			t.Fatalf("chose %q, which was not an input", sel.Video.Path)
		}
		// 3. Never below the floor.
		if sel.Video.Bytes < MinVideoBytes {
			t.Fatalf("chose %q at %d bytes, below the floor", sel.Video.Path, sel.Video.Bytes)
		}
		// 4. Never something the name marks as a sample.
		if hasSampleMarker(sel.Video.Path) {
			t.Fatalf("chose %q, which is marked as a sample", sel.Video.Path)
		}
		// 5. A summary is always produced, so an operator is never left with
		//    nothing to read.
		if sel.Summary() == "" {
			t.Fatal("no summary")
		}
	})
}

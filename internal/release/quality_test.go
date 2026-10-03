package release

import (
	"strings"
	"testing"
	"time"
)

func mustCompile(t *testing.T, p Profile) *Profile {
	t.Helper()
	if err := p.Compile(); err != nil {
		t.Fatalf("compiling %q: %v", p.Name, err)
	}
	return &p
}

func TestQualityOfRealReleases(t *testing.T) {
	for name, wantQ := range map[string]string{
		"The.Matrix.1999.1080p.BluRay.x264-G":          "Bluray-1080p",
		"Film.2020.2160p.UHD.BluRay.REMUX.HEVC-G":      "Remux-2160p",
		"Film.2020.1080p.WEB-DL.DDP5.1-G":              "WEBDL-1080p",
		"Film.2020.1080p.WEBRip.x264-G":                "WEBRip-1080p",
		"Show.S01E01.720p.HDTV.x264-G":                 "HDTV-720p",
		"Film.2001.DVDRip.XviD-G":                      "DVD",
		"Film.2020.CAM.x264-G":                         "CAM",
		"Film.2020.1080p.BluRay.REMUX.AVC.DTS-HD.MA-G": "Remux-1080p",
		"just some words with no quality at all":       "Unknown",
	} {
		if got := QualityOf(Parse(name)); got.Name != wantQ {
			t.Errorf("%s\n  got %q, want %q", name, got.Name, wantQ)
		}
	}
}

// A release naming only half of the pair must classify DOWN, never up. Guessing
// up lets a vague name clear a cutoff it may not actually meet, and the operator
// never finds out because nothing looks wrong.
func TestHalfKnownQualityRoundsDown(t *testing.T) {
	blurayNoRes := QualityOf(Parse("Film.2020.BluRay.x264-GROUP"))
	if blurayNoRes.Resolution == Resolution2160p {
		t.Errorf("an unlabelled Blu-ray was classified as %s", blurayNoRes.Name)
	}
	if blurayNoRes.Name != "Bluray-480p" {
		t.Errorf("got %q, want the lowest Blu-ray tier", blurayNoRes.Name)
	}

	resNoSource := QualityOf(Parse("Film.2020.1080p-GROUP"))
	if !strings.Contains(resNoSource.Name, "1080p") {
		t.Errorf("got %q, want a 1080p tier", resNoSource.Name)
	}
	if resNoSource.Name == "Remux-1080p" || resNoSource.Name == "Bluray-1080p" {
		t.Errorf("an unlabelled 1080p release was promoted to %q", resNoSource.Name)
	}
}

// ---------------------------------------------------------------------------
// Profile validation
// ---------------------------------------------------------------------------

// A bad profile must fail when it is saved, naming the problem — not silently
// reject every release at 3am.
func TestBadProfilesAreRejectedAtCompileTime(t *testing.T) {
	cases := map[string]Profile{
		"no qualities":      {Name: "empty"},
		"unknown quality":   {Name: "typo", Allowed: []string{"Bluray-1081p"}},
		"duplicate quality": {Name: "dup", Allowed: []string{"WEBDL-1080p", "WEBDL-1080p"}},
		"cutoff not allowed": {
			Name: "bad-cutoff", Allowed: []string{"WEBDL-1080p"}, Cutoff: "Remux-2160p",
		},
		"bad preferred regex": {
			Name: "bad-re", Allowed: []string{"WEBDL-1080p"},
			Preferred: []ScoredTerm{{Term: "/[unclosed/", Score: 1}},
		},
		"bad forbidden regex": {
			Name: "bad-forbid", Allowed: []string{"WEBDL-1080p"},
			Forbidden: []string{"/(/"},
		},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			if err := p.Compile(); err == nil {
				t.Error("compiled without complaint")
			} else if !strings.Contains(err.Error(), p.Name) {
				t.Errorf("the error does not name the profile: %v", err)
			}
		})
	}
}

func TestDefaultProfilesAllCompile(t *testing.T) {
	for _, p := range DefaultProfiles() {
		if err := p.Compile(); err != nil {
			t.Errorf("the shipped profile %q does not compile: %v", p.Name, err)
		}
	}
}

// An uncompiled profile must refuse everything rather than accept everything.
// This is the fail-closed direction: a wiring mistake costs an empty queue, not
// a library of cam rips.
func TestUncompiledProfileAcceptsNothing(t *testing.T) {
	var p Profile
	if ok, _ := p.Accepts(Parse("Film.2020.1080p.BluRay-G")); ok {
		t.Error("an uncompiled profile accepted a release")
	}
}

// ---------------------------------------------------------------------------
// Acceptance
// ---------------------------------------------------------------------------

func TestForbiddenTermsRejectOutright(t *testing.T) {
	p := mustCompile(t, Profile{
		Name:      "no-cams",
		Allowed:   qualityNames(),
		Cutoff:    "Bluray-1080p",
		Forbidden: []string{"/\\b(cam|hdcam|telesync)\\b/"},
	})

	ok, rej := p.Accepts(Parse("Film.2020.CAM.x264-GROUP"))
	if ok {
		t.Fatal("a cam rip was accepted by a profile that forbids cams")
	}
	if rej.Reason != ReasonForbiddenTerm {
		t.Errorf("reason = %q, want %q", rej.Reason, ReasonForbiddenTerm)
	}
	if !strings.Contains(rej.Detail, "cam") {
		t.Errorf("the rejection does not say what matched: %q", rej.Detail)
	}
}

func TestRequiredTermsMustAllAppear(t *testing.T) {
	p := mustCompile(t, Profile{
		Name: "must-be-multi", Allowed: qualityNames(), Cutoff: "Bluray-1080p",
		Required: []string{"MULTi"},
	})

	if ok, _ := p.Accepts(Parse("Film.2020.1080p.BluRay.MULTi.x264-G")); !ok {
		t.Error("a release carrying the required term was rejected")
	}
	ok, rej := p.Accepts(Parse("Film.2020.1080p.BluRay.x264-G"))
	if ok {
		t.Fatal("a release missing the required term was accepted")
	}
	if rej.Reason != ReasonMissingTerm {
		t.Errorf("reason = %q", rej.Reason)
	}
}

// A literal term must be matched literally. "5.1" contains a dot the operator
// did not mean as a wildcard.
func TestLiteralTermsAreNotTreatedAsPatterns(t *testing.T) {
	p := mustCompile(t, Profile{
		Name: "literal", Allowed: qualityNames(), Cutoff: "Bluray-1080p",
		Forbidden: []string{"5.1"},
	})

	if ok, _ := p.Accepts(Parse("Film.2020.1080p.BluRay.DTS.5.1-G")); ok {
		t.Error("the literal term did not match where it should")
	}
	// "5x1" would match "5.1" if the dot were a wildcard.
	if ok, _ := p.Accepts(Parse("Film.2020.1080p.BluRay.5x1.Whatever-G")); !ok {
		t.Error("the dot in a literal term behaved as a wildcard")
	}
}

func TestQualityOutsideTheProfileIsRejected(t *testing.T) {
	p := mustCompile(t, Profile{
		Name: "1080-only", Allowed: []string{"WEBDL-1080p", "Bluray-1080p"},
		Cutoff: "Bluray-1080p",
	})

	if ok, _ := p.Accepts(Parse("Film.2020.720p.BluRay.x264-G")); ok {
		t.Error("720p was accepted by a 1080p-only profile")
	}
	ok, rej := p.Accepts(Parse("Film.2020.2160p.BluRay.x264-G"))
	if ok {
		t.Error("2160p was accepted by a 1080p-only profile")
	}
	if rej.Reason != ReasonQualityNotAllowed {
		t.Errorf("reason = %q", rej.Reason)
	}
}

// ---------------------------------------------------------------------------
// The upgrade decision
// ---------------------------------------------------------------------------

func TestUpgradeRules(t *testing.T) {
	p := mustCompile(t, Profile{
		Name: "hd", Allowed: []string{
			"HDTV-720p", "WEBDL-720p", "Bluray-720p",
			"HDTV-1080p", "WEBDL-1080p", "Bluray-1080p",
		},
		Cutoff: "Bluray-1080p",
	})

	tests := []struct {
		name      string
		candidate string
		current   string
		have      bool
		want      bool
	}{
		{"nothing held", "Film.2020.720p.HDTV.x264-G", "", false, true},
		{"better quality", "Film.2020.1080p.BluRay.x264-G", "Film.2020.720p.HDTV.x264-G", true, true},
		{"worse quality", "Film.2020.720p.HDTV.x264-G", "Film.2020.1080p.WEBDL.x264-G", true, false},
		{"same quality", "Film.2020.1080p.WEB-DL.x264-A", "Film.2020.1080p.WEB-DL.x264-B", true, false},
		{"proper of the same quality", "Film.2020.PROPER.1080p.WEB-DL.x264-A", "Film.2020.1080p.WEB-DL.x264-B", true, true},
		{"earlier revision", "Film.2020.1080p.WEB-DL.x264-A", "Film.2020.REPACK.1080p.WEB-DL.x264-B", true, false},
		{"quality outside the profile", "Film.2020.2160p.BluRay.x264-G", "Film.2020.720p.HDTV.x264-G", true, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var current Parsed
			if tc.have {
				current = Parse(tc.current)
			}
			got := p.ShouldUpgrade(Parse(tc.candidate), current, tc.have)
			if got.Should != tc.want {
				t.Errorf("should = %v, want %v (reason: %s)", got.Should, tc.want, got.Reason)
			}
			if got.Reason == "" {
				t.Error("no reason was given, so an operator cannot tell why")
			}
		})
	}
}

// Once what is held meets the cutoff, upgrading stops. Without this an operator
// who asked for 1080p gets their library rewritten every time a marginally
// different 1080p release appears.
func TestCutoffStopsUpgrading(t *testing.T) {
	p := mustCompile(t, Profile{
		Name: "stops-at-webdl",
		Allowed: []string{
			"HDTV-1080p", "WEBRip-1080p", "WEBDL-1080p", "Bluray-1080p", "Remux-1080p",
		},
		Cutoff: "WEBDL-1080p",
	})

	held := Parse("Film.2020.1080p.WEB-DL.x264-G")
	if !p.MeetsCutoff(held) {
		t.Fatal("the held release does not meet the cutoff it was chosen to meet")
	}

	// A genuinely better release must still be refused: the operator said they
	// were satisfied at WEB-DL.
	better := Parse("Film.2020.1080p.BluRay.REMUX.AVC-G")
	got := p.ShouldUpgrade(better, held, true)
	if got.Should {
		t.Errorf("upgraded past the cutoff: %s", got.Reason)
	}
	if !strings.Contains(got.Reason, "cutoff") {
		t.Errorf("the reason does not mention the cutoff: %q", got.Reason)
	}

	// But below the cutoff, upgrading works.
	poor := Parse("Film.2020.1080p.HDTV.x264-G")
	if got := p.ShouldUpgrade(better, poor, true); !got.Should {
		t.Errorf("refused a real upgrade below the cutoff: %s", got.Reason)
	}
}

// Two releases the profile cannot distinguish must not each look better than
// the other, or the queue flaps between them forever.
func TestEqualReleasesDoNotUpgradeEitherWay(t *testing.T) {
	p := mustCompile(t, Profile{
		Name: "hd", Allowed: []string{"WEBDL-1080p", "Bluray-1080p"}, Cutoff: "Bluray-1080p",
	})

	a := Parse("Film.2020.1080p.WEB-DL.x264-AAA")
	b := Parse("Film.2020.1080p.WEB-DL.x264-BBB")

	if p.ShouldUpgrade(a, b, true).Should {
		t.Error("a replaced b")
	}
	if p.ShouldUpgrade(b, a, true).Should {
		t.Error("b replaced a")
	}
}

func TestPreferredTermsBreakTiesWithinAQuality(t *testing.T) {
	p := mustCompile(t, Profile{
		Name: "prefers-atmos", Allowed: []string{"WEBDL-1080p", "Bluray-1080p"},
		Cutoff:    "Bluray-1080p",
		Preferred: []ScoredTerm{{Term: "Atmos", Score: 10}},
	})

	plain := Parse("Film.2020.1080p.WEB-DL.DDP5.1-G")
	atmos := Parse("Film.2020.1080p.WEB-DL.DDP5.1.Atmos-G")

	if p.Score(atmos) <= p.Score(plain) {
		t.Fatalf("scores: atmos %d, plain %d", p.Score(atmos), p.Score(plain))
	}
	if got := p.ShouldUpgrade(atmos, plain, true); !got.Should {
		t.Errorf("the preferred release did not win: %s", got.Reason)
	}
	if got := p.ShouldUpgrade(plain, atmos, true); got.Should {
		t.Error("the less preferred release won")
	}
}

// A negative score avoids without rejecting. "I would rather not" and "never"
// are different instructions and must stay different.
func TestNegativeScoresAvoidWithoutRejecting(t *testing.T) {
	p := mustCompile(t, Profile{
		Name: "dislikes-x265", Allowed: []string{"WEBDL-1080p", "Bluray-1080p"},
		Cutoff:    "Bluray-1080p",
		Preferred: []ScoredTerm{{Term: "x265", Score: -10}},
	})

	x265 := Parse("Film.2020.1080p.WEB-DL.x265-G")
	if ok, _ := p.Accepts(x265); !ok {
		t.Error("a negatively scored release was rejected outright")
	}
	if p.Score(x265) >= 0 {
		t.Errorf("score = %d, want negative", p.Score(x265))
	}
}

func TestSortIsBestFirstAndStable(t *testing.T) {
	p := mustCompile(t, Profile{
		Name: "hd", Allowed: []string{
			"HDTV-720p", "WEBDL-720p", "HDTV-1080p", "WEBDL-1080p", "Bluray-1080p",
		},
		Cutoff: "Bluray-1080p",
	})

	candidates := []Parsed{
		Parse("Film.2020.720p.HDTV.x264-C"),
		Parse("Film.2020.1080p.BluRay.x264-A"),
		Parse("Film.2020.1080p.WEB-DL.x264-B"),
		Parse("Film.2020.PROPER.1080p.WEB-DL.x264-D"),
	}
	p.Sort(candidates)

	if QualityOf(candidates[0]).Name != "Bluray-1080p" {
		t.Errorf("first = %s, want Bluray-1080p", QualityOf(candidates[0]).Name)
	}
	if candidates[1].Revision != 1 {
		t.Error("the PROPER did not outrank the plain release of the same quality")
	}
	if QualityOf(candidates[3]).Name != "HDTV-720p" {
		t.Errorf("last = %s, want HDTV-720p", QualityOf(candidates[3]).Name)
	}

	// Stability: re-sorting an already sorted slice changes nothing.
	before := make([]string, len(candidates))
	for i, c := range candidates {
		before[i] = c.Raw
	}
	p.Sort(candidates)
	for i, c := range candidates {
		if c.Raw != before[i] {
			t.Errorf("sort is not stable: position %d changed", i)
		}
	}
}

// The shipped default must not fill a library the test hardware cannot play.
// The i5-6500T cannot tone-map HDR at all (ADR-0005).
func TestTheDefaultProfileDoesNotDefaultTo4K(t *testing.T) {
	profiles := DefaultProfiles()
	first := profiles[0]
	if err := first.Compile(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(first.Cutoff, "2160") {
		t.Errorf("the first default profile cuts off at %q", first.Cutoff)
	}
	if ok, _ := first.Accepts(Parse("Film.2020.2160p.BluRay.REMUX.HDR.HEVC-G")); ok {
		t.Error("the default profile accepts 2160p HDR, which the target hardware cannot tone-map")
	}
}

// Every shipped profile must refuse a cam rip. It is the one thing nobody wants
// and the easiest to grab by accident, because cams appear first.
func TestEveryDefaultProfileExceptAnyRefusesCams(t *testing.T) {
	for _, p := range DefaultProfiles() {
		if p.Name == "Any" {
			continue // "Any" means any, and says so in its name
		}
		if err := p.Compile(); err != nil {
			t.Fatal(err)
		}
		if ok, _ := p.Accepts(Parse("Film.2020.CAM.x264-GROUP")); ok {
			t.Errorf("profile %q accepts cam rips", p.Name)
		}
		if ok, _ := p.Accepts(Parse("Film.2020.HDTS.x264-GROUP")); ok {
			t.Errorf("profile %q accepts telesyncs", p.Name)
		}
	}
}

// An operator's profile term is compiled with the standard library, so it is
// RE2 and cannot hang the pipeline however it is written.
func TestOperatorPatternsCannotHangThePipeline(t *testing.T) {
	p := mustCompile(t, Profile{
		Name: "pathological", Allowed: qualityNames(), Cutoff: "Bluray-1080p",
		Preferred: []ScoredTerm{{Term: `/(a+)+b/`, Score: 1}},
	})

	done := make(chan struct{})
	go func() {
		p.Score(Parse(strings.Repeat("a", 5000) + ".2020.1080p.BluRay-G"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a catastrophic-backtracking pattern hung the scorer")
	}
}

package release

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// A Quality is a source-and-resolution pair, which together are what actually
// predicts how a file looks.
//
// Neither half is sufficient alone, and this is the mistake worth avoiding: a
// 1080p WEBRip and a 1080p Blu-ray remux share a resolution and are not close
// to the same file. Ranking on resolution gives an operator a library of
// upscales; ranking on source alone gives them a DVD over a 4K stream.
type Quality struct {
	Name       string
	Source     Source
	Resolution Resolution
}

// String is the canonical name, e.g. "Bluray-1080p".
func (q Quality) String() string { return q.Name }

// Known is false for a release whose source or resolution could not be read.
func (q Quality) Known() bool { return q.Name != "" && q.Name != "Unknown" }

// QualityUnknown is what a release that could not be classified gets. It is
// never implicitly acceptable: a profile has to list it.
var QualityUnknown = Quality{Name: "Unknown"}

// DefaultLadder is the out-of-the-box ordering, worst first.
//
// It is a DEFAULT, not a law. The order lives in the profile, and a profile
// that prefers a 1080p WEB-DL to a 2160p remux — because of storage, or because
// the television cannot tone-map HDR, which on the i5-6500T it cannot
// (ADR-0005) — is expressing a legitimate preference, not a misconfiguration.
// Nothing in this package assumes a universal "better".
var DefaultLadder = []Quality{
	{Name: "Unknown", Source: SourceUnknown, Resolution: ResolutionUnknown},
	{Name: "CAM", Source: SourceCAM},
	{Name: "Telesync", Source: SourceTelesync},
	{Name: "Screener", Source: SourceScreener},
	{Name: "SDTV", Source: SourceSDTV},
	{Name: "DVD", Source: SourceDVD},
	{Name: "WEBRip-480p", Source: SourceWEBRip, Resolution: Resolution480p},
	{Name: "WEBDL-480p", Source: SourceWEBDL, Resolution: Resolution480p},
	{Name: "Bluray-480p", Source: SourceBluRay, Resolution: Resolution480p},
	{Name: "HDTV-720p", Source: SourceHDTV, Resolution: Resolution720p},
	{Name: "WEBRip-720p", Source: SourceWEBRip, Resolution: Resolution720p},
	{Name: "WEBDL-720p", Source: SourceWEBDL, Resolution: Resolution720p},
	{Name: "Bluray-720p", Source: SourceBluRay, Resolution: Resolution720p},
	{Name: "HDTV-1080p", Source: SourceHDTV, Resolution: Resolution1080p},
	{Name: "WEBRip-1080p", Source: SourceWEBRip, Resolution: Resolution1080p},
	{Name: "WEBDL-1080p", Source: SourceWEBDL, Resolution: Resolution1080p},
	{Name: "Bluray-1080p", Source: SourceBluRay, Resolution: Resolution1080p},
	{Name: "Remux-1080p", Source: SourceRemux, Resolution: Resolution1080p},
	{Name: "HDTV-2160p", Source: SourceHDTV, Resolution: Resolution2160p},
	{Name: "WEBRip-2160p", Source: SourceWEBRip, Resolution: Resolution2160p},
	{Name: "WEBDL-2160p", Source: SourceWEBDL, Resolution: Resolution2160p},
	{Name: "Bluray-2160p", Source: SourceBluRay, Resolution: Resolution2160p},
	{Name: "Remux-2160p", Source: SourceRemux, Resolution: Resolution2160p},
}

// QualityOf classifies a parsed release.
//
// A release missing one half is classified on the other rather than discarded:
// "Film.2020.BluRay-GROUP" with no resolution is still a Blu-ray, and the most
// useful answer is the lowest Blu-ray tier rather than Unknown. Guessing UP
// would be the dangerous direction, because it would let an unlabelled release
// satisfy a cutoff it may not meet.
func QualityOf(p Parsed) Quality {
	if p.Source == SourceUnknown && p.Resolution == ResolutionUnknown {
		return QualityUnknown
	}

	// Sources that carry no meaningful resolution are matched on source alone.
	switch p.Source {
	case SourceCAM, SourceTelesync, SourceScreener, SourceSDTV, SourceDVD:
		for _, q := range DefaultLadder {
			if q.Source == p.Source {
				return q
			}
		}
	}

	if p.Source != SourceUnknown && p.Resolution != ResolutionUnknown {
		for _, q := range DefaultLadder {
			if q.Source == p.Source && q.Resolution == p.Resolution {
				return q
			}
		}
	}

	// Half-known: take the lowest tier consistent with what IS known, never the
	// highest. An unlabelled release must not be able to clear a cutoff by
	// being vague.
	if p.Source != SourceUnknown {
		for _, q := range DefaultLadder {
			if q.Source == p.Source {
				return q
			}
		}
	}
	for _, q := range DefaultLadder {
		if q.Resolution == p.Resolution && q.Resolution != ResolutionUnknown {
			return q
		}
	}
	return QualityUnknown
}

// ---------------------------------------------------------------------------
// Profiles
// ---------------------------------------------------------------------------

// ScoredTerm adjusts a release's score when its name matches.
//
// Positive scores prefer; negative scores avoid without rejecting. That
// distinction matters: "I would rather not have an x265 encode" and "never give
// me an x265 encode" are different instructions, and collapsing them into one
// leaves an operator with an empty queue and no idea why.
type ScoredTerm struct {
	Term  string
	Score int
	// re is compiled once at profile build time.
	re *regexp.Regexp
}

// Profile is one user's definition of what is acceptable and what is better.
type Profile struct {
	Name string
	// Allowed lists acceptable quality names, WORST FIRST. Position in this
	// slice is the ranking; a quality absent from it is rejected outright.
	Allowed []string
	// Cutoff names the quality at which upgrading stops. Reaching it means the
	// operator is satisfied, so continuing to replace files would be churn:
	// bandwidth, disk writes and a re-import for no benefit they asked for.
	Cutoff string
	// Preferred adjusts ordering within the same quality.
	Preferred []ScoredTerm
	// Required terms must ALL appear. Forbidden terms reject outright.
	Required  []string
	Forbidden []string

	rank      map[string]int
	cutoffIdx int
	required  []*regexp.Regexp
	forbidden []*regexp.Regexp
}

// Compile prepares a profile for use and reports what is wrong with it.
//
// It is a separate step so that a bad profile fails when an operator saves it,
// with a message naming the problem, rather than silently rejecting every
// release at 3am.
func (p *Profile) Compile() error {
	if len(p.Allowed) == 0 {
		return fmt.Errorf("profile %q allows no qualities, so it would reject everything", p.Name)
	}

	known := map[string]bool{}
	for _, q := range DefaultLadder {
		known[q.Name] = true
	}

	p.rank = make(map[string]int, len(p.Allowed))
	for i, name := range p.Allowed {
		if !known[name] {
			return fmt.Errorf("profile %q allows unknown quality %q", p.Name, name)
		}
		if _, dup := p.rank[name]; dup {
			return fmt.Errorf("profile %q lists quality %q twice", p.Name, name)
		}
		p.rank[name] = i
	}

	if p.Cutoff == "" {
		// No cutoff means "upgrade forever", which is a real choice but a
		// surprising default. The top of the ladder is the sane reading.
		p.Cutoff = p.Allowed[len(p.Allowed)-1]
	}
	idx, ok := p.rank[p.Cutoff]
	if !ok {
		return fmt.Errorf("profile %q has cutoff %q, which it does not allow", p.Name, p.Cutoff)
	}
	p.cutoffIdx = idx

	for i := range p.Preferred {
		re, err := compileTerm(p.Preferred[i].Term)
		if err != nil {
			return fmt.Errorf("profile %q: preferred term %q: %w", p.Name, p.Preferred[i].Term, err)
		}
		p.Preferred[i].re = re
	}
	p.required = nil
	for _, t := range p.Required {
		re, err := compileTerm(t)
		if err != nil {
			return fmt.Errorf("profile %q: required term %q: %w", p.Name, t, err)
		}
		p.required = append(p.required, re)
	}
	p.forbidden = nil
	for _, t := range p.Forbidden {
		re, err := compileTerm(t)
		if err != nil {
			return fmt.Errorf("profile %q: forbidden term %q: %w", p.Name, t, err)
		}
		p.forbidden = append(p.forbidden, re)
	}
	return nil
}

// compileTerm turns an operator's term into a pattern.
//
// A term wrapped in slashes is a regular expression; anything else is a literal
// matched case-insensitively. The two forms are distinguished explicitly rather
// than by guessing, because "Director's Cut" contains no regex metacharacters
// an operator meant, but "5.1" contains a dot that they did not.
//
// The pattern is compiled with the standard library, so it is RE2: an operator
// cannot write a profile term that hangs the release pipeline.
func compileTerm(term string) (*regexp.Regexp, error) {
	term = strings.TrimSpace(term)
	if term == "" {
		return nil, fmt.Errorf("empty term")
	}
	if len(term) > 2 && strings.HasPrefix(term, "/") && strings.HasSuffix(term, "/") {
		return regexp.Compile("(?i)" + term[1:len(term)-1])
	}
	return regexp.Compile(`(?i)` + regexp.QuoteMeta(term))
}

// Rejection explains why a release was not acceptable. It is a value rather
// than an error because a rejection is an ordinary outcome that the operator
// needs to see in a list, not an exceptional one.
type Rejection struct {
	Reason string
	Detail string
}

const (
	ReasonQualityNotAllowed = "quality_not_allowed"
	ReasonForbiddenTerm     = "forbidden_term"
	ReasonMissingTerm       = "missing_required_term"
	ReasonUnparsed          = "unparsed"
)

// Accepts reports whether a release is acceptable at all, and why not when it
// is not.
func (p *Profile) Accepts(r Parsed) (bool, *Rejection) {
	if p.rank == nil {
		return false, &Rejection{Reason: ReasonQualityNotAllowed, Detail: "profile was not compiled"}
	}

	for i, re := range p.forbidden {
		if re.MatchString(r.Raw) {
			return false, &Rejection{
				Reason: ReasonForbiddenTerm,
				Detail: fmt.Sprintf("matches forbidden term %q", p.Forbidden[i]),
			}
		}
	}
	for i, re := range p.required {
		if !re.MatchString(r.Raw) {
			return false, &Rejection{
				Reason: ReasonMissingTerm,
				Detail: fmt.Sprintf("does not contain required term %q", p.Required[i]),
			}
		}
	}

	q := QualityOf(r)
	if _, ok := p.rank[q.Name]; !ok {
		return false, &Rejection{
			Reason: ReasonQualityNotAllowed,
			Detail: fmt.Sprintf("quality %s is not in profile %q", q.Name, p.Name),
		}
	}
	return true, nil
}

// Score orders releases of the same quality. Higher is preferred.
func (p *Profile) Score(r Parsed) int {
	var total int
	for _, t := range p.Preferred {
		if t.re != nil && t.re.MatchString(r.Raw) {
			total += t.Score
		}
	}
	return total
}

// Rank is the position of a release's quality in this profile, or -1.
func (p *Profile) Rank(r Parsed) int {
	if p.rank == nil {
		return -1
	}
	if i, ok := p.rank[QualityOf(r).Name]; ok {
		return i
	}
	return -1
}

// MeetsCutoff reports whether a release is good enough that no upgrade should
// be sought.
func (p *Profile) MeetsCutoff(r Parsed) bool {
	rank := p.Rank(r)
	return rank >= 0 && rank >= p.cutoffIdx
}

// Upgrade is the decision: should `candidate` replace `current`?
//
// current may be a zero Parsed, meaning nothing is held yet.
type Upgrade struct {
	Should bool
	Reason string
}

// ShouldUpgrade compares a candidate against what is already held.
//
// The rules, in order, and each exists because of a specific way this goes
// wrong when it is left out:
//
//  1. An unacceptable candidate never wins. Otherwise a forbidden term is only
//     a preference.
//  2. Nothing held means grab it.
//  3. If what is held already meets the cutoff, stop. Without this an operator
//     who asked for 1080p gets their library rewritten every time a marginally
//     better 1080p release appears — bandwidth, disk writes and a re-import for
//     a benefit nobody asked for.
//  4. Higher quality rank wins.
//  5. Same quality, higher revision wins — that is what PROPER and REPACK are
//     for, and it is the one case where replacing a working file is right.
//  6. Same quality and revision, higher preferred score wins.
//  7. Otherwise, no. Equal is not better, and treating it as better is how a
//     queue starts flapping between two releases forever.
func (p *Profile) ShouldUpgrade(candidate, current Parsed, haveCurrent bool) Upgrade {
	if ok, rej := p.Accepts(candidate); !ok {
		return Upgrade{false, "rejected: " + rej.Detail}
	}
	if !haveCurrent {
		return Upgrade{true, "nothing held yet"}
	}
	if p.MeetsCutoff(current) {
		return Upgrade{false, fmt.Sprintf(
			"what is held (%s) already meets the cutoff (%s)",
			QualityOf(current).Name, p.Cutoff)}
	}

	cRank, hRank := p.Rank(candidate), p.Rank(current)
	switch {
	case cRank > hRank:
		return Upgrade{true, fmt.Sprintf("%s beats %s",
			QualityOf(candidate).Name, QualityOf(current).Name)}
	case cRank < hRank:
		return Upgrade{false, fmt.Sprintf("%s is worse than %s",
			QualityOf(candidate).Name, QualityOf(current).Name)}
	}

	if candidate.Revision > current.Revision {
		kind := "REPACK"
		if candidate.Proper {
			kind = "PROPER"
		}
		return Upgrade{true, fmt.Sprintf("%s of the same quality (revision %d beats %d)",
			kind, candidate.Revision, current.Revision)}
	}
	if candidate.Revision < current.Revision {
		return Upgrade{false, "an earlier revision of the same quality"}
	}

	cScore, hScore := p.Score(candidate), p.Score(current)
	if cScore > hScore {
		return Upgrade{true, fmt.Sprintf("same quality, preferred score %d beats %d", cScore, hScore)}
	}
	return Upgrade{false, "not better than what is held"}
}

// Sort orders candidates best-first for this profile. It is stable, so two
// releases the profile cannot distinguish keep the order the indexer gave them
// rather than shuffling between runs.
func (p *Profile) Sort(candidates []Parsed) {
	sort.SliceStable(candidates, func(i, j int) bool {
		ri, rj := p.Rank(candidates[i]), p.Rank(candidates[j])
		if ri != rj {
			return ri > rj
		}
		if candidates[i].Revision != candidates[j].Revision {
			return candidates[i].Revision > candidates[j].Revision
		}
		return p.Score(candidates[i]) > p.Score(candidates[j])
	})
}

// ---------------------------------------------------------------------------
// Defaults
// ---------------------------------------------------------------------------

// DefaultProfiles are the profiles a new instance starts with.
//
// "HD-1080p" is the default default. On the hardware this is being built for —
// an i5-6500T with Skylake graphics, which cannot hardware-decode HEVC 10-bit
// and therefore cannot tone-map HDR at all (ADR-0005) — a 2160p HDR library is
// a library that will not play. A profile can be changed in a minute; a library
// downloaded at the wrong quality takes a week.
func DefaultProfiles() []Profile {
	return []Profile{
		{
			Name: "HD-1080p",
			Allowed: []string{
				"HDTV-720p", "WEBRip-720p", "WEBDL-720p", "Bluray-720p",
				"HDTV-1080p", "WEBRip-1080p", "WEBDL-1080p", "Bluray-1080p",
			},
			Cutoff: "Bluray-1080p",
			Preferred: []ScoredTerm{
				{Term: "/\\b(atmos|dts-?hd|truehd)\\b/", Score: 5},
				{Term: "/\\bx-?265|hevc\\b/", Score: -3},
			},
			Forbidden: []string{"/\\b(cam|hdcam|ts|telesync|hdts)\\b/"},
		},
		{
			Name: "HD-720p",
			Allowed: []string{
				"HDTV-720p", "WEBRip-720p", "WEBDL-720p", "Bluray-720p",
			},
			Cutoff:    "Bluray-720p",
			Forbidden: []string{"/\\b(cam|hdcam|ts|telesync|hdts)\\b/"},
		},
		{
			Name: "Ultra-HD",
			Allowed: []string{
				"WEBDL-1080p", "Bluray-1080p", "Remux-1080p",
				"WEBRip-2160p", "WEBDL-2160p", "Bluray-2160p", "Remux-2160p",
			},
			Cutoff: "Bluray-2160p",
			Preferred: []ScoredTerm{
				{Term: "/\\b(dv|dolby.?vision)\\b/", Score: 8},
				{Term: "HDR10+", Score: 6},
			},
			Forbidden: []string{"/\\b(cam|hdcam|ts|telesync|hdts)\\b/"},
		},
		{
			Name:      "Any",
			Allowed:   qualityNames(),
			Cutoff:    "Bluray-1080p",
			Forbidden: nil,
		},
	}
}

func qualityNames() []string {
	out := make([]string, 0, len(DefaultLadder))
	for _, q := range DefaultLadder {
		out = append(out, q.Name)
	}
	return out
}

// DefaultRank is a quality's position in DefaultLadder, or -1 if it is not on
// it. Higher is better.
//
// # Read the caveat before using this
//
// DefaultLadder is a DEFAULT, not a law — the order that matters lives in a
// profile, and a profile preferring a 1080p WEB-DL to a 2160p remux (for
// storage, or because the television cannot tone-map HDR, which on the i5-6500T
// it cannot — ADR-0005) is expressing a legitimate preference. Ranking by this
// function asserts a universal "better" that this package otherwise refuses to
// assert.
//
// It exists because the IMPORT path needs an ordering and does not yet know
// which profile grabbed a release. That is a real limitation, recorded here
// rather than hidden: when the grab records its profile on the queue row, the
// importer should rank with that profile's ladder and this becomes the fallback
// for files that arrived by some other route.
func DefaultRank(name string) int {
	for i, q := range DefaultLadder {
		if strings.EqualFold(q.Name, name) {
			return i
		}
	}
	return -1
}

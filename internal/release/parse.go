// Package release parses scene and P2P release names.
//
// # Why this is the risky part
//
// Everything downstream depends on getting this right. A release name is the
// only description of a file that exists before it is downloaded, so the
// decision to grab, the decision to upgrade, and the decision of where the file
// belongs are all made from a string somebody typed at 3am in 2009. There is no
// specification. "Scene rules" cover a fraction of what circulates, and the
// interesting half of the corpus predates or ignores them.
//
// So this package is built around three admissions:
//
//  1. **It will be wrong.** Parsed.Confidence and Parsed.Unmatched exist so a
//     caller can tell a clean parse from a guess, and so the UI can show an
//     operator what the parser did rather than only what it concluded.
//  2. **The input is hostile.** A release name arrives from an indexer, which
//     got it from a stranger. It is length-capped before anything looks at it,
//     every pattern is RE2 (linear time — no catastrophic backtracking), and
//     nothing here ever becomes a filesystem path. Path construction happens in
//     the import phase, from sanitised metadata, never from this string.
//  3. **Order is the algorithm.** Titles are whatever comes before the first
//     thing that is definitely not a title. Get the anchor order wrong and
//     "Blade Runner 2049" becomes a 2049 release of "Blade Runner".
//
// # Performance
//
// Parse costs roughly 180µs per name, so a thousand-result indexer page is
// about 0.2s. That is comfortable for a background sync and is where the
// optimisation stopped deliberately.
//
// Two profile-guided fixes got it there from 361µs, and both are worth knowing
// about because neither was visible by reading the code. Regexes were being
// compiled inside Parse — cheap-looking, and leftovers() did it once per token.
// And knownTerm ran sixty separate patterns per token, which was 55% of total
// time; it is now one anchored alternation built at init.
//
// What remains is collect(), which scans the string once per additive
// vocabulary. Merging those into one pattern each would roughly halve the
// remainder. It has not been done because it means restructuring the matching
// of the most correctness-critical code in the package to speed up a background
// task that is already fast enough, and that is a bad trade. BenchmarkParse is
// in the tree so the next person can see the number before deciding otherwise.
package release

import (
	"regexp"
	"strconv"
	"strings"
)

// MaxNameLength bounds parsing. Real release names run to perhaps 200
// characters; anything past this is either a mistake or an attempt to find out
// what happens, and neither deserves the CPU.
const MaxNameLength = 512

// Parsed is everything the parser could determine.
//
// Zero values mean "not found", never "absent from the release" — a 1080p file
// whose name omits the resolution still parses to ResolutionUnknown, and the
// caller decides whether to trust the file's own metadata instead.
type Parsed struct {
	Title string
	Year  int

	// Season is -1 when the name carries no season marker.
	Season int
	// Episodes holds every episode number named, in order. A full-season pack
	// has FullSeason set and no episodes.
	Episodes   []int
	FullSeason bool
	// AbsoluteEpisodes is the anime-style numbering, which coexists with
	// seasons rather than replacing them.
	AbsoluteEpisodes []int
	// AirDate is set for daily shows, as YYYY-MM-DD.
	AirDate string

	Resolution Resolution
	Source     Source
	Codec      Codec
	Audio      Audio
	Channels   string
	HDR        []string
	Editions   []string
	Languages  []string
	Flags      []string

	// Revision is 0 for an original release, 1 for PROPER or REPACK, 2 for
	// REPACK2, and so on. A higher revision of the same quality is an upgrade.
	Revision int
	// Proper distinguishes a PROPER (fixing a rule breach) from a REPACK
	// (the group's own re-release). Both bump Revision; only one is a claim
	// about the earlier release being defective.
	Proper bool

	Group     string
	Container string

	// Confidence is a coarse signal for the UI and for automatic decisions.
	Confidence Confidence
	// Unmatched holds tokens the parser did not recognise, which is the honest
	// way to surface "this name contains something I do not understand".
	Unmatched []string

	Raw string
}

// Confidence says how much of the name the parser accounted for.
type Confidence string

const (
	// ConfidenceHigh means a title plus a resolution or source plus either a
	// year or an episode marker — the shape of an ordinary release.
	ConfidenceHigh Confidence = "high"
	// ConfidenceMedium means a title and at least one quality marker.
	ConfidenceMedium Confidence = "medium"
	// ConfidenceLow means little more than a title was recovered. A caller
	// automating anything on this should not.
	ConfidenceLow Confidence = "low"
)

// IsEpisode reports whether this names television rather than a film.
func (p Parsed) IsEpisode() bool {
	return p.Season >= 0 || len(p.Episodes) > 0 || len(p.AbsoluteEpisodes) > 0 || p.AirDate != ""
}

// ---------------------------------------------------------------------------
// Vocabulary
// ---------------------------------------------------------------------------

// Resolution is the vertical line count, normalised.
type Resolution string

const (
	ResolutionUnknown Resolution = ""
	Resolution480p    Resolution = "480p"
	Resolution540p    Resolution = "540p"
	Resolution576p    Resolution = "576p"
	Resolution720p    Resolution = "720p"
	Resolution1080p   Resolution = "1080p"
	Resolution2160p   Resolution = "2160p"
)

// Source is where the bytes came from. It matters more than resolution for
// quality: a 1080p WEBRip is worse than a 1080p Blu-ray remux by a wide margin.
type Source string

const (
	SourceUnknown  Source = ""
	SourceCAM      Source = "cam"
	SourceTelesync Source = "telesync"
	SourceScreener Source = "screener"
	SourceDVD      Source = "dvd"
	SourceSDTV     Source = "sdtv"
	SourceHDTV     Source = "hdtv"
	SourceWEBRip   Source = "webrip"
	SourceWEBDL    Source = "webdl"
	SourceBluRay   Source = "bluray"
	SourceRemux    Source = "remux"
)

// Codec is the video codec.
type Codec string

const (
	CodecUnknown Codec = ""
	CodecXviD    Codec = "xvid"
	CodecMPEG2   Codec = "mpeg2"
	CodecH264    Codec = "h264"
	CodecH265    Codec = "h265"
	CodecVP9     Codec = "vp9"
	CodecAV1     Codec = "av1"
)

// Audio is the audio codec, at the granularity that affects playability: a
// device that cannot decode TrueHD cannot decode Atmos carried in TrueHD.
type Audio string

const (
	AudioUnknown Audio = ""
	AudioMP3     Audio = "mp3"
	AudioAAC     Audio = "aac"
	AudioOpus    Audio = "opus"
	AudioAC3     Audio = "ac3"
	AudioEAC3    Audio = "eac3"
	AudioDTS     Audio = "dts"
	AudioDTSHD   Audio = "dts-hd"
	AudioDTSX    Audio = "dts-x"
	AudioTrueHD  Audio = "truehd"
	AudioFLAC    Audio = "flac"
	AudioPCM     Audio = "pcm"
)

// ---------------------------------------------------------------------------
// Patterns
// ---------------------------------------------------------------------------
//
// Every pattern is case-insensitive and anchored at a separator boundary, so
// "HDR" does not match inside "SHDRip" and "TS" does not match inside "GUTS".
// Go's regexp is RE2: matching is linear in the input, so a crafted name cannot
// burn CPU through backtracking. That is a deliberate reason to stay with the
// standard library here rather than reaching for a PCRE binding.

// sep is what separates tokens in a release name: dot, space, underscore,
// bracket, or the start/end of the string.
const sep = `(?:^|[\s._\-\[\]()+])`
const sepEnd = `(?:$|[\s._\-\[\]()+])`

// termBodies accumulates every vocabulary body as the patterns are built, so
// that knownTerm can test all of them with ONE regex instead of sixty. Appends
// happen during package variable initialisation, which Go runs before any
// init() function, so buildKnownTermUnion below sees a complete list.
var termBodies []string

func tokenPattern(body string) *regexp.Regexp {
	termBodies = append(termBodies, body)
	return regexp.MustCompile(`(?i)` + sep + `(` + body + `)` + sepEnd)
}

// audioTokenPattern also accepts a digit immediately after the token, because
// the channel count is conventionally welded on: DDP5.1, DD5.1, AAC2.0,
// DTS5.1. Requiring a separator there loses the codec on a large slice of the
// corpus.
func audioTokenPattern(body string) *regexp.Regexp {
	termBodies = append(termBodies, body)
	return regexp.MustCompile(`(?i)` + sep + `(` + body + `)` + `(?:$|[\s._\-\[\]()+]|\d)`)
}

// reKnownTermWhole matches a token that is ENTIRELY a vocabulary term.
//
// Sixty separate FindStringSubmatch calls per token was 55% of parse time — the
// profile said so, which is the argument for having taken one. One anchored
// alternation does the same work in a single pass.
var reKnownTermWhole *regexp.Regexp

func init() {
	bodies := make([]string, 0, len(termBodies)+2)
	bodies = append(bodies, termBodies...)
	// These two are built with MustCompile directly rather than through the
	// helpers above, so they are added by hand.
	bodies = append(bodies, `(?:proper|(?:repack|rerip)\d*)`, `[1-9][._][0-2]`)

	reKnownTermWhole = regexp.MustCompile(`(?i)^(?:` + strings.Join(bodies, "|") + `)$`)
}

// tokenStart converts a match position into the position of the TOKEN.
//
// sep matches either the empty string (at the start of the input) or exactly
// one separator character, so the token begins one byte later unless the match
// is at position zero and the first byte is not itself a separator. Using
// loc[0] directly leaves the separator on the end of every title; using the
// first capture's index is wrong whenever the capture is not the whole token,
// which is the case for every episode pattern — "S05E14" would anchor at the
// "05" and leave a stray "S" behind.
func tokenStart(s string, loc []int) int {
	if loc == nil {
		return -1
	}
	if loc[0] == 0 && !isSepByte(s[0]) {
		return 0
	}
	return loc[0] + 1
}

func isSepByte(b byte) bool {
	switch b {
	case ' ', '\t', '.', '_', '-', '[', ']', '(', ')', '+':
		return true
	}
	return false
}

var (
	// Season and episode. Ordered: the most specific shape must win, because
	// "S01E02E03" also matches the S01E02 pattern.
	reMultiEpisode  = regexp.MustCompile(`(?i)` + sep + `s(\d{1,3})[\s._-]?e(\d{1,4})(?:[\s._-]?(?:e|-)(\d{1,4}))+`)
	reSeasonEpisode = regexp.MustCompile(`(?i)` + sep + `s(\d{1,3})[\s._-]?e(\d{1,4})`)
	reEpisodeRange  = regexp.MustCompile(`(?i)` + sep + `s(\d{1,3})[\s._-]?e(\d{1,4})[\s._-]*-[\s._-]*e?(\d{1,4})`)
	reCrossX        = regexp.MustCompile(`(?i)` + sep + `(\d{1,2})x(\d{1,3})`)
	reSeasonOnly    = regexp.MustCompile(`(?i)` + sep + `(?:s(\d{1,3})|season[\s._-]?(\d{1,3}))` + `(?:` + sepEnd + `|$)`)
	reAirDate       = regexp.MustCompile(`(?i)` + sep + `(\d{4})[._-](\d{2})[._-](\d{2})` + sepEnd)
	reCompletePack  = tokenPattern(`complete|full[\s._-]?season`)

	// A year in brackets or parens is unambiguous; a bare one is not.
	reBracketedYear = regexp.MustCompile(`[\[(](\d{4})[\])]`)
	// \b rather than the separator classes: the classes are CONSUMED, so in
	// "Blade.Runner.2049.2017" the dot before 2017 is eaten by the 2049 match
	// and the real year is never seen. A zero-width boundary consumes nothing.
	reBareYear = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)

	reContainer = regexp.MustCompile(`(?i)\.(mkv|mp4|avi|m4v|ts|wmv|mov|flv|webm|mpg|mpeg|iso)$`)

	// Group: trailing "-GROUP", or "[GROUP]" / "(GROUP)" at the very end.
	//
	// The trailing form allows NO separators inside the group. Allowing dots
	// looks harmless and is not: "-([A-Za-z0-9_.]+)$" grabs everything after
	// the last dash anywhere in the name, so "S02E05-E07.1080p.BluRay.x264"
	// yields the "group" E07.1080p.BluRay.x264 and the whole tail is deleted
	// before any quality term is read. A group is one token or it is not a
	// group. The bracketed form may keep dots, because the brackets delimit it.
	reTrailingGroup  = regexp.MustCompile(`-([A-Za-z0-9_]{2,30})$`)
	reBracketedGroup = regexp.MustCompile(`[\[(]([A-Za-z0-9_. -]{2,30})[\])]\s*$`)

	reRevision = regexp.MustCompile(`(?i)` + sep + `(?:(proper)|(?:repack|rerip)(\d*))` + sepEnd)
)

// vocab maps a pattern to the value it yields. Ordered slices, not maps:
// "DTS-HD MA" must be tried before "DTS", and map iteration is random.
type vocabEntry[T ~string] struct {
	re *regexp.Regexp
	to T
}

var resolutions = []vocabEntry[Resolution]{
	{tokenPattern(`2160p|2160i|4k|uhd`), Resolution2160p},
	{tokenPattern(`1080p|1080i`), Resolution1080p},
	{tokenPattern(`720p|720i`), Resolution720p},
	{tokenPattern(`576p|576i`), Resolution576p},
	{tokenPattern(`540p`), Resolution540p},
	{tokenPattern(`480p|480i`), Resolution480p},
}

var sources = []vocabEntry[Source]{
	// Remux before BluRay: a remux names both, and remux is the stronger claim.
	{tokenPattern(`remux`), SourceRemux},
	{tokenPattern(`blu-?ray|bdrip|brrip|bd(?:25|50|r)|hddvd`), SourceBluRay},
	// WEB-DL before WEBRip before bare WEB.
	{tokenPattern(`web-?dl|webdl|amzn|nf|dsnp|hmax|atvp|hulu`), SourceWEBDL},
	{tokenPattern(`web-?rip|webrip`), SourceWEBRip},
	{tokenPattern(`web`), SourceWEBDL},
	{tokenPattern(`hdtv|pdtv|dsr`), SourceHDTV},
	{tokenPattern(`dvd-?rip|dvd-?r|dvd|ntsc|pal`), SourceDVD},
	{tokenPattern(`sdtv|tvrip`), SourceSDTV},
	{tokenPattern(`dvdscr|scr|screener`), SourceScreener},
	{tokenPattern(`hdts|telesync|ts|tc|telecine`), SourceTelesync},
	{tokenPattern(`cam-?rip|camrip|cam|hdcam`), SourceCAM},
}

var codecs = []vocabEntry[Codec]{
	{tokenPattern(`x-?265|h-?265|h\.265|hevc`), CodecH265},
	{tokenPattern(`x-?264|h-?264|h\.264|avc`), CodecH264},
	{tokenPattern(`av1`), CodecAV1},
	{tokenPattern(`vp9`), CodecVP9},
	{tokenPattern(`xvid|divx`), CodecXviD},
	{tokenPattern(`mpeg-?2`), CodecMPEG2},
}

var audios = []vocabEntry[Audio]{
	// Longest and most specific first, always.
	{audioTokenPattern(`dts-?hd[\s._-]?ma|dtshd[\s._-]?ma`), AudioDTSHD},
	{audioTokenPattern(`dts-?x`), AudioDTSX},
	{audioTokenPattern(`dts-?hd`), AudioDTSHD},
	{audioTokenPattern(`true-?hd`), AudioTrueHD},
	{audioTokenPattern(`dts`), AudioDTS},
	{audioTokenPattern(`e-?ac-?3|ddp|dd\+|dolby[\s._-]?digital[\s._-]?plus`), AudioEAC3},
	{audioTokenPattern(`dd-?ex|ac-?3|dd|dolby[\s._-]?digital`), AudioAC3},
	{audioTokenPattern(`flac`), AudioFLAC},
	{audioTokenPattern(`l?pcm`), AudioPCM},
	{audioTokenPattern(`opus`), AudioOpus},
	{audioTokenPattern(`aac`), AudioAAC},
	{audioTokenPattern(`mp3`), AudioMP3},
}

// The leading boundary is deliberately loose: "DDP5.1" has no separator before
// the 5, and requiring one loses the channel count on most WEB-DL releases.
var reChannels = regexp.MustCompile(`(?i)(?:^|[\s._\-\[\]()+a-z])([1-9])[._]([0-2])` + sepEnd)

// Compiled once, at init.
//
// These used to be compiled inside Parse, which is a mistake worth naming
// because it is invisible and expensive: regexp.MustCompile costs microseconds,
// leftovers() called one of them PER TOKEN, and a parse that should take single
// microseconds took 361 of them. An indexer page of a thousand results turned
// into a third of a second of pure compilation. The benchmark is what found it.
var (
	reSeasonNumber  = regexp.MustCompile(`(?i)s(\d{1,3})`)
	reEpisodeNumber = regexp.MustCompile(`(?i)e(\d{1,4})`)
	reWhitespaceRun = regexp.MustCompile(`\s+`)
	reSeparatorRun  = regexp.MustCompile(`[\s._\-\[\]()+]+`)
	reStructuralTok = regexp.MustCompile(`(?i)^(s\d{1,3}(e\d{1,4})*|e\d{1,4}|season|complete)$`)
)

// hdrFormats are additive: a release can be both Dolby Vision and HDR10.
var hdrFormats = []vocabEntry[string]{
	{tokenPattern(`hdr10\+|hdr10plus`), "HDR10+"},
	{tokenPattern(`dolby[\s._-]?vision|dovi|dv`), "DV"},
	{tokenPattern(`hdr10`), "HDR10"},
	{tokenPattern(`hlg`), "HLG"},
	{tokenPattern(`hdr`), "HDR"},
	{tokenPattern(`sdr`), "SDR"},
}

var editions = []vocabEntry[string]{
	{tokenPattern(`extended(?:[\s._-]?(?:cut|edition|version))?`), "Extended"},
	{tokenPattern(`director'?s?[\s._-]?cut|dc`), "Director's Cut"},
	{tokenPattern(`final[\s._-]?cut`), "Final Cut"},
	{tokenPattern(`theatrical(?:[\s._-]?cut)?`), "Theatrical"},
	{tokenPattern(`unrated`), "Unrated"},
	{tokenPattern(`uncut`), "Uncut"},
	{tokenPattern(`imax`), "IMAX"},
	{tokenPattern(`remastered`), "Remastered"},
	{tokenPattern(`criterion`), "Criterion"},
	{tokenPattern(`special[\s._-]?edition`), "Special Edition"},
	{tokenPattern(`ultimate[\s._-]?edition`), "Ultimate Edition"},
	{tokenPattern(`\d{1,3}th[\s._-]?anniversary`), "Anniversary"},
}

var languages = []vocabEntry[string]{
	{tokenPattern(`multi|multilang`), "MULTi"},
	{tokenPattern(`dual[\s._-]?audio`), "Dual Audio"},
	{tokenPattern(`french|vff|vfq|vostfr|truefrench`), "French"},
	{tokenPattern(`german|ger[\s._-]?dub`), "German"},
	{tokenPattern(`italian`), "Italian"},
	{tokenPattern(`spanish|castellano|latino`), "Spanish"},
	{tokenPattern(`nordic|swedish|danish|norwegian|finnish`), "Nordic"},
	{tokenPattern(`dutch|nl[\s._-]?subs?`), "Dutch"},
	{tokenPattern(`russian|rus`), "Russian"},
	{tokenPattern(`japanese|jpn`), "Japanese"},
	{tokenPattern(`korean|kor`), "Korean"},
	{tokenPattern(`hindi|tamil|telugu`), "Indic"},
	{tokenPattern(`subbed|subs`), "Subbed"},
	{tokenPattern(`dubbed`), "Dubbed"},
}

var flags = []vocabEntry[string]{
	{tokenPattern(`internal`), "INTERNAL"},
	{tokenPattern(`limited`), "LIMITED"},
	{tokenPattern(`readnfo|read[\s._-]?nfo`), "READNFO"},
	{tokenPattern(`hybrid`), "HYBRID"},
	{tokenPattern(`3d`), "3D"},
	{tokenPattern(`h?sbs`), "SBS"},
	{tokenPattern(`hou`), "HOU"},
	{tokenPattern(`open[\s._-]?matte`), "OPEN MATTE"},
	{tokenPattern(`10-?bit`), "10BIT"},
}

// ---------------------------------------------------------------------------
// Parsing
// ---------------------------------------------------------------------------

// Parse reads a release name.
//
// It never returns an error. A name it cannot make sense of yields a Parsed
// with ConfidenceLow and whatever was recovered, because the caller's job is to
// decide what to do with a poor parse, and an error would collapse "this is
// gibberish" and "this is a film I have not seen before" into the same thing.
func Parse(name string) Parsed {
	raw := name
	if len(name) > MaxNameLength {
		name = name[:MaxNameLength]
	}

	p := Parsed{Season: -1, Raw: raw}

	// 1. Container, before anything else: ".ts" as an extension is a
	//    container, while "TS" in the middle is a telesync, and stripping it
	//    first removes the ambiguity entirely.
	if m := reContainer.FindStringSubmatch(name); m != nil {
		p.Container = strings.ToLower(m[1])
		name = name[:len(name)-len(m[0])]
	}

	// 2. Group, before quality terms are consumed: the trailing "-GROUP" is
	//    positional, and the position is gone once the string is chopped.
	name = extractGroup(name, &p)

	// 3. Episode markers, most specific shape first.
	nameLower := name
	firstAnchor := len(name)

	if idx := parseEpisodes(nameLower, &p); idx >= 0 && idx < firstAnchor {
		firstAnchor = idx
	}

	// 4. Year. A bracketed year always wins; a bare year is trusted only if
	//    nothing has claimed it as part of the title.
	if idx := parseYear(name, &p); idx >= 0 && idx < firstAnchor {
		firstAnchor = idx
	}

	// Where the tags start. A scene name is the title, then the year or the
	// episode, then the tags — so with a year or an episode marker found, a
	// vocabulary word BEFORE it is part of the title, not a tag: "The French
	// Connection", "Uncut Gems", "Russian Doll", "Charlotte's Web". Read as
	// tags, those cut the title to "The", "", "" and "Charlottes", and no
	// search could ever match them — found probing language tags for automatic
	// acquisition (ADR-0030). With neither, the vocabulary is read everywhere,
	// as before.
	tagFrom := 0
	if firstAnchor < len(name) {
		tagFrom = firstAnchor
	}

	// 5. Quality vocabulary. Each match is also a candidate anchor, because the
	//    title is whatever precedes the first term that cannot be a title.
	for _, e := range resolutions {
		if idx := matchFrom(e.re, name, tagFrom); idx >= 0 {
			if p.Resolution == ResolutionUnknown {
				p.Resolution = e.to
			}
			if idx < firstAnchor {
				firstAnchor = idx
			}
			break
		}
	}
	for _, e := range sources {
		if idx := matchFrom(e.re, name, tagFrom); idx >= 0 {
			if p.Source == SourceUnknown {
				p.Source = e.to
			}
			if idx < firstAnchor {
				firstAnchor = idx
			}
			break
		}
	}
	for _, e := range codecs {
		if idx := matchFrom(e.re, name, tagFrom); idx >= 0 {
			if p.Codec == CodecUnknown {
				p.Codec = e.to
			}
			if idx < firstAnchor {
				firstAnchor = idx
			}
			break
		}
	}
	for _, e := range audios {
		if idx := matchFrom(e.re, name, tagFrom); idx >= 0 {
			if p.Audio == AudioUnknown {
				p.Audio = e.to
			}
			if idx < firstAnchor {
				firstAnchor = idx
			}
			break
		}
	}
	if m := reChannels.FindStringSubmatch(name[tagFrom:]); m != nil {
		p.Channels = m[1] + "." + m[2]
	}

	// Additive vocabularies: every match counts, and each can be an anchor.
	p.HDR = collect(hdrFormats, name, tagFrom, &firstAnchor)
	p.Editions = collect(editions, name, tagFrom, &firstAnchor)
	p.Languages = collect(languages, name, tagFrom, &firstAnchor)
	p.Flags = collect(flags, name, tagFrom, &firstAnchor)

	// 6. Revision.
	if m := reRevision.FindStringSubmatchIndex(name[tagFrom:]); m != nil {
		for i := range m {
			if m[i] >= 0 {
				m[i] += tagFrom
			}
		}
		sub := reRevision.FindStringSubmatch(name[tagFrom:])
		p.Revision = 1
		if sub[1] != "" {
			p.Proper = true
		} else if sub[2] != "" {
			if n, err := strconv.Atoi(sub[2]); err == nil && n > 1 {
				p.Revision = n
			}
		}
		if idx := tokenStart(name, m); idx >= 0 && idx < firstAnchor {
			firstAnchor = idx
		}
	}

	// 7. The title is everything before the earliest anchor.
	p.Title = cleanTitle(name[:firstAnchor])

	// A title that swallowed the whole string means nothing anchored it, which
	// is worth knowing rather than hiding.
	p.Unmatched = leftovers(name[firstAnchor:], p)
	p.Confidence = score(p)
	return p
}

// matchFirst returns the index where a pattern's captured token begins, or -1.
//
// The index is of the CAPTURE, not the match: the leading separator is part of
// the match, and using it would cut one character too many off every title.
func matchFirst(re *regexp.Regexp, s string) int {
	loc := re.FindStringSubmatchIndex(s)
	if loc == nil {
		return -1
	}
	return loc[2]
}

// matchFrom is matchFirst over s[from:], answering an index into s.
func matchFrom(re *regexp.Regexp, s string, from int) int {
	idx := matchFirst(re, s[from:])
	if idx < 0 {
		return -1
	}
	return from + idx
}

func collect[T ~string](entries []vocabEntry[T], s string, from int, anchor *int) []string {
	var out []string
	seen := map[string]bool{}
	for _, e := range entries {
		if idx := matchFrom(e.re, s, from); idx >= 0 {
			v := string(e.to)
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
			if idx < *anchor {
				*anchor = idx
			}
		}
	}
	return out
}

// parseEpisodes fills in the season and episode fields, returning the index of
// the earliest marker or -1.
func parseEpisodes(s string, p *Parsed) int {
	// Daily shows first: a date is unambiguous and would otherwise be eaten by
	// the year matcher.
	if loc := reAirDate.FindStringSubmatchIndex(s); loc != nil {
		m := reAirDate.FindStringSubmatch(s)
		// Every component is range-checked, and the year goes through the same
		// plausibility test the film path uses. Skipping it here let
		// "0001.01.01" set year 1 — found by FuzzParse, and the sort of thing
		// that reaches a database column and a UI before anyone notices.
		year, errY := strconv.Atoi(m[1])
		month, errM := strconv.Atoi(m[2])
		day, errD := strconv.Atoi(m[3])
		if errY == nil && errM == nil && errD == nil &&
			plausibleYear(year) && month >= 1 && month <= 12 && day >= 1 && day <= 31 {
			p.AirDate = m[1] + "-" + m[2] + "-" + m[3]
			p.Year = year
			return tokenStart(s, loc)
		}
	}

	// A range: S01E01-E04.
	if loc := reEpisodeRange.FindStringSubmatchIndex(s); loc != nil {
		m := reEpisodeRange.FindStringSubmatch(s)
		season, _ := strconv.Atoi(m[1])
		from, _ := strconv.Atoi(m[2])
		to, _ := strconv.Atoi(m[3])
		if to >= from && to-from < 100 {
			p.Season = season
			for e := from; e <= to; e++ {
				p.Episodes = append(p.Episodes, e)
			}
			return tokenStart(s, loc)
		}
	}

	// Repeated markers: S01E01E02E03.
	if loc := reMultiEpisode.FindStringSubmatchIndex(s); loc != nil {
		whole := s[loc[0]:loc[1]]
		season := reSeasonNumber.FindStringSubmatch(whole)
		if season != nil {
			p.Season, _ = strconv.Atoi(season[1])
		}
		for _, em := range reEpisodeNumber.FindAllStringSubmatch(whole, -1) {
			if n, err := strconv.Atoi(em[1]); err == nil {
				p.Episodes = append(p.Episodes, n)
			}
		}
		if len(p.Episodes) > 1 {
			return tokenStart(s, loc)
		}
		p.Episodes = nil
	}

	if loc := reSeasonEpisode.FindStringSubmatchIndex(s); loc != nil {
		m := reSeasonEpisode.FindStringSubmatch(s)
		p.Season, _ = strconv.Atoi(m[1])
		if n, err := strconv.Atoi(m[2]); err == nil {
			p.Episodes = []int{n}
		}
		return tokenStart(s, loc)
	}

	if loc := reCrossX.FindStringSubmatchIndex(s); loc != nil {
		m := reCrossX.FindStringSubmatch(s)
		p.Season, _ = strconv.Atoi(m[1])
		if n, err := strconv.Atoi(m[2]); err == nil {
			p.Episodes = []int{n}
		}
		return tokenStart(s, loc)
	}

	if loc := reSeasonOnly.FindStringSubmatchIndex(s); loc != nil {
		m := reSeasonOnly.FindStringSubmatch(s)
		num := m[1]
		if num == "" {
			num = m[2]
		}
		if n, err := strconv.Atoi(num); err == nil {
			p.Season = n
			p.FullSeason = true
			return tokenStart(s, loc)
		}
	}

	if reCompletePack.MatchString(s) && p.Season >= 0 {
		p.FullSeason = true
	}
	return -1
}

// parseYear resolves the year, returning the index of the token or -1.
//
// The hard case is a title that ends in a number: "Blade Runner 2049 (2017)"
// and "2012 (2009)" both contain two year-shaped tokens, and picking the first
// gives a film nobody released. The rules, in order:
//
//  1. A bracketed or parenthesised year wins outright. It is punctuation the
//     uploader added specifically to disambiguate.
//  2. Otherwise the LAST year-shaped token wins, because a title's number comes
//     before the release's year in every ordering anyone uses.
//  3. A year-shaped token at position zero is part of the title, not the year —
//     "2012.2009.1080p" is the film 2012, released 2009.
func parseYear(s string, p *Parsed) int {
	if loc := reBracketedYear.FindStringSubmatchIndex(s); loc != nil {
		m := reBracketedYear.FindStringSubmatch(s)
		if n, err := strconv.Atoi(m[1]); err == nil && plausibleYear(n) {
			p.Year = n
			return loc[0]
		}
	}

	all := reBareYear.FindAllStringSubmatchIndex(s, -1)
	if len(all) == 0 {
		return -1
	}
	last := all[len(all)-1]
	// The capture starts at last[2]; a capture at index 0 means the string
	// begins with the year, which makes it the title.
	if last[2] == 0 {
		return -1
	}
	n, err := strconv.Atoi(s[last[2]:last[3]])
	if err != nil || !plausibleYear(n) {
		return -1
	}
	if p.Year == 0 {
		p.Year = n
	}
	return last[2]
}

func plausibleYear(n int) bool { return n >= 1888 && n <= 2100 }

// extractGroup pulls the release group off the end and returns the remainder.
//
// The trailing "-GROUP" form is ambiguous with hyphenated quality terms:
// "...WEB-DL" would otherwise yield the group "DL". The guard is that a group
// is never a term the vocabulary recognises.
func extractGroup(name string, p *Parsed) string {
	if m := reBracketedGroup.FindStringSubmatchIndex(name); m != nil {
		candidate := strings.TrimSpace(name[m[2]:m[3]])
		if plausibleGroup(candidate) {
			p.Group = candidate
			return strings.TrimRight(name[:m[0]], " ._-")
		}
	}
	if m := reTrailingGroup.FindStringSubmatchIndex(name); m != nil {
		candidate := name[m[2]:m[3]]
		// "WEB-DL" ends in "-DL", and so does every WEB-DL release ever
		// posted. The guard is not that the candidate looks like a quality
		// term — "DL" does not — but that the candidate JOINED TO WHAT
		// PRECEDES THE DASH is one: "WEB-DL", "Blu-Ray", "DTS-HD", "DD-EX".
		// Testing the pair rather than the tail is what keeps "x264-DEMAND"
		// yielding DEMAND.
		prefix := name[:m[0]]
		if last := lastToken(prefix); last != "" && knownTerm(last+"-"+candidate) {
			return name
		}
		if plausibleGroup(candidate) {
			p.Group = candidate
			return name[:m[0]]
		}
	}
	return name
}

// lastToken returns the final separator-delimited token of s.
func lastToken(s string) string {
	i := len(s)
	for i > 0 && !isSepByte(s[i-1]) {
		i--
	}
	return s[i:]
}

// knownTerm reports whether a token is ENTIRELY a vocabulary term.
//
// "Entirely" is the whole subtlety. Testing for a mere match would reject the
// group DEMAND out of "x264-DEMAND", because "x264" matches inside it. What
// disqualifies a group is that the candidate IS a quality term, not that it
// contains one.
func knownTerm(s string) bool {
	if s == "" {
		return false
	}
	return reKnownTermWhole.MatchString(s)
}

func plausibleGroup(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 2 || len(s) > 30 {
		return false
	}
	if knownTerm(s) {
		return false
	}
	// A group has a name. "5.1" and "2019" are a channel layout and a year,
	// and both sit at the end of a name inside brackets where a group would.
	var hasLetter bool
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			hasLetter = true
			break
		}
	}
	if !hasLetter {
		return false
	}
	if _, err := strconv.Atoi(s); err == nil {
		return false
	}
	return true
}

// cleanTitle turns the raw prefix into something readable.
func cleanTitle(s string) string {
	// A release name is hostile input, so two classes of byte are removed
	// before anything else.
	//
	// Invalid UTF-8 is DROPPED rather than replaced. strings.Map substitutes
	// U+FFFD for an invalid byte, which is three bytes where there was one — so
	// a title can come out longer than the name it was parsed from, and a
	// "sanitised" string grows under sanitisation. Dropping keeps the invariant
	// and leaves no replacement-character noise in a title an operator reads.
	// Found by FuzzParse in about a second, which is the argument for the fuzz
	// target existing at all.
	s = strings.ToValidUTF8(s, "")

	// Control characters: a NUL or a newline surviving into a title would go on
	// to reach a log line, a template, and eventually a filename.
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	s = strings.NewReplacer(".", " ", "_", " ").Replace(s)
	s = strings.Trim(s, " -[](){}")
	s = reWhitespaceRun.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// leftovers reports tokens after the title that nothing in the vocabulary
// explained. It is the parser admitting what it did not understand, which is
// the difference between a tool an operator can debug and one they cannot.
func leftovers(tail string, p Parsed) []string {
	if tail == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, tok := range reSeparatorRun.Split(tail, -1) {
		if tok == "" || seen[strings.ToLower(tok)] {
			continue
		}
		if knownTerm(tok) {
			continue
		}
		// Numbers here are episode counts, years and channel halves, all of
		// which are accounted for elsewhere.
		if _, err := strconv.Atoi(tok); err == nil {
			continue
		}
		if strings.EqualFold(tok, p.Group) {
			continue
		}
		// Episode markers are structure, not vocabulary.
		if reStructuralTok.MatchString(tok) {
			continue
		}
		seen[strings.ToLower(tok)] = true
		out = append(out, tok)
	}
	return out
}

func score(p Parsed) Confidence {
	hasQuality := p.Resolution != ResolutionUnknown || p.Source != SourceUnknown
	hasIdentity := p.Year != 0 || p.IsEpisode()

	switch {
	case p.Title == "":
		return ConfidenceLow
	case hasQuality && hasIdentity:
		return ConfidenceHigh
	case hasQuality || hasIdentity:
		return ConfidenceMedium
	default:
		return ConfidenceLow
	}
}

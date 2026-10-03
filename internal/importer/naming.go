package importer

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/release"
)

// Naming: turning a parsed release into the path it will occupy in a library.
//
// The layout is the one every media server already understands, because an
// operator's library outlives this software and the next thing they point at it
// should be able to read it without a migration:
//
//	Movies/
//	  Blade Runner 2049 (2017)/
//	    Blade Runner 2049 (2017) [Bluray-1080p].mkv
//	    Blade Runner 2049 (2017) [Bluray-1080p].en.srt
//
//	Series/
//	  The Expanse (2015)/
//	    Season 02/
//	      The Expanse (2015) - S02E05 [WEBDL-1080p].mkv
//
// Every component passes through library.SafeComponent before it becomes a
// path. That is not a formality: the title comes from a release name a stranger
// wrote, and "../../etc" is a legal title.

// Layout is where one imported file goes.
type Layout struct {
	// Folder is the item's directory inside the root, e.g.
	// "Blade Runner 2049 (2017)". One per item, shared by every file in it.
	Folder string
	// RelPath is the file's full path inside the root, folder included.
	RelPath string
	// Title and Year are what the item will be recorded as.
	Title string
	Year  int
	// Season and Episode are set for television. Season is -1 for a film.
	Season       int
	Episode      int
	EpisodeLast  int
	IsTelevision bool
}

// ErrUnnameable means the parse did not yield enough to build a path.
var ErrUnnameable = fmt.Errorf("importer: the release name does not say what this is")

// PlanLayout decides where a parsed release belongs.
//
// It refuses rather than guessing when the parse is too thin. An import that
// lands under "Unknown (0)" is worse than one that did not happen: the file is
// now in the library, the operator believes it worked, and finding it later
// means remembering which of the fifty things in that folder it was.
func PlanLayout(p release.Parsed, quality string) (Layout, error) {
	title := strings.TrimSpace(p.Title)
	if title == "" {
		return Layout{}, fmt.Errorf("%w: no title could be read from %q", ErrUnnameable, p.Raw)
	}

	l := Layout{Title: title, Year: p.Year, Season: -1}

	// Television if the name carries a season or an episode. A film never does,
	// and a series pack without either is not something to file blind.
	l.IsTelevision = p.Season >= 0 || len(p.Episodes) > 0 || p.AirDate != ""

	folderName := title
	if p.Year > 0 {
		folderName = fmt.Sprintf("%s (%d)", title, p.Year)
	}
	folder, err := library.SafeComponent(folderName)
	if err != nil {
		return Layout{}, fmt.Errorf("%w: %w", ErrUnnameable, err)
	}
	l.Folder = folder

	ext := p.Container
	if ext == "" {
		ext = "mkv"
	}

	if !l.IsTelevision {
		stem := folderName
		if quality != "" {
			stem = fmt.Sprintf("%s [%s]", folderName, quality)
		}
		name, nerr := library.PreserveExtension(stem, ext)
		if nerr != nil {
			return Layout{}, fmt.Errorf("%w: %w", ErrUnnameable, nerr)
		}
		l.RelPath = path.Join(folder, name)
		return l, nil
	}

	// --- television ---
	if p.Season < 0 {
		// An episode number with no season is ambiguous: it could be an
		// absolute-numbered anime episode, or a name this parser read only
		// half of. Filing it under a guessed season puts it somewhere the
		// operator will not look.
		return Layout{}, fmt.Errorf("%w: %q names an episode but no season",
			ErrUnnameable, p.Raw)
	}
	l.Season = p.Season

	if len(p.Episodes) == 0 {
		// A full-season pack. Each file inside it is imported separately, so a
		// pack reaching here means the selection step found one video file for
		// a whole season — which is not something to file as an episode.
		return Layout{}, fmt.Errorf("%w: %q is a season pack, not a single episode",
			ErrUnnameable, p.Raw)
	}
	l.Episode = p.Episodes[0]
	l.EpisodeLast = p.Episodes[len(p.Episodes)-1]

	seasonDir, err := library.SafeComponent(fmt.Sprintf("Season %02d", p.Season))
	if err != nil {
		return Layout{}, fmt.Errorf("%w: %w", ErrUnnameable, err)
	}

	stem := fmt.Sprintf("%s - %s", folderName, episodeCode(p))
	if quality != "" {
		stem = fmt.Sprintf("%s [%s]", stem, quality)
	}
	name, err := library.PreserveExtension(stem, ext)
	if err != nil {
		return Layout{}, fmt.Errorf("%w: %w", ErrUnnameable, err)
	}
	l.RelPath = path.Join(folder, seasonDir, name)
	return l, nil
}

// PlanEpisodeIn places an episode inside an EXISTING series folder.
//
// Used when a download was grabbed for a particular series (ADR-0023). The
// folder is the one the series already occupies, taken as it is on disk and
// NOT passed through SafeComponent: that function rewrites characters, so an
// operator's "Star Trek: Discovery" would come back as "Star Trek_ Discovery" —
// a second folder, and a series split across two. The folder is checked for
// what containment needs, that it is one component and not "." or "..", and
// the write goes through the vault regardless, which is where containment is
// actually enforced.
//
// The FILE is named from the series' own title and year rather than from the
// release, which is a stranger's spelling of it.
//
// seasonFolders says whether the series files its episodes in season folders
// or flat in its own (ADR-0063).
func PlanEpisodeIn(folder, title string, year int, p release.Parsed, quality string, seasonFolders bool) (Layout, error) {
	if err := oneComponent(folder, "series"); err != nil {
		return Layout{}, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return Layout{}, fmt.Errorf("%w: the series has no title", ErrUnnameable)
	}
	if p.Season < 0 || len(p.Episodes) == 0 {
		return Layout{}, fmt.Errorf("%w: %q is not a single numbered episode", ErrUnnameable, p.Raw)
	}

	// The series' title is usually the provider's, and a provider's title has
	// colons and question marks in it that a release name never does.
	name := nameable(title)
	if year > 0 {
		name = fmt.Sprintf("%s (%d)", name, year)
	}
	stem := fmt.Sprintf("%s - %s", name, episodeCode(p))
	if quality != "" {
		stem = fmt.Sprintf("%s [%s]", stem, quality)
	}
	ext := p.Container
	if ext == "" {
		ext = "mkv"
	}
	file, err := library.PreserveExtension(stem, ext)
	if err != nil {
		return Layout{}, fmt.Errorf("%w: %w", ErrUnnameable, err)
	}
	rel := path.Join(folder, file)
	if seasonFolders {
		seasonDir, err := library.SafeComponent(fmt.Sprintf("Season %02d", p.Season))
		if err != nil {
			return Layout{}, fmt.Errorf("%w: %w", ErrUnnameable, err)
		}
		rel = path.Join(folder, seasonDir, file)
	}
	return Layout{
		Folder: folder, RelPath: rel,
		Title: title, Year: year, IsTelevision: true,
		Season: p.Season, Episode: p.Episodes[0], EpisodeLast: p.Episodes[len(p.Episodes)-1],
	}, nil
}

// PlanFilmIn places a film inside an EXISTING film folder (ADR-0026).
//
// Used when a download was grabbed for a particular film. As for an episode,
// the folder is the film's own, taken as it is stored, and the file is named
// from the film's title and year — "Dune (2021) [Bluray-1080p].mkv" — rather
// than from the release, which may call it "Dune.Part.One".
func PlanFilmIn(folder, title string, year int, p release.Parsed, quality string) (Layout, error) {
	if err := oneComponent(folder, "film"); err != nil {
		return Layout{}, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return Layout{}, fmt.Errorf("%w: the film has no title", ErrUnnameable)
	}
	if p.IsEpisode() {
		return Layout{}, fmt.Errorf("%w: %q is television, not a film", ErrUnnameable, p.Raw)
	}

	stem := nameable(title)
	if year > 0 {
		stem = fmt.Sprintf("%s (%d)", stem, year)
	}
	if quality != "" {
		stem = fmt.Sprintf("%s [%s]", stem, quality)
	}
	ext := p.Container
	if ext == "" {
		ext = "mkv"
	}
	file, err := library.PreserveExtension(stem, ext)
	if err != nil {
		return Layout{}, fmt.Errorf("%w: %w", ErrUnnameable, err)
	}
	return Layout{
		Folder: folder, RelPath: path.Join(folder, file),
		Title: title, Year: year, Season: -1,
	}, nil
}

// oneComponent checks what containment needs of a stored folder name: that it
// is a single directory name and not "." or "..". The write goes through the
// vault regardless, which is where containment is actually enforced; this is
// the earlier, clearer refusal.
func oneComponent(folder, what string) error {
	if folder == "" || folder == "." || folder == ".." ||
		strings.ContainsAny(folder, "/\\\x00") {
		return fmt.Errorf("%w: the %s folder %q is not a single directory name",
			ErrUnnameable, what, folder)
	}
	return nil
}

// ErrUnusableFolder means a folder name an operator typed cannot be used as it
// was typed.
var ErrUnusableFolder = errors.New("importer: that is not usable as a folder name")

// titleNamer writes a provider's title the way people write it in a filename.
//
// A release name never contains a colon or a question mark; a provider's title
// often does — "Star Trek: Discovery", "What If...?" and "M*A*S*H" are all real
// TMDB titles. SafeComponent turns each of those characters into "_", which is
// safe and reads as a fault: "Star Trek_ Discovery". These substitutions come
// first, and SafeComponent still runs afterwards and remains the only thing
// that decides what is safe (ADR-0025, decision 3).
//
// Argument order matters: strings.Replacer tries the old strings in the order
// given, so ": " is written " - " before a bare ":" would become "-".
var titleNamer = strings.NewReplacer(
	" : ", " - ",
	": ", " - ",
	":", "-",
	"/", "-",
	`\`, "-",
	"?", "",
	"*", "",
	`"`, "'",
)

// nameable is a title as it should appear in a folder or file name, before
// SafeComponent.
func nameable(title string) string {
	return strings.TrimSpace(titleNamer.Replace(title))
}

// FolderFor names the folder this software makes for a title it is adding:
// "Title (Year)", the layout the importer has always used and every media
// server reads. Nothing is created here; the folder appears when the first file
// is placed in it (ADR-0025, decision 2).
func FolderFor(title string, year int) (string, error) {
	name := nameable(title)
	if name == "" {
		return "", fmt.Errorf("%w: %q leaves nothing to name a folder with", ErrUnnameable, title)
	}
	if year > 0 {
		name = fmt.Sprintf("%s (%d)", name, year)
	}
	folder, err := library.SafeComponent(name)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnnameable, err)
	}
	return folder, nil
}

// CheckFolderName accepts a folder name an operator typed, or says why not.
//
// Refused, never repaired. SafeComponent could turn almost anything into a
// usable name, and a name silently changed from the one somebody typed is how
// a series ends up in two folders: the one they meant, and the one they got.
func CheckFolderName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%w: it is empty", ErrUnusableFolder)
	}
	safe, err := library.SafeComponent(name)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnusableFolder, err)
	}
	if safe != name {
		return fmt.Errorf("%w: %q would have to be written as %q", ErrUnusableFolder, name, safe)
	}
	return nil
}

// episodeCode renders S02E05, or S02E05-E06 for a file covering a range.
//
// The range form matters: a double-length pilot released as one file is one
// file holding two episodes, and a name claiming only the first makes the
// second look missing forever.
func episodeCode(p release.Parsed) string {
	if len(p.Episodes) == 0 {
		return fmt.Sprintf("S%02d", p.Season)
	}
	first := p.Episodes[0]
	last := p.Episodes[len(p.Episodes)-1]
	if first == last {
		return fmt.Sprintf("S%02dE%02d", p.Season, first)
	}
	return fmt.Sprintf("S%02dE%02d-E%02d", p.Season, first, last)
}

// televisionOf says what sort of television a release is, for a refusal. Unlike
// episodeCode it copes with a release that has no season at all.
func televisionOf(p release.Parsed) string {
	if p.Season >= 0 {
		return episodeCode(p)
	}
	return "an episode with no season number"
}

// SubtitlePath names a sidecar to sit beside its video.
//
// Matching stems is what makes a player find it. The language tag is preserved
// from the original filename when there is one, because "which of these four
// subtitle files is English" is not a question to answer by discarding the
// answer.
func SubtitlePath(videoRel, originalName string) (string, error) {
	dir := path.Dir(videoRel)
	videoStem := strings.TrimSuffix(path.Base(videoRel), path.Ext(path.Base(videoRel)))

	ext := strings.ToLower(path.Ext(originalName))
	if ext == "" {
		return "", fmt.Errorf("%w: the subtitle has no extension", library.ErrUnsafeName)
	}

	stem := videoStem
	if tag := languageTag(originalName); tag != "" {
		stem = videoStem + "." + tag
	}
	name, err := library.PreserveExtension(stem, ext)
	if err != nil {
		return "", err
	}
	return path.Join(dir, name), nil
}

// languageTags are the codes worth recognising in a subtitle filename. Kept
// short and explicit: guessing from arbitrary words produces "2_English" being
// filed as language "2".
var languageTags = map[string]string{
	"en": "en", "eng": "en", "english": "en",
	"es": "es", "spa": "es", "spanish": "es",
	"fr": "fr", "fre": "fr", "fra": "fr", "french": "fr",
	"de": "de", "ger": "de", "deu": "de", "german": "de",
	"it": "it", "ita": "it", "italian": "it",
	"pt": "pt", "por": "pt", "portuguese": "pt",
	"ru": "ru", "rus": "ru", "russian": "ru",
	"ja": "ja", "jpn": "ja", "japanese": "ja",
	"ko": "ko", "kor": "ko", "korean": "ko",
	"zh": "zh", "chi": "zh", "zho": "zh", "chinese": "zh",
	"nl": "nl", "dut": "nl", "dutch": "nl",
	"pl": "pl", "pol": "pl", "polish": "pl",
	"ar": "ar", "ara": "ar", "arabic": "ar",
	"sv": "sv", "swe": "sv", "swedish": "sv",
	"da": "da", "dan": "da", "danish": "da",
	"no": "no", "nor": "no", "norwegian": "no",
	"fi": "fi", "fin": "fi", "finnish": "fi",
	"forced": "forced", "sdh": "sdh",
}

// tagWindow is how many trailing words may carry a language tag: the language
// itself, plus one modifier after it ("movie.en.forced.srt").
const tagWindow = 2

// languageTag reads a language from a subtitle filename, or returns "".
//
// Only the LAST couple of words are considered, and that limit is the whole
// point rather than an optimisation. Scanning the whole name — even backwards —
// finds "en" in "En.Route.2019.1080p.BluRay.srt" and files an English-titled
// film's subtitle under a language it never claimed. Backwards scanning only
// changes which wrong answer wins.
//
// A tag sits at the end because that is where every convention puts it:
// "movie.en.srt", "2_English.srt", "movie.en.forced.srt". Anything further
// forward is part of the title.
func languageTag(name string) string {
	stem := strings.TrimSuffix(path.Base(name), path.Ext(name))
	words := splitWords(strings.ToLower(stem))
	if len(words) == 0 {
		return ""
	}
	start := len(words) - tagWindow
	if start < 0 {
		start = 0
	}

	var lang, modifier string
	for _, w := range words[start:] {
		tag, ok := languageTags[w]
		if !ok {
			continue
		}
		switch tag {
		case "forced", "sdh":
			modifier = tag
		default:
			lang = tag
		}
	}
	switch {
	case lang != "" && modifier != "":
		return lang + "." + modifier
	case lang != "":
		return lang
	default:
		return modifier
	}
}

// RestoredPath decides where a trashed file goes when it is brought back.
//
// Derived from the file's own name rather than from a remembered original path,
// because the original is not recorded — Supersede keeps the basename and a
// timestamp, nothing more. That is a deliberate limit rather than an oversight:
// storing the path a file came from would mean a restore could write anywhere
// in the root the record named, and a name the code derives now is a name the
// current containment rules apply to.
func RestoredPath(name string) (string, error) {
	parsed := release.Parse(name)
	title := strings.TrimSpace(parsed.Title)
	if title == "" {
		return "", fmt.Errorf("%w: %q does not read as a release name", ErrUnnameable, name)
	}
	folderName := title
	if parsed.Year > 0 {
		folderName = fmt.Sprintf("%s (%d)", title, parsed.Year)
	}
	folder, err := library.SafeComponent(folderName)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnnameable, err)
	}
	file, err := library.SafeComponent(name)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnnameable, err)
	}
	return path.Join(folder, file), nil
}

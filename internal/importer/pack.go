package importer

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/release"
)

// Season packs (ADR-0033, decision 4).
//
// A pack's name says which season it is and nothing about which file is which
// episode; only each file's own name can say that. So a pack is planned file
// by file, from names alone, and the plan is a pure function of the file list:
// the importer runs it over what was downloaded, and automatic acquisition
// runs the same function over a .torrent's file list before anything is
// downloaded, so "this pack holds the season" means the same thing to both.

// Why a file of a pack is not imported.
const (
	ReasonPackOtherSeason = "another season"
	ReasonPackNoEpisode   = "does not say which episode it is"
	ReasonPackNotListed   = "not an episode the provider lists"
	ReasonPackTwoFiles    = "two files claim one episode"
)

// PackFile is one file of a pack, the episodes its name says it holds, and the
// subtitles that are its own.
type PackFile struct {
	Video     Candidate
	Season    int
	Episodes  []int
	Subtitles []Candidate
}

// PackPlan is what a pack holds: the files to import, in episode order, and
// every other file with the reason it is not one.
type PackPlan struct {
	Files   []PackFile
	Skipped []Rejection
}

// Holds reports which episodes the plan's files hold, for a pack of one
// season.
func (p PackPlan) Holds() map[int]bool {
	out := map[int]bool{}
	for _, f := range p.Files {
		for _, e := range f.Episodes {
			out[e] = true
		}
	}
	return out
}

// SelectPack is Select for a pack: the same allowlist, sample markers, size
// floor and relative-size rule, and then every video that survives rather
// than the largest. Every episode of a season is about the same size, so
// "largest wins" would import one of them.
func SelectPack(files []Candidate) (videos, subs []Candidate, rejected []Rejection) {
	videos, subs, rejected = sortOut(files)
	if len(videos) == 0 {
		return nil, subs, rejected
	}
	var largest int64
	for _, v := range videos {
		if v.Bytes > largest {
			largest = v.Bytes
		}
	}
	floor := int64(float64(largest) * SampleFraction)
	kept := videos[:0:0]
	for _, v := range videos {
		if v.Bytes < floor {
			rejected = append(rejected, Rejection{v.Path, v.Bytes, ReasonSampleBySize})
			continue
		}
		kept = append(kept, v)
	}
	return kept, subs, rejected
}

// reBareEpisode is an episode number without a season: E03, Ep03, Episode 3.
// Accepted only inside a pack, whose season is sealed into the grab. A bare
// "03" is not: it is how absolute numbering is written (ADR-0023).
var reBareEpisode = regexp.MustCompile(`(?i)(?:^|[\s._\-\[\]()])(?:e|ep|episode)[\s._-]?(\d{1,3})(?:$|[\s._\-\[\]()])`)

// PlanPack plans a pack of one season from its file list.
//
// listed is the provider's episode numbers for the season; a file naming one
// it does not list is skipped. Nil means the list is not consulted — the
// check before queueing, which compares against what is wanted instead.
//
// Only names are read. A path in the list is the uploader's, and nothing here
// opens, joins or resolves one.
func PlanPack(files []Candidate, season int, listed map[int]bool) PackPlan {
	var bySeason map[int]map[int]bool
	if listed != nil {
		bySeason = map[int]map[int]bool{season: listed}
	}
	return PlanSeasons(files, season, season, bySeason)
}

// PlanSeasons plans a pack of the seasons first to last (ADR-0057), each file
// filed as the season and episode its own name says. listed is each season's
// listed episode numbers, or nil when not consulted.
func PlanSeasons(files []Candidate, first, last int, listed map[int]map[int]bool) PackPlan {
	videos, subs, rejected := SelectPack(files)
	plan := PackPlan{Skipped: rejected}

	var numbered []PackFile
	for _, v := range videos {
		season, eps, why := packEpisodes(path.Base(v.Path), first, last)
		if why == "" && listed != nil {
			for _, e := range eps {
				if !listed[season][e] {
					why = fmt.Sprintf("S%02dE%02d is %s for season %d",
						season, e, ReasonPackNotListed, season)
					break
				}
			}
		}
		if why != "" {
			plan.Skipped = append(plan.Skipped, Rejection{v.Path, v.Bytes, why})
			continue
		}
		numbered = append(numbered, PackFile{Video: v, Season: season, Episodes: eps})
	}

	// Two files claiming one episode are both refused: choosing one is a
	// guess at what the uploader meant.
	claims := map[[2]int][]string{}
	for _, f := range numbered {
		for _, e := range f.Episodes {
			k := [2]int{f.Season, e}
			claims[k] = append(claims[k], f.Video.Path)
		}
	}
	for _, f := range numbered {
		var clash string
		for _, e := range f.Episodes {
			if others := claims[[2]int{f.Season, e}]; len(others) > 1 {
				sort.Strings(others)
				clash = fmt.Sprintf("%s: two files claim S%02dE%02d (%s)",
					ReasonPackTwoFiles, f.Season, e, strings.Join(others, ", "))
				break
			}
		}
		if clash != "" {
			plan.Skipped = append(plan.Skipped, Rejection{f.Video.Path, f.Video.Bytes, clash})
			continue
		}
		f.Subtitles = ownSubtitles(f.Video, subs)
		plan.Files = append(plan.Files, f)
	}

	sort.SliceStable(plan.Files, func(i, j int) bool {
		if plan.Files[i].Season != plan.Files[j].Season {
			return plan.Files[i].Season < plan.Files[j].Season
		}
		return plan.Files[i].Episodes[0] < plan.Files[j].Episodes[0]
	})
	sortRejections(plan.Skipped)
	return plan
}

// CheckPack says whether a pack's file list holds every wanted episode of its
// season, as the import would read it, and what it holds when it does not.
// Automatic acquisition asks this of a .torrent's file list before queueing a
// pack (ADR-0033, decision 5): a "pack" that is an archive, or half a season,
// would download in full and import nothing.
func CheckPack(files []Candidate, season int, want map[int]bool) (bool, string) {
	holds := PlanPack(files, season, nil).Holds()
	have := 0
	for e := range want {
		if holds[e] {
			have++
		}
	}
	if have < len(want) {
		return false, fmt.Sprintf("it holds %d of the season's %d episodes", have, len(want))
	}
	return true, ""
}

// packEpisodes reads the season and episodes a file of a pack of seasons
// first to last holds from its base name, or says why it cannot.
func packEpisodes(name string, first, last int) (int, []int, string) {
	p := release.Parse(name)
	switch {
	case p.Season >= 0 && len(p.Episodes) > 0:
		if p.Season < first || p.Season > last {
			grabbed := fmt.Sprintf("season %d", first)
			if last != first {
				grabbed = fmt.Sprintf("seasons %d to %d", first, last)
			}
			return 0, nil, fmt.Sprintf("%s: season %d, but this pack was grabbed for %s",
				ReasonPackOtherSeason, p.Season, grabbed)
		}
		return p.Season, p.Episodes, ""
	case p.Season >= 0:
		// A season marker and no episode: a nested pack, or a season-wide
		// extra. Neither is one episode.
		return 0, nil, ReasonPackNoEpisode
	}
	// A bare E03 belongs to the pack's season, so only a pack of one season
	// can have one (ADR-0057, decision 4).
	if first != last {
		return 0, nil, ReasonPackNoEpisode
	}
	stem := strings.TrimSuffix(name, path.Ext(name))
	if m := reBareEpisode.FindStringSubmatch(stem); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			return first, []int{n}, ""
		}
	}
	return 0, nil, ReasonPackNoEpisode
}

// ownSubtitles is the subtitles that belong to one file of a pack: those whose
// name begins with the file's name without its extension, or that sit in
// Subs/<that name>/. A single download's rule — anything beside the video —
// would give every episode of a pack every subtitle in its folder.
func ownSubtitles(video Candidate, subs []Candidate) []Candidate {
	stem := strings.ToLower(strings.TrimSuffix(path.Base(video.Path), path.Ext(video.Path)))
	var out []Candidate
	for _, s := range subs {
		name := strings.ToLower(path.Base(s.Path))
		dir := strings.ToLower(path.Base(path.Dir(s.Path)))
		if dir == stem || startsWithStem(name, stem) {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// startsWithStem is a prefix at a word boundary: "show.s02e01.en.srt" is
// S02E01's, "show.s02e010.srt" is not.
func startsWithStem(name, stem string) bool {
	if !strings.HasPrefix(name, stem) || len(name) == len(stem) {
		return false
	}
	switch name[len(stem)] {
	case '.', '_', '-', ' ', '[', '(':
		return true
	}
	return false
}

// importPack imports a download grabbed for a whole season, file by file.
//
// Each file is recorded on its own, told apart by its path in the download,
// and each that arrives is audited and closes requests as a single import
// does. A file already imported from this download is not looked at again,
// so a retry after a failure imports what failed and nothing twice (ADR-0033,
// decision 7). What is returned sums the pass up; the error is the first
// failure, if any, because a failure is what gets the download retried.
func (i *Importer) importPack(ctx context.Context, src Source) (Result, error) {
	t := *src.Target
	whole := func(outcome, detail string, err error) (Result, error) {
		res := Result{Outcome: outcome, Detail: detail}
		i.afterImport(ctx, src, res, err)
		return res, err
	}

	item, err := i.store.GetItem(ctx, t.ItemID)
	switch {
	case errors.Is(err, ErrItemNotFound):
		return whole(OutcomeSkipped, fmt.Sprintf("%v (item %d); not re-created from the release name",
			ErrTargetGone, t.ItemID), nil)
	case err != nil:
		return whole(OutcomeFailed, err.Error(), err)
	case item.Kind != KindSeries:
		return whole(OutcomeSkipped, fmt.Sprintf("%v: %s is not a series", ErrNotTheTarget, item.Title), nil)
	}
	last := max(t.LastSeason, t.Season)
	seasons := fmt.Sprintf("season %d", t.Season)
	if last != t.Season {
		seasons = fmt.Sprintf("seasons %d to %d", t.Season, last)
	}
	listed, anyListed := map[int]map[int]bool{}, false
	for n := t.Season; n <= last; n++ {
		eps, err := i.store.SeasonEpisodes(ctx, item.ID, n)
		if err != nil {
			return whole(OutcomeFailed, err.Error(), err)
		}
		listed[n], anyListed = eps, anyListed || len(eps) > 0
	}
	if !anyListed {
		return whole(OutcomeSkipped, fmt.Sprintf("the provider lists no episodes of %s of %s, "+
			"so no file can be told apart from a stray; it is looked at again once the series "+
			"is refreshed", seasons, item.Title), nil)
	}
	done, err := i.store.ImportedFrom(ctx, src.InfoHash)
	if err != nil {
		return whole(OutcomeFailed, err.Error(), err)
	}

	plan := PlanSeasons(src.Files, t.Season, last, listed)
	if len(plan.Files) == 0 && len(done) == 0 {
		for _, r := range plan.Skipped {
			i.afterImport(ctx, src, Result{Outcome: OutcomeSkipped, Detail: r.Reason, Selected: r.Path}, nil)
		}
		return Result{Outcome: OutcomeSkipped, Item: item,
			Detail: fmt.Sprintf("no file of this pack is an episode of %s of %s", seasons, item.Title)}, nil
	}

	// The pack's name is the uploader's description of all of it, so its
	// quality is every file's (ADR-0033, decision 2).
	subject := src.ReleaseTitle
	if strings.TrimSpace(subject) == "" {
		subject = path.Base(src.Dir)
	}
	packParsed := release.Parse(subject)
	quality := release.QualityOf(packParsed).Name

	vault, err := i.roots.OpenVault(ctx, item.RootFolderID)
	if err != nil {
		return whole(OutcomeFailed, err.Error(), err)
	}
	defer func() { _ = vault.Close() }()
	source, err := library.OpenSource(src.Dir)
	if err != nil {
		return whole(OutcomeFailed, err.Error(), err)
	}
	defer func() { _ = source.Close() }()

	var imported, skipped, failed, already int
	var firstErr error
	for _, r := range plan.Skipped {
		skipped++
		i.afterImport(ctx, src, Result{Outcome: OutcomeSkipped, Detail: r.Reason, Selected: r.Path}, nil)
	}
	for _, f := range plan.Files {
		if done[f.Video.Path] {
			already++
			continue
		}
		res, ferr := i.importPackFile(ctx, src.InfoHash, vault, source, item, f,
			packParsed, quality, subject)
		i.afterImport(ctx, src, res, ferr)
		switch res.Outcome {
		case OutcomeImported:
			imported++
		case OutcomeSkipped:
			skipped++
		default:
			failed++
			if firstErr == nil {
				firstErr = ferr
				if firstErr == nil {
					firstErr = errors.New(res.Detail)
				}
			}
		}
	}

	detail := fmt.Sprintf("%s of %s: %d imported, %d skipped, %d failed",
		seasons, item.Title, imported, skipped, failed)
	if already > 0 {
		detail += fmt.Sprintf(" (%d imported before)", already)
	}
	outcome := OutcomeSkipped
	switch {
	case imported > 0:
		outcome = OutcomeImported
	case failed > 0:
		outcome = OutcomeFailed
	}
	return Result{Outcome: outcome, Detail: detail, Item: item}, firstErr
}

// importPackFile imports one file of a pack as the episodes its name holds.
func (i *Importer) importPackFile(ctx context.Context, infoHash string, vault *library.Vault,
	source *library.ContainedSource, item Item, f PackFile,
	packParsed release.Parsed, quality, subject string) (Result, error) {

	parsed := packParsed
	parsed.Season, parsed.Episodes, parsed.FullSeason = f.Season, f.Episodes, false
	if ext := strings.TrimPrefix(path.Ext(f.Video.Path), "."); ext != "" {
		parsed.Container = ext
	}
	layout, err := PlanEpisodeIn(item.Folder, item.Title, item.Year, parsed, quality, item.SeasonFolders)
	if err != nil {
		// A name that cannot be placed is a skip with its reason, as in a
		// single import: the file is the problem, and retrying cannot help.
		return Result{Outcome: OutcomeSkipped, Detail: err.Error(), Selected: f.Video.Path, Item: item}, nil //nolint:nilerr // a skip is an outcome, recorded with its reason
	}
	hostSrc, err := source.HostPath(f.Video.Path)
	if err != nil {
		return Result{Outcome: OutcomeFailed, Selected: f.Video.Path,
			Detail: "the download names a file outside itself: " + err.Error()}, err
	}
	return i.importFile(ctx, infoHash, vault, source, hostSrc, item, item.RootFolderID, layout,
		parsed, quality, subject, Selection{Video: f.Video, Subtitles: f.Subtitles})
}

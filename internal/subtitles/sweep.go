package subtitles

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// Fetching wanted subtitles without a person (ADR-0056).

// Limits of a pass (ADR-0056, decision 3).
const (
	// SearchesPerPass is the most a pass asks OpenSubtitles.
	SearchesPerPass = 10
	// FirstWait is how long a file and language wait after a search found
	// nothing; it doubles with each, up to LongestWait.
	FirstWait   = 24 * time.Hour
	LongestWait = 30 * 24 * time.Hour
	// FailedWait is the wait after OpenSubtitles could not be asked, or
	// refused the key; QuotaWait after the day's quota ran out.
	FailedWait = time.Hour
	QuotaWait  = 24 * time.Hour
)

// What a search came to, as subtitle_search spells it.
const (
	outcomeFetched = "fetched"
	outcomeNothing = "nothing"
	outcomeFailed  = "failed"
)

// threeLetter is the ISO 639-2 codes a probe may give for each two-letter one.
var threeLetter = map[string][]string{
	"en": {"eng"}, "fr": {"fre", "fra"}, "de": {"ger", "deu"}, "es": {"spa"}, "it": {"ita"},
	"pt": {"por"}, "nl": {"dut", "nld"}, "sv": {"swe"}, "no": {"nor", "nob", "nno"}, "da": {"dan"},
	"fi": {"fin"}, "pl": {"pol"}, "ru": {"rus"}, "ja": {"jpn"}, "zh": {"chi", "zho"}, "ko": {"kor"},
	"ar": {"ara"}, "he": {"heb"}, "tr": {"tur"}, "el": {"gre", "ell"}, "cs": {"cze", "ces"},
	"hu": {"hun"}, "ro": {"rum", "ron"}, "uk": {"ukr"}, "hi": {"hin"},
}

// codesFor is every way a language may be written: its two letters and its
// three-letter forms.
func codesFor(lang string) []string { return append([]string{lang}, threeLetter[lang]...) }

// Sweeper runs the pass.
type Sweeper struct {
	svc *Service
	db  *db.DB
	now func() time.Time
}

// NewSweeper builds one.
func NewSweeper(svc *Service, database *db.DB, now func() time.Time) *Sweeper {
	if now == nil {
		now = time.Now
	}
	return &Sweeper{svc: svc, db: database, now: now}
}

type wantedFile struct {
	subject  importer.FileSubject
	language string
	imported time.Time
	state    *state
}

type state struct {
	searched  time.Time
	next      time.Time
	fruitless int
}

// files is every film's and episode's file.
func (s *Sweeper) files(ctx context.Context) ([]importer.FileSubject, map[int64]time.Time, error) {
	// Unscoped: the sweep works for the whole library (ADR-0056), not one person's view of it.
	rows, err := s.db.QueryContext(ctx, `
		SELECT f.id, f.root_folder_id, f.relative_path, f.size_bytes, i.kind, i.title,
		       COALESCE(i.tmdb_id, 0), COALESCE(f.season, 0), COALESCE(f.episode, 0), f.imported_at
		FROM media_file f JOIN media_item i ON i.id = f.item_id
		WHERE i.kind IN ('movie', 'series')`)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []importer.FileSubject
	imported := map[int64]time.Time{}
	for rows.Next() {
		var f importer.FileSubject
		var at string
		if err := rows.Scan(&f.FileID, &f.RootFolderID, &f.RelPath, &f.SizeBytes, &f.Kind, &f.Title,
			&f.TMDBID, &f.Season, &f.Episode, &at); err != nil {
			return nil, nil, err
		}
		imported[f.FileID], _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, f)
	}
	return out, imported, rows.Err()
}

// embedded is each file's embedded text subtitle languages.
func (s *Sweeper) embedded(ctx context.Context) (map[int64]map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT media_file_id, lower(language) FROM media_stream WHERE kind = 'subtitle' AND is_text = 1`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]map[string]bool{}
	for rows.Next() {
		var id int64
		var lang string
		if err := rows.Scan(&id, &lang); err != nil {
			return nil, err
		}
		if out[id] == nil {
			out[id] = map[string]bool{}
		}
		out[id][lang] = true
	}
	return out, rows.Err()
}

func (s *Sweeper) states(ctx context.Context) (map[string]*state, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT media_file_id, language, searched_at, next_at, fruitless FROM subtitle_search`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]*state{}
	for rows.Next() {
		var id int64
		var lang, searched, next string
		st := &state{}
		if err := rows.Scan(&id, &lang, &searched, &next, &st.fruitless); err != nil {
			return nil, err
		}
		st.searched, _ = time.Parse(time.RFC3339Nano, searched)
		st.next, _ = time.Parse(time.RFC3339Nano, next)
		out[fmt.Sprintf("%d:%s", id, lang)] = st
	}
	return out, rows.Err()
}

func (s *Sweeper) record(ctx context.Context, fileID int64, lang, outcome, detail string, fruitless int, next time.Time) error {
	now := s.now().UTC()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO subtitle_search (media_file_id, language, searched_at, next_at, fruitless, outcome, detail)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(media_file_id, language) DO UPDATE SET searched_at = excluded.searched_at,
		    next_at = excluded.next_at, fruitless = excluded.fruitless, outcome = excluded.outcome,
		    detail = excluded.detail`,
		fileID, lang, now.Format(time.RFC3339Nano), next.UTC().Format(time.RFC3339Nano), fruitless, outcome, detail)
	return err
}

// hasSidecar reports whether a subtitle in the language sits beside the file.
func hasSidecar(vault *library.Vault, rel, lang string) bool {
	stem := strings.TrimSuffix(rel, path.Ext(rel))
	for _, code := range codesFor(lang) {
		for _, ext := range []string{".srt", ".vtt", ".ass", ".ssa"} {
			if vault.Exists(stem + "." + code + ext) {
				return true
			}
		}
	}
	return false
}

// wait is how long a file waits after its n-th fruitless search.
func wait(n int) time.Duration {
	d := FirstWait
	for i := 1; i < n && d < LongestWait; i++ {
		d *= 2
	}
	if d > LongestWait {
		d = LongestWait
	}
	return d
}

// Run is one pass: the wanted files that are due, a few at a time
// (ADR-0056). The caller is the scheduled task, with browse alone.
func (s *Sweeper) Run(ctx context.Context) (string, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return "", err
	}
	cred, err := s.svc.credentials(ctx)
	if errors.Is(err, ErrNotConfigured) {
		return "OpenSubtitles has no key; nothing was asked", nil
	}
	if err != nil {
		return "", err
	}
	langs, err := s.svc.Languages(ctx)
	if err != nil {
		return "", err
	}
	if len(langs) == 0 {
		return "no subtitle language is wanted; nothing was asked", nil
	}
	files, imported, err := s.files(ctx)
	if err != nil {
		return "", err
	}
	embedded, err := s.embedded(ctx)
	if err != nil {
		return "", err
	}
	states, err := s.states(ctx)
	if err != nil {
		return "", err
	}

	now := s.now()
	vaults := map[int64]*library.Vault{}
	defer func() {
		for _, v := range vaults {
			_ = v.Close()
		}
	}()
	var never, waited []wantedFile
	have, waiting := 0, 0
	for _, f := range files {
		vault, ok := vaults[f.RootFolderID]
		if !ok {
			if vault, err = s.svc.vaults.OpenVault(ctx, f.RootFolderID); err != nil {
				continue
			}
			vaults[f.RootFolderID] = vault
		}
		for _, lang := range langs {
			present := hasSidecar(vault, f.RelPath, lang)
			for _, code := range codesFor(lang) {
				present = present || embedded[f.FileID][code]
			}
			if present {
				have++
				continue
			}
			w := wantedFile{subject: f, language: lang, imported: imported[f.FileID],
				state: states[fmt.Sprintf("%d:%s", f.FileID, lang)]}
			switch {
			case w.state == nil:
				never = append(never, w)
			case !w.state.next.After(now):
				waited = append(waited, w)
			default:
				waiting++
			}
		}
	}
	sort.SliceStable(never, func(i, j int) bool { return never[i].imported.After(never[j].imported) })
	sort.SliceStable(waited, func(i, j int) bool { return waited[i].state.searched.Before(waited[j].state.searched) })
	due := append(never, waited...)
	if len(due) == 0 {
		return fmt.Sprintf("no subtitle is due (%d held, %d waiting)", have, waiting), nil
	}

	fetched, nothing, failed, searched := 0, 0, 0, 0
	stopped := ""
	for _, w := range due {
		if searched >= SearchesPerPass {
			break
		}
		searched++
		fruitless := 0
		if w.state != nil {
			fruitless = w.state.fruitless
		}
		_, err := s.svc.fetch(ctx, w.subject, cred, w.language, string(authz.TaskSubtitles))
		var outcome, detail string
		var next time.Time
		switch {
		case err == nil, errors.Is(err, ErrHave):
			outcome, next, fruitless = outcomeFetched, now.Add(LongestWait), 0
			fetched++
		case errors.Is(err, ErrNoMatch), errors.Is(err, ErrNoHash):
			fruitless++
			outcome, detail, next = outcomeNothing, err.Error(), now.Add(wait(fruitless))
			nothing++
		case errors.Is(err, ErrQuota):
			outcome, detail, next = outcomeFailed, err.Error(), now.Add(QuotaWait)
			failed++
			stopped = "today's downloads are used up"
		case errors.Is(err, ErrKeyRefused):
			outcome, detail, next = outcomeFailed, err.Error(), now.Add(FailedWait)
			failed++
			stopped = "OpenSubtitles refused the key"
		default:
			outcome, detail, next = outcomeFailed, err.Error(), now.Add(FailedWait)
			failed++
		}
		if rerr := s.record(ctx, w.subject.FileID, w.language, outcome, detail, fruitless, next); rerr != nil {
			return "", rerr
		}
		if stopped != "" {
			break
		}
	}
	summary := fmt.Sprintf("searched %d of %d due: %d fetched, %d found nothing, %d failed",
		searched, len(due), fetched, nothing, failed)
	if stopped != "" {
		summary += "; stopped: " + stopped
	}
	if searched > 0 && failed == searched {
		return "", errors.New(summary)
	}
	return summary, nil
}

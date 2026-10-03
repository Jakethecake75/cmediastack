package playback

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

type storeRig struct {
	t      *testing.T
	db     *db.DB
	store  *Store
	ctx    context.Context
	fileID int64
}

var fixedNow = time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)

func newStoreRig(t *testing.T) *storeRig {
	t.Helper()
	database, err := db.Open(db.Options{Path: filepath.Join(t.TempDir(), "pb.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}

	stamp := fixedNow.Format(time.RFC3339Nano)
	r := &storeRig{t: t, db: database,
		store: NewStore(database, func() time.Time { return fixedNow })}
	r.ctx = r.asBrowser(t.Context())

	// A role and an account, because playback_position references app_user and
	// a rig without one fails every write with a foreign-key error rather than
	// with anything that explains itself.
	if _, err := database.ExecContext(r.ctx, `
		INSERT INTO role (name, rank, builtin, created_at, updated_at)
		VALUES ('Admin', 100, 1, ?, ?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(r.ctx, `
		INSERT INTO app_user (username, email, password_hash, state, role_id,
		                      rating_ceiling, created_at, updated_at)
		VALUES ('jacob', 'jacob@example.com', 'x', 'active', 1, 0, ?, ?)`,
		stamp, stamp); err != nil {
		t.Fatal(err)
	}

	res, err := database.ExecContext(r.ctx, `
		INSERT INTO root_folder (path, kind, label, created_at, updated_at)
		VALUES ('/media/movies', 'movies', 'Films', ?, ?)`, stamp, stamp)
	if err != nil {
		t.Fatal(err)
	}
	rootID, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	res, err = database.ExecContext(r.ctx, `
		INSERT INTO media_item (kind, title, year, sort_title, root_folder_id,
		                        folder, added_at, updated_at)
		VALUES ('movie', 'Arrival', 2016, 'Arrival', ?, 'Arrival (2016)', ?, ?)`,
		rootID, stamp, stamp)
	if err != nil {
		t.Fatal(err)
	}
	itemID, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	res, err = database.ExecContext(r.ctx, `
		INSERT INTO media_file (item_id, root_folder_id, relative_path,
		                        size_bytes, quality, imported_at)
		VALUES (?, ?, 'Arrival (2016)/Arrival.mkv', 1000, 'Bluray-1080p', ?)`,
		itemID, rootID, stamp)
	if err != nil {
		t.Fatal(err)
	}
	if r.fileID, err = res.LastInsertId(); err != nil {
		t.Fatal(err)
	}
	return r
}

// asUser is a context for a specific account, so a test can have two people
// who must not see each other's history.
func (r *storeRig) asUser(id int64) context.Context {
	return authz.WithPrincipal(context.Background(), &authz.Principal{
		UserID: id, Username: "u", State: authz.StateActive,
		MFASatisfied: true,
		Role: authz.Role{ID: 1, Name: "Admin", Rank: 100,
			Permissions: authz.NewPermissionSet(authz.AllPermissions...)},
	})
}

func (r *storeRig) asBrowser(ctx context.Context) context.Context {
	return authz.WithPrincipal(ctx, &authz.Principal{
		UserID: 1, Username: "jacob", State: authz.StateActive,
		MFASatisfied: true,
		Role: authz.Role{ID: 1, Name: "Admin", Rank: 100,
			Permissions: authz.NewPermissionSet(authz.AllPermissions...)},
	})
}

func sampleProbe() Probe {
	return Probe{
		Container: "matroska,webm",
		Duration:  116 * time.Minute,
		Bitrate:   8_000_000,
		SizeBytes: 1000,
		ModTime:   fixedNow,
		ProbedAt:  fixedNow,
		Sandboxed: true,
		Video: []VideoStream{{
			Index: 0, Codec: "hevc", Profile: "Main 10", Level: 150,
			Width: 3840, Height: 2160, BitDepth: 10,
			PixelFormat: "yuv420p10le", FrameRate: 23.976,
			ColorTransfer: "smpte2084", ColorPrimaries: "bt2020", HDR: true,
		}},
		Audio: []AudioStream{
			{Index: 1, Codec: "truehd", Channels: 8, ChannelLayout: "7.1",
				SampleRate: 48000, Language: "eng", Title: "Atmos", Default: true},
			{Index: 2, Codec: "ac3", Channels: 6, ChannelLayout: "5.1",
				SampleRate: 48000, Language: "fra"},
		},
		Subtitles: []SubtitleStream{
			{Index: 3, Codec: "subrip", Language: "eng", Text: true, Default: true},
			{Index: 4, Codec: "hdmv_pgs_subtitle", Language: "eng", Forced: true},
		},
	}
}

func (r *storeRig) identity() FileIdentity {
	return FileIdentity{SizeBytes: 1000, ModTime: fixedNow}
}

// A probe survives the round trip with every field a playback decision needs.
func TestAProbeIsStoredAndReadBackWhole(t *testing.T) {
	r := newStoreRig(t)
	want := sampleProbe()

	if err := r.store.Save(r.ctx, r.fileID, want); err != nil {
		t.Fatal(err)
	}
	got, err := r.store.Get(r.ctx, r.fileID, r.identity())
	if err != nil {
		t.Fatal(err)
	}

	if got.Container != want.Container || got.Duration != want.Duration {
		t.Errorf("format: %q %s, want %q %s",
			got.Container, got.Duration, want.Container, want.Duration)
	}
	if !got.Sandboxed {
		t.Error("the sandbox flag did not survive; an operator auditing later " +
			"would be told a weaker guarantee was the stronger one")
	}
	if len(got.Video) != 1 || len(got.Audio) != 2 || len(got.Subtitles) != 2 {
		t.Fatalf("streams: %d video, %d audio, %d subtitle",
			len(got.Video), len(got.Audio), len(got.Subtitles))
	}

	v := got.Video[0]
	if v.Codec != "hevc" || v.BitDepth != 10 || !v.HDR {
		t.Errorf("video = %+v", v)
	}
	if v.Width != 3840 || v.Height != 2160 {
		t.Errorf("%dx%d", v.Width, v.Height)
	}
	// The whole reason this file would refuse to play on the target hardware.
	if v.Profile != "Main 10" || v.ColorTransfer != "smpte2084" {
		t.Errorf("the fields ADR-0005 decides on did not survive: %+v", v)
	}
	if got.Audio[0].ChannelLayout != "7.1" || got.Audio[0].Language != "eng" {
		t.Errorf("audio = %+v", got.Audio[0])
	}
	// Text and bitmap tracks must stay distinguishable, or subtitles silently
	// never appear.
	if !got.Subtitles[0].Text {
		t.Error("the SRT track came back as non-text")
	}
	if got.Subtitles[1].Text {
		t.Error("the PGS track came back as text; a browser cannot be handed it")
	}
	if !got.Subtitles[1].Forced {
		t.Error("the forced flag did not survive")
	}
}

// The point of recording the file's identity: a file that changed has no valid
// probe, and saying so beats returning a description of a file that no longer
// exists.
func TestAProbeOfADifferentFileIsNotReturned(t *testing.T) {
	r := newStoreRig(t)
	if err := r.store.Save(r.ctx, r.fileID, sampleProbe()); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		id   FileIdentity
	}{
		{"the file grew", FileIdentity{SizeBytes: 2000, ModTime: fixedNow}},
		{"the file shrank", FileIdentity{SizeBytes: 999, ModTime: fixedNow}},
		{"the file was rewritten", FileIdentity{
			SizeBytes: 1000, ModTime: fixedNow.Add(time.Hour)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.store.Get(r.ctx, r.fileID, tc.id)
			if !errors.Is(err, ErrNotProbed) {
				t.Fatalf("err = %v, want ErrNotProbed — a stale probe was "+
					"returned as though it described the file", err)
			}
		})
	}
}

// Filesystems disagree about modification-time resolution. Comparing at full
// precision makes every probe look stale the moment it crosses a mount that
// rounds, and re-probes the whole library forever.
func TestSubSecondClockDriftDoesNotInvalidateAProbe(t *testing.T) {
	r := newStoreRig(t)
	p := sampleProbe()
	p.ModTime = fixedNow.Add(400 * time.Millisecond)
	if err := r.store.Save(r.ctx, r.fileID, p); err != nil {
		t.Fatal(err)
	}

	// The same second, a different nanosecond — which is what a network mount
	// that stores whole seconds reports back.
	if _, err := r.store.Get(r.ctx, r.fileID,
		FileIdentity{SizeBytes: 1000, ModTime: fixedNow}); err != nil {
		t.Fatalf("a probe was discarded over sub-second drift: %v", err)
	}
}

// Re-probing replaces the stream set rather than adding to it. A leftover
// stream from the previous version of a file points at an index that may now be
// something else entirely.
func TestReprobingReplacesTheStreamsRatherThanAddingToThem(t *testing.T) {
	r := newStoreRig(t)
	if err := r.store.Save(r.ctx, r.fileID, sampleProbe()); err != nil {
		t.Fatal(err)
	}

	// The operator re-encoded it: one video track, one audio track, no subs.
	second := Probe{
		Container: "matroska,webm", Duration: 116 * time.Minute,
		SizeBytes: 1000, ModTime: fixedNow, ProbedAt: fixedNow,
		Video: []VideoStream{{Index: 0, Codec: "h264", Width: 1920,
			Height: 1080, BitDepth: 8, PixelFormat: "yuv420p"}},
		Audio: []AudioStream{{Index: 1, Codec: "aac", Channels: 2}},
	}
	if err := r.store.Save(r.ctx, r.fileID, second); err != nil {
		t.Fatal(err)
	}

	got, err := r.store.Get(r.ctx, r.fileID, r.identity())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Video) != 1 || got.Video[0].Codec != "h264" {
		t.Errorf("video = %+v, want only the re-encode", got.Video)
	}
	if len(got.Audio) != 1 {
		t.Errorf("%d audio streams, want 1: %+v", len(got.Audio), got.Audio)
	}
	if len(got.Subtitles) != 0 {
		t.Errorf("%d subtitle streams survived a re-encode that has none: %+v — "+
			"these point at indices that are now something else",
			len(got.Subtitles), got.Subtitles)
	}
	if got.Video[0].HDR {
		t.Error("the previous probe's HDR flag survived; this file is SDR now")
	}
}

// A file that has never been probed is a named absence, not a zero value that
// reads downstream as "a file with no streams".
func TestAFileThatWasNeverProbedSaysSo(t *testing.T) {
	r := newStoreRig(t)
	_, err := r.store.Get(r.ctx, r.fileID, r.identity())
	if !errors.Is(err, ErrNotProbed) {
		t.Fatalf("err = %v, want ErrNotProbed", err)
	}
}

// Reading the library is a permission, and a probe is part of the library.
func TestProbesNeedThePermissionToBrowse(t *testing.T) {
	r := newStoreRig(t)
	if err := r.store.Save(r.ctx, r.fileID, sampleProbe()); err != nil {
		t.Fatal(err)
	}

	nobodyCtx := authz.WithPrincipal(context.Background(), &authz.Principal{
		UserID: 2, Username: "nobody", State: authz.StateActive,
		MFASatisfied: true,
		Role:         authz.Role{ID: 9, Name: "Pending", Rank: 0},
	})

	if _, err := r.store.Get(nobodyCtx, r.fileID, r.identity()); err == nil {
		t.Error("a principal with no permissions read a probe")
	}
	if err := r.store.Save(nobodyCtx, r.fileID, sampleProbe()); err == nil {
		t.Error("a principal with no permissions wrote a probe")
	}
	if err := r.store.Forget(nobodyCtx, r.fileID); err == nil {
		t.Error("a principal with no permissions deleted a probe")
	}
}

// Forgetting removes the streams too, via the foreign key. A probe row deleted
// without its streams would leave rows pointing at a file with no probe, which
// is the shape that makes "what can this server play" answer wrongly.
func TestForgettingAProbeTakesItsStreamsWithIt(t *testing.T) {
	r := newStoreRig(t)
	if err := r.store.Save(r.ctx, r.fileID, sampleProbe()); err != nil {
		t.Fatal(err)
	}

	var before int
	if err := r.db.QueryRowContext(r.ctx,
		`SELECT COUNT(*) FROM media_stream WHERE media_file_id = ?`,
		r.fileID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before != 5 {
		t.Fatalf("%d streams stored, want 5", before)
	}

	// Deleting the FILE is what a library deletion does, and the cascade is
	// what must carry the probe and its streams away with it.
	if _, err := r.db.ExecContext(r.ctx,
		`DELETE FROM media_file WHERE id = ?`, r.fileID); err != nil {
		t.Fatal(err)
	}

	var probes, streams int
	if err := r.db.QueryRowContext(r.ctx,
		`SELECT COUNT(*) FROM media_probe`).Scan(&probes); err != nil {
		t.Fatal(err)
	}
	if err := r.db.QueryRowContext(r.ctx,
		`SELECT COUNT(*) FROM media_stream`).Scan(&streams); err != nil {
		t.Fatal(err)
	}
	if probes != 0 || streams != 0 {
		t.Errorf("after deleting the file: %d probes, %d streams — both should "+
			"have cascaded away", probes, streams)
	}
}

// The operator's question, which is why the streams are a table rather than a
// JSON blob: what in this library will not play on this hardware?
func TestTheLibraryCanBeAskedWhatItCannotPlay(t *testing.T) {
	r := newStoreRig(t)
	if err := r.store.Save(r.ctx, r.fileID, sampleProbe()); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := r.db.QueryRowContext(r.ctx, `
		SELECT COUNT(DISTINCT media_file_id) FROM media_stream
		 WHERE kind = 'video' AND codec = 'hevc' AND bit_depth > 8`).
		Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("found %d files needing 10-bit HEVC decode, want 1 — this is "+
			"the query ADR-0005 makes an operator want", n)
	}
}

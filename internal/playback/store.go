package playback

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// ErrNotProbed means this file has never been probed, or its probe is stale.
var ErrNotProbed = errors.New("playback: no current probe for that file")

// Store persists probes.
//
// # Why a probe expires
//
// A file can change underneath the library — a re-download, an operator's own
// re-encode, a restore from a backup that was not quite the same file. A probe
// kept forever then produces a playback failure that looks like a bug in the
// player and cannot be diagnosed from the outside.
//
// So Get REFUSES a probe whose recorded size and modification time no longer
// match the file, rather than returning it with a warning. A caller that has to
// remember to check freshness is a caller that will forget, and the failure is
// silent.
type Store struct {
	db  *db.DB
	now func() time.Time
}

// NewStore builds the repository.
func NewStore(database *db.DB, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: database, now: now}
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// FileIdentity is what a probe is valid for: one file, as it was.
type FileIdentity struct {
	SizeBytes int64
	ModTime   time.Time
}

// Save records a probe, replacing any previous one for that file.
//
// Browse, not an editing permission. A probe records what a file already
// contains; it changes nothing about the library and nothing on disk. The
// identification pass makes the same argument for attaching a provider id
// (ADR-0019), and for the same reason: reading a fact about an item is not
// editing the item.
func (s *Store) Save(ctx context.Context, fileID int64, p Probe) error {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return err
	}

	return s.db.InTx(ctx, func(tx db.Execer) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO media_probe
			   (media_file_id, size_bytes, mod_time, probed_at, sandboxed,
			    container, duration_ms, bitrate)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(media_file_id) DO UPDATE SET
			   size_bytes = excluded.size_bytes,
			   mod_time    = excluded.mod_time,
			   probed_at   = excluded.probed_at,
			   sandboxed   = excluded.sandboxed,
			   container   = excluded.container,
			   duration_ms = excluded.duration_ms,
			   bitrate     = excluded.bitrate`,
			fileID, p.SizeBytes, ts(p.ModTime), ts(p.ProbedAt), boolInt(p.Sandboxed),
			p.Container, p.Duration.Milliseconds(), p.Bitrate); err != nil {
			return fmt.Errorf("playback: recording a probe: %w", err)
		}

		// Replaced wholesale rather than merged. A file's streams are a set
		// that changed together, and merging would leave a stream from the
		// previous version of the file sitting alongside the new ones —
		// pointing at an index that may now be something else entirely.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM media_stream WHERE media_file_id = ?`, fileID); err != nil {
			return fmt.Errorf("playback: clearing previous streams: %w", err)
		}

		for _, v := range p.Video {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO media_stream
				   (media_file_id, kind, stream_index, codec, profile, level,
				    width, height, bit_depth, pixel_format, frame_rate,
				    color_transfer, color_primaries, hdr)
				 VALUES (?, 'video', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				fileID, v.Index, v.Codec, v.Profile, v.Level,
				v.Width, v.Height, v.BitDepth, v.PixelFormat, v.FrameRate,
				v.ColorTransfer, v.ColorPrimaries, boolInt(v.HDR)); err != nil {
				return fmt.Errorf("playback: recording a video stream: %w", err)
			}
		}
		for _, a := range p.Audio {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO media_stream
				   (media_file_id, kind, stream_index, codec, channels,
				    channel_layout, sample_rate, language, title, is_default)
				 VALUES (?, 'audio', ?, ?, ?, ?, ?, ?, ?, ?)`,
				fileID, a.Index, a.Codec, a.Channels, a.ChannelLayout,
				a.SampleRate, a.Language, a.Title, boolInt(a.Default)); err != nil {
				return fmt.Errorf("playback: recording an audio stream: %w", err)
			}
		}
		for _, sub := range p.Subtitles {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO media_stream
				   (media_file_id, kind, stream_index, codec, language, title,
				    is_forced, is_text, is_default)
				 VALUES (?, 'subtitle', ?, ?, ?, ?, ?, ?, ?)`,
				fileID, sub.Index, sub.Codec, sub.Language, sub.Title,
				boolInt(sub.Forced), boolInt(sub.Text),
				boolInt(sub.Default)); err != nil {
				return fmt.Errorf("playback: recording a subtitle stream: %w", err)
			}
		}
		return nil
	})
}

// Get returns the probe for a file, but only if it still describes that file.
//
// want is the file's identity as it is NOW. A recorded probe whose size or
// modification time differs is ErrNotProbed — not a stale value with a flag,
// because a caller that has to remember to check the flag will forget.
func (s *Store) Get(ctx context.Context, fileID int64, want FileIdentity) (Probe, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return Probe{}, err
	}

	var (
		p          Probe
		modTime    string
		probedAt   string
		sandboxed  int
		durationMS int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT size_bytes, mod_time, probed_at, sandboxed, container,
		        duration_ms, bitrate
		   FROM media_probe WHERE media_file_id = ?`, fileID).
		Scan(&p.SizeBytes, &modTime, &probedAt, &sandboxed, &p.Container,
			&durationMS, &p.Bitrate)
	if errors.Is(err, sql.ErrNoRows) {
		return Probe{}, ErrNotProbed
	}
	if err != nil {
		return Probe{}, fmt.Errorf("playback: reading a probe: %w", err)
	}

	p.ModTime = parseTS(modTime)
	p.ProbedAt = parseTS(probedAt)
	p.Sandboxed = sandboxed == 1
	p.Duration = time.Duration(durationMS) * time.Millisecond

	if !p.describes(want) {
		return Probe{}, ErrNotProbed
	}

	if err := s.loadStreams(ctx, fileID, &p); err != nil {
		return Probe{}, err
	}
	return p, nil
}

// describes reports whether this probe was taken of the file described by want.
//
// Truncated to the second on both sides. Filesystems disagree about
// modification-time resolution — ext4 keeps nanoseconds, many network mounts do
// not — so comparing at full precision makes a probe look stale every time it
// crosses a mount that rounds, and re-probes the whole library forever.
func (p Probe) describes(want FileIdentity) bool {
	if p.SizeBytes != want.SizeBytes {
		return false
	}
	return p.ModTime.Truncate(time.Second).Equal(want.ModTime.UTC().Truncate(time.Second))
}

func (s *Store) loadStreams(ctx context.Context, fileID int64, p *Probe) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT kind, stream_index, codec, profile, level,
		        COALESCE(width, 0), COALESCE(height, 0), COALESCE(bit_depth, 0),
		        COALESCE(pixel_format, ''), COALESCE(frame_rate, 0),
		        COALESCE(color_transfer, ''), COALESCE(color_primaries, ''), hdr,
		        COALESCE(channels, 0), COALESCE(channel_layout, ''),
		        COALESCE(sample_rate, 0),
		        language, title, is_forced, is_text, is_default
		   FROM media_stream WHERE media_file_id = ?
		  ORDER BY stream_index`, fileID)
	if err != nil {
		return fmt.Errorf("playback: reading streams: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			kind                               string
			index, level, width, height, depth int
			codec, profile, pixFmt             string
			frameRate                          float64
			transfer, primaries                string
			hdr, channels, sampleRate          int
			layout, language, title            string
			isForced, isText, isDefault        int
		)
		if err := rows.Scan(&kind, &index, &codec, &profile, &level,
			&width, &height, &depth, &pixFmt, &frameRate,
			&transfer, &primaries, &hdr,
			&channels, &layout, &sampleRate,
			&language, &title, &isForced, &isText, &isDefault); err != nil {
			return fmt.Errorf("playback: reading streams: %w", err)
		}

		switch kind {
		case "video":
			p.Video = append(p.Video, VideoStream{
				Index: index, Codec: codec, Profile: profile, Level: level,
				Width: width, Height: height, BitDepth: depth,
				PixelFormat: pixFmt, FrameRate: frameRate,
				ColorTransfer: transfer, ColorPrimaries: primaries,
				HDR: hdr == 1,
			})
		case "audio":
			p.Audio = append(p.Audio, AudioStream{
				Index: index, Codec: codec, Channels: channels,
				ChannelLayout: layout, SampleRate: sampleRate,
				Language: language, Title: title, Default: isDefault == 1,
			})
		case "subtitle":
			p.Subtitles = append(p.Subtitles, SubtitleStream{
				Index: index, Codec: codec, Language: language, Title: title,
				Forced: isForced == 1, Default: isDefault == 1, Text: isText == 1,
			})
		}
	}
	return rows.Err()
}

// Forget removes a probe. Used when a file leaves the library; the foreign key
// would do it too, and doing it explicitly means a caller that deletes a file
// some other way does not leave a probe behind pointing at nothing.
func (s *Store) Forget(ctx context.Context, fileID int64) error {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM media_probe WHERE media_file_id = ?`, fileID); err != nil {
		return fmt.Errorf("playback: forgetting a probe: %w", err)
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

package main

import (
	"testing"

	"github.com/jakethecake75/cmediastack/internal/download"
	"github.com/jakethecake75/cmediastack/internal/importer"
)

// What a download was grabbed for reaches the importer. This line is the only
// glue between the queue row and the import, and nothing else would notice it
// going missing: the importer would simply work the series out from the
// release name again, as it did before ADR-0023.
func TestAnImportIsToldWhatTheGrabWasFor(t *testing.T) {
	files := []importer.Candidate{{Path: "ep.mkv", Bytes: 1}}
	rec := download.Record{
		InfoHash: "0123456789abcdef0123456789abcdef01234567", Title: "Severance.S02E03.1080p.WEB.H264-GRP",
		Target: &download.Target{ItemID: 4, Season: 2, Episode: 3},
	}
	src := importSource(rec, "/downloads/x", files)
	if src.Target == nil || *src.Target != (importer.Target{ItemID: 4, Season: 2, Episode: 3}) {
		t.Errorf("target = %+v, want item 4 S02E03", src.Target)
	}
	if src.InfoHash != rec.InfoHash || src.ReleaseTitle != rec.Title || src.Dir != "/downloads/x" || len(src.Files) != 1 {
		t.Errorf("source = %+v", src)
	}

	rec.Target = nil
	if plain := importSource(rec, "/downloads/x", files); plain.Target != nil {
		t.Errorf("a download grabbed for nothing was given a target: %+v", plain.Target)
	}
}

// And a film target reaches it as a film. Without the flag the import would be
// handed "item 7, season 0, episode 0" — an episode target naming nothing —
// and refuse a film it should have filed.
func TestAnImportIsToldAGrabWasForAFilm(t *testing.T) {
	rec := download.Record{
		InfoHash: "0123456789abcdef0123456789abcdef01234567", Title: "Dune.Part.One.2021.1080p.BluRay.x264-GRP",
		Target: &download.Target{ItemID: 7, Film: true},
	}
	src := importSource(rec, "/downloads/x", []importer.Candidate{{Path: "dune.mkv", Bytes: 1}})
	if src.Target == nil || *src.Target != (importer.Target{ItemID: 7, Film: true}) {
		t.Errorf("target = %+v, want film 7", src.Target)
	}
}

// And a season reaches it as a season, or the importer would be handed "item 4,
// season 2, episode 0" and refuse every file of the pack as not the episode it
// was grabbed for (ADR-0033).
func TestAnImportIsToldAGrabWasForASeason(t *testing.T) {
	rec := download.Record{
		InfoHash: "0123456789abcdef0123456789abcdef01234567", Title: "Severance.S02.1080p.WEB.H264-GRP",
		Target: &download.Target{ItemID: 4, Season: 2, Pack: true},
	}
	src := importSource(rec, "/downloads/x", []importer.Candidate{{Path: "e1.mkv", Bytes: 1}})
	if src.Target == nil || *src.Target != (importer.Target{ItemID: 4, Season: 2, Pack: true}) {
		t.Errorf("target = %+v, want season 2 of item 4", src.Target)
	}
}

package music

import (
	"testing"
)

// ADR-0045, decision 1.
func TestAudioFilesAndTheirQuality(t *testing.T) {
	for name, want := range map[string]string{
		"01 - Mysterons.flac": "FLAC", "01.MP3": "MP3", "x.m4a": "AAC", "x.opus": "Opus",
		"x.ogg": "Vorbis", "x.wav": "WAV", "cover.jpg": "", "x.mkv": "", "x.cue": "",
	} {
		if got := AudioQuality(name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
		if IsAudio(name) != (want != "") {
			t.Errorf("IsAudio(%s) = %v", name, IsAudio(name))
		}
	}
	if !Better("FLAC", "MP3") || Better("MP3", "FLAC") || Better("FLAC", "WAV") || Better("AAC", "MP3") {
		t.Error("only lossless replaces lossy")
	}
}

var dummy = []Track{
	{ID: 1, Disc: 1, Number: 1, Title: "Mysterons"},
	{ID: 2, Disc: 1, Number: 2, Title: "Sour Times"},
	{ID: 3, Disc: 1, Number: 3, Title: "Strangers"},
	{ID: 6, Disc: 1, Number: 6, Title: "It’s a Fire"},
	{ID: 11, Disc: 1, Number: 11, Title: "Glory Box"},
}

// ADR-0045, decision 3.
func TestAFileIsMatchedToATrack(t *testing.T) {
	for rel, want := range map[string]int64{
		"03 - Strangers.flac":                      3,
		"03. Strangers.mp3":                        3,
		"03 Strangers.flac":                        3,
		"3-strangers.flac":                         3,
		"Portishead - Dummy - 11 - Glory Box.flac": 11,
		"portishead-glory box.mp3":                 11,
		"Its a Fire.flac":                          6,
		"Track 02.flac":                            2,
		"portishead_dummy_11_gl0ry.flac":           11,
	} {
		got, ok := MatchTrack(rel, dummy)
		if !ok || got.ID != want {
			t.Errorf("%s: %v %v, want track %d", rel, got.ID, ok, want)
		}
	}
	for _, rel := range []string{"99 - Bonus.flac", "Interview.flac", "Sour Times vs Strangers.flac"} {
		if got, ok := MatchTrack(rel, dummy); ok {
			t.Errorf("%s matched track %d", rel, got.ID)
		}
	}

	twoDiscs := []Track{
		{ID: 101, Disc: 1, Number: 1, Title: "One"}, {ID: 102, Disc: 1, Number: 2, Title: "Two"},
		{ID: 201, Disc: 2, Number: 1, Title: "Uno"}, {ID: 202, Disc: 2, Number: 2, Title: "Dos"},
	}
	for rel, want := range map[string]int64{
		"2-01 - Uno.flac": 201, "1-02 Two.flac": 102, "201 Uno.flac": 201,
		"CD2/01 - Uno.flac": 201, "Disc 1/02.flac": 102,
	} {
		got, ok := MatchTrack(rel, twoDiscs)
		if !ok || got.ID != want {
			t.Errorf("%s: %v %v, want %d", rel, got.ID, ok, want)
		}
	}
	if _, ok := MatchTrack("01.flac", twoDiscs); ok {
		t.Error("track 1 of which disc? a number alone on two discs must not match")
	}
}

// ADR-0045, decision 2, and the album of a folder.
func TestMusicIsFiledByTheTrackList(t *testing.T) {
	a := Album{Title: "Dummy", Released: "1994-08-22"}
	p, err := TrackPath("Portishead", a, Track{Disc: 1, Number: 3, Title: "Strangers"}, ".FLAC", false)
	if err != nil || p != "Portishead/Dummy (1994)/03 - Strangers.flac" {
		t.Errorf("path %q %v", p, err)
	}
	p, _ = TrackPath("Portishead", a, Track{Disc: 2, Number: 1, Title: "AC/DC: Live?"}, ".mp3", true)
	if p != "Portishead/Dummy (1994)/2-01 - AC-DC - Live.mp3" {
		t.Errorf("an unsafe title filed as %q", p)
	}

	albums := []Album{{ID: 1, Title: "Portishead"}, {ID: 2, Title: "Portishead Live"}, {ID: 3, Title: "Dummy"}}
	for folder, want := range map[string]int64{
		"Dummy (1994)": 3, "Dummy [FLAC 24-96]": 3, "Portishead (1997)": 1,
		"Portishead Live": 2, "Portishead - Dummy (1994) [WEB FLAC]": 3,
		"[1994] Dummy": 3,
	} {
		got, ok := AlbumOfFolder(folder, "Portishead", albums)
		if !ok || got.ID != want {
			t.Errorf("%s: %v %v, want %d", folder, got.ID, ok, want)
		}
	}
	if _, ok := AlbumOfFolder("Third", "Portishead", albums); ok {
		t.Error("an album the artist does not have was matched")
	}
}

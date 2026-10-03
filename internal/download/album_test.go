package download

import "testing"

// TestAnAlbumTargetIsStoredAndReadBack: the fourth shape of target (ADR-0046,
// decision 1) survives the queue; a mixture is refused on the way in, and a
// row that fits no shape is read back as no target.
func TestAnAlbumTargetIsStoredAndReadBack(t *testing.T) {
	s, database := testStore(t)
	album := &Target{ItemID: 3, Album: 30}
	if err := s.Put(t.Context(), targeted(0, album)); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(t.Context(), hashOf(0))
	if err != nil {
		t.Fatal(err)
	}
	if got.Target == nil || *got.Target != *album {
		t.Errorf("target = %+v, want %+v", got.Target, *album)
	}
	// A re-grab from the general search keeps it.
	if err := s.Put(t.Context(), targeted(0, nil)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(t.Context(), hashOf(0)); got.Target == nil || *got.Target != *album {
		t.Errorf("a re-grab lost the album: %+v", got.Target)
	}

	for i, bad := range []*Target{
		{ItemID: 3, Album: 30, Film: true},
		{ItemID: 3, Album: 30, Pack: true, Season: 1},
		{ItemID: 3, Album: 30, Season: 1, Episode: 2},
		{ItemID: 3, Album: -1, Season: 1, Episode: 2},
		{Album: 30},
	} {
		if err := s.Put(t.Context(), targeted(byte(i+1), bad)); err == nil {
			t.Errorf("%+v was stored; it is not one shape of target", *bad)
		}
	}

	for i, cols := range []struct {
		kind                  any
		item, sea, epi, album any
	}{
		{"album", 3, nil, nil, nil},
		{"album", 3, 1, nil, 30},
		{"episode", 4, 2, 3, 30},
		{"film", 7, nil, nil, 30},
	} {
		n := byte(20 + i)
		if err := s.Put(t.Context(), targeted(n, nil)); err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(t.Context(), `
			UPDATE download_queue SET target_kind = ?, target_item_id = ?, target_season = ?,
			       target_episode = ?, target_album_id = ? WHERE info_hash = ?`,
			cols.kind, cols.item, cols.sea, cols.epi, cols.album, hashOf(n)); err != nil {
			t.Fatalf("%d: %v", i, err)
		}
		if got, _ := s.Get(t.Context(), hashOf(n)); got.Target != nil {
			t.Errorf("%d %v: read as %+v, want no target", i, cols, got.Target)
		}
	}
}

// TestABookTargetIsStoredAndReadBack: the fifth shape (ADR-0049, decision 5).
func TestABookTargetIsStoredAndReadBack(t *testing.T) {
	s, database := testStore(t)
	book := &Target{ItemID: 9, Book: true}
	if err := s.Put(t.Context(), targeted(0, book)); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(t.Context(), hashOf(0)); err != nil || got.Target == nil || *got.Target != *book {
		t.Errorf("target = %+v %v", got.Target, err)
	}
	for i, bad := range []*Target{
		{ItemID: 9, Book: true, Film: true},
		{ItemID: 9, Book: true, Album: 3},
		{ItemID: 9, Book: true, Season: 1},
		{Book: true},
	} {
		if err := s.Put(t.Context(), targeted(byte(i+1), bad)); err == nil {
			t.Errorf("%+v was stored", *bad)
		}
	}
	for i, cols := range []struct{ sea, epi any }{{1, nil}, {nil, 2}} {
		n := byte(30 + i)
		if err := s.Put(t.Context(), targeted(n, nil)); err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(t.Context(), `UPDATE download_queue SET target_kind = 'book',
			target_item_id = 9, target_season = ?, target_episode = ? WHERE info_hash = ?`,
			cols.sea, cols.epi, hashOf(n)); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.Get(t.Context(), hashOf(n)); got.Target != nil {
			t.Errorf("a book row with a season or episode read as %+v", got.Target)
		}
	}
}

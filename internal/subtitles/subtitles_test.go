package subtitles

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// ADR-0055, decision 3: the hash, against values worked out by hand.
func TestTheHashIsOpenSubtitlesHash(t *testing.T) {
	zeros := make([]byte, 2*hashChunk)
	if h, err := Hash(bytes.NewReader(zeros), int64(len(zeros))); err != nil || h != "0000000000020000" {
		t.Errorf("128 KiB of zeros: %s %v, want the size alone", h, err)
	}
	marked := make([]byte, 3*hashChunk)
	binary.LittleEndian.PutUint64(marked[0:8], 1)
	binary.LittleEndian.PutUint64(marked[len(marked)-8:], 2)
	binary.LittleEndian.PutUint64(marked[hashChunk+8:], 99) // in the middle: not read
	if h, _ := Hash(bytes.NewReader(marked), int64(len(marked))); h != "0000000000030003" {
		t.Errorf("first word 1, last word 2: %s", h)
	}
	ones := bytes.Repeat([]byte{0xff}, 1<<20)
	// 16384 words of 2^64-1 wrap to size - 16384.
	if h, _ := Hash(bytes.NewReader(ones), int64(len(ones))); h != "00000000000fc000" {
		t.Errorf("1 MiB of 0xff: %s", h)
	}
	if _, err := Hash(bytes.NewReader(zeros[:1000]), 1000); !errors.Is(err, ErrTooSmall) {
		t.Errorf("a small file: %v", err)
	}
}

// ADR-0055, decision 4.
func TestOnlyAnSRTIsTaken(t *testing.T) {
	good := []byte("1\r\n00:00:01,000 --> 00:00:02,000\r\nHello.\r\n")
	if err := CheckSRT(good); err != nil {
		t.Errorf("an SRT: %v", err)
	}
	if err := CheckSRT(append([]byte("\xef\xbb\xbf"), good...)); err != nil {
		t.Errorf("an SRT with a byte-order mark: %v", err)
	}
	for name, body := range map[string][]byte{
		"no timing":  []byte("1\nHello.\n"),
		"not UTF-8":  []byte("1\n00:00:01,000 --> 00:00:02,000\n\xff\xfe\n"),
		"a web page": []byte("<!doctype html><HTML><body>00:00 --> rate limited</body></html>"),
		"too large":  append(bytes.Repeat([]byte("a"), MaxSubtitleBytes), []byte(" --> ")...),
	} {
		if err := CheckSRT(body); !errors.Is(err, ErrNotASubtitle) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// ADR-0055, decision 3: the language, the title, a hash when there is no
// title; a hash match first, then untranslated, then the most downloaded.
func TestTheSubtitleChosenIsTheFiles(t *testing.T) {
	film := Query{Language: "en", TMDBID: 438631, Hash: "abc"}
	got, ok := Choose([]Result{
		{FileID: 1, Language: "fr", HashMatch: true},
		{FileID: 2, Language: "en", TMDBID: 1, HashMatch: true},
		{FileID: 3, Language: "en", ParentTMDBID: 438631, Season: 1, Episode: 1},
		{FileID: 4, Language: "en", TMDBID: 438631, Downloads: 900, Translated: true},
		{FileID: 5, Language: "en", TMDBID: 438631, Downloads: 10},
		{FileID: 6, Language: "en", Downloads: 500},
	}, film)
	if !ok || got.FileID != 6 {
		t.Errorf("film: %d %v, want the most downloaded untranslated one for this film", got.FileID, ok)
	}
	got, _ = Choose([]Result{
		{FileID: 5, Language: "en", TMDBID: 438631, Downloads: 10000},
		{FileID: 7, Language: "en", TMDBID: 438631, HashMatch: true, Downloads: 1, Translated: true},
	}, film)
	if got.FileID != 7 {
		t.Errorf("a hash match does not lead: %d", got.FileID)
	}

	episode := Query{Language: "en", ParentTMDBID: 95396, Season: 2, Episode: 3}
	got, ok = Choose([]Result{
		{FileID: 1, Language: "en", ParentTMDBID: 95396, Season: 2, Episode: 4, Downloads: 99},
		{FileID: 2, Language: "en", ParentTMDBID: 95396, Season: 1, Episode: 3, Downloads: 99},
		{FileID: 3, Language: "en", ParentTMDBID: 1, Season: 2, Episode: 3, Downloads: 99},
		{FileID: 4, Language: "en", ParentTMDBID: 95396, Season: 2, Episode: 3, Downloads: 1},
	}, episode)
	if !ok || got.FileID != 4 {
		t.Errorf("episode: %d %v", got.FileID, ok)
	}

	unidentified := Query{Language: "en", Hash: "abc"}
	if _, ok := Choose([]Result{{FileID: 1, Language: "en", Downloads: 99}}, unidentified); ok {
		t.Error("with no title, a subtitle that is not a hash match was taken")
	}
	if got, ok := Choose([]Result{{FileID: 2, Language: "en", HashMatch: true}}, unidentified); !ok || got.FileID != 2 {
		t.Error("with no title, a hash match was refused")
	}
	if _, ok := Choose(nil, film); ok {
		t.Error("nothing offered, something chosen")
	}
}

// ADR-0055, decisions 1 and 4: the client — its headers, sorted lower-case
// query, a sign-in kept, the quota and a refused key said, an https SRT only.
func TestOpenSubtitlesIsAskedAsItAsks(t *testing.T) {
	var mu sync.Mutex
	var queries, keys, agents, auths []string
	logins := 0
	downloadStatus := http.StatusOK
	srt := "1\n00:00:01,000 --> 00:00:02,000\nHi.\n"
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		keys = append(keys, r.URL.Path+" "+r.Header.Get("Api-Key"))
		agents = append(agents, r.Header.Get("User-Agent"))
		switch r.URL.Path {
		case "/login":
			logins++
			_, _ = w.Write([]byte(`{"token":"tok-1"}`))
		case "/subtitles":
			queries = append(queries, r.URL.RawQuery)
			_, _ = w.Write([]byte(`{"data":[{"attributes":{"language":"EN","download_count":5,"moviehash_match":true,
				"release":"Dune.2021","ai_translated":false,"feature_details":{"tmdb_id":438631},
				"files":[{"file_id":42,"file_name":"dune.srt"}]}},{"attributes":{"language":"en","files":[]}}]}`))
		case "/download":
			auths = append(auths, r.Header.Get("Authorization"))
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["file_id"] != float64(42) || body["sub_format"] != "srt" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(downloadStatus)
			_, _ = w.Write([]byte(`{"link":"` + srv.URL + `/file.srt","remaining":4,"reset_time":"23 hours"}`))
		case "/file.srt":
			_, _ = w.Write([]byte(srt))
		case "/page":
			_, _ = w.Write([]byte("<html>not a subtitle</html>"))
		}
	}))
	defer srv.Close()
	c := NewClient(srv.Client(), srv.URL, "test")
	cred := Credentials{APIKey: "k", Username: "me", Password: "pw"}
	ctx := context.Background()

	res, err := c.Search(ctx, cred, Query{Language: "EN", Hash: "8E245D9679D31E12", TMDBID: 438631})
	if err != nil || len(res) != 1 || res[0].FileID != 42 || res[0].Language != "en" || !res[0].HashMatch ||
		res[0].TMDBID != 438631 {
		t.Fatalf("search: %+v %v", res, err)
	}
	if queries[0] != "languages=en&moviehash=8e245d9679d31e12&tmdb_id=438631" {
		t.Errorf("query %q: sorted and lower-case", queries[0])
	}
	if _, err := c.Search(ctx, cred, Query{Language: "en", ParentTMDBID: 95396, Season: 2, Episode: 3}); err != nil {
		t.Fatal(err)
	}
	if queries[1] != "episode_number=3&languages=en&parent_tmdb_id=95396&season_number=2" {
		t.Errorf("an episode's query %q", queries[1])
	}
	for i := 0; i < 2; i++ {
		dl, err := c.Download(ctx, cred, 42)
		if err != nil || dl.Remaining != 4 {
			t.Fatalf("download: %+v %v", dl, err)
		}
		body, err := c.Fetch(ctx, dl.Link)
		if err != nil || string(body) != srt {
			t.Fatalf("fetch: %q %v", body, err)
		}
	}
	if logins != 1 || auths[0] != "Bearer tok-1" || auths[1] != "Bearer tok-1" {
		t.Errorf("signed in %d time(s), %q", logins, auths)
	}
	// The API key goes to the API, and never to wherever a download link
	// points: that may be another host.
	for i := range keys {
		path, key, _ := strings.Cut(keys[i], " ")
		wantKey := "k"
		if path == "/file.srt" {
			wantKey = ""
		}
		if key != wantKey || agents[i] != "CMediaStack vtest" {
			t.Errorf("request %d to %s: key %q, agent %q", i, path, key, agents[i])
		}
	}
	if _, err := c.Fetch(ctx, "http://example.com/x.srt"); !errors.Is(err, ErrNotASubtitle) {
		t.Errorf("an http link: %v", err)
	}
	if _, err := c.Fetch(ctx, srv.URL+"/page"); !errors.Is(err, ErrNotASubtitle) {
		t.Errorf("a web page: %v", err)
	}
	mu.Lock()
	downloadStatus = http.StatusNotAcceptable
	mu.Unlock()
	if _, err := c.Download(ctx, cred, 42); !errors.Is(err, ErrQuota) {
		t.Errorf("quota: %v", err)
	}
	mu.Lock()
	downloadStatus = http.StatusUnauthorized
	mu.Unlock()
	if _, err := c.Download(ctx, cred, 42); !errors.Is(err, ErrKeyRefused) {
		t.Errorf("a refused key: %v", err)
	}
	mu.Lock()
	downloadStatus = http.StatusOK
	mu.Unlock()
	if _, err := c.Download(ctx, cred, 42); err != nil || logins != 2 {
		t.Errorf("after a refusal the account signs in afresh: %v, %d sign-ins", err, logins)
	}
	// No account: no sign-in, no token.
	if _, err := c.Download(ctx, Credentials{APIKey: "k"}, 42); err != nil || auths[len(auths)-1] != "" {
		t.Errorf("without an account: %v %q", err, auths[len(auths)-1])
	}

}

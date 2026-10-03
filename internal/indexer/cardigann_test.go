package indexer

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
	"gopkg.in/yaml.v3"
)

// A public tracker's definition, written the way upstream ones are.
const publicDefinition = `---
id: sometracker
name: SomeTracker
type: public
encoding: UTF-8
links:
  - https://tracker.example.org/
caps:
  categorymappings:
    - {id: 41, cat: TV/HD, desc: "TV HD"}
    - {id: 42, cat: Movies/HD, desc: "Movies HD"}
  modes:
    search: [q]
settings:
  - name: sort
    type: select
    default: seeders
  - name: freeleech
    type: checkbox
    default: false
search:
  paths:
    - path: "search/{{ if .Keywords }}{{ .Keywords }}{{ else }}latest{{ end }}/"
  inputs:
    cat: "{{ join .Categories \",\" }}"
    order: "{{ .Config.sort }}"
    fl: "{{ if .Config.freeleech }}1{{ end }}"
    words: '{{ re_replace .Query.Keywords "\s+" "+" }}'
    none: "{{ .Query.Album }}{{ .Nothing }}"
  keywordsfilters:
    - name: re_replace
      args: ["\\s+", "-"]
  rows:
    selector: "table.torrents > tbody > tr:has(a.name)"
    filters:
      - name: andmatch
  fields:
    category:
      selector: a[href*="cat="]
      attribute: href
      filters:
        - name: querystring
          args: cat
    title_raw:
      selector: a.name
    title:
      text: "{{ .Result.title_raw }}"
      filters:
        - name: replace
          args: ["_", "."]
    details:
      selector: a.name
      attribute: href
    download:
      selector: a[href$=".torrent"]
      attribute: href
      optional: true
    magnet:
      selector: a[href^="magnet:"]
      attribute: href
      optional: true
    size:
      selector: td:nth-child(3)
    seeders:
      selector: td.seeds
    leechers:
      selector: td:nth-child(5)
      remove: span
    date:
      selector: td.age
      filters:
        - name: regexp
          args: "Added (.+)"
        - name: timeago
    downloadvolumefactor:
      case:
        "span.free": 0
        "*": 1
`

const publicPage = `<html><body>
<table class="torrents"><thead><tr><th>Name</th></tr></thead><tbody>
<tr>
  <td><a href="/browse?cat=41">TV</a></td>
  <td><a class="name" href="/t/1">The_Wire_S01E01_1080p_BluRay_x264-GRP</a> <span class="free">FL</span></td>
  <td>1.5 GB</td><td class="seeds">1,204</td><td>33<span>(new)</span></td>
  <td class="age">Added 2 hours ago</td>
  <td><a href="/dl/1.torrent">get</a></td>
</tr>
<tr>
  <td><a href="/browse?cat=42">Film</a></td>
  <td><a class="name" href="/t/2">The Wire Documentary 2008 720p</a></td>
  <td>700 MB</td><td class="seeds">5</td><td>1</td><td class="age">Added 3 days ago</td>
  <td><a href="magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567">m</a></td>
</tr>
<tr>
  <td><a href="/browse?cat=41">TV</a></td>
  <td><a class="name" href="/t/3">The Wire S01E02 Unsafe</a></td>
  <td>1 GB</td><td class="seeds">9</td><td>0</td><td class="age">Added now</td>
  <td><a href="http://127.0.0.1:9090/x.torrent">get</a></td>
</tr>
<tr>
  <td><a href="/browse?cat=41">TV</a></td>
  <td><a class="name" href="/t/4">Something Else Entirely</a></td>
  <td>1 GB</td><td class="seeds">9</td><td>0</td><td class="age">Added now</td>
  <td><a href="/dl/4.torrent">get</a></td>
</tr>
<tr>
  <td><a href="/browse?cat=41">TV</a></td>
  <td><a class="name" href="/t/5">The Wire S01E01 No Seeders Column</a></td>
  <td>1 GB</td><td>nothing here</td>
  <td><a href="/dl/5.torrent">get</a></td>
</tr>
<tr><td>an advert row with no name link</td></tr>
</tbody></table></body></html>`

func cardigannIndexer(def string) Definition {
	return Definition{ID: 7, Name: "SomeTracker", Kind: KindCardigann,
		BaseURL: "https://tracker.example.org", Cardigann: def, Enabled: true}
}

// TestAPublicTrackerIsSearchedFromItsDefinition pins ADR-0058, decisions 3
// and 4: the request a definition describes, and each row read into a
// result as a feed item would be.
func TestAPublicTrackerIsSearchedFromItsDefinition(t *testing.T) {
	var asked *http.Request
	c := NewClientWithDoer(doerFunc(func(r *http.Request) (*http.Response, error) {
		asked = r
		return respond(http.StatusOK, publicPage), nil
	}))
	got, err := c.Search(t.Context(), cardigannIndexer(publicDefinition),
		Query{Term: "The Wire", Season: 1, Episode: 1, Categories: []int{5000}})
	if err != nil {
		t.Fatal(err)
	}
	if asked.Method != http.MethodGet || asked.URL.Path != "/search/The-Wire-S01E01/" {
		t.Errorf("asked %s %s", asked.Method, asked.URL)
	}
	if q := asked.URL.Query(); q.Get("cat") != "41" || q.Get("order") != "seeders" || q.Get("fl") != "" ||
		q.Get("words") != "The+Wire+S01E01" || !q.Has("none") || q.Get("none") != "" {
		t.Errorf("inputs %v", q)
	}
	// The unsafe link, the row naming something else (andmatch) and the row
	// missing its seeders are gone; the advert row has no title and never
	// was one.
	if len(got) != 1 {
		t.Fatalf("%d results: %+v", len(got), got)
	}
	r := got[0]
	if r.Title != "The.Wire.S01E01.1080p.BluRay.x264-GRP" || r.DownloadURL != "https://tracker.example.org/dl/1.torrent" ||
		r.InfoURL != "https://tracker.example.org/t/1" || r.Seeders != 1204 || r.Leechers != 33 ||
		r.Size != 1536<<20 || len(r.Categories) != 1 || r.Categories[0] != 5040 || r.IndexerID != 7 {
		t.Errorf("result %+v", r)
	}
	if age := time.Since(r.PublishedAt); age < 119*time.Minute || age > 121*time.Minute {
		t.Errorf("published %v, want two hours ago", r.PublishedAt)
	}
	if r.Parsed.Season != 1 || len(r.Parsed.Episodes) != 1 {
		t.Errorf("parsed %+v", r.Parsed)
	}

	// No keywords: the template's else; a magnet is a download link.
	got, err = c.Search(t.Context(), cardigannIndexer(strings.Replace(publicDefinition,
		"      - name: andmatch\n", "      - name: strdump\n", 1)), Query{Season: -1})
	if err != nil {
		t.Fatal(err)
	}
	if asked.URL.Path != "/search/latest/" || asked.URL.Query().Get("cat") != "" {
		t.Errorf("asked %s", asked.URL)
	}
	titles := map[string]string{}
	for _, r := range got {
		titles[r.Title] = r.DownloadURL
	}
	if len(got) != 3 || !strings.HasPrefix(titles["The Wire Documentary 2008 720p"], "magnet:?xt=") {
		t.Errorf("%d results: %v", len(got), titles)
	}

	// A path naming another host is checked like any URL a feed names.
	asked = nil
	evil := strings.Replace(publicDefinition, `"search/{{`, `"http://169.254.169.254/{{`, 1)
	if _, err := c.Search(t.Context(), cardigannIndexer(evil), emptyQuery()); !errors.Is(err, ErrUnsafeURL) || asked != nil {
		t.Errorf("a path to the metadata address: %v (asked: %v)", err, asked != nil)
	}
}

// TestADefinitionIsCheckedWhenSaved pins decisions 2 and 3: what this build
// cannot follow is named when the indexer is saved.
func TestADefinitionIsCheckedWhenSaved(t *testing.T) {
	if err := cardigannIndexer(publicDefinition).Validate(); err != nil {
		t.Fatalf("a public definition: %v", err)
	}
	for _, tc := range []struct {
		name, from, to, want string
	}{
		{"captcha", "search:\n", "login:\n  path: login.php\n  captcha:\n    type: image\nsearch:\n", "captcha"},
		{"login method", "search:\n", "login:\n  method: oauth\nsearch:\n", `sign-in method of "oauth"`},
		{"login selector", "search:\n", "login:\n  form: form:hover\nsearch:\n", "hover"},
		{"download block without selectors", "search:\n", "download:\n  selector: a.dl\nsearch:\n", "names no selectors"},
		{"download before", "search:\n", "download:\n  before:\n    path: x\n  selectors:\n    - {selector: a}\nsearch:\n", "download.before"},
		{"download infohash", "search:\n", "download:\n  infohash:\n    hash: {selector: a}\nsearch:\n", "download.infohash"},
		{"download selector", "search:\n", "download:\n  selectors:\n    - {selector: 'a:visited'}\nsearch:\n", "visited"},
		{"filter", "name: querystring", "name: validate", `a filter "validate"`},
		{"row filter", "name: andmatch", "name: dateheaders", `row filter "dateheaders"`},
		{"selector", "td:nth-child(3)", "td:nth-last-of-type(3)", "nth-last-of-type"},
		{"category", "cat: TV/HD", "cat: TV/Holograms", `"TV/Holograms"`},
		{"template", "{{ .Config.sort }}", "{{ base64 .Config.sort }}", "base64"},
		{"no title", "    title:\n      text:", "    name:\n      text:", "no title field"},
		{"method", "- path: \"search", "- method: put\n      path: \"search", `"put"`},
		{"date headers", "  rows:\n", "  rows:\n    dateheaders:\n      selector: td.date\n", "dateheaders"},
	} {
		def := strings.Replace(publicDefinition, tc.from, tc.to, 1)
		if def == publicDefinition {
			t.Fatalf("%s: the edit did not apply", tc.name)
		}
		err := cardigannIndexer(def).Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want it to name %q", tc.name, err, tc.want)
		}
	}
	if err := cardigannIndexer("").Validate(); err != nil {
		t.Errorf("an update keeping the stored definition: %v", err)
	}
	if _, err := NewClientWithDoer(nil).Search(t.Context(), cardigannIndexer(""), emptyQuery()); err == nil ||
		!strings.Contains(err.Error(), "needs its definition") {
		t.Errorf("searched without a definition: %v", err)
	}
}

// TestAJSONTrackerIsSearched pins the JSON rows: an array at a path, each
// element's own array of torrents, a field reading its parent with "..".
func TestAJSONTrackerIsSearched(t *testing.T) {
	def := `id: jsontracker
name: JSON
links: [https://api.example.org/]
caps:
  categorymappings:
    - {id: movies, cat: Movies}
search:
  paths:
    - path: api/list.json
      method: post
      response: {type: json}
  inputs:
    query_term: "{{ .Keywords }}"
  rows:
    selector: data.movies
    attribute: torrents
    multiple: true
  fields:
    category:
      text: movies
    film:
      selector: ..title
    year:
      selector: ..year
    quality:
      selector: quality
    title:
      text: "{{ .Result.film }} {{ .Result.year }} {{ .Result.quality }}"
    download:
      selector: url
    infohash:
      selector: hash
    seeders:
      selector: seeds
    size:
      selector: size_bytes
    date:
      selector: date_uploaded_unix
      optional: true
`
	var body string
	c := NewClientWithDoer(doerFunc(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		body = r.Method + " " + r.URL.RequestURI() + " " + string(b)
		return respond(http.StatusOK, `{"data":{"movies":[{"title":"Heat","year":1995,"torrents":[
			{"quality":"1080p","url":"https://api.example.org/t/a.torrent","hash":"0123456789ABCDEF0123456789ABCDEF01234567","seeds":40,"size_bytes":2147483648,"date_uploaded_unix":1700000000},
			{"quality":"720p","url":"https://api.example.org/t/b.torrent","hash":"zz","seeds":3,"size_bytes":1}]}]}}`), nil
	}))
	got, err := c.Search(t.Context(), Definition{ID: 1, Name: "JSON", Kind: KindCardigann,
		BaseURL: "https://api.example.org/", Cardigann: def}, Query{Term: "Heat", Season: -1})
	if err != nil {
		t.Fatal(err)
	}
	if body != "POST /api/list.json query_term=Heat" {
		t.Errorf("asked %q", body)
	}
	if len(got) != 2 || got[0].Title != "Heat 1995 1080p" || got[0].Seeders != 40 || got[0].Size != 2<<30 ||
		got[0].InfoHash != "0123456789abcdef0123456789abcdef01234567" || got[1].InfoHash != "" ||
		!got[0].PublishedAt.Equal(time.Unix(1700000000, 0)) || got[0].Categories[0] != 2000 ||
		!got[1].PublishedAt.IsZero() {
		t.Errorf("results %+v", got)
	}
}

// TestTheSelectorSubset pins the CSS this build reads, and that anything
// beyond it is refused rather than misread.
func TestTheSelectorSubset(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(`<div id="top"><ul class="a b">
		<li title="x one">first</li><li><a href="/dl/2.torrent" rel="nofollow">second</a></li>
		<li class="b">third <b>bold</b></li><li data-k="v">fourth</li></ul><p>after</p></div>`))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		sel  string
		want string // the matches' text, joined by |
	}{
		{"li", "first|second|third bold|fourth"},
		{"ul.a.b > li:first-child", "first"},
		{"li:last-child", "fourth"},
		{"li:nth-child(2n)", "second|fourth"},
		{"li:nth-child(odd)", "first|third bold"},
		{"li:nth-child(-n+2)", "first|second"},
		{"li:nth-child(3)", "third bold"},
		{"#top li.b", "third bold"},
		{`li[title~="one"]`, "first"},
		{"li[data-k=v]", "fourth"},
		{`a[href^="/dl/"][href$=".torrent"]`, "second"},
		{`a[href*=dl]`, "second"},
		{"li:has(a)", "second"},
		{"li:not(.b):not(:has(a))", "first|fourth"},
		{"li:contains('BOLD')", "third bold"},
		{"li + li.b", "third bold"},
		{"li[title] + li", "second"},
		{`a[href^="dl"]`, ""},
		{`li[title~="on"]`, ""},
		{"ul:has(ul)", ""},
		{"ul ~ p", "after"},
		{"div > li", ""},
		{"b, a", "second|bold"},
		{"*[rel]", "second"},
	} {
		sel, err := parseSelector(tc.sel)
		if err != nil {
			t.Errorf("%s: %v", tc.sel, err)
			continue
		}
		var texts []string
		for _, n := range sel.selectAll(doc, 100) {
			texts = append(texts, strings.TrimSpace(nodeText(n, nil)))
		}
		if got := strings.Join(texts, "|"); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.sel, got, tc.want)
		}
	}
	for _, bad := range []string{"li:hover", "li:nth-of-type(2)", "a[href|=x]", "li >", "", "li,", "[", ":not(li"} {
		if _, err := parseSelector(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

// TestCardigannFiltersAndValues pins the filters, sizes and dates.
func TestCardigannFiltersAndValues(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		in   string
		size int64
	}{{"1.5 GB", 1536 << 20}, {"700 MiB", 700 << 20}, {"1,234.5 KB", 1264128}, {"1,5 GB", 1536 << 20},
		{"2,048 MB", 2 << 30}, {"123456", 123456}, {"", 0}, {"n/a", 0}} {
		if got := parseHumanSize(tc.in); got != tc.size {
			t.Errorf("size %q = %d, want %d", tc.in, got, tc.size)
		}
	}
	for _, tc := range []struct {
		in   string
		want time.Time
	}{
		{"3 hours ago", now.Add(-3 * time.Hour)},
		{"1 day, 2 hours ago", now.Add(-26 * time.Hour)},
		{"yesterday", now.Add(-24 * time.Hour)},
		{"2 weeks", now.Add(-14 * 24 * time.Hour)},
		{"2026-09-01T10:00:00Z", time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)},
		{"1700000000", time.Unix(1700000000, 0).UTC()},
		{"whenever", time.Time{}},
	} {
		if got := parseCgDate(tc.in, now); !got.Equal(tc.want) {
			t.Errorf("date %q = %v, want %v", tc.in, got, tc.want)
		}
	}
	filters := func(yamlList string) []cgFilter {
		var f struct {
			F []cgFilter `yaml:"f"`
		}
		if err := yaml.Unmarshal([]byte("f:\n"+yamlList), &f); err != nil {
			t.Fatal(err)
		}
		return f.F
	}
	for _, tc := range []struct{ in, filters, want string }{
		{"/x?id=5&n=2", "  - {name: querystring, args: id}", "5"},
		{"Size: 12 GB", "  - {name: regexp, args: 'Size: (\\d+)'}", "12"},
		{"abc", "  - {name: regexp, args: 'z+'}", ""},
		{"a.b.c", "  - {name: split, args: ['.', -1]}", "c"},
		{"a.b.c", "  - {name: split, args: ['.', 1]}", "b"},
		{"[x]", "  - {name: trim, args: '[]'}", "x"},
		{"x", "  - {name: prepend, args: 'p-'}\n  - {name: append, args: '{{ .Config.s }}'}", "p-xS"},
		{"Ünïcödé", "  - {name: diacritics}\n  - {name: tolower}", "unicode"},
		{"a%20b", "  - {name: urldecode}\n  - {name: toupper}", "A B"},
		{"&amp;x", "  - {name: htmldecode}", "&x"},
		{"02/01/2026", "  - {name: dateparse, args: '02/01/2006'}", "2026-01-02T00:00:00Z"},
		{"a b", "  - {name: re_replace, args: ['\\s', '_']}\n  - {name: urlencode}", "a_b"},
	} {
		got := applyCgFilters(tc.in, filters(tc.filters), map[string]any{"Config": map[string]string{"s": "S"}})
		if got != tc.want {
			t.Errorf("%q through %s = %q, want %q", tc.in, tc.filters, got, tc.want)
		}
	}
}

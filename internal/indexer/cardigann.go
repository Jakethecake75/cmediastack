package indexer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"
	"unicode"

	xhtml "golang.org/x/net/html"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
	"gopkg.in/yaml.v3"

	"github.com/jakethecake75/cmediastack/internal/release"
)

// Trackers searched from their Cardigann definitions (ADR-0058).
//
// A definition is the YAML Jackett and Prowlarr maintain for a tracker: where
// its search page is, what to send it, and how to read each row of what comes
// back. It is the operator's configuration, pasted and kept with the indexer;
// the page it reads is the tracker's, and is hostile input like a feed.

// KindCardigann is a tracker searched by its Cardigann definition.
const KindCardigann Kind = "cardigann"

// MaxDefinitionBytes caps a pasted definition. The largest upstream ones are
// a few tens of kilobytes.
const MaxDefinitionBytes = 256 << 10

type cgDef struct {
	ID       string   `yaml:"id"`
	Name     string   `yaml:"name"`
	Encoding string   `yaml:"encoding"`
	Links    []string `yaml:"links"`
	Caps     struct {
		CategoryMappings []struct {
			ID  string `yaml:"id"`
			Cat string `yaml:"cat"`
		} `yaml:"categorymappings"`
		Categories map[string]string `yaml:"categories"`
	} `yaml:"caps"`
	Settings []struct {
		Name    string    `yaml:"name"`
		Type    string    `yaml:"type"`
		Default yaml.Node `yaml:"default"`
	} `yaml:"settings"`
	Login    *cgLogin    `yaml:"login"`
	Download *cgDownload `yaml:"download"`
	Search   struct {
		Path            string            `yaml:"path"`
		Paths           []cgPath          `yaml:"paths"`
		Inputs          map[string]string `yaml:"inputs"`
		KeywordsFilters []cgFilter        `yaml:"keywordsfilters"`
		Preprocessing   yaml.Node         `yaml:"preprocessingfilters"`
		Rows            struct {
			Selector    string     `yaml:"selector"`
			After       int        `yaml:"after"`
			Attribute   string     `yaml:"attribute"`
			Multiple    bool       `yaml:"multiple"`
			Filters     []cgFilter `yaml:"filters"`
			DateHeaders yaml.Node  `yaml:"dateheaders"`
		} `yaml:"rows"`
		Fields yaml.Node `yaml:"fields"`
	} `yaml:"search"`

	// Worked out once the YAML is read.
	fields       []cgNamedField
	siteCats     map[string][]int // site category id → Newznab numbers
	config       map[string]string
	settingTypes map[string]string
}

// cgLogin is how a tracker is signed in to (ADR-0059).
type cgLogin struct {
	Path           string                     `yaml:"path"`
	SubmitPath     string                     `yaml:"submitpath"`
	Method         string                     `yaml:"method"`
	Form           string                     `yaml:"form"`
	Inputs         map[string]string          `yaml:"inputs"`
	SelectorInputs map[string]cgSelectorInput `yaml:"selectorinputs"`
	Cookies        []string                   `yaml:"cookies"`
	Error          []struct {
		Selector string `yaml:"selector"`
		Message  struct {
			Selector string `yaml:"selector"`
			Text     string `yaml:"text"`
		} `yaml:"message"`
	} `yaml:"error"`
	Test struct {
		Path     string `yaml:"path"`
		Selector string `yaml:"selector"`
	} `yaml:"test"`
	Captcha yaml.Node `yaml:"captcha"`
}

// cgDownload is how the real link is read off a details page (ADR-0060).
type cgDownload struct {
	Selectors []struct {
		Selector  string     `yaml:"selector"`
		Attribute string     `yaml:"attribute"`
		Filters   []cgFilter `yaml:"filters"`
	} `yaml:"selectors"`
	Before   yaml.Node `yaml:"before"`
	Infohash yaml.Node `yaml:"infohash"`
}

type cgSelectorInput struct {
	Selector  string `yaml:"selector"`
	Attribute string `yaml:"attribute"`
}

type cgPath struct {
	Path          string            `yaml:"path"`
	Method        string            `yaml:"method"`
	Inputs        map[string]string `yaml:"inputs"`
	InheritInputs *bool             `yaml:"inheritinputs"`
	Categories    []string          `yaml:"categories"`
	Response      struct {
		Type string `yaml:"type"`
	} `yaml:"response"`
}

type cgFilter struct {
	Name string    `yaml:"name"`
	Args yaml.Node `yaml:"args"`
}

type cgField struct {
	Selector  string     `yaml:"selector"`
	Attribute string     `yaml:"attribute"`
	Remove    string     `yaml:"remove"`
	Filters   []cgFilter `yaml:"filters"`
	Text      *string    `yaml:"text"`
	Optional  bool       `yaml:"optional"`
	Default   *string    `yaml:"default"`
	Case      yaml.Node  `yaml:"case"`
}

type cgNamedField struct {
	name string
	cgField
	cases [][2]string // selector, value, in the definition's order
}

// cgFilters is every filter this build follows (ADR-0058, decision 3).
var cgFilters = map[string]bool{
	"querystring": true, "regexp": true, "re_replace": true, "replace": true, "split": true,
	"trim": true, "prepend": true, "append": true, "tolower": true, "toupper": true,
	"urldecode": true, "urlencode": true, "dateparse": true, "timeago": true, "fuzzytime": true,
	"htmldecode": true, "diacritics": true, "strdump": true,
}

// parseCardigann reads and checks a definition, naming every part of it this
// build cannot follow.
func parseCardigann(src string) (*cgDef, error) {
	if strings.TrimSpace(src) == "" {
		return nil, errors.New("indexer: a Cardigann indexer needs its definition")
	}
	if len(src) > MaxDefinitionBytes {
		return nil, fmt.Errorf("indexer: the definition is over %d KiB", MaxDefinitionBytes>>10)
	}
	var d cgDef
	if err := yaml.Unmarshal([]byte(src), &d); err != nil {
		return nil, fmt.Errorf("indexer: the definition is not YAML this build can read: %w", err)
	}
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	if l := d.Login; l != nil {
		switch strings.ToLower(l.Method) {
		case "", "form", "post", "get", "cookie":
		default:
			add("a sign-in method of %q", l.Method)
		}
		if !l.Captcha.IsZero() {
			add("its sign-in asks for a captcha")
		}
		checkTemplate(l.Path, "login path", add)
		checkTemplate(l.SubmitPath, "login submitpath", add)
		for k, v := range l.Inputs {
			checkTemplate(v, "login input "+k, add)
		}
		sels := []string{l.Form, l.Test.Selector}
		for _, si := range l.SelectorInputs {
			sels = append(sels, si.Selector)
		}
		for _, e := range l.Error {
			sels = append(sels, e.Selector, e.Message.Selector)
		}
		for _, s := range sels {
			if s == "" {
				continue
			}
			if _, err := parseSelector(s); err != nil {
				add("login: %v", err)
			}
		}
	}
	if dl := d.Download; dl != nil {
		if !dl.Before.IsZero() {
			add("its download asks something first (download.before)")
		}
		if !dl.Infohash.IsZero() {
			add("its download is built from an infohash (download.infohash)")
		}
		if len(dl.Selectors) == 0 && dl.Before.IsZero() && dl.Infohash.IsZero() {
			add("its download block names no selectors")
		}
		for _, s := range dl.Selectors {
			if _, err := parseSelector(s.Selector); err != nil {
				add("download: %v", err)
			}
			checkFilters(s.Filters, "download", add)
		}
	}
	if !d.Search.Preprocessing.IsZero() {
		add("it filters the page before reading it (preprocessingfilters)")
	}
	if !d.Search.Rows.DateHeaders.IsZero() {
		add("its dates are in header rows (rows.dateheaders)")
	}
	if d.Search.Path != "" {
		d.Search.Paths = append([]cgPath{{Path: d.Search.Path}}, d.Search.Paths...)
	}
	if len(d.Search.Paths) == 0 {
		add("it names no search path")
	}
	isJSON := false
	for _, p := range d.Search.Paths {
		switch strings.ToLower(p.Method) {
		case "", "get", "post":
		default:
			add("a search method of %q", p.Method)
		}
		switch strings.ToLower(p.Response.Type) {
		case "", "html":
		case "json":
			isJSON = true
		default:
			add("a %q response", p.Response.Type)
		}
		checkTemplate(p.Path, "search path", add)
		for k, v := range p.Inputs {
			checkTemplate(v, "input "+k, add)
		}
	}
	for k, v := range d.Search.Inputs {
		checkTemplate(v, "input "+k, add)
	}
	checkFilters(d.Search.KeywordsFilters, "keywordsfilters", add)
	for _, f := range d.Search.Rows.Filters {
		if f.Name != "andmatch" && f.Name != "strdump" {
			add("a row filter %q", f.Name)
		}
	}
	if d.Search.Rows.Selector == "" {
		add("it names no rows selector")
	} else if !isJSON {
		if _, err := parseSelector(d.Search.Rows.Selector); err != nil {
			add("rows: %v", err)
		}
	}

	// Fields, in the order the definition gives them: a text field may read
	// the ones before it.
	if d.Search.Fields.Kind != yaml.MappingNode {
		add("it names no fields")
	}
	for i := 0; i+1 < len(d.Search.Fields.Content); i += 2 {
		f := cgNamedField{name: strings.SplitN(d.Search.Fields.Content[i].Value, "|", 2)[0]}
		if err := d.Search.Fields.Content[i+1].Decode(&f.cgField); err != nil {
			add("field %s: %v", f.name, err)
			continue
		}
		if f.Text != nil {
			checkTemplate(*f.Text, "field "+f.name, add)
		}
		if !isJSON {
			for _, s := range []string{f.Selector, f.Remove} {
				if s == "" {
					continue
				}
				if _, err := parseSelector(s); err != nil {
					add("field %s: %v", f.name, err)
				}
			}
		}
		for j := 0; j+1 < len(f.Case.Content); j += 2 {
			k := f.Case.Content[j].Value
			f.cases = append(f.cases, [2]string{k, f.Case.Content[j+1].Value})
			if _, err := parseSelector(k); err != nil && k != "*" && !isJSON {
				add("field %s: %v", f.name, err)
			}
		}
		checkFilters(f.Filters, "field "+f.name, add)
		d.fields = append(d.fields, f)
	}
	has := map[string]bool{}
	for _, f := range d.fields {
		has[f.name] = true
	}
	if !has["title"] {
		add("it has no title field")
	}
	if !has["download"] && !has["magnet"] {
		add("it has neither a download nor a magnet field")
	}

	d.siteCats = map[string][]int{}
	mapCat := func(id, cat string) {
		n, ok := newznabCategory(cat)
		if !ok {
			add("a category %q that is not a Newznab one", cat)
			return
		}
		d.siteCats[id] = append(d.siteCats[id], n)
	}
	for _, m := range d.Caps.CategoryMappings {
		mapCat(m.ID, m.Cat)
	}
	for id, cat := range d.Caps.Categories {
		mapCat(id, cat)
	}

	d.config, d.settingTypes = map[string]string{}, map[string]string{}
	for _, s := range d.Settings {
		d.settingTypes[s.Name] = s.Type
		var v string
		_ = s.Default.Decode(&v)
		if s.Type == "checkbox" {
			var b bool
			_ = s.Default.Decode(&b)
			v = ""
			if b {
				v = "True"
			}
		}
		d.config[s.Name] = v
	}

	if len(problems) > 0 {
		return nil, fmt.Errorf("indexer: this build cannot follow the definition: %s", strings.Join(problems, "; "))
	}
	return &d, nil
}

func checkTemplate(s, where string, add func(string, ...any)) {
	if _, err := cgTemplate(s); err != nil {
		add("%s: %v", where, err)
	}
}

func checkFilters(fs []cgFilter, where string, add func(string, ...any)) {
	for _, f := range fs {
		if !cgFilters[f.Name] {
			add("%s: a filter %q", where, f.Name)
		}
	}
}

// ---------------------------------------------------------------------------
// Templates
// ---------------------------------------------------------------------------

var (
	reAction    = regexp.MustCompile(`(?s)\{\{.*?\}\}`)
	reStringLit = regexp.MustCompile(`"[^"]*"`)
)

var cgFuncs = template.FuncMap{
	"join": func(list []string, sep string) string { return strings.Join(list, sep) },
	"re_replace": func(s, pattern, repl string) string {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return s
		}
		return re.ReplaceAllString(s, repl)
	},
}

// cgTemplate parses a definition's template. Jackett reads a string literal
// as written, so "\s+" is a pattern rather than a bad Go escape: a literal
// with a backslash in it becomes a raw one.
func cgTemplate(s string) (*template.Template, error) {
	s = reAction.ReplaceAllStringFunc(s, func(a string) string {
		return reStringLit.ReplaceAllStringFunc(a, func(lit string) string {
			if strings.Contains(lit, `\`) && !strings.Contains(lit, "`") {
				return "`" + lit[1:len(lit)-1] + "`"
			}
			return lit
		})
	})
	return template.New("").Funcs(cgFuncs).Option("missingkey=zero").Parse(s)
}

func cgExec(s string, data map[string]any) string {
	t, err := cgTemplate(s)
	if err != nil {
		return ""
	}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return ""
	}
	return strings.ReplaceAll(b.String(), "<no value>", "")
}

// ---------------------------------------------------------------------------
// Searching
// ---------------------------------------------------------------------------

func (c *Client) searchCardigann(ctx context.Context, d Definition, q Query) ([]Result, error) {
	def, err := parseCardigann(d.Cardigann)
	if err != nil {
		return nil, err
	}
	base, err := url.Parse(strings.TrimSpace(d.BaseURL))
	if err != nil {
		return nil, fmt.Errorf("indexer: %w", err)
	}
	if !strings.HasSuffix(base.Path, "/") {
		base.Path += "/"
	}

	data, keywords := def.templateData(d, base, q)
	doer := c.doerFor(d)
	var s *cgSession
	if def.Login != nil {
		s = c.session(d)
		// ponytail: one request at a time per signed-in tracker; per-path locks if that is ever slow.
		s.mu.Lock()
		defer s.mu.Unlock()
		if !s.signedIn {
			if err := c.signIn(ctx, d, def, s, base, data); err != nil {
				return nil, err
			}
		}
		doer = s.doer
	}

	var results []Result
	cats, _ := data["Categories"].([]string)
	for _, p := range def.Search.Paths {
		if !pathFor(p, cats) {
			continue
		}
		page, err := c.cardigannPage(ctx, d, def, base, p, data, doer)
		if errors.Is(err, errSignedOut) && s != nil {
			// The session lapsed: sign in once more and ask again.
			s.signedIn = false
			if err = c.signIn(ctx, d, def, s, base, data); err == nil {
				page, err = c.cardigannPage(ctx, d, def, base, p, data, doer)
			}
		}
		if err != nil {
			return nil, err
		}
		for _, r := range page {
			if r, ok := c.cardigannResult(d, def, base, r); ok {
				results = append(results, r)
			}
			if len(results) >= MaxResults {
				return def.andMatch(results, keywords), nil
			}
		}
	}
	return def.andMatch(results, keywords), nil
}

// templateData is what a definition's templates read: the query, the
// settings (the operator's values over the definition's defaults), the
// categories, and the keywords after their filters.
func (def *cgDef) templateData(d Definition, base *url.URL, q Query) (map[string]any, string) {
	keywords := strings.TrimSpace(q.Term)
	year, monthDay, daily := dailyDate(q.AirDate)
	switch {
	case daily:
		keywords = strings.TrimSpace(keywords + " " + strings.ReplaceAll(q.AirDate, "-", "."))
	case q.Season >= 0 && q.Episode > 0:
		keywords = strings.TrimSpace(fmt.Sprintf("%s S%02dE%02d", keywords, q.Season, q.Episode))
	case q.Season >= 0:
		keywords = strings.TrimSpace(fmt.Sprintf("%s S%02d", keywords, q.Season))
	}
	query := map[string]string{"Keywords": keywords, "Q": strings.TrimSpace(q.Term),
		"IMDBID": q.IMDBID, "IMDBIDShort": strings.TrimPrefix(q.IMDBID, "tt"), "TVDBID": q.TVDBID}
	switch {
	case daily:
		query["Season"], query["Ep"] = year, monthDay
	default:
		if q.Season >= 0 {
			query["Season"] = strconv.Itoa(q.Season)
		}
		if q.Episode > 0 {
			query["Ep"] = strconv.Itoa(q.Episode)
		}
	}
	config := map[string]string{"sitelink": base.String()}
	for k, v := range def.config {
		config[k] = v
	}
	for k, v := range d.Settings {
		if def.settingTypes[k] == "checkbox" {
			v = map[bool]string{true: "True", false: ""}[strings.EqualFold(v, "true") || v == "1"]
		}
		config[k] = v
	}
	data := map[string]any{"Query": query, "Config": config, "Categories": def.siteCategoriesFor(q.Categories),
		"Today": map[string]int{"Year": time.Now().Year()}, "True": "True", "False": ""}
	data["Keywords"] = applyCgFilters(keywords, def.Search.KeywordsFilters, data)
	return data, keywords
}

// pathFor says whether a path is asked for these site categories: a path
// limited to some is asked only for them.
func pathFor(p cgPath, cats []string) bool {
	if len(p.Categories) == 0 || len(cats) == 0 {
		return true
	}
	for _, c := range cats {
		if containsString(p.Categories, c) {
			return true
		}
	}
	return false
}

// siteCategoriesFor maps Newznab categories to the site's own, a parent
// taking every one beneath it.
func (d *cgDef) siteCategoriesFor(want []int) []string {
	var out []string
	for id, ns := range d.siteCats {
		for _, n := range ns {
			for _, w := range want {
				if n == w || (w%1000 == 0 && n/1000*1000 == w) {
					if !containsString(out, id) {
						out = append(out, id)
					}
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

func (d *cgDef) andMatch(results []Result, keywords string) []Result {
	on := false
	for _, f := range d.Search.Rows.Filters {
		on = on || f.Name == "andmatch"
	}
	words := strings.Fields(strings.ToLower(keywords))
	if !on || len(words) == 0 {
		return results
	}
	kept := results[:0]
	for _, r := range results {
		title := strings.ToLower(r.Title)
		all := true
		for _, w := range words {
			all = all && strings.Contains(title, w)
		}
		if all {
			kept = append(kept, r)
		}
	}
	return kept
}

// cardigannPage asks one search path and reads its rows into each row's
// field values.
func (c *Client) cardigannPage(ctx context.Context, d Definition, def *cgDef, base *url.URL,
	p cgPath, data map[string]any, doer Doer) ([]map[string]string, error) {

	ref, err := url.Parse(cgExec(p.Path, data))
	if err != nil {
		return nil, fmt.Errorf("indexer: the search path: %w", err)
	}
	u := base.ResolveReference(ref)
	inputs := map[string]string{}
	if p.InheritInputs == nil || *p.InheritInputs {
		for k, v := range def.Search.Inputs {
			inputs[k] = v
		}
	}
	for k, v := range p.Inputs {
		inputs[k] = v
	}
	values, raw := url.Values{}, ""
	for k, v := range inputs {
		if k == "$raw" {
			raw = cgExec(v, data)
			continue
		}
		values.Set(k, cgExec(v, data))
	}
	encoded := values.Encode()
	if raw != "" {
		encoded = strings.TrimPrefix(encoded+"&"+raw, "&")
	}

	method := http.MethodGet
	var body io.Reader
	if strings.EqualFold(p.Method, "post") {
		method, body = http.MethodPost, strings.NewReader(encoded)
	} else if encoded != "" {
		u.RawQuery = strings.TrimPrefix(u.RawQuery+"&"+encoded, "&")
	}
	if err := c.validatorFor(d)(u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("indexer: %w", err)
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/json;q=0.9,*/*;q=0.8")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	page, final, err := c.doFinal(doer, req, u)
	if err != nil {
		return nil, err
	}
	if def.Login != nil && def.Login.Path != "" {
		if login, lerr := url.Parse(cgExec(def.Login.Path, data)); lerr == nil &&
			final.Path == base.ResolveReference(login).Path {
			return nil, errSignedOut
		}
	}
	if def.Encoding != "" && !strings.EqualFold(def.Encoding, "utf-8") {
		if enc, eerr := htmlindex.Get(def.Encoding); eerr == nil {
			if decoded, _, derr := transform.Bytes(enc.NewDecoder(), page); derr == nil {
				page = decoded
			}
		}
	}
	if strings.EqualFold(p.Response.Type, "json") {
		return def.jsonRows(page, data)
	}
	return def.htmlRows(page, data)
}

// htmlRows reads every row of an HTML page.
func (d *cgDef) htmlRows(page []byte, data map[string]any) ([]map[string]string, error) {
	doc, err := xhtml.Parse(bytes.NewReader(page))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	rowSel, err := parseSelector(d.Search.Rows.Selector)
	if err != nil {
		return nil, err
	}
	var out []map[string]string
	for _, row := range rowSel.selectAll(doc, MaxResults) {
		group := []*xhtml.Node{row}
		for s, i := row.NextSibling, 0; s != nil && i < d.Search.Rows.After; s = s.NextSibling {
			if s.Type == xhtml.ElementNode {
				group = append(group, s)
				i++
			}
		}
		if vals, ok := d.rowValues(data, func(f cgNamedField) (string, bool) { return htmlValue(f, group) }); ok {
			out = append(out, vals)
		}
	}
	return out, nil
}

// htmlValue is a field's value in a row: its selector's first match across
// the row and the rows merged into it, or the row itself.
func htmlValue(f cgNamedField, group []*xhtml.Node) (string, bool) {
	find := func(selector string) *xhtml.Node {
		if selector == "" {
			return group[0]
		}
		sel, err := parseSelector(selector)
		if err != nil {
			return nil
		}
		for _, n := range group {
			if m := sel.selectFirst(n); m != nil {
				return m
			}
		}
		return nil
	}
	if len(f.cases) > 0 {
		for _, cs := range f.cases {
			if cs[0] == "*" || find(cs[0]) != nil {
				return cs[1], true
			}
		}
		return "", false
	}
	n := find(f.Selector)
	if n == nil {
		return "", false
	}
	if f.Attribute != "" {
		return attrOf(n, f.Attribute)
	}
	var skip map[*xhtml.Node]bool
	if f.Remove != "" {
		if sel, err := parseSelector(f.Remove); err == nil {
			skip = map[*xhtml.Node]bool{}
			for _, r := range sel.selectAll(n, MaxResults) {
				skip[r] = true
			}
		}
	}
	return nodeText(n, skip), true
}

// jsonRows reads every row of a JSON response: the array at the rows path,
// each element's array at rows.attribute when it holds several, whose fields
// may read their parent's with "..".
func (d *cgDef) jsonRows(page []byte, data map[string]any) ([]map[string]string, error) {
	var doc any
	if err := json.Unmarshal(page, &doc); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	type jrow struct{ row, parent any }
	var rows []jrow
	list, _ := jsonPath(doc, d.Search.Rows.Selector).([]any)
	for _, r := range list {
		if d.Search.Rows.Attribute == "" {
			rows = append(rows, jrow{row: r})
			continue
		}
		sub, _ := jsonPath(r, d.Search.Rows.Attribute).([]any)
		for _, s := range sub {
			rows = append(rows, jrow{row: s, parent: r})
		}
	}
	var out []map[string]string
	for _, r := range rows {
		if len(out) >= MaxResults {
			break
		}
		vals, ok := d.rowValues(data, func(f cgNamedField) (string, bool) {
			from, path := r.row, f.Selector
			if strings.HasPrefix(path, "..") {
				from, path = r.parent, strings.TrimPrefix(path, "..")
			}
			if len(f.cases) > 0 {
				v := jsonString(jsonPath(from, path))
				for _, cs := range f.cases {
					if cs[0] == "*" || cs[0] == v {
						return cs[1], true
					}
				}
				return "", false
			}
			v := jsonPath(from, path)
			if v == nil {
				return "", false
			}
			return jsonString(v), true
		})
		if ok {
			out = append(out, vals)
		}
	}
	return out, nil
}

// jsonPath follows a dotted path ("data.movies", "$.items") into a decoded
// document.
func jsonPath(v any, path string) any {
	path = strings.TrimPrefix(strings.TrimPrefix(path, "$"), ".")
	if path == "" {
		return v
	}
	for _, key := range strings.Split(path, ".") {
		switch t := v.(type) {
		case map[string]any:
			v = t[key]
		case []any:
			i, err := strconv.Atoi(key)
			if err != nil || i < 0 || i >= len(t) {
				return nil
			}
			v = t[i]
		default:
			return nil
		}
	}
	return v
}

func jsonString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "True"
		}
		return "False"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// rowValues works out a row's fields in the definition's order. A missing
// field that is neither optional nor defaulted drops the row, as it does in
// Jackett.
func (d *cgDef) rowValues(data map[string]any, value func(cgNamedField) (string, bool)) (map[string]string, bool) {
	result := map[string]string{}
	vars := map[string]any{}
	for k, v := range data {
		vars[k] = v
	}
	vars["Result"] = result
	for _, f := range d.fields {
		var v string
		switch {
		case f.Text != nil:
			v = cgExec(*f.Text, vars)
		default:
			got, ok := value(f)
			switch {
			case ok:
				v = got
			case f.Default != nil:
				result[f.name] = cgExec(*f.Default, vars)
				continue
			case f.Optional:
				result[f.name] = ""
				continue
			default:
				return nil, false
			}
		}
		result[f.name] = strings.TrimSpace(applyCgFilters(v, f.Filters, vars))
	}
	return result, true
}

// cardigannResult turns a row's fields into a result, dropping a row without
// a title or a usable link.
func (c *Client) cardigannResult(d Definition, def *cgDef, base *url.URL, f map[string]string) (Result, bool) {
	title := strings.TrimSpace(f["title"])
	if title == "" {
		return Result{}, false
	}
	if len(title) > MaxTitleBytes {
		title = title[:MaxTitleBytes]
	}
	resolve := func(s string) string {
		s = strings.TrimSpace(s)
		if s == "" || isMagnet(s) {
			return s
		}
		ref, err := url.Parse(s)
		if err != nil {
			return ""
		}
		return base.ResolveReference(ref).String()
	}
	download := resolve(f["download"])
	if download == "" {
		download = resolve(f["magnet"])
	}
	if download == "" || !acceptableDownloadLink(c.validatorFor(d), download) {
		return Result{}, false
	}
	details := resolve(f["details"])
	if details == "" {
		details = resolve(f["comments"])
	}
	r := Result{
		Title:       title,
		GUID:        details,
		DownloadURL: download,
		InfoURL:     details,
		InfoHash:    sanitiseInfoHash(f["infohash"]),
		Size:        parseHumanSize(f["size"]),
		PublishedAt: parseCgDate(f["date"], time.Now()),
		Seeders:     atoiClamped(strings.ReplaceAll(f["seeders"], ",", "")),
		Leechers:    atoiClamped(strings.ReplaceAll(f["leechers"], ",", "")),
		Grabs:       atoiClamped(strings.ReplaceAll(f["grabs"], ",", "")),
		IndexerID:   d.ID,
		IndexerName: d.Name,
		Parsed:      release.Parse(title),
	}
	if r.GUID == "" {
		r.GUID = download
	}
	r.Categories = append(r.Categories, def.siteCats[strings.TrimSpace(f["category"])]...)
	return r, true
}

// ---------------------------------------------------------------------------
// Filters
// ---------------------------------------------------------------------------

func filterArgs(f cgFilter) []string {
	switch f.Args.Kind {
	case yaml.ScalarNode:
		return []string{f.Args.Value}
	case yaml.SequenceNode:
		out := make([]string, len(f.Args.Content))
		for i, n := range f.Args.Content {
			out[i] = n.Value
		}
		return out
	}
	return nil
}

func applyCgFilters(v string, fs []cgFilter, data map[string]any) string {
	for _, f := range fs {
		args := filterArgs(f)
		arg := func(i int) string {
			if i < len(args) {
				return args[i]
			}
			return ""
		}
		switch f.Name {
		case "querystring":
			if u, err := url.Parse(strings.TrimSpace(v)); err == nil {
				v = u.Query().Get(arg(0))
			} else {
				v = ""
			}
		case "regexp":
			out := ""
			if re, err := regexp.Compile(arg(0)); err == nil {
				if m := re.FindStringSubmatch(v); m != nil {
					out = m[0]
					if len(m) > 1 {
						out = m[1]
					}
				}
			}
			v = out
		case "re_replace":
			if re, err := regexp.Compile(arg(0)); err == nil {
				v = re.ReplaceAllString(v, arg(1))
			}
		case "replace":
			v = strings.ReplaceAll(v, arg(0), arg(1))
		case "split":
			parts := strings.Split(v, arg(0))
			i, _ := strconv.Atoi(arg(1))
			if i < 0 {
				i += len(parts)
			}
			v = ""
			if i >= 0 && i < len(parts) {
				v = parts[i]
			}
		case "trim":
			if cut := arg(0); cut != "" {
				v = strings.Trim(v, cut)
			} else {
				v = strings.TrimSpace(v)
			}
		case "prepend":
			v = cgExec(arg(0), data) + v
		case "append":
			v += cgExec(arg(0), data)
		case "tolower":
			v = strings.ToLower(v)
		case "toupper":
			v = strings.ToUpper(v)
		case "urldecode":
			if s, err := url.QueryUnescape(v); err == nil {
				v = s
			}
		case "urlencode":
			v = url.QueryEscape(v)
		case "htmldecode":
			v = html.UnescapeString(v)
		case "diacritics":
			if s, _, err := transform.String(transform.Chain(norm.NFD,
				runes.Remove(runes.In(unicode.Mn)), norm.NFC), v); err == nil {
				v = s
			}
		case "dateparse":
			if t, err := time.Parse(arg(0), strings.TrimSpace(v)); err == nil {
				v = t.Format(time.RFC3339)
			}
		case "timeago", "fuzzytime":
			if t, ok := parseTimeAgo(v, time.Now()); ok {
				v = t.Format(time.RFC3339)
			}
		}
	}
	return v
}

var reTimeAgo = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(sec|second|min|minute|hour|hr|h|day|d|week|w|month|mo|year|yr|y)s?\b`)

// parseTimeAgo reads "3 hours ago", "1 day, 2 hours ago", "yesterday",
// "today" and "now".
func parseTimeAgo(s string, now time.Time) (time.Time, bool) {
	l := strings.ToLower(strings.TrimSpace(s))
	switch {
	case l == "now" || l == "just now":
		return now, true
	case strings.HasPrefix(l, "today"):
		return now, true
	case strings.HasPrefix(l, "yesterday"):
		return now.Add(-24 * time.Hour), true
	}
	units := map[string]time.Duration{
		"sec": time.Second, "second": time.Second, "min": time.Minute, "minute": time.Minute,
		"hour": time.Hour, "hr": time.Hour, "h": time.Hour, "day": 24 * time.Hour, "d": 24 * time.Hour,
		"week": 7 * 24 * time.Hour, "w": 7 * 24 * time.Hour, "month": 30 * 24 * time.Hour,
		"mo": 30 * 24 * time.Hour, "year": 365 * 24 * time.Hour, "yr": 365 * 24 * time.Hour,
		"y": 365 * 24 * time.Hour,
	}
	var total time.Duration
	matched := false
	for _, m := range reTimeAgo.FindAllStringSubmatch(l, -1) {
		n, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			continue
		}
		total += time.Duration(n * float64(units[strings.ToLower(m[2])]))
		matched = true
	}
	return now.Add(-total), matched
}

// parseCgDate reads a row's date: what dateparse or timeago left, a feed's
// shapes, or Unix seconds. Unreadable is zero, never now.
func parseCgDate(s string, now time.Time) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	if t := parsePubDate(s); !t.IsZero() {
		return t
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
		return time.Unix(n, 0).UTC()
	}
	if t, ok := parseTimeAgo(s, now); ok {
		return t.UTC()
	}
	return time.Time{}
}

var reHumanSize = regexp.MustCompile(`(?i)([\d.,]+)\s*([kmgtp]?i?b|[kmgtp])?\b`)

// parseHumanSize reads "1.4 GB", "700 MiB", "1,234 MB" or plain bytes, in
// binary units as Jackett reads them.
func parseHumanSize(s string) int64 {
	m := reHumanSize.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0
	}
	num := m[1]
	switch {
	case strings.Contains(num, ".") && strings.Contains(num, ","):
		num = strings.ReplaceAll(num, ",", "")
	case strings.Count(num, ",") == 1 && len(num)-strings.Index(num, ",") <= 3:
		num = strings.Replace(num, ",", ".", 1)
	default:
		num = strings.ReplaceAll(num, ",", "")
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil || f < 0 {
		return 0
	}
	mult := float64(1)
	if unit := strings.ToLower(m[2]); unit != "" {
		switch unit[0] {
		case 'k':
			mult = 1 << 10
		case 'm':
			mult = 1 << 20
		case 'g':
			mult = 1 << 30
		case 't':
			mult = 1 << 40
		case 'p':
			mult = 1 << 50
		}
	}
	const ceiling = float64(1 << 53)
	if v := f * mult; v < ceiling {
		return int64(v)
	}
	return int64(ceiling)
}

// ---------------------------------------------------------------------------
// Categories
// ---------------------------------------------------------------------------

// newznabCategories is the Newznab category table the definitions' caps name.
var newznabCategories = map[string]int{
	"console": 1000, "console/nds": 1010, "console/psp": 1020, "console/wii": 1030, "console/xbox": 1040,
	"console/xbox 360": 1050, "console/wiiware": 1060, "console/xbox 360 dlc": 1070, "console/ps3": 1080,
	"console/other": 1090, "console/3ds": 1110, "console/ps vita": 1120, "console/wiiu": 1130,
	"console/xbox one": 1140, "console/ps4": 1180,
	"movies": 2000, "movies/foreign": 2010, "movies/other": 2020, "movies/sd": 2030, "movies/hd": 2040,
	"movies/uhd": 2045, "movies/bluray": 2050, "movies/3d": 2060, "movies/dvd": 2070, "movies/web-dl": 2080,
	"audio": 3000, "audio/mp3": 3010, "audio/video": 3020, "audio/audiobook": 3030, "audio/lossless": 3040,
	"audio/other": 3050, "audio/foreign": 3060,
	"pc": 4000, "pc/0day": 4010, "pc/iso": 4020, "pc/mac": 4030, "pc/mobile-other": 4040, "pc/games": 4050,
	"pc/mobile-ios": 4060, "pc/mobile-android": 4070,
	"tv": 5000, "tv/web-dl": 5010, "tv/foreign": 5020, "tv/sd": 5030, "tv/hd": 5040, "tv/uhd": 5045,
	"tv/other": 5050, "tv/sport": 5060, "tv/anime": 5070, "tv/documentary": 5080,
	"xxx": 6000, "xxx/dvd": 6010, "xxx/wmv": 6020, "xxx/xvid": 6030, "xxx/x264": 6040, "xxx/uhd": 6045,
	"xxx/pack": 6050, "xxx/imageset": 6060, "xxx/other": 6070, "xxx/sd": 6080, "xxx/web-dl": 6090,
	"books": 7000, "books/mags": 7010, "books/ebook": 7020, "books/comics": 7030, "books/technical": 7040,
	"books/other": 7050, "books/foreign": 7060,
	"other": 8000, "other/misc": 8010, "other/hashed": 8020,
}

func newznabCategory(name string) (int, bool) {
	n, ok := newznabCategories[strings.ToLower(strings.TrimSpace(name))]
	return n, ok
}

// ---------------------------------------------------------------------------
// Signing in (ADR-0059)
// ---------------------------------------------------------------------------

var errSignedOut = errors.New("indexer: the tracker sent the search to its sign-in page")

// checkSettings refuses a value for a setting the definition does not
// declare (ADR-0059, decision 1).
func checkSettings(def *cgDef, settings map[string]string) error {
	var unknown []string
	for k := range settings {
		if t, ok := def.settingTypes[k]; !ok || strings.HasPrefix(t, "info") {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("indexer: the definition declares no setting %s", strings.Join(unknown, ", "))
	}
	return nil
}

// cgSession is one indexer's signed-in session: its cookies, in memory only
// (ADR-0059, decision 4).
type cgSession struct {
	mu       sync.Mutex
	key      [32]byte
	doer     Doer
	jar      http.CookieJar
	signedIn bool
}

// session is the indexer's session, started afresh when its address,
// definition or settings changed.
func (c *Client) session(d Definition) *cgSession {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\x00%s", d.BaseURL, d.Cardigann)
	names := make([]string, 0, len(d.Settings))
	for k := range d.Settings {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		_, _ = fmt.Fprintf(h, "\x00%s=%s", k, d.Settings[k])
	}
	var key [32]byte
	copy(key[:], h.Sum(nil))

	// Asked before the lock: doerFor takes it too. Found on the running
	// binary, where a guard is wired; a test client has none.
	doer := c.doerFor(d)
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.sessions[d.ID]; ok && s.key == key {
		return s
	}
	s := &cgSession{key: key, doer: doer}
	if hc, ok := s.doer.(*http.Client); ok {
		jar, _ := cookiejar.New(nil)
		withJar := *hc
		withJar.Jar = jar
		s.doer, s.jar = &withJar, jar
	}
	if c.sessions == nil {
		c.sessions = map[int64]*cgSession{}
	}
	c.sessions[d.ID] = s
	return s
}

// cardigannDoer is what fetches a link of this indexer: the signed-in
// session for a tracker that signs in, since its download links need it.
func (c *Client) cardigannDoer(ctx context.Context, d Definition) (Doer, error) {
	def, err := parseCardigann(d.Cardigann)
	if err != nil || def.Login == nil {
		return c.doerFor(d), nil //nolint:nilerr // a definition that will not read has no session; the fetch says what is wrong
	}
	base, err := url.Parse(strings.TrimSpace(d.BaseURL))
	if err != nil {
		return nil, fmt.Errorf("indexer: %w", err)
	}
	if !strings.HasSuffix(base.Path, "/") {
		base.Path += "/"
	}
	s := c.session(d)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.signedIn {
		data, _ := def.templateData(d, base, Query{Season: -1})
		if err := c.signIn(ctx, d, def, s, base, data); err != nil {
			return nil, err
		}
	}
	return s.doer, nil
}

// sameOrigin is whether a URL is at the base address's scheme, host and
// port: the only place the operator's values are sent (ADR-0059, decision 3).
func sameOrigin(u, base *url.URL) bool {
	return strings.EqualFold(u.Scheme, base.Scheme) && strings.EqualFold(u.Host, base.Host)
}

// signIn signs the session in as the definition says, then checks the
// definition's error selectors and its test page.
func (c *Client) signIn(ctx context.Context, d Definition, def *cgDef, s *cgSession,
	base *url.URL, data map[string]any) error {

	l := def.Login
	resolve := func(path string) (*url.URL, error) {
		ref, err := url.Parse(cgExec(path, data))
		if err != nil {
			return nil, fmt.Errorf("indexer: the sign-in path: %w", err)
		}
		u := base.ResolveReference(ref)
		if !sameOrigin(u, base) {
			return nil, fmt.Errorf("%w: signing in at %s, which is not the tracker's own address", ErrUnsafeURL, u.Host)
		}
		if err := c.validatorFor(d)(u); err != nil {
			return nil, err
		}
		return u, nil
	}
	send := func(method string, u *url.URL, form url.Values) ([]byte, *url.URL, error) {
		var body io.Reader
		if method == http.MethodPost {
			body = strings.NewReader(form.Encode())
		} else if len(form) > 0 {
			u.RawQuery = strings.TrimPrefix(u.RawQuery+"&"+form.Encode(), "&")
		}
		req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
		if err != nil {
			return nil, nil, fmt.Errorf("indexer: %w", err)
		}
		req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
		if method == http.MethodPost {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		return c.doFinal(s.doer, req, u)
	}
	inputs := func() url.Values {
		v := url.Values{}
		for k, t := range l.Inputs {
			v.Set(k, cgExec(t, data))
		}
		return v
	}
	setCookies := func(header string) {
		if s.jar == nil {
			return
		}
		var cookies []*http.Cookie
		for _, part := range strings.Split(header, ";") {
			if k, v, ok := strings.Cut(strings.TrimSpace(part), "="); ok && k != "" {
				// A cookie this client sends to the tracker, never one it serves: Secure,
				// HttpOnly and SameSite are a server's instructions to a browser.
				cookies = append(cookies, &http.Cookie{Name: strings.TrimSpace(k), Value: strings.TrimSpace(v)}) // #nosec G124 -- sent, not served
			}
		}
		s.jar.SetCookies(base, cookies)
	}

	var page []byte
	var err error
	switch strings.ToLower(l.Method) {
	case "cookie":
		cfg, _ := data["Config"].(map[string]string)
		setCookies(cfg["cookie"])
	case "get":
		u, rerr := resolve(l.Path)
		if rerr != nil {
			return rerr
		}
		page, _, err = send(http.MethodGet, u, inputs())
	case "form":
		u, rerr := resolve(l.Path)
		if rerr != nil {
			return rerr
		}
		loginPage, final, ferr := send(http.MethodGet, u, nil)
		if ferr != nil {
			return ferr
		}
		page, err = submitLoginForm(def, final, loginPage, inputs(), resolve, send)
	default: // post
		u, rerr := resolve(l.Path)
		if rerr != nil {
			return rerr
		}
		page, _, err = send(http.MethodPost, u, inputs())
	}
	if err != nil {
		return fmt.Errorf("indexer: signing in: %w", err)
	}
	for _, ck := range l.Cookies {
		setCookies(ck)
	}
	if page != nil {
		if doc, perr := xhtml.Parse(bytes.NewReader(page)); perr == nil {
			for _, e := range l.Error {
				sel, serr := parseSelector(e.Selector)
				if serr != nil {
					continue
				}
				if n := sel.selectFirst(doc); n != nil {
					msg := strings.TrimSpace(nodeText(n, nil))
					if e.Message.Text != "" {
						msg = e.Message.Text
					} else if ms, merr := parseSelector(e.Message.Selector); merr == nil && e.Message.Selector != "" {
						if m := ms.selectFirst(doc); m != nil {
							msg = strings.TrimSpace(nodeText(m, nil))
						}
					}
					if len(msg) > 200 {
						msg = msg[:200]
					}
					return fmt.Errorf("indexer: signing in was refused: %s", msg)
				}
			}
		}
	}
	if l.Test.Path != "" {
		u, rerr := resolve(l.Test.Path)
		if rerr != nil {
			return rerr
		}
		testPage, _, terr := send(http.MethodGet, u, nil)
		if terr != nil {
			return fmt.Errorf("indexer: checking the sign-in: %w", terr)
		}
		if l.Test.Selector != "" {
			sel, serr := parseSelector(l.Test.Selector)
			doc, perr := xhtml.Parse(bytes.NewReader(testPage))
			if serr != nil || perr != nil || sel.selectFirst(doc) == nil {
				return errors.New("indexer: signing in did not work: the tracker's test page shows no sign of it")
			}
		}
	}
	s.signedIn = true
	return nil
}

// submitLoginForm fills in and submits a login page's form: the form's own
// fields, the definition's selector inputs read from the page, and its
// inputs over them, to the form's action on the tracker's own address.
func submitLoginForm(def *cgDef, pageURL *url.URL, page []byte, inputs url.Values,
	resolve func(string) (*url.URL, error),
	send func(string, *url.URL, url.Values) ([]byte, *url.URL, error)) ([]byte, error) {

	l := def.Login
	doc, err := xhtml.Parse(bytes.NewReader(page))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	formSel := l.Form
	if formSel == "" {
		formSel = "form"
	}
	sel, err := parseSelector(formSel)
	if err != nil {
		return nil, err
	}
	form := sel.selectFirst(doc)
	if form == nil {
		return nil, fmt.Errorf("the sign-in page has no form matching %q", formSel)
	}
	values := url.Values{}
	fieldSel, _ := parseSelector("input[name], select[name], textarea[name]")
	for _, f := range fieldSel.selectAll(form, MaxResults) {
		name, _ := attrOf(f, "name")
		typ, _ := attrOf(f, "type")
		switch strings.ToLower(typ) {
		case "submit", "button", "image", "file", "reset":
			continue
		case "checkbox", "radio":
			if _, checked := attrOf(f, "checked"); !checked {
				continue
			}
		}
		v, _ := attrOf(f, "value")
		values.Set(name, v)
	}
	for k, si := range l.SelectorInputs {
		ss, serr := parseSelector(si.Selector)
		if serr != nil {
			continue
		}
		if n := ss.selectFirst(doc); n != nil {
			if si.Attribute != "" {
				v, _ := attrOf(n, si.Attribute)
				values.Set(k, v)
			} else {
				values.Set(k, strings.TrimSpace(nodeText(n, nil)))
			}
		}
	}
	for k, v := range inputs {
		values[k] = v
	}

	action, _ := attrOf(form, "action")
	if l.SubmitPath != "" {
		action = l.SubmitPath
	}
	target := pageURL
	if strings.TrimSpace(action) != "" {
		ref, perr := url.Parse(strings.TrimSpace(action))
		if perr != nil {
			return nil, fmt.Errorf("the sign-in form's action: %w", perr)
		}
		target = pageURL.ResolveReference(ref)
	}
	// The action is the page's, so it is checked as a path of the tracker's
	// own: the same origin, the same URL rule.
	checked, err := resolve(target.String())
	if err != nil {
		return nil, err
	}
	method := http.MethodPost
	if m, _ := attrOf(form, "method"); strings.EqualFold(m, "get") {
		method = http.MethodGet
	}
	out, _, err := send(method, checked, values)
	return out, err
}

// ---------------------------------------------------------------------------
// The details page (ADR-0060)
// ---------------------------------------------------------------------------

// cardigannLink is the link a grab fetches: the result's own, or, for a
// definition with a download block, the one its selectors find on the
// details page the result links to.
func (c *Client) cardigannLink(ctx context.Context, d Definition, rawURL string) (string, error) {
	def, err := parseCardigann(d.Cardigann)
	if err != nil || def.Download == nil || isMagnet(rawURL) {
		return rawURL, nil //nolint:nilerr // without a definition to follow, the link is fetched as it is and the fetch says what is wrong
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("%w: unparseable", ErrUnsafeURL)
	}
	if err := c.validatorFor(d)(u); err != nil {
		return "", err
	}
	doer, err := c.cardigannDoer(ctx, d)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", fmt.Errorf("indexer: %w", err)
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	page, final, err := c.doFinal(doer, req, u)
	if err != nil {
		return "", err
	}
	doc, err := xhtml.Parse(bytes.NewReader(page))
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	data, _ := def.templateData(d, u, Query{Season: -1})
	for _, s := range def.Download.Selectors {
		sel, serr := parseSelector(s.Selector)
		if serr != nil {
			continue
		}
		n := sel.selectFirst(doc)
		if n == nil {
			continue
		}
		v := strings.TrimSpace(nodeText(n, nil))
		if s.Attribute != "" {
			v, _ = attrOf(n, s.Attribute)
		}
		link := strings.TrimSpace(applyCgFilters(v, s.Filters, data))
		if !isMagnet(link) {
			ref, perr := url.Parse(link)
			if perr != nil {
				continue
			}
			link = final.ResolveReference(ref).String()
		}
		// Checked by the download it goes on to, as any link is (ADR-0060).
		return link, nil
	}
	return "", fmt.Errorf("%w: the details page has no download link its definition can find", ErrIndexerRefused)
}

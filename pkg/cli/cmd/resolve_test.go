package cmd

// Chromium-free table-driven pins for the WEB-61 positional-arg parsing and
// page-id resolution helpers: parseFetchArgs, parseActArgs and
// resolvePageIDFromPages. No browser, no serve daemon, no network — the
// resolution rules are pure functions over a synthetic []browsers.PageInfo.

import (
	"reflect"
	"strings"
	"testing"

	"webmcp/pkg/browsers"
	"webmcp/pkg/cli/ipc"
)

// TestSelectBrowserEntry pins the daemon-side selector: the pinned id picks its
// own entry, and an unknown id fails with the sorted live id list — never with
// the default browser's pages.
func TestSelectBrowserEntry(t *testing.T) {
	entries := []ipc.BrowserEntry{
		{BrowserID: "browser-2", Pages: []browsers.PageInfo{{ID: "page-2", URL: "https://b.com"}}},
		{BrowserID: "browser-1", Pages: []browsers.PageInfo{{ID: "page-1", URL: "https://a.com"}}},
	}

	got, err := selectBrowserEntry(entries, "browser-2", "page list")
	if err != nil {
		t.Fatalf("selectBrowserEntry(browser-2) error = %v, want nil", err)
	}
	if got.BrowserID != "browser-2" || len(got.Pages) != 1 || got.Pages[0].ID != "page-2" {
		t.Errorf("selectBrowserEntry(browser-2) = %+v, want browser-2's page", got)
	}

	_, err = selectBrowserEntry(entries, "browser-9", "page fetch")
	if err == nil {
		t.Fatal("selectBrowserEntry(browser-9) = nil error, want unknown-browser error")
	}
	want := `page fetch: browser "browser-9" not found (running: browser-1, browser-2)`
	if err.Error() != want {
		t.Errorf("selectBrowserEntry(browser-9) error = %q, want %q", err.Error(), want)
	}
}

func TestParseFetchArgs(t *testing.T) {
	tests := []struct {
		name             string
		args             []string
		idFlag, whatFlag string
		wantID           string
		wantWhat         string
		wantURL          string
	}{
		{"no args no flags defaults", nil, "", "", "", "text", ""},
		{"no args, what flag", nil, "", "html", "", "html", ""},
		{"no args, id flag", nil, "page-1", "", "page-1", "text", ""},
		{"1 arg aspect is what", []string{"links"}, "", "", "", "links", ""},
		{"1 arg aspect, what flag loses", []string{"dom"}, "", "html", "", "dom", ""},
		{"1 arg digits is id", []string{"12"}, "", "", "12", "text", ""},
		{"1 arg literal id", []string{"page-1"}, "", "", "page-1", "text", ""},
		{"1 arg non-aspect is id", []string{"bogus"}, "", "", "bogus", "text", ""},
		{"1 arg url is url", []string{"https://x.com"}, "", "", "", "text", "https://x.com"},
		{"1 arg url, what flag", []string{"https://x.com"}, "", "html", "", "html", "https://x.com"},
		{"1 arg url beats id flag", []string{"https://x.com"}, "page-1", "", "", "text", "https://x.com"},
		{"2 args id what", []string{"12", "dom"}, "", "", "12", "dom", ""},
		{"2 args beat both flags", []string{"page-2", "meta"}, "page-1", "links", "page-2", "meta", ""},
		{"2 args what flag fallback", []string{"12", "links"}, "", "text", "12", "links", ""},
		{"2 args url what", []string{"https://x.com", "links"}, "", "", "", "links", "https://x.com"},
		{"2 args url what beat flags", []string{"https://x.com", "links"}, "page-1", "html", "", "links", "https://x.com"},
		{"2 args non-url id what", []string{"bogus", "links"}, "", "", "bogus", "links", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotID, gotWhat, gotURL := parseFetchArgs(tt.args, tt.idFlag, tt.whatFlag)
			if gotID != tt.wantID {
				t.Errorf("parseFetchArgs(%v, %q, %q) id = %q, want %q", tt.args, tt.idFlag, tt.whatFlag, gotID, tt.wantID)
			}
			if gotWhat != tt.wantWhat {
				t.Errorf("parseFetchArgs(%v, %q, %q) what = %q, want %q", tt.args, tt.idFlag, tt.whatFlag, gotWhat, tt.wantWhat)
			}
			if gotURL != tt.wantURL {
				t.Errorf("parseFetchArgs(%v, %q, %q) url = %q, want %q", tt.args, tt.idFlag, tt.whatFlag, gotURL, tt.wantURL)
			}
		})
	}
}

// TestIsURLLike pins the URL-slot detector of the page fetch positional
// grammar: any token containing "://" is a URL, everything else (page ids,
// bare numbers, 'last', aspect words) is not. Page ids can never contain
// "://", so the test is unambiguous.
func TestIsURLLike(t *testing.T) {
	tests := []struct {
		s    string
		want bool
	}{
		{"https://x.com", true},
		{"http://a.b/c", true},
		{"ftp://x", true},
		{"page-1", false},
		{"12", false},
		{"last", false},
		{"links", false},
		{"bogus", false},
	}
	for _, tt := range tests {
		if got := isURLLike(tt.s); got != tt.want {
			t.Errorf("isURLLike(%q) = %v, want %v", tt.s, got, tt.want)
		}
	}
}

func TestParseActArgs(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		flags actArgFlags
		want  actArgs
	}{
		{
			"click with selector positional",
			[]string{"click", "#submit"},
			actArgFlags{},
			actArgs{action: "click", selector: "#submit"},
		},
		{
			"id + input payload",
			[]string{"12", "input", "#search", "pizza"},
			actArgFlags{},
			actArgs{id: "12", action: "input", selector: "#search", value: "pizza"},
		},
		{
			"id + navigate url",
			[]string{"12", "navigate", "https://news.ycombinator.com"},
			actArgFlags{},
			actArgs{id: "12", action: "navigate", value: "https://news.ycombinator.com"},
		},
		{
			"last + eval js",
			[]string{"last", "eval", "() => document.title"},
			actArgFlags{},
			actArgs{id: "last", action: "eval", value: "() => document.title"},
		},
		{
			"scroll x y",
			[]string{"12", "scroll", "0", "500"},
			actArgFlags{},
			actArgs{id: "12", action: "scroll", x: 0, y: 500},
		},
		{
			"wait mode positional",
			[]string{"12", "wait", "stable"},
			actArgFlags{},
			actArgs{id: "12", action: "wait", waitMode: "stable"},
		},
		{
			"wait without mode falls back to flag default",
			[]string{"12", "wait"},
			actArgFlags{waitMode: "ms"},
			actArgs{id: "12", action: "wait", waitMode: "ms"},
		},
		{
			"set_viewport w h",
			[]string{"12", "set_viewport", "800", "600"},
			actArgFlags{},
			actArgs{id: "12", action: "set_viewport", width: 800, height: 600},
		},
		{
			"emulate device",
			[]string{"12", "emulate", "IPhoneX"},
			actArgFlags{},
			actArgs{id: "12", action: "emulate", device: "IPhoneX"},
		},
		{
			"back takes no payload",
			[]string{"back"},
			actArgFlags{},
			actArgs{action: "back"},
		},
		{
			"reload takes no payload",
			[]string{"12", "reload"},
			actArgFlags{},
			actArgs{id: "12", action: "reload"},
		},
		{
			"flag-only invocation",
			nil,
			actArgFlags{id: "page-1", action: "click", selector: "#x"},
			actArgs{id: "page-1", action: "click", selector: "#x"},
		},
		{
			"positional action + flag id",
			[]string{"click", "#y"},
			actArgFlags{id: "page-1"},
			actArgs{id: "page-1", action: "click", selector: "#y"},
		},
		{
			"positionals beat all flags",
			[]string{"12", "input", "#a", "b"},
			actArgFlags{id: "page-9", action: "click", selector: "#z", value: "zz"},
			actArgs{id: "12", action: "input", selector: "#a", value: "b"},
		},
		{
			"scroll flags fall back",
			[]string{"12", "scroll"},
			actArgFlags{x: 10, y: 20},
			actArgs{id: "12", action: "scroll", x: 10, y: 20},
		},
		{
			"select payload",
			[]string{"12", "select", "#lang", "go"},
			actArgFlags{},
			actArgs{id: "12", action: "select", selector: "#lang", value: "go"},
		},
		{
			"hover payload",
			[]string{"12", "hover", "nav a"},
			actArgFlags{},
			actArgs{id: "12", action: "hover", selector: "nav a"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseActArgs(tt.args, tt.flags)
			if err != nil {
				t.Fatalf("parseActArgs(%v, %+v) error = %v, want nil", tt.args, tt.flags, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseActArgs(%v, %+v) = %+v, want %+v", tt.args, tt.flags, got, tt.want)
			}
		})
	}
}

func TestParseActArgsErrors(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			"click extra payload",
			[]string{"click", "a", "b"},
			`page act: action "click" takes at most 1 positional arg ([selector]); got 2`,
		},
		{
			"select extra payload",
			[]string{"12", "select", "a", "b", "c"},
			`page act: action "select" takes at most 2 positional args ([selector] [value]); got 3`,
		},
		{
			"reload no payload allowed",
			[]string{"reload", "x"},
			`page act: action "reload" takes no positional args; got 1`,
		},
		{
			"scroll partial payload",
			[]string{"12", "scroll", "5"},
			`page act: scroll requires both x and y; got 1 positional arg`,
		},
		{
			"set_viewport partial payload",
			[]string{"12", "set_viewport", "800"},
			`page act: set_viewport requires both width and height; got 1 positional arg`,
		},
		{
			"scroll non-numeric x",
			[]string{"12", "scroll", "abc", "500"},
			`page act: scroll x must be a number, got "abc"`,
		},
		{
			"scroll non-numeric y",
			[]string{"12", "scroll", "1", "abc"},
			`page act: scroll y must be a number, got "abc"`,
		},
		{
			"set_viewport non-integer width",
			[]string{"12", "set_viewport", "abc", "500"},
			`page act: set_viewport width must be an integer, got "abc"`,
		},
		{
			"set_viewport non-integer height",
			[]string{"12", "set_viewport", "800", "abc"},
			`page act: set_viewport height must be an integer, got "abc"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseActArgs(tt.args, actArgFlags{})
			if err == nil {
				t.Fatalf("parseActArgs(%v) = nil error, want %q", tt.args, tt.wantErr)
			}
			if err.Error() != tt.wantErr {
				t.Errorf("parseActArgs(%v) error = %q, want %q", tt.args, err.Error(), tt.wantErr)
			}
		})
	}
}

func TestResolvePageIDFromPages(t *testing.T) {
	one := []browsers.PageInfo{{ID: "page-1", URL: "https://a.com"}}
	two := []browsers.PageInfo{
		{ID: "page-1", URL: "https://a.com"},
		{ID: "page-2", URL: "https://b.com"},
	}

	tests := []struct {
		name    string
		raw     string
		pages   []browsers.PageInfo
		label   string
		want    string
		wantErr string
	}{
		{"single page default", "", one, "page fetch", "page-1", ""},
		{"zero pages default", "", nil, "page fetch", "", `page fetch: no open pages — open one with 'web-cli fetch <url>' (or 'web-cli page fetch <url>' to open and read in one step)`},
		{"zero pages literal", "page-1", nil, "page fetch", "", `page fetch: no open pages — open one with 'web-cli fetch <url>' (or 'web-cli page fetch <url>' to open and read in one step)`},
		{"zero pages last", "last", nil, "page act", "", `page act: no open pages — open one with 'web-cli fetch <url>' (or 'web-cli page fetch <url>' to open and read in one step)`},
		{
			"multi page default lists them",
			"", two, "page fetch", "",
			"page fetch: multiple pages open — pass an explicit page id or 'last':\n" +
				"  page-1\thttps://a.com\n" +
				"  page-2\thttps://b.com",
		},
		{
			"last picks highest numeric suffix",
			"last",
			[]browsers.PageInfo{
				{ID: "page-1", URL: "https://a.com"},
				{ID: "page-3", URL: "https://c.com"},
				{ID: "page-2", URL: "https://b.com"},
			},
			"page act", "page-3", "",
		},
		{"last is case-insensitive", "Last", two, "page act", "page-2", ""},
		{"last single page", "last", one, "page close", "page-1", ""},
		{"bare digits normalize", "12", []browsers.PageInfo{{ID: "page-12", URL: "https://x.com"}}, "page fetch", "page-12", ""},
		{
			"bare digits unresolved lists pages",
			"99", one, "page fetch", "",
			"page fetch: page \"page-99\" not found — open pages:\n  page-1\thttps://a.com",
		},
		{"literal passthrough", "page-2", two, "page fetch", "page-2", ""},
		{
			"unresolved literal lists pages",
			"page-9", two, "page close", "",
			"page close: page \"page-9\" not found — open pages:\n" +
				"  page-1\thttps://a.com\n" +
				"  page-2\thttps://b.com",
		},
		{
			"unresolved what word nudges",
			"links", one, "page fetch", "",
			"page fetch: page \"links\" not found — open pages:\n  page-1\thttps://a.com\n" +
				`"links" is an aspect, not a page id — try 'web-cli page fetch [id] links'`,
		},
		{
			"unresolved action word nudges",
			"click", one, "page act", "",
			"page act: page \"click\" not found — open pages:\n  page-1\thttps://a.com\n" +
				`"click" is an aspect, not a page id — try 'web-cli page act [id] click'`,
		},
		{
			"unresolved non-vocab token does not nudge",
			"xyz", one, "page fetch", "",
			"page fetch: page \"xyz\" not found — open pages:\n  page-1\thttps://a.com",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolvePageIDFromPages(tt.raw, tt.pages, tt.label)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("resolvePageIDFromPages(%q, %d pages, %q) = %q, nil error; want error %q", tt.raw, len(tt.pages), tt.label, got, tt.wantErr)
				}
				if err.Error() != tt.wantErr {
					t.Errorf("resolvePageIDFromPages(%q, %d pages, %q) error = %q, want %q", tt.raw, len(tt.pages), tt.label, err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolvePageIDFromPages(%q, %d pages, %q) error = %v, want nil", tt.raw, len(tt.pages), tt.label, err)
			}
			if got != tt.want {
				t.Errorf("resolvePageIDFromPages(%q, %d pages, %q) = %q, want %q", tt.raw, len(tt.pages), tt.label, got, tt.want)
			}
		})
	}
}

// TestResolvePageIDFromPagesNudgeAbsent double-checks that an unresolved token
// that happens to be a digit-normalized id does not trigger the aspect nudge
// (page-12 is never a what/action word).
func TestResolvePageIDFromPagesNudgeAbsent(t *testing.T) {
	_, err := resolvePageIDFromPages("12", []browsers.PageInfo{{ID: "page-1", URL: "https://a.com"}}, "page fetch")
	if err == nil {
		t.Fatal("resolvePageIDFromPages(\"12\", 1 page) = nil error, want unresolved error")
	}
	if strings.Contains(err.Error(), "is an aspect") {
		t.Errorf("unresolved digit-normalized id must not nudge; got %q", err.Error())
	}
}

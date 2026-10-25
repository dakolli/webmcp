// Page-id resolution and positional-arg parsing for the page subcommands
// (WEB-61). Everything here is Chromium-free: resolvePageID gathers the open
// pages client-side (over the existing IPC surface, or the manager's
// non-blocking Browser read) and applies pure string/number rules, so the
// engine and the IPC wire contract stay untouched.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"webmcp/pkg/browsers"
	"webmcp/pkg/cli/ipc"
)

// whatVocab is the fetch-aspect vocabulary shared by `page fetch` parsing and
// the page-id resolution nudge: a one-argument `page fetch <arg>` whose arg is
// one of these words is a what (the aspect to extract), not a page id.
var whatVocab = map[string]bool{
	"html": true, "text": true, "links": true, "meta": true,
	"dom": true, "css": true, "js": true, "screenshot": true,
	"interactives": true,
}

// actionVocab is the page-act action vocabulary shared by `page act` parsing
// and the page-id resolution nudge: `page act <arg>` whose first arg is one of
// these words is an action, not a page id.
var actionVocab = map[string]bool{
	"click": true, "input": true, "type": true, "press_key": true,
	"click_at": true, "hover_at": true, "drag_and_drop": true,
	"upload_file": true, "eval": true, "navigate": true, "back": true,
	"forward": true, "reload": true, "hover": true, "scroll": true,
	"select": true, "wait": true, "set_viewport": true, "emulate": true,
}

// actPayloadSpecs maps each page act action to its positional payload slots,
// in order. The payload count is validated against the slot count (extra args
// are rejected with an expected-payload error; zero payloads is always allowed
// — the --selector/--value/--x/--y/--width/--height/--device flags fill the
// same fields as the fallback). Each slot fills the same-named field of
// actArgs (see parseActArgs).
var actPayloadSpecs = map[string][]string{
	"click":         {"selector"},
	"hover":         {"selector"},
	"select":        {"selector", "value"},
	"input":         {"selector", "text"},
	"navigate":      {"url"},
	"eval":          {"js"},
	"scroll":        {"x", "y"},
	"wait":          {"mode"},
	"set_viewport":  {"w", "h"},
	"emulate":       {"device"},
	"back":          {},
	"forward":       {},
	"reload":        {},
	"type":          {"selector", "text"},
	"press_key":     {"key"},
	"click_at":      {"x", "y"},
	"hover_at":      {"x", "y"},
	"drag_and_drop": {"from_x", "from_y", "to_x", "to_y"},
	"upload_file":   {"selector", "path"},
}

// resolvePageID resolves a user-supplied page id (possibly empty) to a live
// page id, for the command labeled by label ("page fetch", "page act",
// "page close"). It gathers the open pages client-side — over IPC via
// browser.list (which, like `page list`, never lazily launches the browser; a
// "no browser open" answer means zero pages), else via mgr.Browser() (also a
// non-blocking read that never launches; an error means no browser, i.e. zero
// pages) — and applies the pure resolution rules of resolvePageIDFromPages.
// The returned id is the canonical engine id ("page-N"): the caller passes it
// straight to pageFetchViaIPC/pageActViaIPC/pageCloseViaIPC or mgr.Page.
func resolvePageID(ctx context.Context, cmd *cobra.Command, mgr *browsers.Manager, cl *ipc.Client, raw, label string) (string, error) {
	var pages []browsers.PageInfo
	if cl != nil {
		var res ipc.BrowserListResult
		if err := cl.Call(ctx, ipc.MethodBrowserList, nil, &res); err != nil {
			var serverErr *ipc.ServerError
			if errors.As(err, &serverErr) &&
				serverErr.Code == ipc.CodeServerError &&
				strings.Contains(serverErr.Message, "no browser open") {
				// No browser on the daemon → zero pages; the resolution rules
				// below produce the friendly "no open pages" answer. Never a
				// lazy launch: these commands all fail with "no open pages"
				// when the browser is absent, so launching it here would be
				// pure waste (mirrors the in-process mgr.Browser read).
			} else {
				return "", fmt.Errorf("%s: %w", label, err)
			}
		} else {
			pages = res.Pages
		}
	} else {
		// mgr.Browser never launches the browser: with none open it errors,
		// which we treat as zero pages (the resolution rules below produce the
		// friendly "no open pages" answer).
		if b, err := mgr.Browser(""); err == nil {
			pages = b.Pages()
		}
	}
	return resolvePageIDFromPages(raw, pages, label)
}

// resolvePageIDFromPages applies the page-id resolution rules against a
// snapshot of the open pages: an omitted id defaults to the single open page;
// "last" (case-insensitive) resolves to the page with the highest numeric
// page-N suffix (ids are minted incrementally, so that is the most recently
// opened page); a bare number normalizes to page-N; and any literal id must
// name an open page, else the error lists the open pages (id\turl rows,
// mirroring `page list`) plus — when the token is a fetch aspect or act action
// word — the "is an aspect, not a page id" nudge. Zero open pages always
// yields the same "no open pages" error.
func resolvePageIDFromPages(raw string, pages []browsers.PageInfo, label string) (string, error) {
	if isAllDigits(raw) {
		raw = "page-" + raw // bare numbers normalize to the engine's page-N form
	}

	if len(pages) == 0 {
		return "", noOpenPagesErr(label)
	}

	if raw == "" {
		switch len(pages) {
		case 1:
			return string(pages[0].ID), nil
		default:
			return "", fmt.Errorf("%s: multiple pages open — pass an explicit page id or 'last':\n%s", label, pageRows(pages))
		}
	}

	if strings.EqualFold(raw, "last") {
		best, max := "", -1
		for _, p := range pages {
			if n, err := pageNumSuffix(p.ID); err == nil && n > max {
				max, best = n, string(p.ID)
			}
		}
		if best != "" {
			return best, nil
		}
		return "", unresolvedPageErr(raw, pages, label)
	}

	for _, p := range pages {
		if string(p.ID) == raw {
			return raw, nil
		}
	}
	return "", unresolvedPageErr(raw, pages, label)
}

// noOpenPagesErr is the zero-page resolution error, shared by every resolution
// rule: it is the actionable hint to open a page first, pointing at both the
// two-step form (fetch then read) and the one-step 'page fetch <url>' form.
// 'page act' and 'page close' share it unchanged — they still cannot create
// pages, but the hint stays accurate for them too.
func noOpenPagesErr(label string) error {
	return fmt.Errorf("%s: no open pages — open one with 'web-cli fetch <url>' (or 'web-cli page fetch <url>' to open and read in one step)", label)
}

// unresolvedPageErr is the "no such page" error: it lists the open pages as
// "  id\turl" rows (tab-separated, mirroring `page list` output) and, when the
// offending token is a fetch aspect or act action word, appends the nudge
// steering the user to the positional grammar (e.g. `page fetch links` where
// links is an aspect, not an id).
func unresolvedPageErr(id string, pages []browsers.PageInfo, label string) error {
	msg := fmt.Sprintf("%s: page %q not found — open pages:\n%s", label, id, pageRows(pages))
	if whatVocab[id] || actionVocab[id] {
		msg += fmt.Sprintf("\n%q is an aspect, not a page id — try 'web-cli %s [id] %s'", id, label, id)
	}
	return errors.New(msg)
}

// pageRows renders the open pages as "  id\turl" lines for resolution error
// messages, mirroring `page list` output (tab-separated, no trailing newline).
func pageRows(pages []browsers.PageInfo) string {
	var b strings.Builder
	for _, p := range pages {
		fmt.Fprintf(&b, "  %s\t%s\n", p.ID, p.URL)
	}
	return strings.TrimRight(b.String(), "\n")
}

// pageNumSuffix returns the numeric suffix of a canonical page-N id, or an
// error for ids that do not match the canonical form.
func pageNumSuffix(id browsers.PageID) (int, error) {
	s := string(id)
	if !strings.HasPrefix(s, "page-") {
		return 0, fmt.Errorf("not a page-N id: %q", s)
	}
	n, err := strconv.Atoi(strings.TrimPrefix(s, "page-"))
	if err != nil {
		return 0, err
	}
	return n, nil
}

// isAllDigits reports whether s is a non-empty string of ASCII digits.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// isURLLike reports whether s looks like an absolute URL, i.e. it contains the
// "://" scheme separator. Page ids can never contain "://" (they are minted as
// page-N), so the test is unambiguous for the positional grammar: a URL-like
// token is a URL, never a page id. Flag values never go through this test — a
// --id flag value stays a page id by definition.
func isURLLike(s string) bool {
	return strings.Contains(s, "://")
}

// parseFetchArgs merges `page fetch` positional args with the --id/--what flag
// values: a positional wins over its flag counterpart when both supply the
// field. Grammar: 0 args → all three fields empty; 1 arg → a fetch-aspect word
// is the what, else a URL-like token (contains "://") is the url, else the id;
// 2 args → args[0] URL-like ? (url, what) : (id, what) (cobra enforces the
// MaximumNArgs(2) bound). A positional URL wins over --id/--what exactly like
// the other positionals; --id is never treated as a URL (flag semantics
// unchanged). The what defaults to "text" when neither the positional nor the
// flag supplies it ("show me the text of my page").
func parseFetchArgs(args []string, idFlag, whatFlag string) (id, what, url string) {
	switch len(args) {
	case 1:
		if whatVocab[args[0]] {
			what = args[0]
		} else if isURLLike(args[0]) {
			url = args[0]
		} else {
			id = args[0]
		}
	case 2:
		if isURLLike(args[0]) {
			url, what = args[0], args[1]
		} else {
			id, what = args[0], args[1]
		}
	}
	if id == "" && url == "" {
		id = idFlag // a positional URL wins over --id entirely
	}
	if what == "" {
		what = whatFlag
	}
	if what == "" {
		what = "text"
	}
	return id, what, url
}

// actArgFlags carries the page act --flag values that positional args may
// override: a positional always wins over its flag counterpart, and the flag
// is the fallback when the positional grammar leaves the field unfilled.
type actArgFlags struct {
	id, action, selector, handle, value, key, button, targetSelector, waitMode, device string
	x, y, targetX, targetY                                                             float64
	clickCount, width, height                                                          int
	paths                                                                              []string
}

// actArgs is the merged result of `page act` parsing: the effective
// id/action/payload fields after the positionals (which win) are merged over
// the flag values.
type actArgs struct {
	id, action, selector, handle, value, key, button, targetSelector, waitMode, device string
	x, y, targetX, targetY                                                             float64
	clickCount, width, height                                                          int
	paths                                                                              []string
}

// parseActArgs parses `page act` positional args against the flag values and
// returns the merged effective arguments. Grammar: `act [id] [action]
// [payload...]` — when args[0] is an action word it is the action and the rest
// is its payload (id omitted); otherwise args[0] is the id, args[1] the action
// and the rest the payload. The payload count is validated against the
// action's positional slots (extra args are rejected with an expected-payload
// error); each payload fills the action's fields per actPayloadSpecs, with the
// flags as fallbacks.
func parseActArgs(args []string, f actArgFlags) (actArgs, error) {
	var a actArgs
	var payload []string
	haveID, haveAction := false, false
	switch {
	case len(args) == 0:
		// Flags supply everything; positionals win only when present.
	case actionVocab[args[0]]:
		a.action = args[0]
		haveAction = true
		payload = args[1:]
	default:
		a.id = args[0]
		haveID = true
		if len(args) >= 2 {
			a.action = args[1]
			haveAction = true
			payload = args[2:]
		}
	}

	if slots, ok := actPayloadSpecs[a.action]; ok && len(payload) > len(slots) {
		if len(slots) == 0 {
			return a, fmt.Errorf("page act: action %q takes no positional args; got %d", a.action, len(payload))
		}
		word := "args"
		if len(slots) == 1 {
			word = "arg"
		}
		desc := "[" + strings.Join(slots, "] [") + "]"
		return a, fmt.Errorf("page act: action %q takes at most %d positional %s (%s); got %d",
			a.action, len(slots), word, desc, len(payload))
	}

	// Fill the per-action fields from the validated payload.
	xSet, ySet, txSet, tySet, wSet, hSet := false, false, false, false, false, false
	switch a.action {
	case "click", "hover", "select", "input", "type":
		if len(payload) >= 1 {
			a.selector = payload[0]
		}
		if len(payload) >= 2 {
			a.value = payload[1]
		}
	case "press_key":
		if len(payload) >= 1 {
			a.key = payload[0]
		}
	case "click_at", "hover_at":
		if len(payload) == 1 {
			return a, fmt.Errorf("page act: %s requires both x and y; got 1 positional arg", a.action)
		}
		if len(payload) >= 2 {
			x, err := strconv.ParseFloat(payload[0], 64)
			if err != nil {
				return a, fmt.Errorf("page act: %s x must be a number, got %q", a.action, payload[0])
			}
			y, err := strconv.ParseFloat(payload[1], 64)
			if err != nil {
				return a, fmt.Errorf("page act: %s y must be a number, got %q", a.action, payload[1])
			}
			a.x, a.y, xSet, ySet = x, y, true, true
		}
	case "drag_and_drop":
		if len(payload) >= 4 {
			fx, err := strconv.ParseFloat(payload[0], 64)
			if err != nil {
				return a, fmt.Errorf("page act: drag_and_drop from_x must be a number, got %q", payload[0])
			}
			fy, err := strconv.ParseFloat(payload[1], 64)
			if err != nil {
				return a, fmt.Errorf("page act: drag_and_drop from_y must be a number, got %q", payload[1])
			}
			tx, err := strconv.ParseFloat(payload[2], 64)
			if err != nil {
				return a, fmt.Errorf("page act: drag_and_drop to_x must be a number, got %q", payload[2])
			}
			ty, err := strconv.ParseFloat(payload[3], 64)
			if err != nil {
				return a, fmt.Errorf("page act: drag_and_drop to_y must be a number, got %q", payload[3])
			}
			a.x, a.y, a.targetX, a.targetY = fx, fy, tx, ty
			xSet, ySet, txSet, tySet = true, true, true, true
		}
	case "upload_file":
		if len(payload) >= 1 {
			a.selector = payload[0]
		}
		if len(payload) >= 2 {
			a.paths = payload[1:]
		}
	case "navigate", "eval":
		if len(payload) >= 1 {
			a.value = payload[0]
		}
	case "scroll":
		if len(payload) == 1 {
			return a, fmt.Errorf("page act: scroll requires both x and y; got 1 positional arg")
		}
		if len(payload) >= 2 {
			x, err := strconv.ParseFloat(payload[0], 64)
			if err != nil {
				return a, fmt.Errorf("page act: scroll x must be a number, got %q", payload[0])
			}
			y, err := strconv.ParseFloat(payload[1], 64)
			if err != nil {
				return a, fmt.Errorf("page act: scroll y must be a number, got %q", payload[1])
			}
			a.x, a.y, xSet, ySet = x, y, true, true
		}
	case "wait":
		if len(payload) >= 1 {
			a.waitMode = payload[0]
		}
	case "set_viewport":
		if len(payload) == 1 {
			return a, fmt.Errorf("page act: set_viewport requires both width and height; got 1 positional arg")
		}
		if len(payload) >= 2 {
			w, err := strconv.Atoi(payload[0])
			if err != nil {
				return a, fmt.Errorf("page act: set_viewport width must be an integer, got %q", payload[0])
			}
			h, err := strconv.Atoi(payload[1])
			if err != nil {
				return a, fmt.Errorf("page act: set_viewport height must be an integer, got %q", payload[1])
			}
			a.width, a.height, wSet, hSet = w, h, true, true
		}
	case "emulate":
		if len(payload) >= 1 {
			a.device = payload[0]
		}
	}

	// Merge the flags in: positionals win; flags are the fallback.
	if !haveID {
		a.id = f.id
	}
	if !haveAction {
		a.action = f.action
	}
	if a.selector == "" {
		a.selector = f.selector
	}
	if a.handle == "" {
		a.handle = f.handle
	}
	if a.value == "" {
		a.value = f.value
	}
	if a.key == "" {
		a.key = f.key
	}
	if a.button == "" {
		a.button = f.button
	}
	if a.clickCount == 0 {
		a.clickCount = f.clickCount
	}
	if a.targetSelector == "" {
		a.targetSelector = f.targetSelector
	}
	if len(a.paths) == 0 {
		a.paths = f.paths
	}
	if a.waitMode == "" {
		a.waitMode = f.waitMode
	}
	if a.device == "" {
		a.device = f.device
	}
	if !xSet {
		a.x = f.x
	}
	if !ySet {
		a.y = f.y
	}
	if !txSet {
		a.targetX = f.targetX
	}
	if !tySet {
		a.targetY = f.targetY
	}
	if !wSet {
		a.width = f.width
	}
	if !hSet {
		a.height = f.height
	}
	return a, nil
}

package pageops

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dakolli/webmcp/pkg/browsers"
)

// dialogState is the one-line JSON result of a dialog-policy action: the policy
// now in effect, the prompt text it supplies, the ring size and the newest
// recorded dialog or auth challenge.
type dialogState struct {
	// Policy is accept or dismiss.
	Policy string `json:"policy"`
	// PromptText is the prompt text an accept supplies.
	PromptText string `json:"prompt_text"`
	// Count is how many dialog/auth records the ring holds.
	Count int `json:"count"`
	// Dropped counts records evicted by the ring bound.
	Dropped int `json:"dropped"`
	// Last is the newest recorded dialog or auth challenge, or null when the
	// ring is empty.
	Last *browsers.DialogEntry `json:"last"`
}

// httpAuthResult is the one-line JSON result of an HTTP basic-auth action. Note
// is present only when Chromium already cached a credential for the page's
// origin.
type httpAuthResult struct {
	// HTTPAuth is armed or cleared.
	HTTPAuth string `json:"http_auth"`
	// Note explains why a different credential needs a fresh page.
	Note string `json:"note,omitempty"`
}

// PageDialogAccept switches the page's dialog policy to accept, answering
// prompt dialogs with prompt, and returns
// {policy,prompt_text,count,dropped,last}.
func PageDialogAccept(ctx context.Context, p *browsers.Page, prompt string) (string, error) {
	if _, done, err := beginPage(ctx, p, "dialog_accept"); err != nil {
		return "", err
	} else {
		done()
	}
	p.AcceptDialogs(prompt)
	return encodeDialogState(p, browsers.DialogAccept, prompt)
}

// PageDialogDismiss switches the page's dialog policy to dismiss and returns
// {policy,prompt_text,count,dropped,last}.
func PageDialogDismiss(ctx context.Context, p *browsers.Page, prompt string) (string, error) {
	if _, done, err := beginPage(ctx, p, "dialog_dismiss"); err != nil {
		return "", err
	} else {
		done()
	}
	p.DismissDialogs(prompt)
	return encodeDialogState(p, browsers.DialogDismiss, prompt)
}

// PageDialogs returns the dialog/auth ring as
// {entries,count,dropped,truncated,policy}: the newest 100 records of a
// 50-dialog ring, oldest first, plus the policy now answering dialogs. The
// 50-entry ring never fills the 100-entry emit cap, so truncated is always 0.
// It never changes the policy and rejects a page the watchdog marked unusable
// before reading the ring.
func PageDialogs(_ context.Context, p *browsers.Page) (string, error) {
	if err := guardRead(p); err != nil {
		return "", err
	}
	entries, dropped, policy := p.Dialogs()
	return encodeDialogs(entries, dropped, policy)
}

// encodeDialogs marshals the dialog ring in the shared log envelope, adding the
// current answer policy beside the counters.
func encodeDialogs(entries []browsers.DialogEntry, dropped int, policy string) (string, error) {
	entries, truncated := trimLogs(entries)
	b, err := json.Marshal(logsEnvelope[browsers.DialogEntry]{
		Entries:   entries,
		Count:     len(entries),
		Dropped:   dropped,
		Truncated: truncated,
		Policy:    &policy,
	})
	if err != nil {
		return "", fmt.Errorf("encode dialogs log: %w", err)
	}
	return string(b), nil
}

// PageHTTPAuthSet arms per-page HTTP basic-auth credentials and returns
// {"http_auth":"armed"} plus note when Chromium already cached a credential for
// the page's origin. The credential is never echoed.
func PageHTTPAuthSet(ctx context.Context, p *browsers.Page, user, pass string) (string, error) {
	if _, done, err := beginPage(ctx, p, "http_auth"); err != nil {
		return "", err
	} else {
		done()
	}
	if err := p.SetHTTPAuth(user, pass); err != nil {
		return "", err
	}
	return encodeHTTPAuth(ctx, p, "armed")
}

// PageHTTPAuthClear disarms the page's HTTP basic-auth credentials and returns
// {"http_auth":"cleared"} plus note when Chromium already cached a credential
// for the page's origin.
func PageHTTPAuthClear(ctx context.Context, p *browsers.Page) (string, error) {
	if _, done, err := beginPage(ctx, p, "http_auth_clear"); err != nil {
		return "", err
	} else {
		done()
	}
	p.ClearHTTPAuth()
	return encodeHTTPAuth(ctx, p, "cleared")
}

// encodeHTTPAuth renders one HTTP-auth result, adding the origin-cache note when
// the page already supplied a credential there.
func encodeHTTPAuth(ctx context.Context, p *browsers.Page, status string) (string, error) {
	note := ""
	if liveURL, _, err := p.TryState(ctx, "http_auth"); err == nil && p.HTTPAuthCachedFor(liveURL) {
		note = "Chromium cached the credential for this origin; open a new page to test a different credential"
	}
	b, err := json.Marshal(httpAuthResult{HTTPAuth: status, Note: note})
	if err != nil {
		return "", fmt.Errorf("encode http auth result: %w", err)
	}
	return string(b), nil
}

// encodeDialogState renders the current policy and ring size as one JSON line.
func encodeDialogState(p *browsers.Page, policy, prompt string) (string, error) {
	entries, dropped, current := p.Dialogs()
	if current != "" {
		policy = current
	}
	b, err := json.Marshal(dialogState{
		Policy:     policy,
		PromptText: prompt,
		Count:      len(entries),
		Dropped:    dropped,
		Last:       lastDialog(entries),
	})
	if err != nil {
		return "", fmt.Errorf("encode dialog state: %w", err)
	}
	return string(b), nil
}

// lastDialog returns a copy of the newest ring entry, or nil when the ring is
// empty.
func lastDialog(entries []browsers.DialogEntry) *browsers.DialogEntry {
	if len(entries) == 0 {
		return nil
	}
	last := entries[len(entries)-1]
	return &last
}

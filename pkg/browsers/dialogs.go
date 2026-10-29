package browsers

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

const (
	// dialogRingCap bounds the retained JavaScript-dialog and HTTP-auth entries
	// per page.
	dialogRingCap = 50
	// dialogMessageLimit bounds a stored dialog message or auth challenge text
	// at 500 runes.
	dialogMessageLimit = 500
	// errInvalidAuthCredential is the CDP error text Chromium reports when a
	// navigation reaches a basic-auth challenge with no usable credential.
	// rod surfaces it as plain text, so matching stays on this token and never
	// a bare "net::" prefix.
	errInvalidAuthCredential = "ERR_INVALID_AUTH_CREDENTIALS"

	// DialogAccept and DialogDismiss are the two answer policies.
	DialogAccept  = "accept"
	DialogDismiss = "dismiss"

	// Dialog entry types.
	dialogAlert      = "alert"
	dialogConfirm    = "confirm"
	dialogPrompt     = "prompt"
	dialogBeforeLoad = "beforeunload"
	dialogAuth       = "auth"
)

// DialogEntry is one answered JavaScript dialog or one rejected HTTP-auth
// challenge. A dialog is recorded by the engine as it opens and answered at
// once, so the renderer never waits for the caller.
type DialogEntry struct {
	// Seq is the per-page sequence number, oldest first.
	Seq int64 `json:"seq"`
	// Type is alert, confirm, prompt, beforeunload or auth.
	Type string `json:"type"`
	// Message is the dialog text, redacted and clipped to 500 runes.
	Message string `json:"message"`
	// MessageCut reports that Message was clipped at the rune bound.
	MessageCut bool `json:"message_cut"`
	// DefaultPrompt is the value the dialog offered for a prompt.
	DefaultPrompt string `json:"default_prompt"`
	// URL is the frame URL that opened the dialog or requested the challenge.
	URL string `json:"url"`
	// Policy is the answer policy in effect when the entry was recorded:
	// accept, dismiss or, for a rejected auth challenge, dismiss.
	Policy string `json:"policy"`
	// PromptText is the prompt text the policy supplies when accepting.
	PromptText string `json:"prompt_text"`
	// MS is how long the dialog stayed open before the engine answered it.
	MS int64 `json:"ms"`
}

// openDialog is the dialog the event handler is answering right now. claimed
// keeps a policy change from answering it a second time.
type openDialog struct {
	typ     string
	claimed bool
}

// pageDialogs holds one page's dialog/auth ring, its answer policy and the CDP
// subscriptions that answer dialogs and auth challenges. Its mutex is
// deliberately independent of the page operation lock: a dialog blocks the
// renderer, so answering it must never wait behind a read that the dialog has
// already blocked.
type pageDialogs struct {
	mu         sync.Mutex
	ring       ringBuffer[DialogEntry]
	seq        int64
	policy     string
	prompt     string
	open       *openDialog
	client     *rod.Page
	cancel     context.CancelFunc
	authClient *rod.Page
	authCancel context.CancelFunc
	authUser   string
	authPass   string
	authTried  map[proto.FetchRequestID]bool
	// authOrigins records the origins that supplied a basic-auth credential, so
	// a later credential change can say Chromium has already cached one there.
	authOrigins map[string]struct{}
}

// newPageDialogs returns a page's dialog state with the default dismiss policy
// and the bounded entry ring.
func newPageDialogs() *pageDialogs {
	return &pageDialogs{
		ring:        ringBuffer[DialogEntry]{cap: dialogRingCap},
		policy:      DialogDismiss,
		authTried:   make(map[proto.FetchRequestID]bool),
		authOrigins: make(map[string]struct{}),
	}
}

// startDialogs subscribes to JavaScript dialogs on a freshly created page, in
// the same pre-registration window as observation and proxy auth. Every dialog
// is answered immediately from the page's policy so it never blocks the
// renderer; the default policy is dismiss.
func (p *Page) startDialogs() {
	if p.Rod == nil {
		return
	}
	d := newPageDialogs()
	client, cancel := p.Rod.WithCancel()
	d.client = client
	d.cancel = cancel
	p.dialogs = d

	stop := client.EachEvent(func(e *proto.PageJavascriptDialogOpening) {
		p.answerDialogEvent(e)
	})
	go stop()
}

// answerDialogEvent records one dialog and answers it from the current policy
// without holding the page operation lock.
func (p *Page) answerDialogEvent(e *proto.PageJavascriptDialogOpening) {
	d := p.dialogs
	if d == nil {
		return
	}
	started := time.Now()
	message, cut := clipDialogMessage(e.Message)

	d.mu.Lock()
	d.seq++
	entry := DialogEntry{
		Seq:           d.seq,
		Type:          dialogType(e.Type),
		Message:       message,
		MessageCut:    cut,
		DefaultPrompt: e.DefaultPrompt,
		URL:           e.URL,
		Policy:        d.policy,
		PromptText:    d.prompt,
	}
	open := &openDialog{typ: entry.Type}
	d.open = open
	client := d.client
	d.mu.Unlock()

	if client != nil {
		d.mu.Lock()
		answer := !open.claimed
		open.claimed = true
		d.mu.Unlock()
		if answer {
			_ = handleDialog(client, entry.Type, entry.Policy == DialogAccept, entry.PromptText)
		}
	}

	d.mu.Lock()
	if d.open == open {
		d.open = nil
	}
	entry.MS = time.Since(started).Milliseconds()
	d.ring.push(entry)
	d.mu.Unlock()
}

// AcceptDialogs switches the page to accept dialogs, using promptText for a
// prompt, and answers the dialog that is open right now.
func (p *Page) AcceptDialogs(promptText string) {
	p.setDialogPolicy(DialogAccept, promptText)
}

// DismissDialogs switches the page to dismiss dialogs and answers the dialog
// that is open right now.
func (p *Page) DismissDialogs(promptText string) {
	p.setDialogPolicy(DialogDismiss, promptText)
}

// setDialogPolicy stores the answer policy and answers an open dialog that the
// event handler has not claimed yet.
func (p *Page) setDialogPolicy(policy, promptText string) {
	d := p.dialogs
	if d == nil {
		return
	}
	d.mu.Lock()
	d.policy = policy
	d.prompt = promptText
	client := d.client
	open := d.open
	if open != nil && !open.claimed {
		open.claimed = true
	} else {
		open = nil
	}
	d.mu.Unlock()
	if client != nil && open != nil {
		_ = handleDialog(client, open.typ, policy == DialogAccept, promptText)
	}
}

// Dialogs returns a copy of the page's dialog/auth entries oldest first, the
// number dropped by the ring bound, and the current answer policy.
func (p *Page) Dialogs() ([]DialogEntry, int, string) {
	d := p.dialogs
	if d == nil {
		return []DialogEntry{}, 0, DialogDismiss
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ring.take(), int(d.ring.dropped), d.policy
}

// ClearDialogs empties the dialog/auth ring and resets its dropped counter,
// returning how many entries were cleared.
func (p *Page) ClearDialogs() int {
	d := p.dialogs
	if d == nil {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ring.clear()
}

// SetHTTPAuth arms per-page HTTP basic-auth credentials. It subscribes to the
// CDP Fetch domain, continues every paused request and supplies the credential
// to the first auth challenge of each request. A second challenge is not
// retried. The page must have no armed request interception, which owns the
// same Fetch domain.
func (p *Page) SetHTTPAuth(user, pass string) error {
	if strings.TrimSpace(user) == "" {
		return fmt.Errorf("http auth: username is required — pass the basic-auth username, and a password when the realm asks for one")
	}
	if p.httpAuthArmed() {
		return fmt.Errorf("http auth: credentials are already armed on page %s — clear them first to replace them", p.ID)
	}
	if it := p.armedInterceptor(); it != nil {
		return fmt.Errorf(
			"http auth: page %s has request interception armed, which owns the CDP Fetch domain — call intercept_clear, then set the credential",
			p.ID,
		)
	}
	d := p.dialogs
	if d == nil {
		return fmt.Errorf("http auth: page %s has no live dialog client — reopen the page and retry", p.ID)
	}
	rodPage, err := p.rodForDialogClient()
	if err != nil {
		return err
	}

	client, cancel := rodPage.WithCancel()
	d.mu.Lock()
	d.authClient = client
	d.authCancel = cancel
	d.authUser = user
	d.authPass = pass
	d.authTried = make(map[proto.FetchRequestID]bool)
	d.mu.Unlock()

	stop := client.EachEvent(
		func(e *proto.FetchRequestPaused) {
			_ = proto.FetchContinueRequest{RequestID: e.RequestID}.Call(client)
		},
		func(e *proto.FetchAuthRequired) {
			p.answerHTTPAuth(client, e)
		},
	)
	go stop()

	if err := (proto.FetchEnable{HandleAuthRequests: true}).Call(client); err != nil {
		cancel()
		d.mu.Lock()
		if d.authClient == client {
			d.authClient = nil
			d.authCancel = nil
			d.authUser, d.authPass = "", ""
			d.authTried = nil
		}
		d.mu.Unlock()
		return fmt.Errorf(
			"http auth: enable the CDP Fetch domain: %w; the page may already have another Fetch handler — close and reopen it",
			err,
		)
	}
	return nil
}

// ClearHTTPAuth disarms the page's basic-auth credentials and disables the
// Fetch domain so a later interception arm can own it.
func (p *Page) ClearHTTPAuth() {
	d := p.dialogs
	if d == nil {
		return
	}
	d.mu.Lock()
	cancel := d.authCancel
	d.authClient = nil
	d.authCancel = nil
	d.authUser, d.authPass = "", ""
	d.authTried = nil
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}

	unlock, ok := p.TryLock()
	if !ok {
		return
	}
	rodPage := p.Rod
	unlock()
	if rodPage == nil {
		return
	}
	_ = proto.FetchDisable{}.Call(rodPage)
}

// answerHTTPAuth supplies the stored credential to the first challenge of a
// request and records a rejected second challenge instead of retrying it.
func (p *Page) answerHTTPAuth(client *rod.Page, e *proto.FetchAuthRequired) {
	d := p.dialogs
	if d == nil {
		return
	}
	d.mu.Lock()
	first := d.claimAuthAttempt(e.RequestID)
	user, pass := d.authUser, d.authPass
	if e.Request != nil {
		if origin := urlOrigin(e.Request.URL); origin != "" {
			if first {
				if d.authOrigins == nil {
					d.authOrigins = make(map[string]struct{})
				}
				d.authOrigins[origin] = struct{}{}
			} else {
				delete(d.authOrigins, origin)
			}
		}
	}
	d.mu.Unlock()

	if first {
		_ = (&proto.FetchContinueWithAuth{
			RequestID: e.RequestID,
			AuthChallengeResponse: &proto.FetchAuthChallengeResponse{
				Response: proto.FetchAuthChallengeResponseResponseProvideCredentials,
				Username: user,
				Password: pass,
			},
		}).Call(client)
		return
	}

	p.recordAuthChallenge(e)
	_ = (&proto.FetchContinueWithAuth{
		RequestID: e.RequestID,
		AuthChallengeResponse: &proto.FetchAuthChallengeResponse{
			Response: proto.FetchAuthChallengeResponseResponseCancelAuth,
		},
	}).Call(client)
}

// claimAuthAttempt reports whether this is the first challenge for id and marks
// it answered. A second challenge for the same request is never retried. The
// caller must hold d.mu.
func (d *pageDialogs) claimAuthAttempt(id proto.FetchRequestID) bool {
	if d.authTried == nil {
		d.authTried = make(map[proto.FetchRequestID]bool)
	}
	if d.authTried[id] {
		return false
	}
	d.authTried[id] = true
	return true
}

// recordAuthChallenge appends a rejected HTTP-auth challenge to the dialog ring
// with the realm it names and the dismiss policy.
func (p *Page) recordAuthChallenge(e *proto.FetchAuthRequired) {
	d := p.dialogs
	if d == nil {
		return
	}
	realm := ""
	if e.AuthChallenge != nil {
		realm = e.AuthChallenge.Realm
	}
	rawURL := ""
	if e.Request != nil {
		rawURL = e.Request.URL
	}
	message, cut := clipDialogMessage(fmt.Sprintf(
		"HTTP authentication rejected the supplied credentials for realm %q",
		realm,
	))

	d.mu.Lock()
	defer d.mu.Unlock()
	d.seq++
	d.ring.push(DialogEntry{
		Seq:        d.seq,
		Type:       dialogAuth,
		Message:    message,
		MessageCut: cut,
		URL:        rawURL,
		Policy:     DialogDismiss,
	})
}

// stopDialogs cancels the page's dialog and auth subscriptions. It makes no CDP
// call and is idempotent, so every teardown path may call it outside the page
// lock.
func (p *Page) stopDialogs() {
	d := p.dialogs
	if d == nil {
		return
	}
	d.mu.Lock()
	cancel := d.cancel
	authCancel := d.authCancel
	d.cancel = nil
	d.authCancel = nil
	d.client = nil
	d.authClient = nil
	d.authUser, d.authPass = "", ""
	d.authTried = nil
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if authCancel != nil {
		authCancel()
	}
}

// httpAuthArmed reports whether the page holds HTTP basic-auth credentials.
func (p *Page) httpAuthArmed() bool {
	d := p.dialogs
	if d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.authUser != "" || d.authPass != ""
}

// HTTPAuthCachedFor reports whether this page supplied a credential to a
// challenge from the same origin as rawURL, which Chromium then caches for that
// origin.
func (p *Page) HTTPAuthCachedFor(rawURL string) bool {
	d := p.dialogs
	origin := urlOrigin(rawURL)
	if d == nil || origin == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.authOrigins[origin]
	return ok
}

// IsInvalidAuthCredential reports whether err is Chromium's rejected
// basic-auth challenge (net::ERR_INVALID_AUTH_CREDENTIALS).
func IsInvalidAuthCredential(err error) bool {
	return err != nil && strings.Contains(err.Error(), errInvalidAuthCredential)
}

// InvalidAuthCredentialError names a navigation that reached a basic-auth
// challenge with no usable credential and tells the caller to arm one before
// navigating again.
func InvalidAuthCredentialError(rawURL string, pageID PageID) error {
	return fmt.Errorf(
		"navigate: %q answered the basic-auth challenge with no usable credential (net::%s); the page stays open as session %q — arm a credential with page_act action=\"http_auth\" value=\"user:password\" (CLI: browser page act %s http_auth user:password) BEFORE navigating, then navigate again with page_act action=\"navigate\" value=%q (CLI: browser page act %s navigate %q)",
		rawURL, errInvalidAuthCredential, pageID, pageID, rawURL, pageID, rawURL,
	)
}

// urlOrigin returns scheme://host for an absolute URL, or "" when it cannot be
// parsed or carries no host.
func urlOrigin(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// armedInterceptor returns the page's live interceptor, or nil when none is
// armed.
func (p *Page) armedInterceptor() *interceptor {
	p.interceptMu.Lock()
	defer p.interceptMu.Unlock()
	if p.intercept == nil || p.intercept.isStopped() {
		return nil
	}
	return p.intercept
}

// rodForDialogClient reads the live rod page without holding the page lock
// across a CDP call.
func (p *Page) rodForDialogClient() (*rod.Page, error) {
	unlock, ok := p.TryLock()
	if !ok {
		if err := p.Unusable(); err != nil {
			return nil, fmt.Errorf("http auth: %w", err)
		}
		return nil, fmt.Errorf("http auth: page %s is busy — retry once the current operation returns", p.ID)
	}
	rodPage := p.Rod
	unlock()
	if rodPage == nil {
		return nil, fmt.Errorf("http auth: page %s is closed — reopen the page and retry", p.ID)
	}
	return rodPage, nil
}

// handleDialog accepts or dismisses one dialog. Only an accepted prompt carries
// a prompt text.
func handleDialog(client *rod.Page, typ string, accept bool, promptText string) error {
	msg := &proto.PageHandleJavaScriptDialog{Accept: accept}
	if typ == dialogPrompt && accept {
		msg.PromptText = promptText
	}
	return msg.Call(client)
}

// dialogType maps a CDP dialog type to the wire vocabulary.
func dialogType(t proto.PageDialogType) string {
	switch t {
	case proto.PageDialogTypeAlert:
		return dialogAlert
	case proto.PageDialogTypeConfirm:
		return dialogConfirm
	case proto.PageDialogTypePrompt:
		return dialogPrompt
	case proto.PageDialogTypeBeforeunload:
		return dialogBeforeLoad
	default:
		return string(t)
	}
}

// clipDialogMessage redacts sensitive values through the body redactor, then
// clips the text to dialogMessageLimit runes and reports whether it was cut.
func clipDialogMessage(s string) (string, bool) {
	redacted, _ := redactBody(s, false)
	if utf8.RuneCountInString(redacted) <= dialogMessageLimit {
		return redacted, false
	}
	runes := []rune(redacted)
	return string(runes[:dialogMessageLimit]), true
}

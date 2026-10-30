package browsers

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-rod/rod/lib/proto"
	"github.com/ysmood/gson"
)

func TestNewPageDialogsDefaults(t *testing.T) {
	d := newPageDialogs()
	if d.policy != DialogDismiss {
		t.Fatalf("default policy = %q, want %q", d.policy, DialogDismiss)
	}
	p := &Page{dialogs: d}
	entries, dropped, policy := p.Dialogs()
	if len(entries) != 0 || dropped != 0 || policy != DialogDismiss {
		t.Fatalf("empty Dialogs() = %d entries, %d dropped, %q; want 0, 0, %q", len(entries), dropped, policy, DialogDismiss)
	}
}

func TestDialogPolicyChange(t *testing.T) {
	p := &Page{dialogs: newPageDialogs()}
	p.AcceptDialogs("typed")
	if _, _, policy := p.Dialogs(); policy != DialogAccept {
		t.Fatalf("policy after AcceptDialogs = %q, want %q", policy, DialogAccept)
	}
	p.DismissDialogs("")
	if _, _, policy := p.Dialogs(); policy != DialogDismiss {
		t.Fatalf("policy after DismissDialogs = %q, want %q", policy, DialogDismiss)
	}
}

func TestClipDialogMessage(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		wantLen  int
		wantCut  bool
		contains string
	}{
		{name: "short kept", in: "hello", wantLen: 5},
		{name: "clipped at the rune bound", in: "é" + strings.Repeat("a", dialogMessageLimit), wantLen: dialogMessageLimit, wantCut: true},
		{name: "redacted value", in: `{"password":"hunter2"}`, wantLen: len(`{"password":"<redacted>"}`), contains: redactedValue},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, cut := clipDialogMessage(tt.in)
			if cut != tt.wantCut {
				t.Fatalf("cut = %v, want %v", cut, tt.wantCut)
			}
			if n := len([]rune(got)); n != tt.wantLen {
				t.Fatalf("len = %d runes, want %d", n, tt.wantLen)
			}
			if tt.contains != "" && !strings.Contains(got, tt.contains) {
				t.Fatalf("message %q does not contain %q", got, tt.contains)
			}
		})
	}
}

func TestDialogRingDropsOldest(t *testing.T) {
	p := &Page{dialogs: newPageDialogs()}
	for i := 0; i < dialogRingCap+3; i++ {
		p.dialogs.mu.Lock()
		p.dialogs.seq++
		p.dialogs.ring.push(DialogEntry{Seq: p.dialogs.seq})
		p.dialogs.mu.Unlock()
	}
	entries, dropped, _ := p.Dialogs()
	if len(entries) != dialogRingCap {
		t.Fatalf("ring holds %d entries, want %d", len(entries), dialogRingCap)
	}
	if dropped != 3 {
		t.Fatalf("dropped = %d, want 3", dropped)
	}
	if entries[0].Seq != 4 {
		t.Fatalf("oldest retained seq = %d, want 4", entries[0].Seq)
	}
	if cleared := p.ClearDialogs(); cleared != dialogRingCap {
		t.Fatalf("ClearDialogs = %d, want %d", cleared, dialogRingCap)
	}
	if _, dropped, _ := p.Dialogs(); dropped != 0 {
		t.Fatalf("dropped after clear = %d, want 0", dropped)
	}
}

func TestViolationRingDropsOldest(t *testing.T) {
	p := &Page{violations: newPageViolations()}
	for i := 0; i < violationRingCap+3; i++ {
		p.violations.record(CSPViolation{Directive: "script-src"})
	}
	entries, dropped := p.CSPViolations()
	if len(entries) != violationRingCap {
		t.Fatalf("ring holds %d entries, want %d", len(entries), violationRingCap)
	}
	if dropped != 3 {
		t.Fatalf("dropped = %d, want 3", dropped)
	}
	if entries[0].Seq != 4 {
		t.Fatalf("oldest retained seq = %d, want 4", entries[0].Seq)
	}
	if cleared := p.ClearCSPViolations(); cleared != violationRingCap {
		t.Fatalf("ClearCSPViolations = %d, want %d", cleared, violationRingCap)
	}
	if _, dropped := p.CSPViolations(); dropped != 0 {
		t.Fatalf("dropped after clear = %d, want 0", dropped)
	}
}

func TestDecodeViolation(t *testing.T) {
	got := decodeViolation(gson.New(map[string]any{
		"directive":   "script-src",
		"blocked_uri": "inline",
		"source_file": "https://example.test/",
		"line":        7,
		"column":      3,
		"disposition": "enforce",
	}))
	want := CSPViolation{
		Directive:   "script-src",
		BlockedURI:  "inline",
		SourceFile:  "https://example.test/",
		Line:        7,
		Column:      3,
		Disposition: "enforce",
	}
	if got != want {
		t.Fatalf("decodeViolation = %+v, want %+v", got, want)
	}
}

func TestClaimAuthAttemptIsOneShot(t *testing.T) {
	d := newPageDialogs()
	id := proto.FetchRequestID("42")
	if !d.claimAuthAttempt(id) {
		t.Fatal("first challenge was not claimed")
	}
	if d.claimAuthAttempt(id) {
		t.Fatal("second challenge for the same request was retried")
	}
	if !d.claimAuthAttempt(proto.FetchRequestID("43")) {
		t.Fatal("a different request was not claimed")
	}
}

func TestDialogTypeMapping(t *testing.T) {
	tests := []struct {
		in   proto.PageDialogType
		want string
	}{
		{proto.PageDialogTypeAlert, dialogAlert},
		{proto.PageDialogTypeConfirm, dialogConfirm},
		{proto.PageDialogTypePrompt, dialogPrompt},
		{proto.PageDialogTypeBeforeunload, dialogBeforeLoad},
	}
	for _, tt := range tests {
		if got := dialogType(tt.in); got != tt.want {
			t.Errorf("dialogType(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestIsInvalidAuthCredential(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "rod wraps the CDP text", err: errors.New("navigation failed: net::ERR_INVALID_AUTH_CREDENTIALS"), want: true},
		{name: "bare net prefix is not enough", err: errors.New("net::ERR_CONNECTION_REFUSED"), want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsInvalidAuthCredential(tt.err); got != tt.want {
				t.Fatalf("IsInvalidAuthCredential(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestHTTPAuthCachedForOrigin(t *testing.T) {
	p := &Page{dialogs: newPageDialogs()}
	p.dialogs.mu.Lock()
	p.dialogs.authOrigins["https://example.com"] = struct{}{}
	p.dialogs.mu.Unlock()
	if !p.HTTPAuthCachedFor("https://example.com/protected/page") {
		t.Fatal("same-origin URL was not reported as supplied")
	}
	if p.HTTPAuthCachedFor("https://other.example.com/protected/page") {
		t.Fatal("a different origin was reported as supplied")
	}
	if p.HTTPAuthCachedFor("not a url") {
		t.Fatal("an unparseable URL was reported as supplied")
	}
}

package browsers

import (
	"context"
	"fmt"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// DocMarker is the window property that tags a document with the token which
// scopes the element handles minted from it. It is set on the page's top-level
// document for every document creation, so a handle from a previous document
// (navigation, reload, redirect) is rejected instead of acting on a backend
// node id that Chromium reused in the newer document.
const DocMarker = "__webmcpDoc"

// docMarkerScript stamps a fresh token on every top-level document before the
// document's own scripts run. Frames are skipped: handles carry the page's
// top-level document token, and stamping each subframe with its own token
// would reject every iframe handle minted for it.
const docMarkerScript = "if (window === window.top) window." + DocMarker +
	" = Math.random().toString(36).slice(2, 10)"

// docTokenJS returns the current document's marker, stamping one when the
// document predates the marker script (or a site cleared it).
const docTokenJS = "() => window." + DocMarker +
	" || (window." + DocMarker + " = Math.random().toString(36).slice(2, 10))"

// MintHandle renders the action handle for one element:
// g<document>-b<backendNodeId>. The document token binds the handle to the
// document that minted it, so a stale handle can never resolve to a reused
// backend node id in a newer document.
func MintHandle(docToken string, id proto.DOMBackendNodeID) string {
	return fmt.Sprintf("g%s-b%d", docToken, id)
}

// CaptureSnapshot returns the page's DOM snapshot and the token of the
// top-level document that produced it. The marker script is installed once per
// page, so every later navigation stamps a fresh token before its own scripts
// run; the current document is stamped immediately when it predates the
// script. The method takes the page lock itself (mirroring Page.Interactives):
// callers must NOT pre-lock, and the snapshot and its token always come from
// the same lock scope.
func (p *Page) CaptureSnapshot(ctx context.Context) (*proto.DOMSnapshotCaptureSnapshotResult, string, error) {
	p.Lock()
	defer p.Unlock()
	if p.Rod == nil {
		return nil, "", fmt.Errorf("page session is closed")
	}

	page := p.Rod.Context(ctx)
	if !p.docMarkerInstalled {
		if _, err := page.EvalOnNewDocument(docMarkerScript); err != nil {
			return nil, "", fmt.Errorf("install document marker: %w", err)
		}
		p.docMarkerInstalled = true
	}
	token, err := documentToken(page)
	if err != nil {
		return nil, "", err
	}
	snap, err := page.CaptureDOMSnapshot()
	if err != nil {
		return nil, "", fmt.Errorf("capture DOM snapshot: %w", err)
	}
	return snap, token, nil
}

// documentToken reads the top-level document's marker, stamping a fresh token
// when the document has none. Callers must hold the page lock.
func documentToken(page *rod.Page) (string, error) {
	res, err := page.Eval(docTokenJS)
	if err != nil {
		return "", fmt.Errorf("read document marker: %w", err)
	}
	if res.Value.Nil() {
		return "", fmt.Errorf("read document marker: empty token")
	}
	token := res.Value.Str()
	if token == "" {
		return "", fmt.Errorf("read document marker: empty token")
	}
	return token, nil
}

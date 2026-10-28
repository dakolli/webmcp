package pageops

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/dakolli/webmcp/pkg/browsers"
)

// ReplayRequest is one captured network entry to resend with optional
// overrides. A zero Method and nil Headers keep the captured values, Body is
// sent only when BodySet, Query edits the captured URL's parameters, and
// CheckScope authorizes each URL before it is requested; the surface wires the
// manager's scope allowlist here.
type ReplayRequest struct {
	Target     string
	Method     string
	Headers    map[string]string
	Query      map[string]string
	Body       string
	BodySet    bool
	MaxBytes   int64
	CheckScope func(rawURL string) error
}

// PageReplay resends one captured network entry through the engine, which reads
// the retained request body over CDP and enforces CheckScope before any network
// call. Query edits are applied to the captured URL first, so the engine sends
// the edited URL and the result's request.url shows it. The result is the
// engine's JSON envelope.
func PageReplay(ctx context.Context, p *browsers.Page, req ReplayRequest) (string, error) {
	spec := browsers.ReplaySpec{
		Target:     req.Target,
		Method:     req.Method,
		Headers:    req.Headers,
		Body:       req.Body,
		BodySet:    req.BodySet,
		MaxBytes:   req.MaxBytes,
		CheckScope: req.CheckScope,
	}
	if len(req.Query) > 0 {
		if rawURL, ok := replayCapturedURL(p, req.Target); ok {
			edited, err := replayQueryURL(rawURL, req.Query)
			if err != nil {
				return "", err
			}
			spec.URL = edited
		}
	}
	result, err := p.Replay(ctx, spec)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("replay: encode result for %s: %w", req.Target, err)
	}
	return string(encoded), nil
}

// replayCapturedURL resolves target to the URL of exactly one captured entry.
// It reports false when the target is blank, unknown or ambiguous so the engine
// can report its own canonical target error.
func replayCapturedURL(p *browsers.Page, target string) (string, bool) {
	entries, _ := p.NetworkEntries()
	target = strings.TrimSpace(target)
	if target == "" {
		return "", false
	}
	if seq, err := strconv.ParseInt(target, 10, 64); err == nil {
		for _, entry := range entries {
			if entry.Seq == seq {
				return entry.URL, true
			}
		}
		return "", false
	}
	matched := ""
	count := 0
	for _, entry := range entries {
		if strings.Contains(entry.URL, target) {
			matched = entry.URL
			count++
		}
	}
	return matched, count == 1
}

// replayQueryURL applies query edits to rawURL: a non-empty value replaces the
// parameter and an empty value removes it. Every other parameter is kept.
func replayQueryURL(rawURL string, edits map[string]string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("replay: captured URL %q is not parseable: %w", rawURL, err)
	}
	query := u.Query()
	for name, value := range edits {
		if value == "" {
			query.Del(name)
			continue
		}
		query.Set(name, value)
	}
	u.RawQuery = query.Encode()
	return u.String(), nil
}

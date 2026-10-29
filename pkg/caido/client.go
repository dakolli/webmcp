// Package caido reads Caido proxy history through its GraphQL endpoint.
package caido

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dakolli/webmcp/pkg/config"
)

const (
	defaultURL    = "http://127.0.0.1:8080/graphql"
	requestBudget = 15 * time.Second
	maxLimit      = 100
	maxBodyBytes  = 65536
	maxBodyFetch  = 8 << 20
	maxPages      = 5
	maxPageRows   = 100
	maxResponse   = 64 << 20
)

// probeQuery is the cheapest authenticated query used to test a bearer token.
const probeQuery = `query Probe { requestsByOffset(limit: 1) { count { value } } }`

// metadataQuery lists history without bodies, newest first.
const metadataQuery = `query Search($limit: Int, $offset: Int, $filter: HTTPQLInput) {
  requestsByOffset(limit: $limit, offset: $offset, filter: $filter, order: {by: CREATED_AT, ordering: DESC}) {
    count { value }
    nodes {
      id host method path query port isTls length createdAt
      response { id statusCode length roundtripTime createdAt }
    }
  }
}`

// statusQuery reads the project, scopes, capture settings and history count in
// one request. The aliases keep the four field groups distinct in the response.
const statusQuery = `query Status {
  project: currentProject { project { id name path status temporary } readOnly }
  scopes: scopes { id name allowlist denylist }
  capture: passthroughOptions { captureList passthroughList outOfScope }
  history: requestsByOffset(limit: 1) { count { value } }
}`

// guestMutation exchanges guest access for an access token.
const guestMutation = `mutation Guest { loginAsGuest { token { accessToken refreshToken expiresAt } error { __typename } } }`

var errTokenRejected = errors.New("caido: token rejected")

// ErrNoSession reports that every configured credential was rejected and guest
// access is disabled.
var ErrNoSession = errors.New("caido: no session — the instance rejected the credential and guest access is off")

// ErrNoCredential reports that no usable token is cached, so a search cannot
// run.
var ErrNoCredential = errors.New("caido: no credential — set pat, or set access_token, or set allow_guest = true")

// ErrGuestRejected reports that the instance refused a guest session.
var ErrGuestRejected = errors.New("caido: guest access rejected — the instance does not allow guests; set access_token or pat")

// AuthPath names the credential path Login used, for `browser config show`.
type AuthPath string

const (
	AuthAccessToken AuthPath = "access_token"
	AuthPAT         AuthPath = "pat"
	AuthPATGuest    AuthPath = "pat (rejected), guest"
	AuthGuest       AuthPath = "guest"
	AuthNone        AuthPath = "none"
)

// RawDecodeError wraps the base64 failure behind ParseRaw.
type RawDecodeError struct {
	Err error
}

// Error returns the underlying base64 error text.
func (e *RawDecodeError) Error() string { return e.Err.Error() }

// Unwrap exposes the underlying base64 error.
func (e *RawDecodeError) Unwrap() error { return e.Err }

// Client is a Caido GraphQL client holding one cached access token.
type Client struct {
	HTTP     *http.Client
	URL      string
	mu       sync.Mutex
	token    string
	expires  time.Time
	authPath AuthPath
}

// NewClient returns a client for the GraphQL endpoint, defaulting to the local
// Caido instance when url is empty. Every request has a 15 second timeout.
func NewClient(url string) *Client {
	if url == "" {
		url = defaultURL
	}
	return &Client{
		HTTP: &http.Client{Timeout: requestBudget},
		URL:  url,
	}
}

// Query describes one history search.
type Query struct {
	URL       string
	Method    string
	Status    int
	Limit     int
	BodyBytes int
	Since     time.Duration
	Part      string
	Raw       bool
}

// Result is the bounded outcome of one search. Count is the rows returned,
// Total is the whole matching history on the server, Scanned is the rows
// examined, and Dropped is the rows the url/since filter rejected. The applied
// fields are set only when a request was clamped.
type Result struct {
	Entries          []Entry `json:"entries"`
	Count            int     `json:"count"`
	Total            int     `json:"total"`
	Scanned          int     `json:"scanned"`
	Dropped          int     `json:"dropped"`
	LimitApplied     int     `json:"limit_applied,omitempty"`
	BodyBytesApplied int     `json:"body_bytes_applied,omitempty"`
}

// Entry is one HTTP transaction reduced to its request and response.
type Entry struct {
	ID            string              `json:"id"`
	Method        string              `json:"method"`
	URL           string              `json:"url"`
	Host          string              `json:"host"`
	Path          string              `json:"path"`
	Status        int                 `json:"status"`
	Mime          string              `json:"mime,omitempty"`
	Bytes         int                 `json:"bytes"`
	ResponseBytes int                 `json:"response_bytes"`
	MS            int                 `json:"ms"`
	CreatedAt     string              `json:"created_at,omitempty"`
	ReqHeaders    map[string][]string `json:"request_headers,omitempty"`
	RespHeaders   map[string][]string `json:"response_headers,omitempty"`
	ReqBody       string              `json:"request_body,omitempty"`
	RespBody      string              `json:"response_body,omitempty"`
	ReqCut        bool                `json:"request_cut,omitempty"`
	RespCut       bool                `json:"response_cut,omitempty"`
	URLRedacted   bool                `json:"url_redacted,omitempty"`
}

// Status is the resolved view of the Caido instance that caido_search reads.
// Selected is the instance's project name and Configured is the [caido] project
// value, so a mismatch is visible before the launch switch exists.
type Status struct {
	Auth    string        `json:"auth"`
	URL     string        `json:"url"`
	Project ProjectStatus `json:"project"`
	Scopes  []ScopeStatus `json:"scopes"`
	Capture Capture       `json:"capture"`
	History int           `json:"history"`
}

// ProjectStatus is the current project with its read-only flag.
type ProjectStatus struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	Status     string `json:"status"`
	Temporary  bool   `json:"temporary"`
	ReadOnly   bool   `json:"read_only"`
	Selected   string `json:"selected"`
	Configured string `json:"configured"`
}

// ScopeStatus is one scope with a rendered allowlist verdict. Matches is
// "all traffic" for an empty allowlist and "allowlist" otherwise.
type ScopeStatus struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Allowlist []string `json:"allowlist"`
	Denylist  []string `json:"denylist"`
	Matches   string   `json:"matches"`
	Focus     bool     `json:"focus"`
}

// Capture is the passthroughOptions object. Unfiltered reports the null case,
// where Caido records every request and only that single field is emitted.
type Capture struct {
	CaptureList     []string `json:"capture_list,omitempty"`
	PassthroughList []string `json:"passthrough_list,omitempty"`
	OutOfScope      *bool    `json:"out_of_scope,omitempty"`
	Unfiltered      bool     `json:"unfiltered,omitempty"`
}

// Login caches a usable token and reports which path authenticated. It tries an
// access token as the bearer, then a PAT, then a guest session when allowGuest
// is true. A rejected credential falls through to the next step.
func (c *Client) Login(
	ctx context.Context,
	accessToken string,
	pat string,
	allowGuest bool,
) (AuthPath, error) {
	c.mu.Lock()
	cached, expiry, path := c.token, c.expires, c.authPath
	c.mu.Unlock()
	if cached != "" && (expiry.IsZero() || time.Now().Before(expiry)) {
		return path, nil
	}

	if accessToken != "" {
		err := c.call(ctx, accessToken, probeQuery, nil, nil)
		if err == nil {
			c.store(accessToken, time.Time{}, AuthAccessToken)
			return AuthAccessToken, nil
		}
		if !errors.Is(err, errTokenRejected) {
			return AuthNone, err
		}
	}

	patRejected := false
	if pat != "" {
		err := c.call(ctx, pat, probeQuery, nil, nil)
		if err == nil {
			c.store(pat, time.Time{}, AuthPAT)
			return AuthPAT, nil
		}
		if !errors.Is(err, errTokenRejected) {
			return AuthNone, err
		}
		patRejected = true
	}

	if !allowGuest {
		return AuthNone, ErrNoSession
	}

	token, expires, err := c.loginAsGuest(ctx)
	if err != nil {
		return AuthNone, err
	}
	path = AuthGuest
	if patRejected {
		path = AuthPATGuest
	}
	c.store(token, expires, path)
	return path, nil
}

// Search reads newest-first history metadata, filters it client-side, then
// fetches raw messages only for the rows it keeps. It pages back at most
// maxPages pages of maxPageRows rows to satisfy the url/since filter.
func (c *Client) Search(ctx context.Context, q Query) (Result, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	limitApplied := 0
	if limit > maxLimit {
		limit = maxLimit
		limitApplied = maxLimit
	}
	bodyBytes := q.BodyBytes
	if bodyBytes < 0 {
		bodyBytes = 0
	}
	bodyBytesApplied := 0
	if bodyBytes > maxBodyBytes {
		bodyBytes = maxBodyBytes
		bodyBytesApplied = maxBodyBytes
	}
	part := strings.ToLower(strings.TrimSpace(q.Part))
	if part == "" {
		part = "both"
	}
	wantReq := part != "response"
	wantResp := part != "request"

	token := c.currentToken()
	if token == "" {
		return Result{}, ErrNoCredential
	}

	cutoff := int64(0)
	if q.Since > 0 {
		cutoff = time.Now().Add(-q.Since).UnixMilli()
	}
	needle := strings.ToLower(strings.TrimSpace(q.URL))
	// Always scan full pages, so the documented bound (maxPages × maxPageRows)
	// is the real scan depth whatever the caller asked to receive.
	pageSize := maxPageRows

	kept := make([]keptRow, 0, limit)
	scanned, dropped, total := 0, 0, 0

search:
	for page := 0; page < maxPages; page++ {
		vars := map[string]any{"limit": pageSize, "offset": page * pageSize}
		if code := buildHTTPQL(q.Method, q.Status); code != "" {
			vars["filter"] = map[string]string{"code": code}
		}
		var meta struct {
			RequestsByOffset connection `json:"requestsByOffset"`
		}
		if err := c.call(ctx, token, metadataQuery, vars, &meta); err != nil {
			var gql *graphqlError
			if errors.As(err, &gql) {
				return Result{}, fmt.Errorf("caido: requestsByOffset failed: %s", gql.Error())
			}
			return Result{}, err
		}
		if page == 0 {
			total = meta.RequestsByOffset.Count.Value
		}
		rows := meta.RequestsByOffset.Nodes
		for _, n := range rows {
			scanned++
			url := composeURL(n)
			if needle != "" && !strings.Contains(strings.ToLower(url), needle) {
				dropped++
				continue
			}
			if cutoff > 0 && n.CreatedAt > 0 && n.CreatedAt < cutoff {
				dropped++
				break search
			}
			row := keptRow{
				entry:  entryFromNode(n, url),
				reqLen: n.Length,
			}
			if n.Response != nil {
				row.respID = n.Response.ID
				row.respLen = n.Response.Length
			}
			kept = append(kept, row)
			if len(kept) == limit {
				break search
			}
		}
		if len(rows) < pageSize {
			break
		}
	}

	if len(kept) > 0 {
		if err := c.fillBodies(ctx, token, kept, bodyBytes, q.Raw, wantReq, wantResp); err != nil {
			return Result{}, err
		}
	}

	entries := make([]Entry, len(kept))
	for i := range kept {
		entries[i] = kept[i].entry
		if !q.Raw {
			if scrubbed, n := RedactURL(entries[i].URL); n > 0 {
				entries[i].URL = scrubbed
				entries[i].URLRedacted = true
			}
		}
	}
	return Result{
		Entries:          entries,
		Count:            len(entries),
		Total:            total,
		Scanned:          scanned,
		Dropped:          dropped,
		LimitApplied:     limitApplied,
		BodyBytesApplied: bodyBytesApplied,
	}, nil
}

// Status reads the project, every scope, the capture settings and the history
// count in one GraphQL request. A configured project or scope name is only
// compared, never written.
func (c *Client) Status(
	ctx context.Context,
	configuredProject string,
	focusScope string,
) (Status, error) {
	token := c.currentToken()
	if token == "" {
		return Status{}, ErrNoCredential
	}

	var data struct {
		Project *struct {
			Project struct {
				ID        string `json:"id"`
				Name      string `json:"name"`
				Path      string `json:"path"`
				Status    string `json:"status"`
				Temporary bool   `json:"temporary"`
			} `json:"project"`
			ReadOnly bool `json:"readOnly"`
		} `json:"project"`
		Scopes []struct {
			ID        string   `json:"id"`
			Name      string   `json:"name"`
			Allowlist []string `json:"allowlist"`
			Denylist  []string `json:"denylist"`
		} `json:"scopes"`
		Capture *struct {
			CaptureList     []string `json:"captureList"`
			PassthroughList []string `json:"passthroughList"`
			OutOfScope      bool     `json:"outOfScope"`
		} `json:"capture"`
		History struct {
			Count struct {
				Value int `json:"value"`
			} `json:"count"`
		} `json:"history"`
	}
	if err := c.call(ctx, token, statusQuery, nil, &data); err != nil {
		var gql *graphqlError
		if errors.As(err, &gql) {
			return Status{}, fmt.Errorf("caido: status query failed: %s", gql.Error())
		}
		return Status{}, err
	}

	configuredProject = strings.TrimSpace(configuredProject)
	focusScope = strings.TrimSpace(focusScope)
	st := Status{
		Auth:    authMode(c.currentAuth()),
		URL:     c.URL,
		Scopes:  make([]ScopeStatus, 0, len(data.Scopes)),
		History: data.History.Count.Value,
		Project: ProjectStatus{Configured: configuredProject},
	}
	if data.Project != nil {
		st.Project.ID = data.Project.Project.ID
		st.Project.Name = data.Project.Project.Name
		st.Project.Path = data.Project.Project.Path
		st.Project.Status = data.Project.Project.Status
		st.Project.Temporary = data.Project.Project.Temporary
		st.Project.ReadOnly = data.Project.ReadOnly
		st.Project.Selected = data.Project.Project.Name
	}

	if data.Capture == nil {
		st.Capture = Capture{Unfiltered: true}
	} else {
		outOfScope := data.Capture.OutOfScope
		st.Capture = Capture{
			CaptureList:     emptyIfNil(data.Capture.CaptureList),
			PassthroughList: emptyIfNil(data.Capture.PassthroughList),
			OutOfScope:      &outOfScope,
		}
	}

	for _, s := range data.Scopes {
		matches := "allowlist"
		if len(s.Allowlist) == 0 {
			matches = "all traffic"
		}
		st.Scopes = append(st.Scopes, ScopeStatus{
			ID:        s.ID,
			Name:      s.Name,
			Allowlist: emptyIfNil(s.Allowlist),
			Denylist:  emptyIfNil(s.Denylist),
			Matches:   matches,
			Focus:     focusScope != "" && s.Name == focusScope,
		})
	}
	return st, nil
}

// Selection returns the [caido] project and scope_name from webmcp.toml. It
// reads the file directly because config.CaidoFile does not carry those keys
// yet; WEB-264 replaces this with typed fields.
func Selection() (project, scope string) {
	data, err := os.ReadFile(config.CaidoConfigPath())
	if err != nil {
		return "", ""
	}
	doc, err := config.ParseTOML(data)
	if err != nil {
		return "", ""
	}
	project, _ = doc.String("caido", "project")
	scope, _ = doc.String("caido", "scope_name")
	return strings.TrimSpace(project), strings.TrimSpace(scope)
}

// authMode renders the credential path as the mode that actually worked.
func authMode(path AuthPath) string {
	if path == AuthPATGuest {
		return "guest"
	}
	if path == "" {
		return string(AuthNone)
	}
	return string(path)
}

// emptyIfNil renders an absent list as [] so the JSON stays addressable.
func emptyIfNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// currentAuth returns the credential path under the lock.
func (c *Client) currentAuth() AuthPath {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.authPath
}

// keptRow carries the entry plus the ids and declared lengths needed for the
// body phase.
type keptRow struct {
	entry   Entry
	respID  string
	reqLen  int
	respLen int
}

// fillBodies fetches the raw request and response messages in one aliased
// query. A side whose declared length exceeds maxBodyFetch is refused outright
// (headers included) and marked cut, because transferring it is the risk.
func (c *Client) fillBodies(
	ctx context.Context,
	token string,
	rows []keptRow,
	bodyBytes int,
	plain bool,
	wantReq bool,
	wantResp bool,
) error {
	type ref struct {
		alias string
		index int
		side  string
	}
	var query strings.Builder
	query.WriteString("query Bodies {")
	refs := make([]ref, 0, len(rows)*2)
	for i := range rows {
		if wantReq && rows[i].entry.ID != "" {
			if rows[i].reqLen > maxBodyFetch {
				rows[i].entry.ReqCut = true
			} else {
				alias := "r" + strconv.Itoa(i)
				fmt.Fprintf(&query, " %s: request(id: %s) { raw }", alias, strconv.Quote(rows[i].entry.ID))
				refs = append(refs, ref{alias: alias, index: i, side: "request"})
			}
		}
		if wantResp && rows[i].respID != "" {
			if rows[i].respLen > maxBodyFetch {
				rows[i].entry.RespCut = true
			} else {
				alias := "s" + strconv.Itoa(i)
				fmt.Fprintf(&query, " %s: response(id: %s) { raw }", alias, strconv.Quote(rows[i].respID))
				refs = append(refs, ref{alias: alias, index: i, side: "response"})
			}
		}
	}
	query.WriteString(" }")
	if len(refs) == 0 {
		return nil
	}

	var data map[string]*struct {
		Raw *string `json:"raw"`
	}
	if err := c.call(ctx, token, query.String(), nil, &data); err != nil {
		var gql *graphqlError
		if errors.As(err, &gql) {
			return fmt.Errorf("caido: raw bodies failed: %s", gql.Error())
		}
		return err
	}
	for _, r := range refs {
		body := data[r.alias]
		if body == nil || body.Raw == nil {
			continue
		}
		if err := applyRaw(&rows[r.index].entry, r.side, *body.Raw, bodyBytes, plain); err != nil {
			return err
		}
	}
	return nil
}

// applyRaw splits one raw message and writes the chosen side onto the entry.
func applyRaw(e *Entry, side, raw string, bodyBytes int, plain bool) error {
	_, headers, body, err := ParseRaw(raw)
	if err != nil {
		var decode *RawDecodeError
		if errors.As(err, &decode) {
			return fmt.Errorf("caido: raw message from request %s is not base64: %w", e.ID, decode.Err)
		}
		return err
	}
	// Redact the whole body before the cut: a JSON body trimmed mid-object no
	// longer parses, so cutting first would let credentials through.
	text := string(body)
	if !plain {
		headers = RedactHeaders(headers)
		text, _, err = RedactBody(text)
		if err != nil {
			return err
		}
	}
	text, cut := cutBody([]byte(text), bodyBytes)
	if side == "request" {
		e.ReqHeaders = headers
		e.ReqBody = text
		e.ReqCut = cut
		return nil
	}
	e.RespHeaders = headers
	e.RespBody = text
	e.RespCut = cut
	if mime := contentType(headers); mime != "" {
		e.Mime = mime
	}
	return nil
}

// ParseRaw decodes a base64 HTTP/1.1 message into its start line, headers and
// body. Header names are lowercased and the first value wins.
func ParseRaw(raw string) (string, map[string][]string, []byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return "", nil, nil, &RawDecodeError{Err: err}
	}
	head, body, found := bytes.Cut(decoded, []byte("\r\n\r\n"))
	if !found {
		head, body = decoded, nil
	}
	lines := bytes.Split(head, []byte("\r\n"))
	headers := make(map[string][]string, len(lines))
	if len(lines) == 0 {
		return "", headers, body, nil
	}
	startLine := string(lines[0])
	for _, line := range lines[1:] {
		name, value, ok := bytes.Cut(line, []byte(":"))
		if !ok {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(string(name)))
		if key == "" {
			continue
		}
		if _, exists := headers[key]; exists {
			continue
		}
		headers[key] = []string{strings.TrimSpace(string(value))}
	}
	return startLine, headers, body, nil
}

// cutBody caps a body at limit bytes, trimming a partial trailing rune. A
// non-positive limit means the caller asked for no body at all, so the result is
// empty and not reported as a cut.
func cutBody(body []byte, limit int) (string, bool) {
	if limit <= 0 {
		return "", false
	}
	if len(body) <= limit {
		return string(body), false
	}
	b := body[:limit]
	for i := 0; i < utf8.UTFMax-1 && len(b) > 0 && !utf8.Valid(b); i++ {
		b = b[:len(b)-1]
	}
	return string(b), true
}

// contentType returns the media type without parameters.
func contentType(headers map[string][]string) string {
	values := headers["content-type"]
	if len(values) == 0 {
		return ""
	}
	media, _, _ := strings.Cut(values[0], ";")
	return strings.TrimSpace(media)
}

// requestNode is one row of the metadata query.
type requestNode struct {
	ID        string        `json:"id"`
	Host      string        `json:"host"`
	Method    string        `json:"method"`
	Path      string        `json:"path"`
	Query     string        `json:"query"`
	Port      int           `json:"port"`
	IsTLS     bool          `json:"isTls"`
	Length    int           `json:"length"`
	CreatedAt int64         `json:"createdAt"`
	Response  *responseNode `json:"response"`
}

// responseNode is the response half of one row.
type responseNode struct {
	ID            string `json:"id"`
	StatusCode    int    `json:"statusCode"`
	Length        int    `json:"length"`
	RoundtripTime int    `json:"roundtripTime"`
	CreatedAt     int64  `json:"createdAt"`
}

// connection is the Relay-style page returned by requestsByOffset.
type connection struct {
	Count struct {
		Value int `json:"value"`
	} `json:"count"`
	Nodes []requestNode `json:"nodes"`
}

// entryFromNode maps one metadata row onto the result entry.
func entryFromNode(n requestNode, url string) Entry {
	e := Entry{
		ID:        n.ID,
		Method:    n.Method,
		URL:       url,
		Host:      n.Host,
		Path:      n.Path,
		Bytes:     n.Length,
		CreatedAt: formatMillis(n.CreatedAt),
	}
	if n.Response != nil {
		e.Status = n.Response.StatusCode
		e.ResponseBytes = n.Response.Length
		e.MS = n.Response.RoundtripTime
	}
	return e
}

// composeURL builds the URL the schema does not expose as a field.
func composeURL(n requestNode) string {
	scheme, defaultPort := "http", 80
	if n.IsTLS {
		scheme, defaultPort = "https", 443
	}
	var b strings.Builder
	b.WriteString(scheme)
	b.WriteString("://")
	b.WriteString(n.Host)
	if n.Port != 0 && n.Port != defaultPort {
		b.WriteString(":")
		b.WriteString(strconv.Itoa(n.Port))
	}
	path := n.Path
	if path == "" {
		path = "/"
	}
	b.WriteString(path)
	if n.Query != "" {
		b.WriteString("?")
		b.WriteString(n.Query)
	}
	return b.String()
}

// formatMillis renders epoch milliseconds as RFC3339.
func formatMillis(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

// buildHTTPQL composes the only server-side filter: method and status code.
func buildHTTPQL(method string, status int) string {
	clauses := make([]string, 0, 2)
	if method != "" {
		clauses = append(clauses, fmt.Sprintf("req.method.eq:%q", strings.ToUpper(method)))
	}
	if status > 0 {
		clauses = append(clauses, fmt.Sprintf("resp.code.eq:%d", status))
	}
	return strings.Join(clauses, " AND ")
}

// currentToken returns the cached token under the lock.
func (c *Client) currentToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

// store records a token, its expiry and the path that authenticated.
func (c *Client) store(token string, expires time.Time, path AuthPath) {
	c.mu.Lock()
	c.token, c.expires, c.authPath = token, expires, path
	c.mu.Unlock()
}

// gqlError is one GraphQL error envelope member.
type gqlError struct {
	Message    string `json:"message"`
	Extensions struct {
		Caido struct {
			Reason string `json:"reason"`
			Code   string `json:"code"`
		} `json:"CAIDO"`
	} `json:"extensions"`
}

// graphqlError carries the server's error messages to the caller.
type graphqlError struct {
	messages []string
}

// Error joins the GraphQL messages the server returned.
func (e *graphqlError) Error() string { return strings.Join(e.messages, "; ") }

// call performs one GraphQL POST, mapping transport and auth failures to the
// exact messages the agent needs.
func (c *Client) call(ctx context.Context, token, query string, vars map[string]any, out any) error {
	payload := map[string]any{"query": query}
	if len(vars) > 0 {
		payload["variables"] = vars
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("caido: request failed: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("caido: request failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		if isDialError(err) {
			return fmt.Errorf("caido: no instance at %s — start it with make caido", c.URL)
		}
		return fmt.Errorf("caido: request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return fmt.Errorf("caido: request failed: %w", err)
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []gqlError      `json:"errors"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("caido: request failed: %w", err)
	}
	if len(envelope.Errors) > 0 {
		if unauthorized(envelope.Errors) {
			return errTokenRejected
		}
		messages := make([]string, len(envelope.Errors))
		for i, e := range envelope.Errors {
			messages[i] = e.Message
		}
		return &graphqlError{messages: messages}
	}
	if out != nil && len(envelope.Data) > 0 && string(envelope.Data) != "null" {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("caido: request failed: %w", err)
		}
	}
	return nil
}

// loginAsGuest logs in as a guest and returns its access token and expiry.
func (c *Client) loginAsGuest(ctx context.Context) (string, time.Time, error) {
	var data struct {
		LoginAsGuest struct {
			Token *struct {
				AccessToken string `json:"accessToken"`
				ExpiresAt   string `json:"expiresAt"`
			} `json:"token"`
		} `json:"loginAsGuest"`
	}
	if err := c.call(ctx, "", guestMutation, nil, &data); err != nil {
		var gql *graphqlError
		if errors.Is(err, errTokenRejected) || errors.As(err, &gql) {
			return "", time.Time{}, ErrGuestRejected
		}
		return "", time.Time{}, err
	}
	token := data.LoginAsGuest.Token
	if token == nil || token.AccessToken == "" {
		return "", time.Time{}, ErrGuestRejected
	}
	expires := time.Now().Add(7 * 24 * time.Hour)
	if token.ExpiresAt != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, token.ExpiresAt); err == nil {
			expires = parsed
		}
	}
	return token.AccessToken, expires, nil
}

// unauthorized reports whether any error carries the AUTHORIZATION code or the
// INVALID_TOKEN reason, the two shapes Caido uses for a rejected bearer.
func unauthorized(errs []gqlError) bool {
	for _, e := range errs {
		if strings.EqualFold(e.Extensions.Caido.Code, "AUTHORIZATION") ||
			strings.EqualFold(e.Extensions.Caido.Reason, "INVALID_TOKEN") {
			return true
		}
	}
	return false
}

// isDialError reports whether a request failed while connecting.
func isDialError(err error) bool {
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
}

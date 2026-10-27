// Package har encodes observed page traffic as HAR 1.2 that imports into
// Caido, Burp and Chrome DevTools. It accepts the engine's plain network
// entries plus optional body fetchers, so it imports nothing from pkg/mcp or
// pkg/cli: every surface passes data in.
package har

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"webmcp/pkg/browsers"
)

const (
	// DefaultMaxBytes caps one HAR export at 512 KB. The oldest entries are
	// dropped first until the encoded document fits.
	DefaultMaxBytes = 512 << 10
	// maxBodyFetches bounds the bodies one export fetches, so a heavy page
	// cannot hang the export.
	maxBodyFetches = 50
	// maxBodyFetchBytes bounds the decoded body bytes one export fetches at
	// 8 MB.
	maxBodyFetchBytes = 8 << 20
	// bodyNoteLimit bounds one body-failure reason quoted in log.comment.
	bodyNoteLimit = 200
	// pageID is the single page ref this export describes.
	pageID = "page_1"
)

// PageInfo identifies the page an export describes.
type PageInfo struct {
	// Title is the page title reported at export time.
	Title string
}

// Options controls the output cap, the ring-loss count and the optional body
// fetchers. A nil fetcher keeps the export header-only.
type Options struct {
	// MaxBytes caps the encoded document. Zero uses DefaultMaxBytes; a negative
	// value exports every buffered entry instead, so the cap drops nothing and
	// log.truncated stays 0.
	MaxBytes int
	// Dropped counts entries the capture ring evicted before export. They are
	// added to log.truncated and stated in log.comment.
	Dropped int
	// ResponseBody fetches the response body of the entry with the given Seq.
	// Fetches run only for entries that survive the cap, newest first, one body
	// at a time, until the export's budget runs out.
	ResponseBody BodyFetcher
	// RequestBody fetches the request post body of the entry with the given
	// Seq. It is consulted only when the captured request headers announce a
	// body, mirroring the replay seam's rule.
	RequestBody BodyFetcher
	// Cookies snapshots the browser cookie jar once per export. The encoder
	// matches the snapshot to each request URL and fills request.cookies; a nil
	// fetcher leaves both cookie arrays empty. A fetch failure is stated in
	// log.comment and never fails the export.
	Cookies CookieFetcher
	// Secrets keeps cookie values instead of replacing them with the redaction
	// marker. It mirrors the page's header secret-capture setting.
	Secrets bool
}

// CookieFetcher snapshots the browser's cookie jar. The encoder calls it once,
// before matching the jar to the exported entries.
type CookieFetcher func() ([]browsers.Cookie, error)

// BodyFetcher fetches one body for the entry with the given Seq. The caller
// binds its own context and page locking; the encoder calls it serially, never
// more than one fetch at a time.
type BodyFetcher func(seq int64) (Body, error)

// Body is one fetched payload. Text holds UTF-8 content and Base64 holds binary
// content encoded with standard base64; they are mutually exclusive. Bytes is
// the decoded payload size, never the encoded length, and a zero falls back to
// the text length.
type Body struct {
	Text   string
	Base64 string
	Bytes  int64
}

// payload returns the text to export, its HAR encoding name and the decoded
// byte count. Binary content becomes base64, which every HAR reader decodes.
func (b Body) payload() (string, string, int64) {
	size := b.Bytes
	if size <= 0 {
		size = int64(len(b.Text))
	}
	if b.Base64 != "" {
		return b.Base64, "base64", size
	}
	if utf8.ValidString(b.Text) {
		return b.Text, "", size
	}
	return base64.StdEncoding.EncodeToString([]byte(b.Text)), "base64", size
}

// Archive is the HAR 1.2 document root.
type Archive struct {
	Log Log `json:"log"`
}

// Log is the HAR 1.2 log object. Truncated and BodiesSkipped are project custom
// fields. Truncated counts the entries missing from this export because the
// size cap dropped them plus the entries the capture ring evicted; an uncapped
// export reports 0 there and states any ring loss in Comment. BodiesSkipped
// counts the surviving entries whose bodies were never fetched because the
// export's body budget ran out. Both are always emitted, as 0 when nothing was
// skipped.
type Log struct {
	Version       string  `json:"version"`
	Creator       Creator `json:"creator"`
	Pages         []Page  `json:"pages"`
	Entries       []Entry `json:"entries"`
	Comment       string  `json:"comment,omitempty"`
	Truncated     int     `json:"truncated"`
	BodiesSkipped int     `json:"bodies_skipped"`
}

// Creator names the application that produced the archive.
type Creator struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Page is one exported page and its load timings.
type Page struct {
	StartedDateTime string      `json:"startedDateTime"`
	ID              string      `json:"id"`
	Title           string      `json:"title"`
	PageTimings     PageTimings `json:"pageTimings"`
}

// PageTimings carries the page load milestones. Unknown milestones are -1.
type PageTimings struct {
	OnContentLoad float64 `json:"onContentLoad"`
	OnLoad        float64 `json:"onLoad"`
}

// Entry is one exported request/response round trip.
type Entry struct {
	Pageref         string   `json:"pageref,omitempty"`
	StartedDateTime string   `json:"startedDateTime"`
	Time            float64  `json:"time"`
	Request         Request  `json:"request"`
	Response        Response `json:"response"`
	Cache           Cache    `json:"cache"`
	Timings         Timings  `json:"timings"`
	// ServerIPAddress is the peer IP Chrome reported for the response. It is a
	// standard HAR 1.2 field and is omitted when Chrome did not report one.
	ServerIPAddress string `json:"serverIPAddress,omitempty"`
	// FrameID is the CDP frame that issued the request. It is a project custom
	// field, emitted only when CDP reported one.
	FrameID string `json:"_frameId,omitempty"`
	// LoaderID is the CDP loader of that frame's document. It is a project
	// custom field, emitted only when CDP reported one.
	LoaderID string `json:"_loaderId,omitempty"`
	// TopDocument marks the request that produced the page's current top-level
	// document, emitted only on that entry. It is a project custom field.
	TopDocument bool `json:"_topDocument,omitempty"`

	// seq links the entry back to the observed network entry, so a body fetcher
	// can address it. It is never serialized.
	seq int64
}

// Request describes the outgoing request.
type Request struct {
	Method      string    `json:"method"`
	URL         string    `json:"url"`
	HTTPVersion string    `json:"httpVersion"`
	Headers     []Header  `json:"headers"`
	QueryString []Query   `json:"queryString"`
	PostData    *PostData `json:"postData,omitempty"`
	Cookies     []Cookie  `json:"cookies"`
	HeadersSize int64     `json:"headersSize"`
	BodySize    int64     `json:"bodySize"`
}

// PostData is a captured request body. HAR 1.2 has no request-body encoding
// field, so a binary body is base64-encoded into Text, which is what Caido,
// Burp and DevTools accept for an opaque payload.
type PostData struct {
	MimeType string `json:"mimeType"`
	Text     string `json:"text"`
}

// Response describes the returned response.
type Response struct {
	Status      int      `json:"status"`
	StatusText  string   `json:"statusText"`
	HTTPVersion string   `json:"httpVersion"`
	Headers     []Header `json:"headers"`
	Cookies     []Cookie `json:"cookies"`
	Content     Content  `json:"content"`
	RedirectURL string   `json:"redirectURL"`
	HeadersSize int64    `json:"headersSize"`
	BodySize    int64    `json:"bodySize"`
}

// Header is one name/value header pair.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Query is one name/value query parameter.
type Query struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Cookie is one HAR cookie. Request cookies come from the browser jar and are
// matched to the request URL; response cookies stay empty because CDP does not
// report Set-Cookie. Values are redacted unless secret capture is on. Every
// scope field is always emitted, with expires empty for a session cookie.
type Cookie struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Path     string `json:"path"`
	Domain   string `json:"domain"`
	Expires  string `json:"expires"`
	HTTPOnly bool   `json:"httpOnly"`
	Secure   bool   `json:"secure"`
}

// Content describes the response body. Text and Encoding are absent until the
// body is fetched; Size is then the decoded byte count, never the encoded one.
type Content struct {
	Size     int64  `json:"size"`
	MimeType string `json:"mimeType"`
	Text     string `json:"text,omitempty"`
	Encoding string `json:"encoding,omitempty"`
}

// Cache is an empty cache object: no cache state was observed.
type Cache struct{}

// Timings holds the request phases in milliseconds. Unmeasured phases are -1.
type Timings struct {
	Blocked float64 `json:"blocked"`
	DNS     float64 `json:"dns"`
	Connect float64 `json:"connect"`
	Send    float64 `json:"send"`
	Wait    float64 `json:"wait"`
	Receive float64 `json:"receive"`
	SSL     float64 `json:"ssl"`
}

// logMeta is the export-level accounting reported in log.comment and the
// custom log counters.
type logMeta struct {
	limit          int
	ringDropped    int
	uncapped       bool
	bodies         bool
	bodiesSkipped  int
	bodyFailures   int
	firstBodyError string
	cookies        bool
	secrets        bool
	cookieError    string
}

// Encode builds HAR 1.2 from the observed network entries and encodes it. It
// drops the oldest entries until the document fits opts.MaxBytes, fetches the
// bodies of the surviving entries within a fixed budget, and reports both
// accounting counts plus the ring loss in log.truncated and log.comment.
func Encode(
	page PageInfo,
	entries []browsers.NetworkEntry,
	opts Options,
) ([]byte, error) {
	started := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	built := make([]Entry, 0, len(entries))
	observed := make(map[int64]browsers.NetworkEntry, len(entries))
	for _, e := range entries {
		built = append(built, buildEntry(started, e))
		observed[e.Seq] = e
	}

	limit := opts.MaxBytes
	if limit == 0 {
		limit = DefaultMaxBytes
	}
	meta := logMeta{
		limit:       limit,
		ringDropped: opts.Dropped,
		uncapped:    limit < 0,
		bodies:      opts.ResponseBody != nil || opts.RequestBody != nil,
		cookies:     opts.Cookies != nil,
		secrets:     opts.Secrets,
	}

	if opts.Cookies != nil {
		attachCookies(built, opts, &meta)
	}

	kept, capDropped := built, 0
	if limit > 0 {
		fitted, _, err := fit(page, started, built, limit, meta, 0)
		if err != nil {
			return nil, err
		}
		kept, capDropped = fitted, len(built)-len(fitted)
	}

	fetchBodies(kept, observed, opts, &meta)

	if limit < 0 {
		return marshal(page, started, kept, 0, meta)
	}
	_, out, err := fit(page, started, kept, limit, meta, capDropped)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// fit returns the newest entries whose encoded document fits limit, the bytes
// of that document, and nothing else. base counts the entries a previous fit
// already dropped, so the reported truncation stays a total.
func fit(
	page PageInfo,
	started string,
	entries []Entry,
	limit int,
	meta logMeta,
	base int,
) ([]Entry, []byte, error) {
	out, err := marshal(page, started, entries, base, meta)
	if err != nil {
		return nil, nil, err
	}
	if len(out) <= limit {
		return entries, out, nil
	}

	lo, hi := 0, len(entries)
	var best []Entry
	var bestOut []byte
	for lo <= hi {
		mid := (lo + hi) / 2
		kept := entries[len(entries)-mid:]
		out, err := marshal(page, started, kept, base+len(entries)-mid, meta)
		if err != nil {
			return nil, nil, err
		}
		if len(out) <= limit {
			best, bestOut = kept, out
			lo = mid + 1
			continue
		}
		hi = mid - 1
	}
	if bestOut != nil {
		return best, bestOut, nil
	}
	empty, err := marshal(page, started, nil, base+len(entries), meta)
	if err != nil {
		return nil, nil, err
	}
	return nil, empty, nil
}

// marshal encodes one document over the given entry slice.
func marshal(
	page PageInfo,
	started string,
	entries []Entry,
	capDropped int,
	meta logMeta,
) ([]byte, error) {
	out, err := json.Marshal(newArchive(page, started, entries, capDropped, meta))
	if err != nil {
		return nil, fmt.Errorf("encode HAR: %w", err)
	}
	return out, nil
}

// bodyBudget bounds one export's body fetching so a heavy page cannot hang it.
type bodyBudget struct {
	fetches int
	bytes   int64
}

// allows reports whether another body may be fetched.
func (b *bodyBudget) allows() bool {
	return b.fetches < maxBodyFetches && b.bytes < maxBodyFetchBytes
}

// fetchBodies fills in the bodies of the entries that survived the cap, newest
// first and one fetch at a time, stopping when the budget runs out.
func fetchBodies(
	kept []Entry,
	observed map[int64]browsers.NetworkEntry,
	opts Options,
	meta *logMeta,
) {
	if opts.ResponseBody == nil && opts.RequestBody == nil {
		return
	}
	budget := bodyBudget{}
	for i := len(kept) - 1; i >= 0; i-- {
		if !budget.allows() {
			meta.bodiesSkipped = i + 1
			return
		}
		src, ok := observed[kept[i].seq]
		if !ok {
			continue
		}
		if opts.ResponseBody != nil {
			attachResponseBody(&kept[i], src, opts.ResponseBody, &budget, meta)
		}
		if opts.RequestBody != nil && requestCarriesBody(src) {
			attachRequestBody(&kept[i], src, opts.RequestBody, &budget, meta)
		}
	}
}

// attachResponseBody adds the fetched response body to the entry, keeping the
// measured size when the body cannot be read.
func attachResponseBody(
	entry *Entry,
	src browsers.NetworkEntry,
	fetch BodyFetcher,
	budget *bodyBudget,
	meta *logMeta,
) {
	body, err := fetch(src.Seq)
	budget.fetches++
	if err != nil {
		noteBodyFailure(meta, err)
		return
	}
	if body.Text == "" && body.Base64 == "" {
		return
	}
	budget.bytes += body.Bytes
	text, encoding, size := body.payload()
	entry.Response.Content.Text = text
	entry.Response.Content.Encoding = encoding
	entry.Response.Content.Size = size
}

// attachRequestBody adds the fetched request body to the entry as postData and
// reports its decoded size.
func attachRequestBody(
	entry *Entry,
	src browsers.NetworkEntry,
	fetch BodyFetcher,
	budget *bodyBudget,
	meta *logMeta,
) {
	if !budget.allows() {
		return
	}
	body, err := fetch(src.Seq)
	budget.fetches++
	if err != nil {
		noteBodyFailure(meta, err)
		return
	}
	if body.Text == "" && body.Base64 == "" {
		return
	}
	budget.bytes += body.Bytes
	text, _, size := body.payload()
	entry.Request.PostData = &PostData{MimeType: requestMIME(src), Text: text}
	entry.Request.BodySize = size
}

// noteBodyFailure records why one body is absent, keeping the first reason so
// log.comment stays one line of prose.
func noteBodyFailure(meta *logMeta, err error) {
	meta.bodyFailures++
	if meta.firstBodyError != "" {
		return
	}
	meta.firstBodyError = noteReason(err)
}

// noteReason renders an error as one bounded comment clause.
func noteReason(err error) string {
	reason := strings.Join(strings.Fields(err.Error()), " ")
	if len(reason) > bodyNoteLimit {
		reason = reason[:bodyNoteLimit] + "…"
	}
	return reason
}

// requestCarriesBody reports whether the captured request headers announce a
// body, using the same rule the replay seam applies before it fetches post data.
func requestCarriesBody(e browsers.NetworkEntry) bool {
	if headerOf(e.ReqHeaders, "Content-Type") != "" {
		return true
	}
	length := headerOf(e.ReqHeaders, "Content-Length")
	return length != "" && length != "0"
}

// requestMIME returns the request's declared content type, falling back to the
// opaque payload label when the capture omitted the header.
func requestMIME(e browsers.NetworkEntry) string {
	if mime := headerOf(e.ReqHeaders, "Content-Type"); mime != "" {
		return mime
	}
	return "application/octet-stream"
}

// headerOf returns the value of name, case-insensitively.
func headerOf(headers map[string]string, name string) string {
	for k, v := range headers {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

// attachCookies snapshots the browser cookie jar once and fills request.cookies
// on every entry. response.cookies stays empty because CDP does not report
// Set-Cookie, so populating it would be a guess.
func attachCookies(entries []Entry, opts Options, meta *logMeta) {
	jar, err := opts.Cookies()
	if err != nil {
		meta.cookieError = noteReason(err)
		return
	}
	for i := range entries {
		entries[i].Request.Cookies = requestCookies(entries[i].Request.URL, jar, opts.Secrets)
	}
}

// requestCookies returns the jar cookies a request to raw would carry, sorted by
// domain, path and name. A Secure cookie is excluded from a non-HTTPS URL, and
// the value is replaced with the redaction marker unless capture is on.
func requestCookies(
	raw string,
	jar []browsers.Cookie,
	secrets bool,
) []Cookie {
	out := []Cookie{}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return out
	}
	path := u.Path
	if path == "" {
		path = "/"
	}
	for _, c := range jar {
		if !cookieDomainMatches(c.Domain, u.Hostname()) {
			continue
		}
		if !cookiePathMatches(c.Path, path) {
			continue
		}
		if c.Secure && u.Scheme != "https" {
			continue
		}
		value := c.Value
		if !secrets {
			value = browsers.RedactedValue
		}
		out = append(out, Cookie{
			Name:     c.Name,
			Value:    value,
			Path:     c.Path,
			Domain:   c.Domain,
			Expires:  cookieExpires(c.Expires),
			HTTPOnly: c.HTTPOnly,
			Secure:   c.Secure,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		di := strings.ToLower(strings.TrimPrefix(out[i].Domain, "."))
		dj := strings.ToLower(strings.TrimPrefix(out[j].Domain, "."))
		if di != dj {
			return di < dj
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// cookieDomainMatches reports whether host is in the cookie's domain: an exact
// host match or a subdomain of a domain cookie. The leading dot is ignored.
func cookieDomainMatches(domain, host string) bool {
	d := strings.ToLower(strings.TrimPrefix(domain, "."))
	h := strings.ToLower(host)
	if d == "" || h == "" {
		return false
	}
	return h == d || strings.HasSuffix(h, "."+d)
}

// cookiePathMatches reports whether the cookie path covers the request path,
// using the RFC 6265 prefix rule.
func cookiePathMatches(cookiePath, requestPath string) bool {
	if cookiePath == "" {
		cookiePath = "/"
	}
	if requestPath == cookiePath {
		return true
	}
	if !strings.HasPrefix(requestPath, cookiePath) {
		return false
	}
	return strings.HasSuffix(cookiePath, "/") || requestPath[len(cookiePath)] == '/'
}

// cookieExpires renders an epoch expiry as HAR's ISO 8601 string, or "" for a
// session cookie.
func cookieExpires(expires int64) string {
	if expires <= 0 {
		return ""
	}
	return time.Unix(expires, 0).UTC().Format(time.RFC3339)
}

// newArchive assembles the document for one kept entry slice. capDropped counts
// entries cut to satisfy the size limit; meta carries the ring loss and body
// accounting.
func newArchive(
	page PageInfo,
	started string,
	entries []Entry,
	capDropped int,
	meta logMeta,
) Archive {
	if entries == nil {
		entries = []Entry{}
	}
	truncated := capDropped + meta.ringDropped
	if meta.uncapped {
		truncated = 0
	}
	return Archive{Log: Log{
		Version: "1.2",
		Creator: Creator{Name: "web-mcp", Version: "1.0"},
		Pages: []Page{{
			StartedDateTime: started,
			ID:              pageID,
			Title:           page.Title,
			PageTimings:     PageTimings{OnContentLoad: -1, OnLoad: -1},
		}},
		Entries:       entries,
		Comment:       comment(capDropped, meta),
		Truncated:     truncated,
		BodiesSkipped: meta.bodiesSkipped,
	}}
}

// comment states the export's provenance, redaction rule, cap, body fetching
// and the ring loss that predates it.
func comment(capDropped int, meta logMeta) string {
	var b strings.Builder
	b.WriteString("web-mcp HAR 1.2 export. ")
	switch {
	case meta.cookies && meta.cookieError != "":
		b.WriteString("The cookie snapshot failed (" + meta.cookieError +
			"), so request.cookies is empty. response.cookies is empty because " +
			"Chrome does not report Set-Cookie in CDP events. ")
	case meta.cookies:
		b.WriteString("request.cookies is snapshotted from the browser cookie " +
			"jar at export time and matched to each request URL by domain, path " +
			"and Secure scheme. response.cookies is empty because Chrome does " +
			"not report Set-Cookie in CDP events. ")
		if meta.secrets {
			b.WriteString("Cookie values are exported because secret capture is on. ")
		} else {
			b.WriteString("Cookie values are <redacted>. ")
		}
	default:
		b.WriteString("request.cookies and response.cookies are empty because no " +
			"cookie snapshot was requested. ")
	}
	b.WriteString("Authorization and X-CSRF-Token header values are <redacted>. " +
		"startedDateTime is the export time because the network ring records " +
		"monotonic timestamps only. time is the measured total and is reported " +
		"as timings.wait; unmeasured phases are -1. ")
	if meta.uncapped {
		b.WriteString("Cap: none; every buffered entry is exported and truncated is 0. ")
	} else {
		b.WriteString("Cap " + strconv.Itoa(meta.limit) + " bytes. ")
	}
	b.WriteString("A redirect hop is its own entry, with response.redirectURL " +
		"set from the hop's Location header. serverIPAddress is the peer IP " +
		"Chrome reported for the response, omitted when Chrome reported none. " +
		"_frameId and _loaderId carry the CDP document identity when the " +
		"browser reported it, and _topDocument marks the request for the " +
		"page's current top-level document.")
	if meta.bodies {
		b.WriteString(" Response and request bodies were fetched on demand for " +
			"the entries that survived the cap, newest first and one at a time; " +
			"a binary response body is base64 with content.encoding=base64, and " +
			"a request body without a Content-Type is labelled " +
			"application/octet-stream.")
	}
	if meta.bodiesSkipped > 0 {
		b.WriteString(" Body budget: the fetch limit stopped the export, so " +
			strconv.Itoa(meta.bodiesSkipped) +
			" entries carry headers only; see bodies_skipped.")
	}
	if meta.bodyFailures > 0 {
		b.WriteString(" " + strconv.Itoa(meta.bodyFailures) +
			" bodies were unavailable to the browser (first: " +
			meta.firstBodyError + ").")
	}
	if capDropped > 0 {
		b.WriteString(" Truncated: the " + strconv.Itoa(capDropped) +
			" oldest entries were dropped to fit the size cap.")
	}
	if meta.ringDropped > 0 {
		b.WriteString(" Ring loss: " + strconv.Itoa(meta.ringDropped) +
			" entries were evicted by the capture ring before export.")
	}
	return b.String()
}

// buildEntry maps one observed network entry onto the HAR entry shape.
func buildEntry(started string, e browsers.NetworkEntry) Entry {
	reqHeaders := headerList(e.ReqHeaders)
	respHeaders := headerList(e.RespHeaders)
	timeMS, timings := timing(e)
	statusText := ""
	if e.Status > 0 {
		statusText = http.StatusText(e.Status)
	}
	version := e.Protocol
	if version == "" {
		version = "HTTP/1.1"
	}
	return Entry{
		Pageref:         pageID,
		StartedDateTime: started,
		Time:            timeMS,
		ServerIPAddress: e.RemoteIP,
		FrameID:         e.FrameID,
		LoaderID:        e.LoaderID,
		TopDocument:     e.TopDocument,
		seq:             e.Seq,
		Request: Request{
			Method:      e.Method,
			URL:         e.URL,
			HTTPVersion: version,
			Headers:     reqHeaders,
			QueryString: queryString(e.URL),
			Cookies:     []Cookie{},
			HeadersSize: -1,
			BodySize:    -1,
		},
		Response: Response{
			Status:      e.Status,
			StatusText:  statusText,
			HTTPVersion: version,
			Headers:     respHeaders,
			Cookies:     []Cookie{},
			Content:     Content{Size: e.Bytes, MimeType: e.MIME},
			RedirectURL: redirectURL(e.Status, respHeaders),
			HeadersSize: -1,
			BodySize:    e.Bytes,
		},
		Cache:   Cache{},
		Timings: timings,
	}
}

// timing reports the measured total and its HAR timing split. Only the total
// round trip is measured; it is booked as wait, while the phases the engine
// does not measure stay -1. A failed or pending request has no measured total.
func timing(e browsers.NetworkEntry) (float64, Timings) {
	if e.Failed || (e.Status == 0 && e.MS == 0) {
		return -1, Timings{
			Blocked: -1,
			DNS:     -1,
			Connect: -1,
			Send:    -1,
			Wait:    -1,
			Receive: -1,
			SSL:     -1,
		}
	}
	ms := float64(e.MS)
	return ms, Timings{
		Blocked: -1,
		DNS:     -1,
		Connect: -1,
		Send:    0,
		Wait:    ms,
		Receive: 0,
		SSL:     -1,
	}
}

// headerList renders a header map in stable name order, redacting sensitive
// values while keeping their names.
func headerList(headers map[string]string) []Header {
	if len(headers) == 0 {
		return []Header{}
	}
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Header, 0, len(keys))
	for _, k := range keys {
		v := headers[k]
		if browsers.IsSensitiveHeader(k) {
			v = browsers.RedactedValue
		}
		out = append(out, Header{Name: k, Value: v})
	}
	return out
}

// queryString parses the URL's query parameters into HAR name/value pairs.
func queryString(raw string) []Query {
	u, err := url.Parse(raw)
	if err != nil || u.RawQuery == "" {
		return []Query{}
	}
	values, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return []Query{}
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Query, 0, len(values))
	for _, k := range keys {
		for _, v := range values[k] {
			out = append(out, Query{Name: k, Value: v})
		}
	}
	return out
}

// redirectURL returns the Location header for a redirect status, or "".
func redirectURL(status int, headers []Header) string {
	if status < 300 || status >= 400 {
		return ""
	}
	for _, h := range headers {
		if strings.EqualFold(h.Name, "Location") {
			return h.Value
		}
	}
	return ""
}

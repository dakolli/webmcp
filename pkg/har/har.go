// Package har encodes observed page traffic as HAR 1.2. It accepts the
// engine's plain network entries and writes a bounded, parseable JSON archive.
// It imports nothing from pkg/mcp or pkg/cli: every surface passes data in.
package har

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"webmcp/pkg/browsers"
)

const (
	// DefaultMaxBytes caps one HAR export at 512 KB. The oldest entries are
	// dropped first until the encoded document fits.
	DefaultMaxBytes = 512 << 10
	// pageID is the single page ref this export describes.
	pageID = "page_1"
)

// PageInfo identifies the page an export describes.
type PageInfo struct {
	// Title is the page title reported at export time.
	Title string
}

// Options controls the output cap and the ring-loss count.
type Options struct {
	// MaxBytes caps the encoded document; zero uses DefaultMaxBytes.
	MaxBytes int
	// Dropped counts entries the capture ring evicted before export. They are
	// added to log.truncated and stated in log.comment.
	Dropped int
}

// Archive is the HAR 1.2 document root.
type Archive struct {
	Log Log `json:"log"`
}

// Log is the HAR 1.2 log object. Truncated is a project custom field: it
// counts the entries missing from this export, both the oldest entries dropped
// to satisfy the size cap and the entries the capture ring evicted. It is
// always emitted, as 0 when nothing was dropped.
type Log struct {
	Version   string  `json:"version"`
	Creator   Creator `json:"creator"`
	Pages     []Page  `json:"pages"`
	Entries   []Entry `json:"entries"`
	Comment   string  `json:"comment,omitempty"`
	Truncated int     `json:"truncated"`
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
	// FrameID is the CDP frame that issued the request. It is a project custom
	// field, emitted only when CDP reported one.
	FrameID string `json:"_frameId,omitempty"`
	// LoaderID is the CDP loader of that frame's document. It is a project
	// custom field, emitted only when CDP reported one.
	LoaderID string `json:"_loaderId,omitempty"`
	// TopDocument marks the request that produced the page's current top-level
	// document, emitted only on that entry. It is a project custom field.
	TopDocument bool `json:"_topDocument,omitempty"`
}

// Request describes the outgoing request.
type Request struct {
	Method      string   `json:"method"`
	URL         string   `json:"url"`
	HTTPVersion string   `json:"httpVersion"`
	Headers     []Header `json:"headers"`
	QueryString []Query  `json:"queryString"`
	Cookies     []Cookie `json:"cookies"`
	HeadersSize int64    `json:"headersSize"`
	BodySize    int64    `json:"bodySize"`
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

// Cookie is one cookie. The field set is present for HAR readers even though
// this exporter does not parse cookie values.
type Cookie struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Path     string `json:"path,omitempty"`
	Domain   string `json:"domain,omitempty"`
	Expires  string `json:"expires,omitempty"`
	HTTPOnly bool   `json:"httpOnly,omitempty"`
	Secure   bool   `json:"secure,omitempty"`
}

// Content describes the response body as measured on the wire.
type Content struct {
	Size     int64  `json:"size"`
	MimeType string `json:"mimeType"`
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

// Encode builds HAR 1.2 from the observed network entries and encodes it. It
// drops the oldest entries until the document fits opts.MaxBytes and reports
// both that count and the ring loss from opts.Dropped in log.truncated and
// log.comment.
func Encode(
	page PageInfo,
	entries []browsers.NetworkEntry,
	opts Options,
) ([]byte, error) {
	limit := opts.MaxBytes
	if limit <= 0 {
		limit = DefaultMaxBytes
	}
	started := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	built := make([]Entry, 0, len(entries))
	for _, e := range entries {
		built = append(built, buildEntry(started, e))
	}

	doc := newArchive(page, started, built, 0, opts.Dropped, limit)
	b, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encode HAR: %w", err)
	}
	if len(b) <= limit {
		return b, nil
	}

	lo, hi := 0, len(built)
	best := []byte(nil)
	for lo <= hi {
		mid := (lo + hi) / 2
		kept := built[len(built)-mid:]
		doc := newArchive(page, started, kept, len(built)-mid, opts.Dropped, limit)
		out, err := json.Marshal(doc)
		if err != nil {
			return nil, fmt.Errorf("encode HAR: %w", err)
		}
		if len(out) <= limit {
			best = out
			lo = mid + 1
			continue
		}
		hi = mid - 1
	}
	if best != nil {
		return best, nil
	}

	doc = newArchive(page, started, nil, len(built), opts.Dropped, limit)
	b, err = json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encode HAR: %w", err)
	}
	return b, nil
}

// newArchive assembles the document for one kept entry slice. capDropped counts
// entries cut to satisfy the size limit, ringDropped counts entries the capture
// ring evicted before export.
func newArchive(
	page PageInfo,
	started string,
	entries []Entry,
	capDropped int,
	ringDropped int,
	limit int,
) Archive {
	if entries == nil {
		entries = []Entry{}
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
		Entries:   entries,
		Comment:   comment(limit, capDropped, ringDropped),
		Truncated: capDropped + ringDropped,
	}}
}

// comment states the export's provenance, redaction rule, size-cap truncation
// and ring loss.
func comment(limit, capDropped, ringDropped int) string {
	c := "web-mcp HAR 1.2 export. Cookie and Set-Cookie are absent because " +
		"Chrome omits them from CDP events, so the cookies arrays are empty. " +
		"Authorization and X-CSRF-Token header values are <redacted>. " +
		"startedDateTime is the export time because the network ring " +
		"records monotonic timestamps only. time is the measured total and is " +
		"reported as timings.wait; unmeasured phases are -1. Cap " +
		strconv.Itoa(limit) + " bytes. A redirect hop is its own entry, with " +
		"response.redirectURL set from the hop's Location header. _frameId and " +
		"_loaderId carry the CDP document identity when the browser reported " +
		"it, and _topDocument marks the request for the page's current " +
		"top-level document."
	if capDropped > 0 {
		c += " Truncated: the " + strconv.Itoa(capDropped) +
			" oldest entries were dropped to fit the size cap."
	}
	if ringDropped > 0 {
		c += " Ring loss: " + strconv.Itoa(ringDropped) +
			" entries were evicted by the capture ring before export."
	}
	return c
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
	return Entry{
		Pageref:         pageID,
		StartedDateTime: started,
		Time:            timeMS,
		FrameID:         e.FrameID,
		LoaderID:        e.LoaderID,
		TopDocument:     e.TopDocument,
		Request: Request{
			Method:      e.Method,
			URL:         e.URL,
			HTTPVersion: "",
			Headers:     reqHeaders,
			QueryString: queryString(e.URL),
			Cookies:     []Cookie{},
			HeadersSize: -1,
			BodySize:    -1,
		},
		Response: Response{
			Status:      e.Status,
			StatusText:  statusText,
			HTTPVersion: "",
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

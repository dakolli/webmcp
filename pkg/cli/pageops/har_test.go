package pageops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"webmcp/pkg/browsers"
	"webmcp/pkg/har"
)

// fakeHARPage records the body reads PageHAR performs, so the test proves both
// fetchers reach the page and the cap reaches the encoder.
type fakeHARPage struct {
	entries  []browsers.NetworkEntry
	dropped  int
	response browsers.NetworkBody
	request  browsers.NetworkBody
	cookies  []browsers.Cookie
	secrets  bool
	gotResp  []int64
	gotReq   []int64
	gotCook  int
}

func (f *fakeHARPage) NetworkEntries() ([]browsers.NetworkEntry, int) {
	return f.entries, f.dropped
}

func (f *fakeHARPage) NetworkBody(_ context.Context, seq int64) (browsers.NetworkBody, error) {
	f.gotResp = append(f.gotResp, seq)
	return f.response, nil
}

func (f *fakeHARPage) NetworkRequestBody(_ context.Context, seq int64) (browsers.NetworkBody, error) {
	f.gotReq = append(f.gotReq, seq)
	return f.request, nil
}

func (f *fakeHARPage) Cookies(context.Context) ([]browsers.Cookie, error) {
	f.gotCook++
	return f.cookies, nil
}

func (f *fakeHARPage) SecretsCaptured() bool {
	return f.secrets
}

func TestPageHARForwardsCapAndBothBodies(t *testing.T) {
	t.Run("pageHAR forwards an unbounded cap and both bodies", func(t *testing.T) {
		p := &fakeHARPage{
			entries: []browsers.NetworkEntry{{
				Method:     "POST",
				URL:        "https://example.test/submit",
				Status:     200,
				MIME:       "text/plain",
				Bytes:      5,
				Seq:        7,
				ReqHeaders: map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
			}},
			dropped:  1,
			response: browsers.NetworkBody{Seq: 7, Bytes: 5, Body: "hello"},
			request:  browsers.NetworkBody{Seq: 7, Bytes: 3, Base64: "AAEC"},
			cookies: []browsers.Cookie{{
				Name:   "sid",
				Value:  "abc",
				Domain: "example.test",
				Path:   "/",
			}},
		}

		out, err := pageHAR(context.Background(), p, har.PageInfo{Title: "T"}, -1)
		if err != nil {
			t.Fatalf("pageHAR() error = %v", err)
		}
		if len(p.gotResp) != 1 || p.gotResp[0] != 7 {
			t.Errorf("response body fetches = %v, want [7]", p.gotResp)
		}
		if len(p.gotReq) != 1 || p.gotReq[0] != 7 {
			t.Errorf("request body fetches = %v, want [7]", p.gotReq)
		}

		var doc har.Archive
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatalf("unmarshal HAR: %v", err)
		}
		if !strings.Contains(doc.Log.Comment, "Cap: none") {
			t.Errorf("log.comment = %q, want the uncapped marker", doc.Log.Comment)
		}
		if doc.Log.Truncated != 0 {
			t.Errorf("log.truncated = %d, want 0 for an uncapped export", doc.Log.Truncated)
		}
		if len(doc.Log.Entries) != 1 {
			t.Fatalf("entries = %d, want 1", len(doc.Log.Entries))
		}
		entry := doc.Log.Entries[0]
		if entry.Response.Content.Text != "hello" || entry.Response.Content.Size != 5 {
			t.Errorf("response content = %+v, want text hello size 5", entry.Response.Content)
		}
		if entry.Request.PostData == nil || entry.Request.PostData.Text != "AAEC" {
			t.Errorf("request postData = %+v, want base64 AAEC", entry.Request.PostData)
		}
		if entry.Request.BodySize != 3 {
			t.Errorf("request bodySize = %d, want 3 (decoded)", entry.Request.BodySize)
		}
		if p.gotCook != 1 {
			t.Errorf("cookie snapshots = %d, want 1 per export", p.gotCook)
		}
		if len(entry.Request.Cookies) != 1 || entry.Request.Cookies[0].Name != "sid" {
			t.Fatalf("request cookies = %+v, want the sid jar cookie", entry.Request.Cookies)
		}
		if entry.Request.Cookies[0].Value != browsers.RedactedValue {
			t.Errorf("cookie value = %q, want the redaction marker", entry.Request.Cookies[0].Value)
		}
		if len(entry.Response.Cookies) != 0 {
			t.Errorf("response cookies = %+v, want none", entry.Response.Cookies)
		}
	})

	t.Run("PageHAR forwards a zero cap as the encoder default", func(t *testing.T) {
		out, err := PageHAR(context.Background(), &browsers.Page{}, 0)
		if err != nil {
			t.Fatalf("PageHAR() error = %v", err)
		}
		var doc har.Archive
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatalf("unmarshal HAR: %v", err)
		}
		if !strings.Contains(doc.Log.Comment, fmt.Sprintf("Cap %d bytes", har.DefaultMaxBytes)) {
			t.Errorf("log.comment = %q, want the default cap", doc.Log.Comment)
		}
	})
}

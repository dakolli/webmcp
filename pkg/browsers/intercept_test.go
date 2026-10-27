package browsers

import (
	"strings"
	"testing"

	"github.com/go-rod/rod/lib/proto"
	"github.com/ysmood/gson"
)

func TestInterceptPattern(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		url     string
		want    bool
	}{
		{"star matches everything", "*", "https://example.com/any/path", true},
		{"substring glob matches", "*api*", "https://example.com/v1/api/ping", true},
		{"substring glob rejects", "*api*", "https://example.com/v1/page", false},
		{"question mark matches one rune", "https://x.test/?", "https://x.test/a", true},
		{"question mark rejects two runes", "https://x.test/?", "https://x.test/ab", false},
		{"dots are literal", "https://x.test/a.b", "https://x.test/a.b", true},
		{"dots do not match any rune", "https://x.test/a.b", "https://x.test/axb", false},
		{"plus is literal", "https://x.test/a+b", "https://x.test/a+b", true},
		{"plus does not repeat", "https://x.test/a+b", "https://x.test/aaab", false},
		{"matching is case-sensitive", "https://x.test/API", "https://x.test/api", false},
		{"anchored on the whole URL", "*/ping", "https://x.test/ping/extra", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			re, err := compileInterceptPattern(tc.pattern)
			if err != nil {
				t.Fatalf("compile %q: %v", tc.pattern, err)
			}
			if got := re.MatchString(tc.url); got != tc.want {
				t.Fatalf("pattern %q vs %q = %t, want %t", tc.pattern, tc.url, got, tc.want)
			}
		})
	}
}

func TestCompileInterceptRule(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		c, err := compileInterceptRule(InterceptRule{ID: "r1", Action: "fulfill", Body: "<p>hi</p>"})
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		if c.rule.URLPattern != "*" {
			t.Fatalf("url pattern: got %q, want *", c.rule.URLPattern)
		}
		if c.rule.Status != 200 {
			t.Fatalf("fulfill status: got %d, want 200", c.rule.Status)
		}
	})

	t.Run("delay is clamped", func(t *testing.T) {
		c, err := compileInterceptRule(InterceptRule{ID: "r1", Action: "allow", DelayMS: 60000})
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		if c.rule.DelayMS != int(interceptDelayCap.Milliseconds()) {
			t.Fatalf("delay: got %d, want %d", c.rule.DelayMS, interceptDelayCap.Milliseconds())
		}
	})

	t.Run("method is uppercased", func(t *testing.T) {
		c, err := compileInterceptRule(InterceptRule{ID: "r1", Action: "block", Method: " post "})
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		if c.rule.Method != "POST" {
			t.Fatalf("method: got %q, want POST", c.rule.Method)
		}
	})

	cases := []struct {
		name string
		rule InterceptRule
	}{
		{"missing id", InterceptRule{Action: "block"}},
		{"unknown action", InterceptRule{ID: "r1", Action: "drop"}},
		{"status out of range", InterceptRule{ID: "r1", Action: "fulfill", Status: 99}},
		{"negative delay", InterceptRule{ID: "r1", Action: "allow", DelayMS: -1}},
		{"empty header name", InterceptRule{ID: "r1", Action: "rewrite", Headers: map[string]string{" ": "x"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := compileInterceptRule(tc.rule); err == nil {
				t.Fatal("expected an error, got nil")
			}
		})
	}

	t.Run("too many headers", func(t *testing.T) {
		headers := make(map[string]string, interceptHeaderCap+1)
		for i := range interceptHeaderCap + 1 {
			headers[string(rune('a'+i))] = "v"
		}
		if _, err := compileInterceptRule(InterceptRule{ID: "r1", Action: "rewrite", Headers: headers}); err == nil {
			t.Fatal("expected an error, got nil")
		}
	})
}

func TestMatchInterceptRule(t *testing.T) {
	compiled, err := compileInterceptBatch([]InterceptRule{
		{ID: "rewrite-api", URLPattern: "*api*", Method: "post", Action: "rewrite"},
		{ID: "block-api", URLPattern: "*api*", Action: "block"},
		{ID: "allow-all", URLPattern: "*", Action: "allow"},
	})
	if err != nil {
		t.Fatalf("compile batch: %v", err)
	}
	it := &interceptor{rules: compiled}

	t.Run("first match wins and method is case-insensitive", func(t *testing.T) {
		rule, ok := it.match("POST", "https://example.com/api/orders")
		if !ok || rule.ID != "rewrite-api" {
			t.Fatalf("got rule=%q ok=%t, want rewrite-api", rule.ID, ok)
		}
	})

	t.Run("method filter falls through to the next rule", func(t *testing.T) {
		rule, ok := it.match("GET", "https://example.com/api/orders")
		if !ok || rule.ID != "block-api" {
			t.Fatalf("got rule=%q ok=%t, want block-api", rule.ID, ok)
		}
	})

	t.Run("catch-all matches", func(t *testing.T) {
		rule, ok := it.match("GET", "https://example.com/page")
		if !ok || rule.ID != "allow-all" {
			t.Fatalf("got rule=%q ok=%t, want allow-all", rule.ID, ok)
		}
	})

	t.Run("no rule matches", func(t *testing.T) {
		empty := &interceptor{}
		if rule, ok := empty.match("GET", "https://example.com/"); ok {
			t.Fatalf("got rule=%q, want no match", rule.ID)
		}
	})
}

func TestAssignInterceptIDs(t *testing.T) {
	t.Run("mints unique ids around existing ones", func(t *testing.T) {
		used := map[string]bool{"r1": true}
		rules, next, err := assignInterceptIDs([]InterceptRule{{Action: "block"}, {Action: "allow"}}, used, 1)
		if err != nil {
			t.Fatalf("assign: %v", err)
		}
		if rules[0].ID != "r2" || rules[1].ID != "r3" {
			t.Fatalf("ids: got %q/%q, want r2/r3", rules[0].ID, rules[1].ID)
		}
		if next != 4 {
			t.Fatalf("next: got %d, want 4", next)
		}
	})

	t.Run("duplicate id in a batch is refused", func(t *testing.T) {
		_, _, err := assignInterceptIDs([]InterceptRule{
			{ID: "dup", Action: "block"},
			{ID: "dup", Action: "allow"},
		}, map[string]bool{}, 1)
		if err == nil {
			t.Fatal("expected an error, got nil")
		}
	})
}

func TestMergeInterceptHeaders(t *testing.T) {
	base := proto.NetworkHeaders{
		"Accept":     gson.New("text/html"),
		"X-Test":     gson.New("old"),
		"User-Agent": gson.New("wm"),
	}
	got := mergeInterceptHeaders(base, map[string]string{
		"x-test":        "new",
		"Authorization": "Bearer secret",
	})
	headers := make(map[string]string, len(got))
	for _, h := range got {
		headers[strings.ToLower(h.Name)] = h.Value
	}
	if headers["x-test"] != "new" {
		t.Fatalf("override: got %q, want new", headers["x-test"])
	}
	if headers["accept"] != "text/html" || headers["user-agent"] != "wm" {
		t.Fatalf("base headers dropped: %v", headers)
	}
	if headers["authorization"] != "Bearer secret" {
		t.Fatalf("rewrite header must reach the browser unredacted, got %q", headers["authorization"])
	}
	if mergeInterceptHeaders(base, nil) != nil {
		t.Fatal("no overrides must return nil")
	}
}

func TestFulfillHeaders(t *testing.T) {
	t.Run("adds a content type for a body", func(t *testing.T) {
		got := fulfillHeaders(nil, true)
		if len(got) != 1 || got[0].Name != "Content-Type" {
			t.Fatalf("headers: %+v", got)
		}
	})

	t.Run("keeps an explicit content type", func(t *testing.T) {
		got := fulfillHeaders(
			map[string]string{"content-type": "application/json"},
			true,
		)
		if len(got) != 1 || got[0].Value != "application/json" {
			t.Fatalf("headers: %+v", got)
		}
	})

	t.Run("no body adds nothing", func(t *testing.T) {
		if got := fulfillHeaders(nil, false); len(got) != 0 {
			t.Fatalf("headers: %+v", got)
		}
	})
}

func TestRedactInterceptHeaders(t *testing.T) {
	source := InterceptRule{
		ID:      "r1",
		Action:  InterceptRewrite,
		Headers: map[string]string{"Authorization": "Bearer secret", "X-Test": "1"},
	}
	if engine := cloneInterceptRule(source); engine.Headers["Authorization"] != "Bearer secret" {
		t.Fatalf("engine copy must keep the real value, got %q", engine.Headers["Authorization"])
	}
	rule := redactedInterceptRule(source)
	if rule.Headers["Authorization"] != redactedValue {
		t.Fatalf("authorization: got %q, want %s", rule.Headers["Authorization"], redactedValue)
	}
	if rule.Headers["X-Test"] != "1" {
		t.Fatalf("x-test: got %q, want 1", rule.Headers["X-Test"])
	}
	if source.Headers["Authorization"] != "Bearer secret" {
		t.Fatal("redaction mutated the stored rule")
	}
}

func TestObserverRecordIntercept(t *testing.T) {
	t.Run("a hit before the network event joins the same entry", func(t *testing.T) {
		page := &Page{observer: newPageObserver()}
		id := proto.NetworkRequestID("req-1")
		page.observer.recordIntercept(&proto.FetchRequestPaused{
			NetworkID:    id,
			ResourceType: proto.NetworkResourceTypeXHR,
			Request: &proto.NetworkRequest{
				Method: "POST",
				URL:    "https://example.com/api/orders",
			},
		}, InterceptRule{ID: "r1", Action: InterceptBlock})

		page.observer.recordRequest(&proto.NetworkRequestWillBeSent{
			RequestID: id,
			Request: &proto.NetworkRequest{
				Method: "POST",
				URL:    "https://example.com/api/orders?full=1",
			},
		})

		entries, _ := page.NetworkEntries()
		if len(entries) != 1 {
			t.Fatalf("entries: got %d, want 1 (the intercept hit must not duplicate)", len(entries))
		}
		got := entries[0]
		if got.Intercepted != InterceptBlock || got.InterceptRuleID != "r1" {
			t.Fatalf("intercept tag: got %q/%q", got.Intercepted, got.InterceptRuleID)
		}
		if !got.Failed || got.Error == "" {
			t.Fatalf("block must mark the entry failed with an error, got %+v", got)
		}
		if got.URL != "https://example.com/api/orders?full=1" {
			t.Fatalf("url: got %q", got.URL)
		}
	})

	t.Run("a hit after the network event tags it", func(t *testing.T) {
		page := &Page{observer: newPageObserver()}
		id := proto.NetworkRequestID("req-2")
		page.observer.recordRequest(&proto.NetworkRequestWillBeSent{
			RequestID: id,
			Request: &proto.NetworkRequest{
				Method: "GET",
				URL:    "https://example.com/api/ping",
			},
		})
		page.observer.recordIntercept(&proto.FetchRequestPaused{
			NetworkID: id,
			Request:   &proto.NetworkRequest{Method: "GET", URL: "https://example.com/api/ping"},
		}, InterceptRule{ID: "r2", Action: InterceptFulfill, Status: 204, Headers: map[string]string{"Content-Type": "application/json"}})

		entries, _ := page.NetworkEntries()
		if len(entries) != 1 {
			t.Fatalf("entries: got %d, want 1", len(entries))
		}
		got := entries[0]
		if got.Intercepted != InterceptFulfill || got.InterceptRuleID != "r2" {
			t.Fatalf("intercept tag: got %q/%q", got.Intercepted, got.InterceptRuleID)
		}
		if got.Status != 204 || got.MIME != "application/json" {
			t.Fatalf("fulfill outcome: got status=%d mime=%q", got.Status, got.MIME)
		}
	})
}

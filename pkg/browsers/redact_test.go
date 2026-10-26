package browsers

import (
	"fmt"
	"strings"
	"testing"

	"github.com/go-rod/rod/lib/proto"
	"github.com/ysmood/gson"
)

func TestRedactHeaders(t *testing.T) {
	headers := proto.NetworkHeaders{
		"Cookie":        gson.New("session=abc"),
		"Set-Cookie":    gson.New("token=xyz; HttpOnly"),
		"Authorization": gson.New("Bearer secret"),
		"X-CSRF-Token":  gson.New("csrf-value"),
		"Content-Type":  gson.New("application/json"),
	}

	t.Run("redacts sensitive values by default", func(t *testing.T) {
		got, truncated := redactHeaders(headers, false)
		if truncated {
			t.Fatal("unexpected header truncation")
		}
		for _, name := range []string{"Cookie", "Set-Cookie", "Authorization", "X-CSRF-Token"} {
			if got[name] != redactedValue {
				t.Fatalf("%s: got %q, want %q", name, got[name], redactedValue)
			}
		}
		if got["Content-Type"] != "application/json" {
			t.Fatalf("Content-Type: got %q, want it untouched", got["Content-Type"])
		}
	})

	t.Run("keeps values when secrets capture is on", func(t *testing.T) {
		got, _ := redactHeaders(headers, true)
		if got["Cookie"] != "session=abc" || got["Authorization"] != "Bearer secret" {
			t.Fatalf("secret capture: got %+v", got)
		}
	})

	t.Run("bounds the stored header set", func(t *testing.T) {
		big := proto.NetworkHeaders{}
		for i := range networkHeaderCountLimit * 2 {
			big[fmt.Sprintf("x-header-%02d", i)] = gson.New(strings.Repeat("v", networkHeaderValueLimit*2))
		}
		got, truncated := redactHeaders(big, false)
		if !truncated {
			t.Fatal("expected the count/value bound to report truncation")
		}
		if len(got) > networkHeaderCountLimit {
			t.Fatalf("header count: got %d, want <= %d", len(got), networkHeaderCountLimit)
		}
	})
}

func TestBuildNetworkBodyCap(t *testing.T) {
	t.Run("large text body is cut and marked", func(t *testing.T) {
		entry := NetworkEntry{Seq: 7, URL: "https://example.com/big", MIME: "application/json"}
		res := &proto.NetworkGetResponseBodyResult{Body: strings.Repeat("a", networkBodyCap+1024)}

		body, err := buildNetworkBody(entry, res)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		if !body.Truncated {
			t.Fatal("expected truncated")
		}
		if len(body.Body) != networkBodyCap {
			t.Fatalf("body length: got %d, want %d", len(body.Body), networkBodyCap)
		}
		if body.Bytes != int64(networkBodyCap+1024) {
			t.Fatalf("bytes: got %d, want the full size", body.Bytes)
		}
	})

	t.Run("binary body reports base64 only", func(t *testing.T) {
		entry := NetworkEntry{Seq: 8, URL: "https://example.com/img", MIME: "image/png"}
		res := &proto.NetworkGetResponseBodyResult{Body: "AAEC", Base64Encoded: true}

		body, err := buildNetworkBody(entry, res)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		if body.Body != "" || body.Base64 != "AAEC" {
			t.Fatalf("binary body: got body=%q base64=%q", body.Body, body.Base64)
		}
		if body.ContentType != "image/png" || body.Bytes != 3 {
			t.Fatalf("binary meta: got %+v", body)
		}
	})
}

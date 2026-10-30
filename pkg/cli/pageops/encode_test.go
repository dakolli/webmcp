package pageops

import (
	"encoding/json"
	"html"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// TestEncodePayload pins the variant set and the raw passthrough for every
// context, against both a script payload and a mixed quote/ampersand payload.
func TestEncodePayload(t *testing.T) {
	t.Parallel()
	payloads := []string{
		`<script>alert(1)</script>`,
		`a&b"c'd e`,
	}
	tests := []struct {
		context string
		labels  []string
	}{
		{"html-text", []string{"raw", "entity", "html5-attr"}},
		{"attr-double", []string{"raw", "entity", "url", "html5-attr"}},
		{"attr-single", []string{"raw", "entity", "url", "html5-attr"}},
		{"attr-unquoted", []string{"raw", "entity", "url", "html5-attr"}},
		{"js-string", []string{"raw", "js", "js-hex", "unicode"}},
		{"js-template", []string{"raw", "js", "js-hex", "unicode"}},
		{"css-value", []string{"raw", "url", "unicode"}},
		{"url-query", []string{"raw", "url", "url-double"}},
		{"url-path", []string{"raw", "url", "url-double"}},
	}
	for _, tt := range tests {
		for _, payload := range payloads {
			t.Run(tt.context+"/"+payload, func(t *testing.T) {
				t.Parallel()
				out, err := EncodePayload(payload, tt.context, nil)
				if err != nil {
					t.Fatalf("EncodePayload(%q, %q) error = %v", payload, tt.context, err)
				}
				var res EncodeResult
				if err := json.Unmarshal([]byte(out), &res); err != nil {
					t.Fatalf("result is not JSON: %v (%s)", err, out)
				}
				if res.Context != tt.context {
					t.Errorf("context = %q, want %q", res.Context, tt.context)
				}
				labels := make([]string, 0, len(res.Variants))
				for _, v := range res.Variants {
					labels = append(labels, v.Label)
				}
				if !slices.Equal(labels, tt.labels) {
					t.Errorf("labels = %v, want %v", labels, tt.labels)
				}
				if res.Variants[0].Label != "raw" || res.Variants[0].Value != payload {
					t.Errorf("raw variant = %+v, want the original payload", res.Variants[0])
				}
			})
		}
	}
}

// TestEncodeAttrDoubleEntityHasNoBareQuote proves the entity variant cannot
// terminate a double-quoted attribute.
func TestEncodeAttrDoubleEntityHasNoBareQuote(t *testing.T) {
	t.Parallel()
	payload := `a&b"c'd e`
	out, err := EncodePayload(payload, "attr-double", []string{"entity"})
	if err != nil {
		t.Fatalf("EncodePayload error = %v", err)
	}
	entity := encodeVariantValue(t, out, "entity")
	if strings.Contains(entity, `"`) {
		t.Fatalf("entity variant contains a bare quote: %q", entity)
	}
	if got := html.UnescapeString(entity); got != payload {
		t.Errorf("entity roundtrip = %q, want %q", got, payload)
	}
}

// TestEncodeJSStringEscapesQuotesAndAngle proves the js variant escapes both
// quote styles and the angle brackets.
func TestEncodeJSStringEscapesQuotesAndAngle(t *testing.T) {
	t.Parallel()
	out, err := EncodePayload(`a&b"c'd e`, "js-string", []string{"js"})
	if err != nil {
		t.Fatalf("EncodePayload error = %v", err)
	}
	js := encodeVariantValue(t, out, "js")
	for _, want := range []string{`\"`, `\'`, `\x26`} {
		if !strings.Contains(js, want) {
			t.Errorf("js variant %q does not contain %q", js, want)
		}
	}

	out, err = EncodePayload(`<script>alert(1)</script>`, "js-string", []string{"js"})
	if err != nil {
		t.Fatalf("EncodePayload error = %v", err)
	}
	js = encodeVariantValue(t, out, "js")
	for _, want := range []string{`\x3c`, `\x3e`} {
		if !strings.Contains(js, want) {
			t.Errorf("js variant %q does not contain %q", js, want)
		}
	}
	if strings.ContainsAny(js, "<>") {
		t.Errorf("js variant %q keeps a bare angle bracket", js)
	}
}

// TestEncodeURLRoundtrip proves both url variants decode back to the payload.
func TestEncodeURLRoundtrip(t *testing.T) {
	t.Parallel()
	payload := `a&b"c'd e`
	tests := []struct {
		context  string
		unescape func(string) (string, error)
	}{
		{"url-query", url.QueryUnescape},
		{"url-path", url.PathUnescape},
	}
	for _, tt := range tests {
		t.Run(tt.context, func(t *testing.T) {
			t.Parallel()
			out, err := EncodePayload(payload, tt.context, []string{"url", "url-double"})
			if err != nil {
				t.Fatalf("EncodePayload error = %v", err)
			}
			once, err := tt.unescape(encodeVariantValue(t, out, "url"))
			if err != nil {
				t.Fatalf("unescape url: %v", err)
			}
			if once != payload {
				t.Errorf("url roundtrip = %q, want %q", once, payload)
			}
			twice, err := tt.unescape(encodeVariantValue(t, out, "url-double"))
			if err != nil {
				t.Fatalf("unescape url-double: %v", err)
			}
			twice, err = tt.unescape(twice)
			if err != nil {
				t.Fatalf("unescape url-double twice: %v", err)
			}
			if twice != payload {
				t.Errorf("url-double roundtrip = %q, want %q", twice, payload)
			}
		})
	}
}

// TestEncodeRejectsUnknownContext proves the error names every accepted
// context.
func TestEncodeRejectsUnknownContext(t *testing.T) {
	t.Parallel()
	_, err := EncodePayload("x", "html", nil)
	if err == nil {
		t.Fatal("EncodePayload with an unknown context returned no error")
	}
	for _, context := range EncodeContexts() {
		if !strings.Contains(err.Error(), context) {
			t.Errorf("error %q does not name context %q", err, context)
		}
	}
}

// TestEncodeVariantsNarrow proves variants narrows the output and a label the
// context does not offer is refused.
func TestEncodeVariantsNarrow(t *testing.T) {
	t.Parallel()
	out, err := EncodePayload(`<x>`, "attr-double", []string{"entity"})
	if err != nil {
		t.Fatalf("EncodePayload error = %v", err)
	}
	var res EncodeResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	labels := make([]string, 0, len(res.Variants))
	for _, v := range res.Variants {
		labels = append(labels, v.Label)
	}
	if want := []string{"raw", "entity"}; !slices.Equal(labels, want) {
		t.Errorf("labels = %v, want %v", labels, want)
	}

	if _, err := EncodePayload("x", "attr-double", []string{"js"}); err == nil {
		t.Fatal("EncodePayload with an unavailable variant returned no error")
	}
}

// encodeVariantValue decodes a result and returns the named variant.
func encodeVariantValue(t *testing.T, out, label string) string {
	t.Helper()
	var res EncodeResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("result is not JSON: %v (%s)", err, out)
	}
	for _, v := range res.Variants {
		if v.Label == label {
			return v.Value
		}
	}
	t.Fatalf("variant %q is missing from %s", label, out)
	return ""
}

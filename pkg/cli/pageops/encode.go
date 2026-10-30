package pageops

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"sort"
	"strings"
	"unicode/utf16"
)

// encodeContextVariants maps each payload context to the variants it offers.
// raw is added to every context, so it is never listed here.
var encodeContextVariants = map[string][]string{
	"html-text":     {"entity", "html5-attr"},
	"attr-double":   {"entity", "html5-attr", "url"},
	"attr-single":   {"entity", "html5-attr", "url"},
	"attr-unquoted": {"entity", "html5-attr", "url"},
	"js-string":     {"js", "js-hex", "unicode"},
	"js-template":   {"js", "js-hex", "unicode"},
	"css-value":     {"url", "unicode"},
	"url-query":     {"url", "url-double"},
	"url-path":      {"url", "url-double"},
}

// encodeVariantOrder is the canonical result order shared by every context.
var encodeVariantOrder = []string{
	"raw", "entity", "url", "url-double",
	"js", "js-hex", "unicode", "html5-attr",
}

// EncodeContexts returns the accepted encode contexts, sorted.
func EncodeContexts() []string {
	names := make([]string, 0, len(encodeContextVariants))
	for name := range encodeContextVariants {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// EncodeVariant is one labelled encoding of a payload.
type EncodeVariant struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// EncodeResult is encode's payload: the requested context and its variants.
type EncodeResult struct {
	Context  string          `json:"context"`
	Variants []EncodeVariant `json:"variants"`
}

// EncodePayload encodes value for the given context without touching a page.
// variants narrows the result to the named labels; an empty list returns the
// full set for the context. raw is always present.
func EncodePayload(value, context string, variants []string) (string, error) {
	offered, ok := encodeContextVariants[context]
	if !ok {
		return "", fmt.Errorf(
			"encode: unknown context %q (must be one of %s)",
			context,
			strings.Join(EncodeContexts(), ", "),
		)
	}

	selected := map[string]bool{"raw": true}
	if len(variants) == 0 {
		for _, label := range offered {
			selected[label] = true
		}
	} else {
		allowed := map[string]bool{"raw": true}
		for _, label := range offered {
			allowed[label] = true
		}
		for _, raw := range variants {
			label := strings.TrimSpace(raw)
			if !allowed[label] {
				return "", fmt.Errorf(
					"encode: variant %q is not available for context %q (must be one of %s)",
					label,
					context,
					strings.Join(append([]string{"raw"}, offered...), ", "),
				)
			}
			selected[label] = true
		}
	}

	res := EncodeResult{
		Context:  context,
		Variants: make([]EncodeVariant, 0, len(selected)),
	}
	for _, label := range encodeVariantOrder {
		if !selected[label] {
			continue
		}
		res.Variants = append(res.Variants, EncodeVariant{
			Label: label,
			Value: encodeValue(value, label, context),
		})
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(res); err != nil {
		return "", fmt.Errorf("encode: marshal result: %w", err)
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// encodeValue renders one labelled encoding of value for context.
func encodeValue(value, label, context string) string {
	switch label {
	case "entity":
		return html.EscapeString(value)
	case "html5-attr":
		return html5AttrEscape(value)
	case "url":
		return urlEscape(value, context)
	case "url-double":
		return urlEscape(urlEscape(value, context), context)
	case "js":
		return jsEscape(value, context == "js-template")
	case "js-hex":
		return jsHexEscape(value)
	case "unicode":
		return unicodeEscape(value, context)
	default:
		return value
	}
}

// html5AttrEscape encodes every character HTML5 forbids in an unquoted
// attribute value, so the result is safe in any attribute position.
func html5AttrEscape(value string) string {
	var b strings.Builder
	b.Grow(len(value) * 2)
	for _, r := range value {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&#x27;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '=':
			b.WriteString("&#x3D;")
		case '`':
			b.WriteString("&#x60;")
		case ' ':
			b.WriteString("&#x20;")
		case '\t':
			b.WriteString("&#x9;")
		case '\n':
			b.WriteString("&#xA;")
		case '\r':
			b.WriteString("&#xD;")
		case '\f':
			b.WriteString("&#xC;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// urlEscape percent-encodes value for a query or path position.
func urlEscape(value, context string) string {
	if context == "url-query" {
		return url.QueryEscape(value)
	}
	return url.PathEscape(value)
}

// jsEscape backslash-escapes value for a JavaScript string literal. template
// also escapes the backtick and dollar sign so the value cannot close or
// interpolate a template literal.
func jsEscape(value string, template bool) string {
	var b strings.Builder
	b.Grow(len(value) * 2)
	for _, r := range value {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '\'':
			b.WriteString(`\'`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '<':
			b.WriteString(`\x3c`)
		case '>':
			b.WriteString(`\x3e`)
		case '&':
			b.WriteString(`\x26`)
		case '/':
			b.WriteString(`\/`)
		case '`':
			if template {
				b.WriteString(`\x60`)
				continue
			}
			b.WriteRune(r)
		case '$':
			if template {
				b.WriteString(`\x24`)
				continue
			}
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// jsHexEscape escapes every rune: two hex digits up to 0xFF, otherwise a
// unicode escape (a surrogate pair above the BMP).
func jsHexEscape(value string) string {
	var b strings.Builder
	b.Grow(len(value) * 4)
	for _, r := range value {
		if r <= 0xFF {
			fmt.Fprintf(&b, `\x%02x`, r)
			continue
		}
		writeUnicodeEscape(&b, r)
	}
	return b.String()
}

// unicodeEscape renders value as CSS hex escapes in a CSS value and as
// JavaScript unicode escapes everywhere else.
func unicodeEscape(value, context string) string {
	if context == "css-value" {
		return cssUnicodeEscape(value)
	}
	var b strings.Builder
	b.Grow(len(value) * 6)
	for _, r := range value {
		writeUnicodeEscape(&b, r)
	}
	return b.String()
}

// cssUnicodeEscape renders every rune as a six-digit CSS escape, which needs
// no trailing space to terminate.
func cssUnicodeEscape(value string) string {
	var b strings.Builder
	b.Grow(len(value) * 7)
	for _, r := range value {
		fmt.Fprintf(&b, `\%06x`, r)
	}
	return b.String()
}

// writeUnicodeEscape writes r as \uXXXX, splitting an astral rune into a
// surrogate pair.
func writeUnicodeEscape(b *strings.Builder, r rune) {
	if r > 0xFFFF {
		hi, lo := utf16.EncodeRune(r)
		fmt.Fprintf(b, `\u%04x\u%04x`, hi, lo)
		return
	}
	fmt.Fprintf(b, `\u%04x`, r)
}

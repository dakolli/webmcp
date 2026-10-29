package caido

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// readableHeaders stay readable even when their name contains a credential
// substring: these are transport metadata, not credentials. Exempted before the
// name test so e.g. "key" in a cache key name does not hide the header.
var readableHeaders = map[string]bool{
	"content-type":   true,
	"content-length": true,
	"accept":         true,
	"user-agent":     true,
	"host":           true,
	"referer":        true,
	"origin":         true,
	"date":           true,
	"server":         true,
	"etag":           true,
	"cache-control":  true,
	// CORS and policy headers carry evidence rather than secrets; their names
	// contain credential substrings ("credentials", "policy", "key").
	"access-control-allow-credentials": true,
	"access-control-allow-origin":      true,
	"access-control-allow-headers":     true,
	"access-control-allow-methods":     true,
	"access-control-expose-headers":    true,
	"access-control-max-age":           true,
	"strict-transport-security":        true,
	"content-security-policy":          true,
	"permissions-policy":               true,
	"x-frame-options":                  true,
	"x-content-type-options":           true,
	"vary":                             true,
	"expires":                          true,
	"last-modified":                    true,
}

// sensitiveHeaderParts are substrings that mark a header value as a credential.
var sensitiveHeaderParts = []string{
	"token",
	"secret",
	"auth",
	"key",
	"cookie",
	"session",
	"csrf",
	"signature",
	"password",
	"credential",
}

// sensitiveKeyParts match JSON keys whose value is a credential. The api variants
// are all listed because a header name such as X-Api-Key arrives as a JSON key in
// an echo response and its punctuation is not normalised.
var sensitiveKeyParts = []string{
	"token",
	"secret",
	"password",
	"passw",
	"authorization",
	"cookie",
	"session",
	"apikey",
	"api_key",
	"api-key",
	"csrf",
	"signature",
	"credential",
}

// credentialKey matches a credential-shaped key for the plain-body pass.
const credentialKey = `[a-z0-9_.-]*(?:token|secret|passw|auth|api[-_]?key|apikey|session|cookie|csrf|signature)[a-z0-9_.-]*`

var (
	pairPattern = regexp.MustCompile(`(?i)(` + credentialKey + `)(\s*[=:]\s*)("[^"]*"|[^\s&;"'<>]+)`)
	tagPattern  = regexp.MustCompile(`(?i)<(` + credentialKey + `)>([^<]*)</[a-z0-9_.-]*>`)
)

// encodeBody renders a redacted JSON value. It is a variable so tests can force
// the encode failure that decoded JSON cannot otherwise produce.
var encodeBody = func(v any) (string, error) {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// RedactHeaders returns a copy of h with credential header values replaced.
// A header is a credential when its lowercased name contains a credential
// substring, unless the name is one of the readable transport headers.
func RedactHeaders(h map[string][]string) map[string][]string {
	if h == nil {
		return nil
	}
	out := make(map[string][]string, len(h))
	for name, values := range h {
		if sensitiveHeader(name) && !booleanValues(values) {
			out[name] = []string{"<redacted>"}
			continue
		}
		if urlHeader(name) {
			out[name] = redactURLValues(values)
			continue
		}
		out[name] = values
	}
	return out
}

// urlHeader reports whether a header value is a URL that can carry a credential
// in its query string.
func urlHeader(name string) bool {
	switch strings.ToLower(name) {
	case "referer", "location", "content-location", "origin":
		return true
	}
	return false
}

// redactURLValues scrubs credential-shaped query parameters from header values
// that hold URLs.
func redactURLValues(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i], _ = redactPatterns(v)
	}
	return out
}

// booleanValues reports whether every value is a bare boolean, so a policy flag
// such as "true" is not hidden as a credential.
func booleanValues(values []string) bool {
	if len(values) == 0 {
		return false
	}
	for _, v := range values {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "false":
		default:
			return false
		}
	}
	return true
}

// RedactURL scrubs credential-shaped query parameters from a composed URL so a
// token in the query string cannot reach the caller.
func RedactURL(s string) (string, int) {
	return redactPatterns(s)
}

// RedactBody scrubs a body and returns the result, the replacement count, and
// any failure. It first walks valid JSON by key name, then runs a pattern pass
// over the result so form, plain and XML bodies are scrubbed too.
func RedactBody(s string) (string, int, error) {
	out := s
	count := 0

	var value any
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	if err := dec.Decode(&value); err == nil {
		redacted, n := redactValue(value)
		if n > 0 {
			encoded, err := encodeBody(redacted)
			if err != nil {
				return "", 0, fmt.Errorf("caido: redacting JSON body: %w", err)
			}
			out = encoded
			count += n
		}
	}

	scrubbed, n := redactPatterns(out)
	return scrubbed, count + n, nil
}

// redactPatterns scrubs credential-shaped key/value pairs and XML elements.
func redactPatterns(s string) (string, int) {
	count := 0
	s = pairPattern.ReplaceAllStringFunc(s, func(m string) string {
		parts := pairPattern.FindStringSubmatch(m)
		if alreadyRedacted(parts[3]) {
			return m
		}
		count++
		return parts[1] + parts[2] + "<redacted>"
	})
	s = tagPattern.ReplaceAllStringFunc(s, func(m string) string {
		parts := tagPattern.FindStringSubmatch(m)
		if alreadyRedacted(parts[2]) {
			return m
		}
		count++
		return "<" + parts[1] + "><redacted></" + parts[1] + ">"
	})
	return s, count
}

// alreadyRedacted reports whether a value is the redaction marker already.
func alreadyRedacted(value string) bool {
	return strings.EqualFold(strings.Trim(strings.TrimSpace(value), `"`), "<redacted>")
}

// sensitiveHeader reports whether a header name denotes a credential.
func sensitiveHeader(name string) bool {
	lower := strings.ToLower(name)
	if readableHeaders[lower] {
		return false
	}
	for _, part := range sensitiveHeaderParts {
		if strings.Contains(lower, part) {
			return true
		}
	}
	return false
}

// redactValue walks an arbitrary JSON value and replaces credential values.
func redactValue(value any) (any, int) {
	switch v := value.(type) {
	case map[string]any:
		count := 0
		for key, item := range v {
			if sensitiveJSONKey(key) {
				v[key] = "<redacted>"
				count++
				continue
			}
			next, n := redactValue(item)
			v[key] = next
			count += n
		}
		return v, count
	case []any:
		count := 0
		for i, item := range v {
			next, n := redactValue(item)
			v[i] = next
			count += n
		}
		return v, count
	default:
		return value, 0
	}
}

// sensitiveJSONKey reports whether a JSON key names a credential.
func sensitiveJSONKey(key string) bool {
	lower := strings.ToLower(key)
	for _, part := range sensitiveKeyParts {
		if strings.Contains(lower, part) {
			return true
		}
	}
	return false
}

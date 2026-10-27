package browsers

import (
	"bytes"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/go-rod/rod/lib/proto"
)

const (
	// RedactedValue is the single replacement marker every redaction path writes.
	RedactedValue = "<redacted>"
	// redactedValue keeps the package-internal name used by existing callers.
	redactedValue = RedactedValue
	// networkHeaderCountLimit bounds how many headers one direction keeps.
	networkHeaderCountLimit = 32
	// networkHeaderValueLimit bounds one stored header value.
	networkHeaderValueLimit = 512
	// networkHeaderBudget bounds the stored name+value bytes per direction,
	// keeping 200 entries with both header sets well under 2 MB.
	networkHeaderBudget = 4 << 10
	// bodyRedactionMaxDepth bounds how many escaped-JSON layers the body scan
	// decodes before it reports the remainder as unreached.
	bodyRedactionMaxDepth = 6
)

// sensitiveHeaders lists, lowercased, the header names whose values are
// replaced by redactedValue unless the page opted into secret capture.
var sensitiveHeaders = map[string]bool{
	"cookie":        true,
	"set-cookie":    true,
	"authorization": true,
	"x-csrf-token":  true,
}

// IsSensitiveHeader reports whether a header or body key holds a secret under
// the one redaction rule shared by the network ring, the HAR export and replay.
func IsSensitiveHeader(name string) bool {
	return sensitiveHeaders[strings.ToLower(strings.TrimSpace(name))]
}

// SensitiveHeaderNames returns the canonical sensitive header key set, sorted,
// so every surface can be checked against the same rule.
func SensitiveHeaderNames() []string {
	names := make([]string, 0, len(sensitiveHeaders))
	for name := range sensitiveHeaders {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// redactHeaders converts CDP headers into a bounded string map. Values of
// sensitiveHeaders become redactedValue unless keep is true; a set cut by the
// count, value or byte bound reports truncated.
func redactHeaders(h proto.NetworkHeaders, keep bool) (map[string]string, bool) {
	if len(h) == 0 {
		return nil, false
	}
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]string, min(len(h), networkHeaderCountLimit))
	used := 0
	for _, k := range keys {
		if len(out) >= networkHeaderCountLimit {
			return out, true
		}
		v := h[k].Str()
		if !keep && sensitiveHeaders[strings.ToLower(k)] {
			v = redactedValue
		}
		v = cutString(v, networkHeaderValueLimit)
		if used+len(k)+len(v) > networkHeaderBudget {
			return out, true
		}
		used += len(k) + len(v)
		out[k] = v
	}
	return out, false
}

// sensitiveBodySegments lists, lowercased, the key segments whose body values
// are replaced with redactedValue. A key is split on non-alphanumeric runes, so
// "sails.sid" matches "sid" while "consideration" does not.
var sensitiveBodySegments = map[string]bool{
	"cookie":        true,
	"cookies":       true,
	"authorization": true,
	"auth":          true,
	"token":         true,
	"session":       true,
	"sessionid":     true,
	"sessionkey":    true,
	"sid":           true,
	"wmsession":     true,
	"csrf":          true,
	"csrftoken":     true,
	"secret":        true,
	"password":      true,
	"passwd":        true,
	"apikey":        true,
}

// Body redaction patterns: JSON string fields, header-like lines, key=value
// pairs and auth credentials. They mirror the header redactor's key list and
// add the session-id and bearer-token shapes a body carries.
var (
	bodyJSONPair   = regexp.MustCompile(`"([^"\\\r\n]{1,80})"\s*:\s*"((?:[^"\\]|\\.)*)"`)
	bodyJSONScalar = regexp.MustCompile(`"([^"\\\r\n]{1,80})"\s*:\s*(?:-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?|true|false|null)\b`)
	bodyHeaderLine = regexp.MustCompile(`(?m)^[ \t]*([A-Za-z0-9_.\-]{1,80})[ \t]*:[ \t]*([^\r\n]+)$`)
	bodyKeyValue   = regexp.MustCompile(`(?i)\b([A-Za-z0-9_.\-]{1,80})=([^&;,\s"']+)`)
	bodyAuthToken  = regexp.MustCompile(`(?i)\b(Bearer|Basic)(\s+)([A-Za-z0-9._~+/=\-]{4,1000})`)
)

// bodyRedaction is the outcome of one text-body scan: the rewritten body, how
// many values were replaced, and the escaped forms the scan could not reach.
type bodyRedaction struct {
	Body      string
	Count     int
	Unreached []string
}

// redactBody replaces sensitive values in a text body and reports how many. It
// is the two-result view of redactTextBody for callers that do not need the
// reach report.
func redactBody(body string, keep bool) (string, int) {
	r := redactTextBody(body, keep)
	return r.Body, r.Count
}

// redactTextBody redacts the body itself and every JSON string nested inside
// it, so a secret encoded in an escaped copy is reached as well. keep skips
// redaction entirely.
func redactTextBody(body string, keep bool) bodyRedaction {
	if keep || body == "" {
		return bodyRedaction{Body: body}
	}
	count := 0
	unreached := map[string]bool{}
	body = redactBodyPatterns(body, &count)
	flagUnreachableScalars(body, unreached)
	body = redactEscapedForms(body, &count, unreached, 0)
	out := bodyRedaction{Body: body, Count: count}
	if len(unreached) > 0 {
		out.Unreached = make([]string, 0, len(unreached))
		for name := range unreached {
			out.Unreached = append(out.Unreached, name)
		}
		sort.Strings(out.Unreached)
	}
	return out
}

// redactBodyPatterns applies every body pattern once, replacing all matches of
// each pattern rather than the first.
func redactBodyPatterns(body string, count *int) string {
	body = redactBodyMatches(body, bodyJSONPair, 1, 2, isSensitiveBodyKey, count)
	body = redactBodyMatches(body, bodyHeaderLine, 1, 2, isSensitiveBodyKey, count)
	body = redactBodyMatches(body, bodyKeyValue, 1, 2, isSensitiveBodyKey, count)
	body = redactBodyMatches(body, bodyAuthToken, 1, 3, alwaysSensitive, count)
	return body
}

// redactBodyMatches rewrites every match whose key group is sensitive, keeping
// the text before and after the value group intact. A value already carrying
// the marker is left alone so repeated passes never inflate the count.
func redactBodyMatches(
	s string,
	re *regexp.Regexp,
	keyGroup, valueGroup int,
	sensitive func(string) bool,
	count *int,
) string {
	locs := re.FindAllStringSubmatchIndex(s, -1)
	if len(locs) == 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	last := 0
	for _, loc := range locs {
		b.WriteString(s[last:loc[0]])
		key := s[loc[2*keyGroup]:loc[2*keyGroup+1]]
		vStart, vEnd := loc[2*valueGroup], loc[2*valueGroup+1]
		if sensitive(key) && s[vStart:vEnd] != redactedValue {
			b.WriteString(s[loc[0]:vStart])
			b.WriteString(redactedValue)
			b.WriteString(s[vEnd:loc[1]])
			*count += 1
		} else {
			b.WriteString(s[loc[0]:loc[1]])
		}
		last = loc[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

// flagUnreachableScalars reports a sensitive JSON key whose value is a
// non-string scalar: rewriting it as <redacted> would change its JSON type, so
// the scan states the gap instead of leaving it silently unredacted.
func flagUnreachableScalars(s string, unreached map[string]bool) {
	for _, loc := range bodyJSONScalar.FindAllStringSubmatchIndex(s, -1) {
		if isSensitiveBodyKey(s[loc[2]:loc[3]]) {
			unreached["non-string-json-value"] = true
			return
		}
	}
}

// redactEscapedForms finds JSON string literals, decodes each, redacts the
// decoded content recursively and re-escapes it, so a secret nested in an
// escaped JSON string is reached without disturbing the surrounding document.
// An encode layer it cannot reach is reported in unreached.
func redactEscapedForms(
	s string,
	count *int,
	unreached map[string]bool,
	depth int,
) string {
	if depth >= bodyRedactionMaxDepth {
		if strings.Contains(s, `\"`) {
			unreached["escaped-json-depth"] = true
		}
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != '"' {
			b.WriteByte(s[i])
			i++
			continue
		}
		end := jsonStringEnd(s, i)
		if end < 0 {
			b.WriteString(s[i:])
			break
		}
		literal := s[i:end]
		decoded, ok := decodeJSONString(literal)
		if !ok {
			if strings.Contains(literal, `\"`) {
				unreached["unescaped-string"] = true
			}
			b.WriteString(literal)
			i = end
			continue
		}
		innerCount := 0
		inner := redactBodyPatterns(decoded, &innerCount)
		flagUnreachableScalars(inner, unreached)
		inner = redactEscapedForms(inner, &innerCount, unreached, depth+1)
		if innerCount == 0 && inner == decoded {
			b.WriteString(literal)
		} else {
			*count += innerCount
			b.WriteString(encodeJSONString(inner))
		}
		i = end
	}
	return b.String()
}

// jsonStringEnd returns the index just past the closing quote of the JSON
// string starting at start, or -1 when the string is unterminated.
func jsonStringEnd(s string, start int) int {
	for i := start + 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
	return -1
}

// decodeJSONString decodes one JSON string literal, reporting false when it is
// not a valid JSON string.
func decodeJSONString(literal string) (string, bool) {
	var s string
	if err := json.Unmarshal([]byte(literal), &s); err != nil {
		return "", false
	}
	return s, true
}

// encodeJSONString re-escapes s as a JSON string literal without HTML escaping,
// so the redaction marker keeps its literal form.
func encodeJSONString(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return strconv.Quote(s)
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

// isSensitiveBodyKey reports whether a body key holds a secret, comparing whole
// segments so a substring such as "sid" inside "consideration" never matches.
func isSensitiveBodyKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(strings.Trim(key, `"';`)))
	if sensitiveHeaders[k] {
		return true
	}
	for _, seg := range strings.FieldsFunc(k, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if sensitiveBodySegments[seg] {
			return true
		}
	}
	return false
}

// alwaysSensitive marks an auth credential value for redaction.
func alwaysSensitive(string) bool { return true }

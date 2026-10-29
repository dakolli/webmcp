package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// arrayMarker and lineMarker suffix a stored key with the metadata Strings
// needs: whether the value is an array and the source line of the key. A bare
// key cannot contain a NUL byte, so the synthetic keys never collide.
const (
	arrayMarker = "\x00array"
	lineMarker  = "\x00line"
)

// TOML is a flat, one-level TOML document. The outer key is the section name
// ("" for keys before any header) and every value is stored as text.
type TOML map[string]map[string]string

var (
	errTOMLSyntax       = errors.New("expected key = value")
	errTOMLSection      = errors.New("malformed section header")
	errTOMLDuplicateKey = errors.New("duplicate key")
	errTOMLValue        = errors.New("unsupported value")
)

// TOMLError reports the source line of a webmcp.toml parse failure.
type TOMLError struct {
	Line int
	Err  error
}

// Error renders the failure with the file name and the offending line.
func (e *TOMLError) Error() string {
	return fmt.Sprintf("config: webmcp.toml line %d: %v", e.Line, e.Err)
}

// Unwrap exposes the underlying parse error.
func (e *TOMLError) Unwrap() error {
	return e.Err
}

// ParseTOML parses the supported flat TOML subset: comments, blank lines,
// [section] headers, quoted strings with \" and \\ escapes, booleans, integers
// and single- or multi-line string arrays. Unknown sections and keys are kept
// for forward compatibility.
func ParseTOML(data []byte) (TOML, error) {
	doc := TOML{"": {}}
	section := ""
	lines := strings.Split(string(data), "\n")
	for i := 0; i < len(lines); i++ {
		lineNo := i + 1
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			name, err := parseSection(line)
			if err != nil {
				return nil, &TOMLError{Line: lineNo, Err: err}
			}
			section = name
			if doc[section] == nil {
				doc[section] = map[string]string{}
			}
			continue
		}
		key, raw, err := splitKeyValue(line)
		if err != nil {
			return nil, &TOMLError{Line: lineNo, Err: err}
		}
		if doc[section] == nil {
			doc[section] = map[string]string{}
		}
		if _, exists := doc[section][key]; exists {
			return nil, &TOMLError{
				Line: lineNo,
				Err:  fmt.Errorf("%w %q in [%s]", errTOMLDuplicateKey, key, section),
			}
		}
		doc[section][key+lineMarker] = strconv.Itoa(lineNo)
		if strings.HasPrefix(strings.TrimSpace(raw), "[") {
			text, span, err := gatherArray(line, lines[i+1:])
			if err != nil {
				return nil, &TOMLError{Line: lineNo, Err: err}
			}
			items, err := parseArrayItems(text)
			if err != nil {
				return nil, &TOMLError{Line: lineNo, Err: err}
			}
			encoded, err := json.Marshal(items)
			if err != nil {
				return nil, &TOMLError{Line: lineNo, Err: err}
			}
			doc[section][key] = string(encoded)
			doc[section][key+arrayMarker] = "1"
			i += span
			continue
		}
		value, err := parseValue(raw)
		if err != nil {
			return nil, &TOMLError{Line: lineNo, Err: err}
		}
		doc[section][key] = value
	}
	return doc, nil
}

func parseSection(line string) (string, error) {
	line = strings.TrimSpace(cutComment(line))
	if !strings.HasPrefix(line, "[") || !strings.HasSuffix(line, "]") {
		return "", errTOMLSection
	}
	name := strings.TrimSpace(line[1 : len(line)-1])
	if name == "" || strings.ContainsAny(name, "[]") {
		return "", errTOMLSection
	}
	return name, nil
}

func splitKeyValue(line string) (string, string, error) {
	idx := strings.IndexByte(line, '=')
	if idx < 0 {
		return "", "", errTOMLSyntax
	}
	key := strings.TrimSpace(line[:idx])
	if !isBareKey(key) {
		return "", "", errTOMLSyntax
	}
	return key, line[idx+1:], nil
}

// gatherArray returns the bracketed array text that begins on first and the
// number of following lines it consumed. It skips comment text outside quotes
// and tracks quoted escapes, so a "]" inside a string never closes the array.
func gatherArray(first string, rest []string) (string, int, error) {
	text := first + "\n" + strings.Join(rest, "\n")
	var b strings.Builder
	depth := 0
	started := false
	inQuote := false
	escaped := false
	consumed := 0
	for i := 0; i < len(text); i++ {
		c := text[i]
		if !started {
			if c == '[' {
				started = true
			} else {
				continue
			}
		}
		switch {
		case inQuote:
			b.WriteByte(c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inQuote = false
			}
		case c == '"':
			inQuote = true
			b.WriteByte(c)
		case c == '#':
			for i+1 < len(text) && text[i+1] != '\n' {
				i++
			}
		case c == '[':
			depth++
			b.WriteByte(c)
		case c == ']':
			depth--
			b.WriteByte(c)
			if depth == 0 {
				return b.String(), consumed, nil
			}
		case c == '\n':
			consumed++
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return "", 0, errTOMLValue
}

// parseArrayItems parses a bracketed list of quoted strings, allowing a
// trailing comma and whitespace or newlines between elements.
func parseArrayItems(raw string) ([]string, error) {
	body := strings.TrimSpace(raw)
	if !strings.HasPrefix(body, "[") || !strings.HasSuffix(body, "]") {
		return nil, errTOMLValue
	}
	body = body[1 : len(body)-1]
	var items []string
	for {
		body = strings.TrimSpace(body)
		if body == "" {
			return items, nil
		}
		if !strings.HasPrefix(body, `"`) {
			return nil, errTOMLValue
		}
		value, rest, err := parseQuoted(body)
		if err != nil {
			return nil, err
		}
		items = append(items, value)
		rest = strings.TrimSpace(rest)
		if rest == "" {
			return items, nil
		}
		if rest[0] != ',' {
			return nil, errTOMLValue
		}
		body = rest[1:]
	}
}

func parseValue(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errTOMLValue
	}
	if strings.HasPrefix(raw, `"`) {
		value, rest, err := parseQuoted(raw)
		if err != nil {
			return "", err
		}
		rest = strings.TrimSpace(rest)
		if rest != "" && !strings.HasPrefix(rest, "#") {
			return "", errTOMLValue
		}
		return value, nil
	}

	raw = strings.TrimSpace(cutComment(raw))
	if raw == "" {
		return "", errTOMLValue
	}
	if raw == "true" || raw == "false" {
		return raw, nil
	}
	if _, err := strconv.Atoi(raw); err == nil {
		return raw, nil
	}
	return "", errTOMLValue
}

func parseQuoted(raw string) (string, string, error) {
	var value strings.Builder
	escaped := false
	for i := 1; i < len(raw); i++ {
		c := raw[i]
		if escaped {
			switch c {
			case '"', '\\':
				value.WriteByte(c)
			default:
				return "", "", errTOMLValue
			}
			escaped = false
			continue
		}
		switch c {
		case '\\':
			escaped = true
		case '"':
			return value.String(), raw[i+1:], nil
		default:
			value.WriteByte(c)
		}
	}
	return "", "", errTOMLValue
}

func cutComment(s string) string {
	inQuote := false
	escaped := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case escaped:
			escaped = false
		case c == '\\' && inQuote:
			escaped = true
		case c == '"':
			inQuote = !inQuote
		case c == '#' && !inQuote:
			return s[:i]
		}
	}
	return s
}

func isBareKey(key string) bool {
	if key == "" {
		return false
	}
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// String returns the raw text value for section/key and whether it is present.
func (t TOML) String(section, key string) (string, bool) {
	value, ok := t[section][key]
	return value, ok
}

// Strings returns the string-array value for section/key, whether it is
// present, and an error when the key holds a bare value instead of a list.
func (t TOML) Strings(section, key string) ([]string, bool, error) {
	values, ok := t[section]
	if !ok {
		return nil, false, nil
	}
	raw, present := values[key]
	if !present {
		return nil, false, nil
	}
	if values[key+arrayMarker] != "1" {
		line, _ := strconv.Atoi(values[key+lineMarker])
		return nil, true, &TOMLError{
			Line: line,
			Err:  fmt.Errorf("%s needs a [ ... ] list", key),
		}
	}
	var items []string
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, true, fmt.Errorf("%s.%s: %w", section, key, err)
	}
	return items, true, nil
}

// Bool parses a boolean value; ok is false when the key is absent.
func (t TOML) Bool(section, key string) (value bool, ok bool, err error) {
	raw, ok := t.String(section, key)
	if !ok {
		return false, false, nil
	}
	value, err = strconv.ParseBool(raw)
	if err != nil {
		return false, true, fmt.Errorf("%s.%s: %w", section, key, err)
	}
	return value, true, nil
}

// Int parses an integer value; ok is false when the key is absent.
func (t TOML) Int(section, key string) (value int, ok bool, err error) {
	raw, ok := t.String(section, key)
	if !ok {
		return 0, false, nil
	}
	value, err = strconv.Atoi(raw)
	if err != nil {
		return 0, true, fmt.Errorf("%s.%s: %w", section, key, err)
	}
	return value, true, nil
}

package toolenv

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// DecodeArgs decodes raw tool arguments into dst. Boolean strings, numeric
// strings and JSON-encoded arrays or objects are coerced, one failing field per
// pass, until the arguments decode or no further coercion applies; a remaining
// type mismatch is rewritten into a sentence naming the field and the wanted
// type.
func DecodeArgs(tool string, args json.RawMessage, dst any) error {
	if len(bytes.TrimSpace(args)) == 0 {
		return nil
	}
	normalized := coerceBooleanStrings(args)
	for {
		err := json.Unmarshal(normalized, dst)
		if err == nil {
			return nil
		}
		coerced, ok := coerceJSONStringArgs(normalized, err)
		if !ok {
			coerced, ok = coerceNumberString(normalized, err)
		}
		if !ok || bytes.Equal(coerced, normalized) {
			if explained := explainArgTypeError(tool, normalized, dst, err); explained != err {
				return explained
			}
			return fmt.Errorf("%s: invalid arguments: %w", tool, err)
		}
		normalized = coerced
	}
}

// coerceJSONStringArgs rewrites the argument that failed to decode from a JSON
// string into the array or object it encodes, so clients that stringify every
// non-scalar can still fill structured fields. DecodeArgs calls it once per
// failing field, so every stringified array or object in a call is coerced. It
// reports false unless the failure is a type error for a field that wants a
// slice, array, map or struct and the string decodes to exactly that JSON kind.
func coerceJSONStringArgs(args json.RawMessage, err error) (json.RawMessage, bool) {
	var typeErr *json.UnmarshalTypeError
	if !errors.As(err, &typeErr) || typeErr.Field == "" {
		return nil, false
	}
	want := typeErr.Type
	for want != nil && want.Kind() == reflect.Pointer {
		want = want.Elem()
	}
	if want == nil {
		return nil, false
	}
	switch want.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map, reflect.Struct:
	default:
		return nil, false
	}

	raw, ok := fieldValue(args, typeErr.Field)
	if !ok {
		return nil, false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, false
	}
	var parsed any
	if err := json.Unmarshal([]byte(s), &parsed); err != nil {
		return nil, false
	}
	switch parsed.(type) {
	case []any:
		if want.Kind() != reflect.Slice && want.Kind() != reflect.Array {
			return nil, false
		}
	case map[string]any:
		if want.Kind() != reflect.Map && want.Kind() != reflect.Struct {
			return nil, false
		}
	default:
		return nil, false
	}

	var set func(json.RawMessage, []string, json.RawMessage) (json.RawMessage, bool)
	set = func(cur json.RawMessage, path []string, val json.RawMessage) (json.RawMessage, bool) {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(cur, &obj); err != nil {
			return nil, false
		}
		child, ok := obj[path[0]]
		if !ok {
			return nil, false
		}
		if len(path) == 1 {
			obj[path[0]] = val
		} else {
			child, ok = set(child, path[1:], val)
			if !ok {
				return nil, false
			}
			obj[path[0]] = child
		}
		out, err := json.Marshal(obj)
		if err != nil {
			return nil, false
		}
		return out, true
	}
	return set(args, strings.Split(typeErr.Field, "."), json.RawMessage(strings.TrimSpace(s)))
}

// coerceBooleanStrings rewrites top-level arguments whose value is the JSON
// string "true" or "false" (case-insensitive, trimmed) into the JSON boolean
// literal. Clients that stringify every argument can then still set boolean
// fields; every other string is left untouched so type errors stay actionable.
func coerceBooleanStrings(args json.RawMessage) json.RawMessage {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(args, &obj); err != nil {
		return args
	}
	changed := false
	for key, raw := range obj {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			continue
		}
		switch {
		case strings.EqualFold(strings.TrimSpace(s), "true"):
			obj[key] = json.RawMessage("true")
			changed = true
		case strings.EqualFold(strings.TrimSpace(s), "false"):
			obj[key] = json.RawMessage("false")
			changed = true
		}
	}
	if !changed {
		return args
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return args
	}
	return out
}

// explainArgTypeError replaces a JSON type mismatch in tool arguments with a
// sentence naming the tool, field and wanted type, plus the value that was sent.
// A mismatch inside an object names the failing member as field["key"]. Non-type
// errors are returned unchanged.
func explainArgTypeError(
	tool string,
	args json.RawMessage,
	dst any,
	err error,
) error {
	var typeErr *json.UnmarshalTypeError
	if !errors.As(err, &typeErr) || typeErr.Field == "" {
		return err
	}
	label := fmt.Sprintf("%q", typeErr.Field)
	raw, ok := fieldValue(args, typeErr.Field)
	if key, member, found := failingObjectMember(raw, fieldType(dst, typeErr.Field)); found {
		label = fmt.Sprintf("%s[%q]", typeErr.Field, key)
		raw, ok = member, true
	}
	return fmt.Errorf(
		"%s: %s must be %s, but the request sent %s.",
		tool,
		label,
		wantedType(typeErr.Type),
		sentValue(raw, ok),
	)
}

// failingObjectMember returns the first member of an object argument that does
// not decode into the map's value type, so a type mismatch inside a map names
// the member instead of the whole object. It reports false when the field is
// not map-typed, the value is not an object, or every member fits.
func failingObjectMember(
	raw json.RawMessage,
	field reflect.Type,
) (string, json.RawMessage, bool) {
	for field != nil && field.Kind() == reflect.Pointer {
		field = field.Elem()
	}
	if field == nil || field.Kind() != reflect.Map {
		return "", nil, false
	}
	want := field.Elem()
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return "", nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	if _, err := dec.Token(); err != nil {
		return "", nil, false
	}
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return "", nil, false
		}
		key, ok := token.(string)
		if !ok {
			return "", nil, false
		}
		var member json.RawMessage
		if err := dec.Decode(&member); err != nil {
			return "", nil, false
		}
		if err := json.Unmarshal(member, reflect.New(want).Interface()); err != nil {
			return key, member, true
		}
	}
	return "", nil, false
}

// fieldType resolves the Go type a dotted JSON field path decodes into, or nil
// when the path cannot be followed.
func fieldType(dst any, field string) reflect.Type {
	t := reflect.TypeOf(dst)
	for _, name := range strings.Split(field, ".") {
		for t != nil && (t.Kind() == reflect.Pointer ||
			t.Kind() == reflect.Slice || t.Kind() == reflect.Array) {
			t = t.Elem()
		}
		if t == nil {
			return nil
		}
		switch t.Kind() {
		case reflect.Struct:
			next, ok := jsonFieldType(t, name)
			if !ok {
				return nil
			}
			t = next
		case reflect.Map:
			t = t.Elem()
		default:
			return nil
		}
	}
	return t
}

// jsonFieldType returns the type of the struct field whose JSON name is name.
func jsonFieldType(t reflect.Type, name string) (reflect.Type, bool) {
	for i := range t.NumField() {
		f := t.Field(i)
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if tag == name {
			return f.Type, true
		}
	}
	return nil, false
}

// wantedType renders a JSON field's expected type in plain words.
func wantedType(t reflect.Type) string {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil {
		return "the correct type"
	}
	switch t.Kind() {
	case reflect.Bool:
		return "a boolean (true or false)"
	case reflect.String:
		return "a string"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Slice, reflect.Array:
		return "an array"
	case reflect.Map, reflect.Struct:
		return "an object"
	default:
		return "a " + t.Kind().String()
	}
}

// sentValue describes the raw argument value that failed to decode. ok reports
// whether the value could be resolved at all.
func sentValue(raw json.RawMessage, ok bool) string {
	if !ok {
		return "an unexpected value"
	}
	s := strings.TrimSpace(string(raw))
	switch {
	case s == "" || s == "null":
		return "null"
	case s[0] == '"':
		return "the string " + clipValue(s)
	case s[0] == '{':
		return "the object " + clipValue(s)
	case s[0] == '[':
		return "the array " + clipValue(s)
	case s == "true" || s == "false":
		return "the boolean " + s
	default:
		return "the number " + clipValue(s)
	}
}

// fieldValue resolves a dotted JSON field path against the raw arguments.
func fieldValue(args json.RawMessage, field string) (json.RawMessage, bool) {
	cur := args
	for _, part := range strings.Split(field, ".") {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(cur, &obj); err != nil {
			return nil, false
		}
		next, ok := obj[part]
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

// clipValue caps a rendered value so a large array or object cannot flood the
// error message.
func clipValue(s string) string {
	const limit = 80
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}

// coerceNumberString rewrites an argument that failed to decode because a JSON
// string was sent for a numeric field, when that string is a bare number that
// fits the field's type. Clients that stringify scalars can still set numeric
// fields; a non-numeric string keeps the original type error.
func coerceNumberString(args json.RawMessage, err error) (json.RawMessage, bool) {
	var typeErr *json.UnmarshalTypeError
	if !errors.As(err, &typeErr) || typeErr.Field == "" {
		return nil, false
	}
	want := typeErr.Type
	for want != nil && want.Kind() == reflect.Pointer {
		want = want.Elem()
	}
	if want == nil || !isNumberKind(want.Kind()) {
		return nil, false
	}
	raw, ok := fieldValue(args, typeErr.Field)
	if !ok {
		return nil, false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, false
	}
	literal := strings.TrimSpace(s)
	if !isBareNumber(literal) {
		return nil, false
	}
	if err := json.Unmarshal([]byte(literal), reflect.New(want).Interface()); err != nil {
		return nil, false
	}
	return setArgument(args, typeErr.Field, json.RawMessage(literal))
}

// isNumberKind reports whether a Go kind holds a schema number or integer.
func isNumberKind(k reflect.Kind) bool {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

// isBareNumber reports whether s is a JSON number literal with no surrounding
// text, so "8000" and "-1.5" qualify but "8,000", "eight" and "null" do not.
func isBareNumber(s string) bool {
	if s == "" {
		return false
	}
	if s[0] != '-' && (s[0] < '0' || s[0] > '9') {
		return false
	}
	return json.Valid([]byte(s))
}

// setArgument replaces a dotted field path in args with val, re-encoding each
// enclosing object so a nested numeric field is rewritten in place.
func setArgument(
	args json.RawMessage,
	field string,
	val json.RawMessage,
) (json.RawMessage, bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(args, &obj); err != nil {
		return nil, false
	}
	head, rest, nested := strings.Cut(field, ".")
	if !nested {
		obj[head] = val
	} else {
		child, ok := obj[head]
		if !ok {
			return nil, false
		}
		rewritten, ok := setArgument(child, rest, val)
		if !ok {
			return nil, false
		}
		obj[head] = rewritten
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, false
	}
	return out, true
}

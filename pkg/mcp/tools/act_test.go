package tools

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"webmcp/pkg/browsers"
)

func TestActValidateRequiredArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    string
		wantErr string
	}{
		{name: "click missing selector", args: `{"action":"click"}`, wantErr: "click requires selector"},
		{name: "click with handle", args: `{"action":"click","handle":"g1-b2"}`},
		{name: "input missing selector", args: `{"action":"input"}`, wantErr: "input requires selector"},
		{name: "type missing selector", args: `{"action":"type"}`, wantErr: "type requires selector"},
		{name: "hover missing selector", args: `{"action":"hover"}`, wantErr: "hover requires selector"},
		{name: "select missing selector", args: `{"action":"select"}`, wantErr: "select requires selector"},
		{name: "upload missing selector", args: `{"action":"upload_file"}`, wantErr: "upload_file requires selector"},
		{name: "click_at missing y", args: `{"action":"click_at","x":1}`, wantErr: "click_at requires x and y"},
		{name: "hover_at missing x", args: `{"action":"hover_at","y":1}`, wantErr: "hover_at requires x and y"},
		{name: "click_at explicit zero coords", args: `{"action":"click_at","x":0,"y":0}`},
		{name: "eval missing value", args: `{"action":"eval"}`, wantErr: "eval requires value"},
		{name: "navigate missing value", args: `{"action":"navigate"}`, wantErr: "navigate requires value"},
		{name: "replay missing target", args: `{"action":"replay"}`, wantErr: "replay requires target"},
		{name: "replay with URL substring", args: `{"action":"replay","target":"/api/user"}`},
		{name: "replay with seq and overrides", args: `{"action":"replay","target":"12","method":"PUT","headers":{"X-Test":"1"},"value":"{}"}`},
		{name: "replay with max_bytes", args: `{"action":"replay","target":"12","max_bytes":4096}`},
		{name: "replay negative max_bytes", args: `{"action":"replay","target":"12","max_bytes":-1}`, wantErr: "max_bytes must be >= 0"},
		{name: "back needs nothing", args: `{"action":"back"}`},
		{name: "sinks_install needs nothing", args: `{"action":"sinks_install"}`},
		{name: "sinks_clear needs nothing", args: `{"action":"sinks_clear"}`},
		{name: "inject_canary defaults", args: `{"action":"inject_canary"}`},
		{name: "inject_canary custom value and targets", args: `{"action":"inject_canary","value":"wm-canary-deadbeef","targets":["hash"]}`},
		{name: "postmessage_install needs nothing", args: `{"action":"postmessage_install"}`},
		{name: "postmessage_clear needs nothing", args: `{"action":"postmessage_clear"}`},
		{name: "post_message missing value", args: `{"action":"post_message"}`, wantErr: "post_message requires value"},
		{name: "post_message with value", args: `{"action":"post_message","value":"{\"t\":\"wm\"}"}`},
		{name: "post_message with frame and origin", args: `{"action":"post_message","value":"hi","target_selector":"iframe#w","origin":"https://w.example"}`},
		{name: "wait_open needs nothing", args: `{"action":"wait_open"}`},
		{name: "wait_open with timeout", args: `{"action":"wait_open","timeout_ms":5000}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var a actArgs
			if err := json.Unmarshal([]byte(tt.args), &a); err != nil {
				t.Fatalf("unmarshal args: %v", err)
			}
			var provided map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tt.args), &provided); err != nil {
				t.Fatalf("unmarshal provided: %v", err)
			}
			err := a.validate(provided)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validate() error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validate() = nil, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validate() = %q, want substring %q", err, tt.wantErr)
			}
		})
	}
}

// TestReplaySurface pins the replay contract an agent reads from the tool: the
// body cap is optional with a 256 KB ceiling, and the result always carries
// truncated, chain and body_redactions.
func TestReplaySurface(t *testing.T) {
	schema := (&Act{}).InputSchema()

	maxBytes, ok := schema.Properties["max_bytes"].(map[string]any)
	if !ok {
		t.Fatal("max_bytes schema property is missing")
	}
	desc, _ := maxBytes["description"].(string)
	for _, want := range []string{"262144", "truncated", "bytes"} {
		if !strings.Contains(desc, want) {
			t.Errorf("max_bytes description is missing %q: %q", want, desc)
		}
	}

	action, ok := schema.Properties["action"].(map[string]any)
	if !ok {
		t.Fatal("action schema property is missing")
	}
	propDesc, _ := action["description"].(string)
	toolDesc := (&Act{}).Description()
	for _, want := range []string{"body_redactions", "truncated", "chain", "max_bytes"} {
		if !strings.Contains(propDesc, want) {
			t.Errorf("action schema description is missing %q", want)
		}
		if !strings.Contains(toolDesc, want) {
			t.Errorf("page_act description is missing %q", want)
		}
	}
}

// TestActSinksActions pins that both tracer actions are advertised in the
// schema enum and described on both surfaces, and that they need no argument
// beyond session_id and action.
func TestActSinksActions(t *testing.T) {
	action, ok := (&Act{}).InputSchema().Properties["action"].(map[string]any)
	if !ok {
		t.Fatal("action schema property is missing")
	}
	enum, ok := action["enum"].([]string)
	if !ok {
		t.Fatal("action schema property has no enum")
	}
	propDesc, _ := action["description"].(string)
	toolDesc := (&Act{}).Description()

	for _, name := range []string{"sinks_install", "sinks_clear"} {
		if !slices.Contains(enum, name) {
			t.Errorf("action enum = %v, want %s included", enum, name)
		}
		if !strings.Contains(propDesc, name) {
			t.Errorf("action schema description does not name %s", name)
		}
		if !strings.Contains(toolDesc, name) {
			t.Errorf("page_act description does not name %s", name)
		}
	}

	for _, want := range []string{"never starts by itself", "what=sinks", "200-hit ring", "per document"} {
		if !strings.Contains(toolDesc, want) {
			t.Errorf("page_act description is missing %q", want)
		}
	}

	if got := actActionList(); !strings.Contains(got, "sinks_install, sinks_clear") {
		t.Errorf("actActionList() = %q, want both sinks actions", got)
	}
}

// TestPostMessageActions pins the postMessage send surface: both recorder
// actions and the sender are advertised, target_selector and origin are
// described, and the payload-free actions stay advertised.
func TestPostMessageActions(t *testing.T) {
	action, ok := (&Act{}).InputSchema().Properties["action"].(map[string]any)
	if !ok {
		t.Fatal("action schema property is missing")
	}
	enum, ok := action["enum"].([]string)
	if !ok {
		t.Fatal("action schema property has no enum")
	}
	propDesc, _ := action["description"].(string)
	toolDesc := (&Act{}).Description()

	for _, name := range []string{"postmessage_install", "postmessage_clear", "post_message"} {
		if !slices.Contains(enum, name) {
			t.Errorf("action enum = %v, want %s included", enum, name)
		}
		if !strings.Contains(propDesc, name) {
			t.Errorf("action schema description does not name %s", name)
		}
		if !strings.Contains(toolDesc, name) {
			t.Errorf("page_act description does not name %s", name)
		}
	}

	origin, ok := (&Act{}).InputSchema().Properties["origin"].(map[string]any)
	if !ok {
		t.Fatal("origin schema property is missing")
	}
	if def, _ := origin["default"].(string); def != "*" {
		t.Errorf("origin default = %v, want *", origin["default"])
	}

	target, ok := (&Act{}).InputSchema().Properties["target_selector"].(map[string]any)
	if !ok {
		t.Fatal("target_selector schema property is missing")
	}
	desc, _ := target["description"].(string)
	for _, want := range []string{"post_message", "self", "parent", "contentWindow", "not a frame"} {
		if !strings.Contains(desc, want) {
			t.Errorf("target_selector description is missing %q: %q", want, desc)
		}
	}
	for _, want := range []string{"self", "parent", "falling back to top"} {
		if !strings.Contains(propDesc, want) {
			t.Errorf("action schema description is missing %q", want)
		}
		if !strings.Contains(toolDesc, want) {
			t.Errorf("page_act description is missing %q", want)
		}
	}

	if got := actActionList(); !strings.Contains(got, "postmessage_install, postmessage_clear, post_message") {
		t.Errorf("actActionList() = %q, want the postMessage actions", got)
	}
}

// TestWaitOpenAction pins the wait_open surface: it is advertised in the schema
// enum, the action description and the tool description, and it needs no
// payload beyond session_id and action.
func TestWaitOpenAction(t *testing.T) {
	action, ok := (&Act{}).InputSchema().Properties["action"].(map[string]any)
	if !ok {
		t.Fatal("action schema property is missing")
	}
	enum, ok := action["enum"].([]string)
	if !ok {
		t.Fatal("action schema property has no enum")
	}
	if !slices.Contains(enum, "wait_open") {
		t.Fatalf("action enum = %v, want wait_open included", enum)
	}

	propDesc, _ := action["description"].(string)
	toolDesc := (&Act{}).Description()
	for _, want := range []string{"wait_open", "session_id", "opener"} {
		if !strings.Contains(propDesc, want) {
			t.Errorf("action schema description is missing %q", want)
		}
		if !strings.Contains(toolDesc, want) {
			t.Errorf("page_act description is missing %q", want)
		}
	}
	if got := actActionList(); !strings.Contains(got, "wait_open") {
		t.Errorf("actActionList() = %q, want wait_open", got)
	}
}

// TestInjectCanaryAction pins the inject_canary surface: it is advertised, it
// names its targets and report shape, and the guard error text is exactly the
// documented one.
func TestInjectCanaryAction(t *testing.T) {
	action, ok := (&Act{}).InputSchema().Properties["action"].(map[string]any)
	if !ok {
		t.Fatal("action schema property is missing")
	}
	enum, ok := action["enum"].([]string)
	if !ok {
		t.Fatal("action schema property has no enum")
	}
	if !slices.Contains(enum, "inject_canary") {
		t.Fatalf("action enum = %v, want inject_canary included", enum)
	}

	propDesc, _ := action["description"].(string)
	toolDesc := (&Act{}).Description()
	for _, want := range []string{"inject_canary", "query", "hash", "postmessage", "name"} {
		if !strings.Contains(propDesc, want) {
			t.Errorf("action schema description is missing %q", want)
		}
		if !strings.Contains(toolDesc, want) {
			t.Errorf("page_act description is missing %q", want)
		}
	}

	targets, ok := (&Act{}).InputSchema().Properties["targets"].(map[string]any)
	if !ok {
		t.Fatal("targets schema property is missing")
	}
	items, ok := targets["items"].(map[string]any)
	if !ok {
		t.Fatal("targets schema property has no items")
	}
	names, ok := items["enum"].([]string)
	if !ok {
		t.Fatal("targets items have no enum")
	}
	for _, want := range []string{"query", "hash", "postmessage", "name"} {
		if !slices.Contains(names, want) {
			t.Errorf("targets enum = %v, want %q", names, want)
		}
	}

	got := fmt.Errorf("page_act: %w", browsers.ErrCanaryNeedsTracer).Error()
	want := `page_act: inject_canary needs the sink tracer — run action "sinks_install" first`
	if got != want {
		t.Fatalf("guard error = %q, want %q", got, want)
	}
}

// TestActDecodeCoercesEveryStructuredField pins that a call carrying two
// stringified structured fields decodes both, in one decodeArgs pass chain.
func TestActDecodeCoercesEveryStructuredField(t *testing.T) {
	args := json.RawMessage(`{
		"action":"replay_fuzz",
		"target":"12",
		"points":"[{\"param\":\"q\",\"location\":\"query\"}]",
		"payloads":"[\"a\",\"b\"]"
	}`)
	var a actArgs
	if err := decodeArgs("page_act", args, &a); err != nil {
		t.Fatalf("decodeArgs() error = %v", err)
	}
	if len(a.Points) != 1 || a.Points[0].Param != "q" || a.Points[0].Location != browsers.FuzzQuery {
		t.Fatalf("points = %+v, want one query point for q", a.Points)
	}
	if !slices.Equal(a.Payloads, []string{"a", "b"}) {
		t.Fatalf("payloads = %v, want [a b]", a.Payloads)
	}
}

// TestActDecodeTypeErrorNamesField pins that a real type mismatch names the
// offending field and the type the schema wants, with no fixed retry example.
func TestActDecodeTypeErrorNamesField(t *testing.T) {
	var a actArgs
	err := decodeArgs("page_act", json.RawMessage(`{"action":"wait","timeout_ms":"soon"}`), &a)
	if err == nil {
		t.Fatal("decodeArgs() = nil, want a type error")
	}
	if got, want := err.Error(), `page_act: "timeout_ms" must be a number`; !strings.Contains(got, want) {
		t.Fatalf("decodeArgs() = %q, want it to name %q", got, want)
	}
	if strings.Contains(err.Error(), "Retry with") {
		t.Fatalf("decodeArgs() = %q, want no fixed retry call", err)
	}
}

// TestActDecodeUncoercibleFieldHasNoExampleHint pins that a structured field
// holding text that is not JSON fails actionably and carries no example call.
func TestActDecodeUncoercibleFieldHasNoExampleHint(t *testing.T) {
	var a actArgs
	err := decodeArgs("page_act", json.RawMessage(`{"action":"reload","points":"not-json"}`), &a)
	if err == nil {
		t.Fatal("decodeArgs() = nil, want a type error")
	}
	if !strings.Contains(err.Error(), `"points" must be an array`) {
		t.Fatalf("decodeArgs() = %q, want it to name points and the array type", err)
	}
	for _, banned := range []string{"Retry with", "page-1", "reload"} {
		if strings.Contains(err.Error(), banned) {
			t.Fatalf("decodeArgs() = %q, want no %q hint", err, banned)
		}
	}
}

// TestActSchemaDocumentsCoercionAndTiming pins the audit wording: stringified
// arrays are advertised, a sub-second timeout is warned about, raw disables the
// pause for that call, and the over-bound example quotes the engine message.
func TestActSchemaDocumentsCoercionAndTiming(t *testing.T) {
	schema := (&Act{}).InputSchema()
	tests := []struct {
		field  string
		phrase string
	}{
		{"points", "stringified JSON array"},
		{"payloads", "stringified JSON array"},
		{"timeout_ms", "raw:true"},
		{"timeout_ms", "1000 ms"},
		{"raw", "for this call"},
		{"raw", "no pre/post action pause"},
	}
	for _, tt := range tests {
		prop, ok := schema.Properties[tt.field].(map[string]any)
		if !ok {
			t.Fatalf("%s schema property is missing", tt.field)
		}
		desc, _ := prop["description"].(string)
		if !strings.Contains(desc, tt.phrase) {
			t.Errorf("%s description is missing %q: %q", tt.field, tt.phrase, desc)
		}
	}
	for _, desc := range []string{(&Act{}).Description(), schemaDesc(schema.Properties["raw"])} {
		if strings.Contains(desc, "never overruns") {
			t.Errorf("description still promises a pause never overruns the call")
		}
	}
	if want := "replay_fuzz: requests computed 243, over the 100 bound"; !strings.Contains((&Act{}).Description(), want) {
		t.Errorf("page_act description does not quote the engine message %q", want)
	}
}

func schemaDesc(prop any) string {
	m, _ := prop.(map[string]any)
	desc, _ := m["description"].(string)
	return desc
}

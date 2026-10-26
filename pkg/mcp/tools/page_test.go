package tools

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"slices"
	"strings"
	"testing"

	"webmcp/pkg/mcp/models"
)

// testCarrier is the image carrier decoded by the tests.
type testCarrier struct {
	OK       bool                       `json:"ok"`
	Data     map[string]json.RawMessage `json:"data"`
	Image    models.ContentBlock        `json:"image"`
	Fallback struct {
		AsDataURI bool   `json:"as_data_uri"`
		Field     string `json:"field"`
	} `json:"fallback"`
}

func testPNG(t *testing.T) (string, string) {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatalf("encode test PNG: %v", err)
	}
	encoded := base64.StdEncoding.EncodeToString(buf.Bytes())
	return "data:image/png;base64," + encoded, encoded
}

// TestPageFetchIncludeValues pins the storage flag plumbing: the argument
// decodes, the schema defaults it to false, and storage is registered as a
// self-capped aspect that never sees max_bytes truncation.
func TestPageFetchIncludeValues(t *testing.T) {
	var a pageFetchArgs
	if err := decodeArgs("page_fetch", json.RawMessage(
		`{"session_id":"page-1","what":"storage","include_values":true}`,
	), &a); err != nil {
		t.Fatalf("decodeArgs() error = %v", err)
	}
	if !a.IncludeValues {
		t.Error("include_values did not decode")
	}

	prop, ok := (&Page{}).InputSchema().Properties["include_values"].(map[string]any)
	if !ok {
		t.Fatal("include_values schema property is missing")
	}
	if def, ok := prop["default"].(bool); !ok || def {
		t.Errorf("include_values default = %v, want false", prop["default"])
	}

	what, ok := (&Page{}).InputSchema().Properties["what"].(map[string]any)
	if !ok {
		t.Fatal("what schema property is missing")
	}
	enum, _ := what["enum"].([]string)
	if !slices.Contains(enum, "storage") {
		t.Errorf("what enum = %v, want storage included", enum)
	}

	asp, ok := aspects["storage"]
	if !ok || asp.read == nil {
		t.Fatal("storage aspect is not registered")
	}
	if asp.trunc != truncationNone {
		t.Errorf("storage truncation = %v, want truncationNone", asp.trunc)
	}
}

// TestPageFetchSinks pins the read side of the tracer surface: sinks is a
// self-capped aspect present in the enum, and the description carries the
// read-only and arming contract.
func TestPageFetchSinks(t *testing.T) {
	asp, ok := aspects["sinks"]
	if !ok || asp.read == nil {
		t.Fatal("sinks aspect is not registered")
	}
	if asp.trunc != truncationNone {
		t.Errorf("sinks truncation = %v, want truncationNone", asp.trunc)
	}

	what, ok := (&Page{}).InputSchema().Properties["what"].(map[string]any)
	if !ok {
		t.Fatal("what schema property is missing")
	}
	enum, _ := what["enum"].([]string)
	if !slices.Contains(enum, "sinks") {
		t.Errorf("what enum = %v, want sinks included", enum)
	}

	desc := (&Page{}).Description()
	for _, want := range []string{"read-only", "never arms capture", "sinks_install", "200-hit ring", "per document"} {
		if !strings.Contains(desc, want) {
			t.Errorf("page_fetch description is missing %q", want)
		}
	}
}

// TestPageFetchPostMessage pins the postMessage read surface: it is a
// self-capped aspect present in the enum, and the description carries the
// opt-in arming, aggregation and clear contract.
func TestPageFetchPostMessage(t *testing.T) {
	asp, ok := aspects["postmessage"]
	if !ok || asp.read == nil {
		t.Fatal("postmessage aspect is not registered")
	}
	if asp.trunc != truncationNone {
		t.Errorf("postmessage truncation = %v, want truncationNone", asp.trunc)
	}

	what, ok := (&Page{}).InputSchema().Properties["what"].(map[string]any)
	if !ok {
		t.Fatal("what schema property is missing")
	}
	enum, _ := what["enum"].([]string)
	if !slices.Contains(enum, "postmessage") {
		t.Errorf("what enum = %v, want postmessage included", enum)
	}

	desc := (&Page{}).Description()
	for _, want := range []string{"postMessage recorder", "postmessage_install", "postmessage_clear", "target=clear", "frames", "500 runes"} {
		if !strings.Contains(desc, want) {
			t.Errorf("page_fetch description is missing %q", want)
		}
	}

	propDesc, _ := what["description"].(string)
	for _, want := range []string{"postmessage", "postmessage_install", "target=clear"} {
		if !strings.Contains(propDesc, want) {
			t.Errorf("what schema description is missing %q", want)
		}
	}
}

func TestScreenshotCarrier(t *testing.T) {
	uri, encoded := testPNG(t)

	out, err := screenshotCarrier(uri, true)
	if err != nil {
		t.Fatalf("screenshotCarrier() error = %v", err)
	}
	var got testCarrier
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode carrier: %v", err)
	}
	if !got.OK {
		t.Error("carrier ok = false, want true")
	}
	if got.Image.Type != "image" || got.Image.MimeType != "image/png" {
		t.Errorf("image block = %+v, want type image and mimeType image/png", got.Image)
	}
	if got.Image.Data != encoded {
		t.Error("image block does not carry the PNG base64 payload")
	}
	if !got.Fallback.AsDataURI {
		t.Error("fallback does not expose the data URI")
	}
	if len(got.Data) == 0 {
		t.Fatal("carrier data is empty, want screenshot metadata")
	}
	if n := strings.Count(out, encoded); n != 1 {
		t.Errorf("base64 payload occurs %d times in the carrier, want exactly 1", n)
	}
}

func TestMarksCarrier(t *testing.T) {
	uri, encoded := testPNG(t)
	marks := `{"marks":[{"index":1,"role":"a","text":"Docs","x":1,"y":2,"width":3,"height":4}],"screenshot":"` +
		uri + `","viewport_width":1280,"viewport_height":800,"truncated":0}`

	out, err := marksCarrier(marks)
	if err != nil {
		t.Fatalf("marksCarrier() error = %v", err)
	}
	var got testCarrier
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode carrier: %v", err)
	}
	if got.Image.Type != "image" || got.Image.Data != encoded {
		t.Error("marks carrier does not carry the annotated PNG as an image block")
	}
	if got.Fallback.Field != "screenshot" {
		t.Errorf("fallback field = %q, want screenshot", got.Fallback.Field)
	}
	if _, ok := got.Data["screenshot"]; ok {
		t.Error("text data still contains the screenshot data URI")
	}
	if _, ok := got.Data["marks"]; !ok {
		t.Error("text data dropped the mark table")
	}
	if _, ok := got.Data["viewport_width"]; !ok {
		t.Error("text data dropped the viewport dimensions")
	}
	if n := strings.Count(out, encoded); n != 1 {
		t.Errorf("base64 payload occurs %d times in the carrier, want exactly 1", n)
	}
}

// TestSessionQualifiersAreOptionalOnPageSurfaces pins the declarative lookup
// contract on wire surfaces: page_fetch, page_act and session_close each accept
// an optional browser_id and owner, and neither is ever required.
func TestSessionQualifiersAreOptionalOnPageSurfaces(t *testing.T) {
	surfaces := []struct {
		name   string
		schema models.InputSchema
	}{
		{"page_fetch", (&Page{}).InputSchema()},
		{"page_act", (&Act{}).InputSchema()},
		{"session_close", (&SessionClose{}).InputSchema()},
	}
	for _, surface := range surfaces {
		t.Run(surface.name, func(t *testing.T) {
			for _, key := range []string{"browser_id", "owner"} {
				if _, ok := surface.schema.Properties[key]; !ok {
					t.Fatalf("%s schema is missing %q", surface.name, key)
				}
			}
			for _, required := range surface.schema.Required {
				if required == "browser_id" || required == "owner" {
					t.Fatalf("%s marks %q required; both must stay optional", surface.name, required)
				}
			}
		})
	}

	var a actArgs
	if err := json.Unmarshal([]byte(
		`{"session_id":"page-1","action":"back","browser_id":"browser-2","owner":"task-b"}`,
	), &a); err != nil {
		t.Fatalf("decode page_act qualifiers: %v", err)
	}
	if a.BrowserID != "browser-2" || a.Owner != "task-b" {
		t.Fatalf("page_act qualifiers = %q/%q, want browser-2/task-b", a.BrowserID, a.Owner)
	}
}

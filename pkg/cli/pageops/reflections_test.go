package pageops

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ysmood/gson"
)

// TestDecodeReflectionsReadsRawEvalValue pins the decode seam: the eval result
// arrives as an unparsed gson value and must decode in one pass. Reading it
// through any accessor first makes Unmarshal fail with "value has been parsed",
// which is exactly the defect this guards. A null result means the selector
// matched nothing.
func TestDecodeReflectionsReadsRawEvalValue(t *testing.T) {
	const payload = `{"marker":"wm-probe-1a2b","url":"https://x.test","total":0,"hits":[]}`

	probe, err := decodeReflections(gson.NewFrom(payload))
	if err != nil {
		t.Fatalf("decodeReflections() error = %v, want nil", err)
	}
	if probe == nil {
		t.Fatal("decodeReflections() = nil, want the decoded probe")
	}
	if probe.Marker != "wm-probe-1a2b" {
		t.Fatalf("marker = %q, want wm-probe-1a2b", probe.Marker)
	}

	b, err := json.Marshal(shapeReflections(*probe))
	if err != nil {
		t.Fatalf("marshal reflections: %v", err)
	}
	const want = `{"marker":"wm-probe-1a2b","count":0,"truncated":0,"hits":[]}`
	if string(b) != want {
		t.Fatalf("reflections = %s, want %s", b, want)
	}

	nullProbe, err := decodeReflections(gson.NewFrom("null"))
	if err != nil {
		t.Fatalf("decodeReflections(null) error = %v, want nil", err)
	}
	if nullProbe != nil {
		t.Fatalf("decodeReflections(null) = %+v, want nil", nullProbe)
	}
}

// TestReflectionsContext pins the context classifier over saved raw facts: the
// node kind and the delimiter the probe observed choose the context name.
func TestReflectionsContext(t *testing.T) {
	const marker = "wm-probe-1a2b"
	cases := []struct {
		name string
		raw  reflectionRaw
		want string
	}{
		{
			name: "double-quoted attribute",
			raw:  reflectionRaw{Where: "attr", Attribute: "value", Quote: "\"", Raw: marker},
			want: "attr-double",
		},
		{
			name: "single-quoted attribute",
			raw:  reflectionRaw{Where: "attr", Attribute: "href", Quote: "'", Raw: marker},
			want: "attr-single",
		},
		{
			name: "unquoted attribute",
			raw:  reflectionRaw{Where: "attr", Attribute: "href", Quote: "", Raw: marker},
			want: "attr-unquoted",
		},
		{
			name: "text node",
			raw:  reflectionRaw{Where: "dom-text", Raw: marker},
			want: "html-text",
		},
		{
			name: "script string",
			raw:  reflectionRaw{Where: "script-text", Quote: "\"", Raw: "var x = \"" + marker + "\""},
			want: "js-string",
		},
		{
			name: "script template",
			raw:  reflectionRaw{Where: "script-text", Quote: "`", Raw: "var x = `" + marker + "`"},
			want: "js-template",
		},
		{
			name: "style text",
			raw:  reflectionRaw{Where: "style-text", Raw: ".a{content:\"" + marker + "\"}"},
			want: "css-value",
		},
		{
			name: "comment",
			raw:  reflectionRaw{Where: "comment", Raw: marker},
			want: "comment",
		},
		{
			name: "url",
			raw:  reflectionRaw{Where: "url", Raw: "https://x.test/?q=" + marker},
			want: "url",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shapeReflectionHit(marker, tc.raw)
			if got.Context != tc.want {
				t.Fatalf("context = %q, want %q", got.Context, tc.want)
			}
		})
	}
}

// TestReflectionsRisk pins the risk rule: dangerous only when a break character
// survives in an executable context, escaped when the entity form is present,
// inert otherwise. Survival is scoped to the marker's own special characters.
func TestReflectionsRisk(t *testing.T) {
	cases := []struct {
		name     string
		marker   string
		raw      reflectionRaw
		wantRisk string
		wantSurv []string
	}{
		{
			name:     "raw less-than in html text is dangerous",
			marker:   "wm<probe",
			raw:      reflectionRaw{Where: "dom-text", Raw: "wm<probe"},
			wantRisk: "dangerous",
			wantSurv: []string{"<"},
		},
		{
			name:     "entity less-than in html text is escaped",
			marker:   "wm<probe",
			raw:      reflectionRaw{Where: "dom-text", Raw: "wm&lt;probe"},
			wantRisk: "escaped",
			wantSurv: []string{},
		},
		{
			name:     "plain marker is inert",
			marker:   "wm-probe-1a2b",
			raw:      reflectionRaw{Where: "dom-text", Raw: "wm-probe-1a2b"},
			wantRisk: "inert",
			wantSurv: []string{},
		},
		{
			name:     "raw quote in a double-quoted attribute is dangerous",
			marker:   "wm\"probe",
			raw:      reflectionRaw{Where: "attr", Attribute: "value", Quote: "\"", Raw: "wm\"probe"},
			wantRisk: "dangerous",
			wantSurv: []string{"\""},
		},
		{
			name:     "raw quote in a single-quoted attribute is dangerous",
			marker:   "wm'probe",
			raw:      reflectionRaw{Where: "attr", Attribute: "href", Quote: "'", Raw: "wm'probe"},
			wantRisk: "dangerous",
			wantSurv: []string{"'"},
		},
		{
			name:     "entity quote in a double-quoted attribute is escaped",
			marker:   "wm\"probe",
			raw:      reflectionRaw{Where: "attr", Attribute: "value", Quote: "\"", Raw: "wm&quot;probe"},
			wantRisk: "escaped",
			wantSurv: []string{},
		},
		{
			name:     "breakout in a js string is dangerous",
			marker:   "wm\"probe",
			raw:      reflectionRaw{Where: "script-text", Quote: "\"", Raw: "var x = \"wm\"probe\""},
			wantRisk: "dangerous",
			wantSurv: []string{"\""},
		},
		{
			name:     "less-than in a style value is dangerous",
			marker:   "wm<probe",
			raw:      reflectionRaw{Where: "style-text", Raw: ".a{content:\"wm<probe\"}"},
			wantRisk: "dangerous",
			wantSurv: []string{"<"},
		},
		{
			name:     "url context is never executable",
			marker:   "wm<probe",
			raw:      reflectionRaw{Where: "url", Raw: "https://x.test/?q=wm<probe"},
			wantRisk: "inert",
			wantSurv: []string{"<"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shapeReflectionHit(tc.marker, tc.raw)
			if got.Risk != tc.wantRisk {
				t.Fatalf("risk = %q, want %q", got.Risk, tc.wantRisk)
			}
			if len(got.Survived) != len(tc.wantSurv) || (len(tc.wantSurv) > 0 && !reflect.DeepEqual(got.Survived, tc.wantSurv)) {
				t.Fatalf("survived = %v, want %v", got.Survived, tc.wantSurv)
			}
		})
	}
}

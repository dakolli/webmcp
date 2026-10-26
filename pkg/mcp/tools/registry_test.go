package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeArgsCoercesBooleanStrings(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"lowercase true", `"true"`, true},
		{"mixed case with spaces", `" True "`, true},
		{"lowercase false", `"false"`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var a struct {
				IgnoreCerts *bool `json:"ignore_certs"`
			}
			args := json.RawMessage(`{"ignore_certs":` + tt.input + `}`)
			if err := decodeArgs("browser_create", args, &a); err != nil {
				t.Fatalf("decodeArgs() error = %v", err)
			}
			if a.IgnoreCerts == nil || *a.IgnoreCerts != tt.want {
				t.Fatalf("ignore_certs = %v, want %v", a.IgnoreCerts, tt.want)
			}
		})
	}
}

func TestDecodeArgsCoercesJSONStringArgs(t *testing.T) {
	tests := []struct {
		name  string
		args  string
		check func(t *testing.T, scope []string, headers map[string]string)
	}{
		{
			name: "array field",
			args: `{"scope":"[\"wikipedia.org\"]"}`,
			check: func(t *testing.T, scope []string, _ map[string]string) {
				t.Helper()
				if len(scope) != 1 || scope[0] != "wikipedia.org" {
					t.Fatalf("scope = %v, want [wikipedia.org]", scope)
				}
			},
		},
		{
			name: "object field",
			args: `{"headers":"{\"X-A\":\"b\"}"}`,
			check: func(t *testing.T, _ []string, headers map[string]string) {
				t.Helper()
				if headers["X-A"] != "b" {
					t.Fatalf("headers = %v, want X-A=b", headers)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var a struct {
				Scope   []string          `json:"scope"`
				Headers map[string]string `json:"headers"`
			}
			if err := decodeArgs("browser_create", json.RawMessage(tt.args), &a); err != nil {
				t.Fatalf("decodeArgs() error = %v", err)
			}
			tt.check(t, a.Scope, a.Headers)
		})
	}
}

func TestDecodeArgsExplainsTypeMismatch(t *testing.T) {
	t.Run("string for bool", func(t *testing.T) {
		var a struct {
			IgnoreCerts *bool `json:"ignore_certs"`
		}
		err := decodeArgs(
			"browser_create",
			json.RawMessage(`{"ignore_certs":"yes"}`),
			&a,
		)
		if err == nil {
			t.Fatal("decodeArgs() = nil, want an argument type error")
		}
		want := `browser_create: "ignore_certs" must be a boolean (true or false), but the request sent the string "yes".`
		if got := firstLine(err.Error()); got != want {
			t.Fatalf("decodeArgs() first line = %q, want %q", got, want)
		}
		if !strings.Contains(err.Error(), "\nRetry with: ") {
			t.Fatalf("decodeArgs() = %q, want a Retry with hint", err)
		}
	})

	t.Run("number for string", func(t *testing.T) {
		var a struct {
			BrowserID string `json:"browser_id"`
		}
		err := decodeArgs("page_list", json.RawMessage(`{"browser_id":123}`), &a)
		if err == nil {
			t.Fatal("decodeArgs() = nil, want an argument type error")
		}
		want := `page_list: "browser_id" must be a string, but the request sent the number 123.`
		if got := firstLine(err.Error()); got != want {
			t.Fatalf("decodeArgs() first line = %q, want %q", got, want)
		}
		if !strings.Contains(err.Error(), "\nRetry with: ") {
			t.Fatalf("decodeArgs() = %q, want a Retry with hint", err)
		}
	})

	t.Run("unparseable structured value explains the mismatch", func(t *testing.T) {
		var a struct {
			Scope []string `json:"scope"`
		}
		err := decodeArgs("browser_create", json.RawMessage(`{"scope":"not-json"}`), &a)
		if err == nil {
			t.Fatal("decodeArgs() = nil, want an argument error")
		}
		if !strings.Contains(err.Error(), `"scope" must be an array`) {
			t.Fatalf("decodeArgs() = %q, want the field and wanted type", err)
		}
		if !strings.Contains(err.Error(), "\nRetry with: ") {
			t.Fatalf("decodeArgs() = %q, want a Retry with hint", err)
		}
	})

	t.Run("string field holding json text stays a string", func(t *testing.T) {
		var a struct {
			Value string `json:"value"`
		}
		if err := decodeArgs("page_act", json.RawMessage(`{"value":"[1,2]"}`), &a); err != nil {
			t.Fatalf("decodeArgs() error = %v", err)
		}
		if a.Value != "[1,2]" {
			t.Fatalf("value = %q, want the literal text %q", a.Value, "[1,2]")
		}
	})
}

func TestRegistryExecuteRejectsUnknownTool(t *testing.T) {
	r := NewEmptyRegistry()
	if _, err := r.Execute(context.Background(), "nope", json.RawMessage(`{}`)); err == nil {
		t.Fatal("Execute(unknown) = nil, want an unsupported tool error")
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

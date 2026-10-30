package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/dakolli/webmcp/pkg/caido"
	"github.com/dakolli/webmcp/pkg/config"
)

func TestRenderCaidoInstructions(t *testing.T) {
	tests := []struct {
		name string
		st   caido.Status
		want []string
		deny []string
	}{
		{
			name: "empty allowlist renders all traffic",
			st: caido.Status{
				Project: caido.ProjectStatus{Name: "webmcp"},
				Scopes: []caido.ScopeStatus{
					{ID: "1", Name: "target"},
				},
			},
			want: []string{
				"Caido project: webmcp (persistent).",
				"scope target (id 1): all traffic",
			},
			deny: []string{"Focus scope:"},
		},
		{
			name: "focus scope line appears when configured",
			st: caido.Status{
				Project: caido.ProjectStatus{Name: "webmcp", Temporary: true},
				Scopes: []caido.ScopeStatus{
					{ID: "1", Name: "target", Focus: true},
					{ID: "2", Name: "other", Allowlist: []string{"example.com"}},
				},
			},
			want: []string{
				"Caido project: webmcp (temporary).",
				"scope target (id 1): all traffic",
				"scope other (id 2): example.com",
				"Focus scope: target (1).",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			text := renderCaidoInstructions(tc.st)
			for _, want := range tc.want {
				if !strings.Contains(text, want) {
					t.Errorf("instructions missing %q:\n%s", want, text)
				}
			}
			for _, deny := range tc.deny {
				if strings.Contains(text, deny) {
					t.Errorf("instructions unexpectedly contain %q:\n%s", deny, text)
				}
			}
			if len(text) >= caidoInstructionsLimit {
				t.Errorf("instructions are %d characters, want under %d", len(text), caidoInstructionsLimit)
			}
		})
	}
}

func TestRenderCaidoInstructionsStaysUnderLimit(t *testing.T) {
	scopes := make([]caido.ScopeStatus, 0, 20)
	for i := 0; i < 20; i++ {
		scopes = append(scopes, caido.ScopeStatus{
			ID:   "scope-id-with-some-width",
			Name: "a-rather-long-scope-name",
			Allowlist: []string{
				"alpha.example.com",
				"beta.example.com",
				"gamma.example.com",
				"delta.example.com",
				"epsilon.example.com",
			},
		})
	}
	text := renderCaidoInstructions(caido.Status{
		Project: caido.ProjectStatus{Name: "webmcp"},
		Scopes:  scopes,
	})
	if len(text) >= caidoInstructionsLimit {
		t.Fatalf("instructions are %d characters, want under %d:\n%s", len(text), caidoInstructionsLimit, text)
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection refused")
}

func TestCaidoInstructionsUnavailableOnError(t *testing.T) {
	c := newCaidoInstructions(config.CaidoFile{
		URL:        "http://127.0.0.1:1/graphql",
		AllowGuest: true,
	})
	c.client.HTTP = &http.Client{Transport: failingTransport{}}

	text := c.instructions(context.Background())
	if text == "" {
		t.Fatal("expected a degraded instruction string, got empty")
	}
	if !strings.Contains(text, "Caido history is unavailable:") {
		t.Errorf("expected unavailable notice, got %q", text)
	}
	if !strings.Contains(text, "connection refused") {
		t.Errorf("expected the failure reason, got %q", text)
	}
}

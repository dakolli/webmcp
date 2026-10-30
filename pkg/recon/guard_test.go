package recon

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGuardScope(t *testing.T) {
	cases := []struct {
		name    string
		guard   Guard
		hosts   []string
		wantErr bool
	}{
		{
			name:  "bare host covers subdomain",
			guard: Guard{Scope: []string{"example.com"}},
			hosts: []string{"api.example.com"},
		},
		{
			name:    "out of scope names target",
			guard:   Guard{Scope: []string{"example.com"}},
			hosts:   []string{"evil.test"},
			wantErr: true,
		},
		{
			name:    "strict empty scope denies",
			guard:   Guard{ScopeStrict: true},
			hosts:   []string{"example.com"},
			wantErr: true,
		},
		{
			name:  "open empty scope allows",
			guard: Guard{},
			hosts: []string{"anything.test"},
		},
	}
	for i := range cases {
		tc := &cases[i]
		t.Run(tc.name, func(t *testing.T) {
			err := tc.guard.CheckHosts(tc.hosts)
			if tc.wantErr && err == nil {
				t.Fatalf("CheckHosts(%v) = nil, want an error", tc.hosts)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("CheckHosts(%v) = %v, want nil", tc.hosts, err)
			}
			if tc.name == "out of scope names target" {
				for _, want := range []string{"evil.test", "example.com"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not name %q", err, want)
					}
				}
			}
		})
	}
}

func TestGuardCheckTargetsClassifiesURL(t *testing.T) {
	err := (&Guard{Scope: []string{"example.com"}}).CheckTargets([]string{"https://evil.test/path"})
	if err == nil {
		t.Fatal("CheckTargets accepted an out-of-scope URL")
	}
	if !strings.Contains(err.Error(), "evil.test") {
		t.Errorf("error %q does not name the target host", err)
	}
}

func TestGuardCaps(t *testing.T) {
	hosts := make([]string, MaxHosts+1)
	urls := make([]string, MaxURLs+1)
	ports := make([]string, MaxPorts+1)
	for i := range ports {
		ports[i] = "80"
	}
	cases := []struct {
		name  string
		check func(*Guard) error
		want  string
	}{
		{
			name:  "hosts",
			check: func(g *Guard) error { return g.CheckHosts(hosts) },
			want:  "501",
		},
		{
			name:  "urls",
			check: func(g *Guard) error { return g.CheckURLs(urls) },
			want:  "5001",
		},
		{
			name:  "ports",
			check: func(g *Guard) error { return g.CheckPorts(ports) },
			want:  "201",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.check(&Guard{})
			if err == nil {
				t.Fatal("over-cap call returned nil")
			}
			for _, want := range []string{tc.want, "output_file"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

func TestGuardReservePaces(t *testing.T) {
	g := &Guard{RPS: 5, Burst: 10}
	now := time.Unix(0, 0)
	for i := 0; i < DefaultBurst; i++ {
		if delay := g.reserve("example.com", now); delay != 0 {
			t.Fatalf("reserve %d of burst = %s, want 0", i+1, delay)
		}
	}
	if delay := g.reserve("example.com", now); delay <= 0 {
		t.Fatalf("reserve past burst = %s, want a positive wait", delay)
	}
}

func TestReconImportsNoMCP(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, imp := range parsed.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			if strings.Contains(path, "/pkg/mcp") {
				t.Errorf("%s imports %q; pkg/recon must not import pkg/mcp", file, path)
			}
		}
	}
}

// init registers a spec the way a scanner file does, proving a tool added from
// another file's init() appears in Get and All without a central table.
func init() {
	Register(Spec{
		ID:      "recon_init_test",
		Module:  "example.com/init-test",
		Pin:     "v1.0.0",
		BinName: "init-test",
	})
}

func TestRegistryInitRegistration(t *testing.T) {
	spec, ok := Get("recon_init_test")
	if !ok {
		t.Fatal("Get did not find the spec registered from init()")
	}
	if spec.BinName != "init-test" {
		t.Errorf("BinName = %q, want init-test", spec.BinName)
	}
	found := false
	for _, s := range All() {
		if s.ID == spec.ID {
			found = true
		}
	}
	if !found {
		t.Error("All did not include the spec registered from init()")
	}
}

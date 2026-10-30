package caido

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"
)

// TestLiveCaido reads Login, Status and Search against a running instance,
// skipped unless CAIDO_LIVE=1. It issues queries only and changes no state.
func TestLiveCaido(t *testing.T) {
	if os.Getenv("CAIDO_LIVE") != "1" {
		t.Skip("set CAIDO_LIVE=1 to test against a live instance")
	}

	base := os.Getenv("CAIDO_URL")
	if base == "" {
		base = "http://127.0.0.1:8080"
	}
	endpoint, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parsing CAIDO_URL %q: %v", base, err)
	}
	if endpoint.Path == "" || endpoint.Path == "/" {
		endpoint.Path = "/graphql"
	}
	c := NewClient(endpoint.String())

	const budget = 20 * time.Second

	t.Run("login", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), budget)
		defer cancel()
		path, err := c.Login(
			ctx,
			os.Getenv("CAIDO_ACCESS_TOKEN"),
			os.Getenv("CAIDO_PAT"),
			true,
		)
		if err != nil {
			t.Fatalf("Login: %v", err)
		}
		if mode := path.Mode(); mode == "none" {
			t.Fatalf("Login mode = %q, want guest or pat", mode)
		}
	})

	t.Run("status", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), budget)
		defer cancel()
		st, err := c.Status(ctx, "", "")
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if st.Project.Name == "" {
			t.Fatal("Status returned an empty project name")
		}
	})

	t.Run("search", func(t *testing.T) {
		probeCtx, probeCancel := context.WithTimeout(context.Background(), budget)
		defer probeCancel()
		probe, err := c.Search(probeCtx, Query{Limit: 1})
		if err != nil {
			t.Fatalf("Search probe: %v", err)
		}
		if len(probe.Entries) == 0 || probe.Entries[0].Host == "" {
			t.Skip("no history to search — visit a page through the proxy first")
		}
		host := probe.Entries[0].Host

		ctx, cancel := context.WithTimeout(context.Background(), budget)
		defer cancel()
		res, err := c.Search(ctx, Query{Host: host, Limit: 1})
		if err != nil {
			t.Fatalf("Search host %q: %v", host, err)
		}
		if res.HTTPQL == "" {
			t.Fatal("Search echoed an empty httpql")
		}
	})
}

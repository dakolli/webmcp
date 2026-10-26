package browsers

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"

	"webmcp/pkg/config"
)

// parseProxy validates an upstream proxy address and returns the value for
// --proxy-server plus the credential key (lowercase host:port).
func parseProxy(raw string) (server, host string, err error) {
	server = strings.TrimSpace(raw)
	if server == "" {
		return "", "", fmt.Errorf(`browsers: proxy is empty; pass host:port (e.g. "127.0.0.1:8080") or scheme://host:port`)
	}

	addr := server
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}
	u, err := url.Parse(addr)
	if err != nil {
		return "", "", fmt.Errorf("browsers: invalid proxy %q: %w; pass host:port or scheme://host:port", server, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks4", "socks5":
	default:
		return "", "", fmt.Errorf(
			"browsers: invalid proxy %q: unsupported scheme %q; use http, https, socks4 or socks5",
			server,
			u.Scheme,
		)
	}
	if u.User != nil {
		return "", "", fmt.Errorf(
			"browsers: invalid proxy %q: credentials are not accepted here; put them in %s and chmod 600 it",
			server,
			proxyConfigFile(),
		)
	}
	if u.Hostname() == "" {
		return "", "", fmt.Errorf("browsers: invalid proxy %q: missing host; pass host:port", server)
	}
	port := u.Port()
	if port == "" {
		return "", "", fmt.Errorf("browsers: invalid proxy %q: missing port; pass host:port", server)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", "", fmt.Errorf(
			"browsers: invalid proxy %q: port %q must be a number between 1 and 65535",
			server,
			port,
		)
	}
	if u.Path != "" && u.Path != "/" {
		return "", "", fmt.Errorf("browsers: invalid proxy %q: unexpected path %q; pass host:port", server, u.Path)
	}
	host = strings.ToLower(net.JoinHostPort(u.Hostname(), port))
	return server, host, nil
}

// proxyConfigFile returns the proxy.json path for error messages, falling back
// to the documented location when the config dir cannot be resolved.
func proxyConfigFile() string {
	path, err := config.ProxyConfigPath()
	if err != nil {
		return "~/.config/browser-mcp/proxy.json"
	}
	return path
}

// isLoopbackHost reports whether a host:port names the local machine.
func isLoopbackHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// proxyAuth is the credential store for one browser's upstream proxy.
type proxyAuth struct {
	// host is the lowercase host:port credential key.
	host string
	// cred is the configured credential, valid when ok is true.
	cred config.ProxyCredential
	// ok reports whether proxy.json has an entry for host.
	ok bool
	// file is the proxy.json path named in actionable errors.
	file string
}

// enabled reports whether pages must arm the CDP auth handler: credentials are
// configured, or the proxy is remote and may demand them. A local intercept
// proxy without credentials therefore pays no interception cost.
func (a *proxyAuth) enabled() bool {
	return a.ok || !isLoopbackHost(a.host)
}

// missingError is the loud, actionable error for an auth challenge with no
// matching proxy.json entry.
func (a *proxyAuth) missingError() error {
	return fmt.Errorf(
		`browsers: proxy %s requires authentication but %s has no entry for %q; add `+
			`{"proxies":{"%s":{"username":"USER","password":"PASS"}}} to %s and run: chmod 600 %s`,
		a.host,
		a.file,
		a.host,
		a.host,
		a.file,
		a.file,
	)
}

// proxyAuthPage is one page's armed auth handler plus its last failure.
type proxyAuthPage struct {
	// cancel stops the page's auth handler goroutine.
	cancel context.CancelFunc
	// err is the last actionable proxy-auth failure, consumed by NewPage.
	err error
}

// startProxyAuth arms the CDP auth handler on a freshly created page, in the
// same pre-registration window as stealth and viewport. It is a no-op unless
// the browser has a proxy that needs auth handling.
func (b *Browser) startProxyAuth(p *rod.Page) {
	auth := b.proxyAuth
	if auth == nil || !auth.enabled() {
		return
	}

	authPage, cancel := p.WithCancel()
	b.mu.Lock()
	if b.authPages == nil {
		b.authPages = make(map[*rod.Page]*proxyAuthPage)
	}
	b.authPages[p] = &proxyAuthPage{cancel: cancel}
	b.mu.Unlock()

	answered := make(map[proto.FetchRequestID]bool)
	stop := authPage.EachEvent(
		func(e *proto.FetchRequestPaused) {
			_ = proto.FetchContinueRequest{RequestID: e.RequestID}.Call(authPage)
		},
		func(e *proto.FetchAuthRequired) {
			b.answerProxyAuth(authPage, p, auth, e, answered)
		},
	)
	go stop()
	_ = proto.FetchEnable{HandleAuthRequests: true}.Call(authPage)
}

// answerProxyAuth answers one proxy auth challenge from proxy.json. A missing
// entry cancels the challenge and records the loud, path-naming error; a
// rejected credential is cancelled after one retry instead of looping.
func (b *Browser) answerProxyAuth(
	client *rod.Page,
	page *rod.Page,
	auth *proxyAuth,
	e *proto.FetchAuthRequired,
	answered map[proto.FetchRequestID]bool,
) {
	resp := &proto.FetchAuthChallengeResponse{
		Response: proto.FetchAuthChallengeResponseResponseCancelAuth,
	}
	cred, ok := auth.cred, auth.ok
	switch {
	case !ok:
		b.recordProxyAuthError(page, auth.missingError())
	case answered[e.RequestID]:
		b.recordProxyAuthError(page, fmt.Errorf(
			"browsers: proxy %s rejected the credentials from %s",
			auth.host,
			auth.file,
		))
	default:
		answered[e.RequestID] = true
		resp = &proto.FetchAuthChallengeResponse{
			Response: proto.FetchAuthChallengeResponseResponseProvideCredentials,
			Username: cred.Username,
			Password: cred.Password,
		}
	}
	_ = proto.FetchContinueWithAuth{RequestID: e.RequestID, AuthChallengeResponse: resp}.Call(client)
}

// recordProxyAuthError stores the last failure for the page and logs the first
// one loudly, so a failing subresource flood does not spam the log.
func (b *Browser) recordProxyAuthError(p *rod.Page, err error) {
	b.mu.Lock()
	first := false
	if entry, ok := b.authPages[p]; ok {
		first = entry.err == nil
		entry.err = err
	}
	b.mu.Unlock()

	if first && b.logger != nil {
		b.logger.Error("browsers: proxy authentication failed", slog.String("error", err.Error()))
	}
}

// takeProxyAuthError returns and clears the page's last proxy-auth failure.
func (b *Browser) takeProxyAuthError(p *rod.Page) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry, ok := b.authPages[p]
	if !ok || entry.err == nil {
		return nil
	}
	err := entry.err
	entry.err = nil
	return err
}

// stopProxyAuth disarms and forgets the page's auth handler. It is idempotent.
func (b *Browser) stopProxyAuth(p *rod.Page) {
	b.mu.Lock()
	entry, ok := b.authPages[p]
	if ok {
		delete(b.authPages, p)
	}
	b.mu.Unlock()
	if ok {
		entry.cancel()
	}
}

package server

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dakolli/webmcp/pkg/caido"
	"github.com/dakolli/webmcp/pkg/config"
)

const (
	// caidoInstructionsLimit is the hard ceiling for the Caido block appended to
	// the MCP initialize instructions.
	caidoInstructionsLimit = 400
	// caidoInstructionTTL bounds reuse of one rendered block, so a reconnect
	// storm costs a single GraphQL read.
	caidoInstructionTTL = 5 * time.Second
	// caidoInstructionBudget bounds the status read made during initialize.
	caidoInstructionBudget = 3 * time.Second
	// caidoScopeHostLimit bounds the hosts rendered for one scope allowlist.
	caidoScopeHostLimit = 4
)

// caidoInstructionTail closes the block with the two rules an agent needs before
// its first caido_search call.
const caidoInstructionTail = "\n" +
	`caido_search defaults to the focus scope. Pass scope_id for another scope, or "*" for all.` +
	"\n" +
	"Never delete a scope. Create your own, or reuse one that matches your target host."

// baseInstructions is the browser-automation guidance every initialize carries.
const baseInstructions = "web-mcp provides stateful Chromium browser automation tools (fetch_page, page_fetch, page_act, session_close, browser_create, etc.) supporting element clicks, text input, key presses/combos (press_key), coordinate mouse movement/clicks (click_at, hover_at), drag-and-drop (drag_and_drop), and file uploads (upload_file). Predefined task templates are available via the prompts/list and prompts/get MCP protocol endpoints to guide common workflows such as page exploration, form filling, and content extraction."

// caidoInstructions renders the live Caido project and scope state and caches
// one render for a short TTL.
type caidoInstructions struct {
	client   *caido.Client
	settings config.CaidoFile

	mu      sync.Mutex
	text    string
	expires time.Time
}

// newCaidoInstructions constructs the renderer for the resolved Caido settings.
func newCaidoInstructions(cf config.CaidoFile) *caidoInstructions {
	return &caidoInstructions{
		client:   caido.NewClient(cf.URL),
		settings: cf,
	}
}

// initializeInstructions returns the server-wide guidance, appending the live
// Caido block when Caido is enabled.
func (s *Server) initializeInstructions(ctx context.Context) string {
	if s.caido == nil {
		return baseInstructions
	}
	return baseInstructions + "\n\n" + s.caido.instructions(ctx)
}

// instructions returns the cached Caido block, reading the instance again when
// the cache has expired. A failed read degrades to a short unavailable line and
// never blocks the initialize handshake.
func (c *caidoInstructions) instructions(ctx context.Context) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.text != "" && time.Now().Before(c.expires) {
		return c.text
	}
	c.text = c.read(ctx)
	c.expires = time.Now().Add(caidoInstructionTTL)
	return c.text
}

// read logs in and reads the project, scopes and focus selection in one status
// call, translating any failure into the unavailable line.
func (c *caidoInstructions) read(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, caidoInstructionBudget)
	defer cancel()
	if _, err := c.client.Login(
		ctx,
		c.settings.AccessToken,
		c.settings.PAT,
		c.settings.AllowGuest,
	); err != nil {
		return caidoUnavailable(err)
	}
	project, focus := caido.Selection()
	st, err := c.client.Status(ctx, project, focus)
	if err != nil {
		return caidoUnavailable(err)
	}
	return renderCaidoInstructions(st)
}

// caidoUnavailable renders the degraded block shown when Caido cannot be read.
func caidoUnavailable(err error) string {
	return fmt.Sprintf(
		"Caido history is unavailable: %s. Continue without caido_search.",
		strings.TrimSpace(err.Error()),
	)
}

// renderCaidoInstructions renders the project, every scope, the focus scope and
// the usage rules. The result is always under caidoInstructionsLimit.
func renderCaidoInstructions(st caido.Status) string {
	head := caidoProjectLine(st.Project)
	focus := caidoFocusLine(st.Scopes)
	lines := caidoScopeLines(st.Scopes)

	var b strings.Builder
	b.WriteString(head)
	shown := 0
	for _, line := range lines {
		marker := 0
		if shown+1 < len(lines) {
			marker = len(fmt.Sprintf("\n+%d more scopes", len(lines)-shown-1))
		}
		if shown > 0 && b.Len()+1+len(line)+marker+len(focus)+len(caidoInstructionTail) > caidoInstructionsLimit {
			break
		}
		b.WriteByte('\n')
		b.WriteString(line)
		shown++
	}
	if shown < len(lines) {
		fmt.Fprintf(&b, "\n+%d more scopes", len(lines)-shown)
	}
	if focus != "" {
		b.WriteByte('\n')
		b.WriteString(focus)
	}
	b.WriteString(caidoInstructionTail)

	if b.Len() > caidoInstructionsLimit {
		return caidoCompactInstructions(st)
	}
	return b.String()
}

// caidoCompactInstructions renders the bounded fallback used when a scope list
// is too wide for the full block.
func caidoCompactInstructions(st caido.Status) string {
	var b strings.Builder
	b.WriteString(caidoProjectLine(st.Project))
	fmt.Fprintf(&b, "\n%d scopes configured.", len(st.Scopes))
	if focus := caidoFocusLine(st.Scopes); focus != "" {
		b.WriteByte('\n')
		b.WriteString(focus)
	}
	b.WriteString(caidoInstructionTail)
	return b.String()
}

// caidoProjectLine renders the selected project and whether it is temporary.
func caidoProjectLine(p caido.ProjectStatus) string {
	name := p.Name
	if name == "" {
		name = "unknown"
	}
	mode := "persistent"
	if p.Temporary {
		mode = "temporary"
	}
	return fmt.Sprintf("Caido project: %s (%s).", name, mode)
}

// caidoScopeLines renders one line per scope, naming an empty allowlist as all
// traffic.
func caidoScopeLines(scopes []caido.ScopeStatus) []string {
	lines := make([]string, 0, len(scopes))
	for _, sc := range scopes {
		lines = append(lines, caidoScopeLine(sc))
	}
	return lines
}

// caidoScopeLine renders a scope with a bounded allowlist.
func caidoScopeLine(sc caido.ScopeStatus) string {
	prefix := fmt.Sprintf("scope %s (id %s): ", sc.Name, sc.ID)
	if len(sc.Allowlist) == 0 {
		return prefix + "all traffic"
	}
	if len(sc.Allowlist) > caidoScopeHostLimit {
		return prefix +
			strings.Join(sc.Allowlist[:caidoScopeHostLimit], ", ") +
			fmt.Sprintf(", +%d more", len(sc.Allowlist)-caidoScopeHostLimit)
	}
	return prefix + strings.Join(sc.Allowlist, ", ")
}

// caidoFocusLine renders the configured focus scope, or "" when none is set.
func caidoFocusLine(scopes []caido.ScopeStatus) string {
	for _, sc := range scopes {
		if sc.Focus {
			return fmt.Sprintf("Focus scope: %s (%s).", sc.Name, sc.ID)
		}
	}
	return ""
}

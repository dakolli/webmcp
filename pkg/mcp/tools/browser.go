package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/dakolli/webmcp/pkg/browsers"
	"github.com/dakolli/webmcp/pkg/config"
	"github.com/dakolli/webmcp/pkg/mcp/models"
)

// ownerPattern is the accepted shape of a caller-supplied owner label.
var ownerPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// normalizeOwner trims and validates a caller-supplied owner label, returning
// "" when none was supplied.
func normalizeOwner(owner string) (string, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return "", nil
	}
	if !ownerPattern.MatchString(owner) {
		return "", fmt.Errorf(
			"owner %q is invalid — use 1 to 64 characters from A-Z, a-z, 0-9, '.', '_' and '-' only",
			owner,
		)
	}
	return owner, nil
}

// BrowserCreate implements the "browser_create" tool: it creates or reuses a
// live Chromium browser owned by the calling client and returns its browser_id
// so the agent can open pages inside it via fetch_page / page_list.
type BrowserCreate struct {
	// mgr is the shared browsers.Manager owning the single Browser.
	mgr *browsers.Manager
}

// NewBrowserCreate constructs the browser_create tool wired to the given
// browsers.Manager.
func NewBrowserCreate(mgr *browsers.Manager) *BrowserCreate {
	return &BrowserCreate{mgr: mgr}
}

// Name returns the MCP tool name "browser_create".
func (t *BrowserCreate) Name() string {
	return "browser_create"
}

// Description returns a customer/LLM-facing description of browser_create.
func (t *BrowserCreate) Description() string {
	return "Creates or reuses a live Chromium browser and returns its browser_id. " +
		"Idempotent: calling again in the same session with identical options gets the same " +
		"browser_id back, without launching a second process. Set owner to claim your own " +
		"browser when several agents share one session: it joins the create key, so the same " +
		"owner with the same options reuses one browser and a different owner gets a separate " +
		"one. Browsers are EPHEMERAL by default: the profile lives under " +
		"~/.config/browser-mcp/profiles/.ephemeral and is deleted when the browser closes, so " +
		"close browsers you are done with (browser_close) and idle ones expire automatically. " +
		"Pass persist=true (with an optional profile name) to keep a persistent profile and its " +
		"logins across restarts. A bare profile name resolves under " +
		"~/.config/browser-mcp/profiles; relative paths are rejected. Proxy: pass a local proxy " +
		"such as 127.0.0.1:8080 for intercept tools; credentials never go in an argument — put " +
		"them in ~/.config/browser-mcp/proxy.json (mode 0600). " +
		"Headers: pass headers to send an identity header on EVERY request from every page of " +
		"this browser, e.g. {\"X-Bug-Bounty\":\"your-handle\"}. Scope: pass scope as the request " +
		"allowlist (bare hosts also cover their subdomains, '*.example.com' works, 'host:port' is " +
		"accepted). No scope means no limit; scope_strict=true with an empty scope denies every " +
		"request. Scope now governs navigation, replay, redirect hops and intercepted subresources: " +
		"an out-of-scope request is refused before it is sent. Rate limit: pass rps and burst to cap " +
		"requests per host (default 5/s with a burst of 10); a throttled request waits, and one whose " +
		"deadline cannot fit the wait fails as timeout. The effective policy is reported as limits. " +
		"Example: { \"owner\": \"scraper-a\" } and { \"owner\": \"scraper-b\" } get two browsers; " +
		"{ \"persist\": true, \"profile\": \"audit\" } keeps a profile; " +
		"{ \"headers\": {\"X-Bug-Bounty\":\"me\"}, \"scope\": [\"example.com\", \"*.iana.org\"], " +
		"\"rps\": 2, \"burst\": 4 } confines navigation and spaces requests. " +
		"Chromium switches: pass chromium_args to append switches for this browser only, e.g. " +
		"[\"--disable-gpu\"], on top of the built-in quiet set and [browser] chromium_flags." +
		resultEnvelopeDoc
}

// InputSchema returns the JSON Schema for browser_create's arguments: optional
// bin, profile, persist, headless, stealth, and position parameters.
func (t *BrowserCreate) InputSchema() models.InputSchema {
	return models.InputSchema{
		Type: "object",
		Properties: map[string]any{
			"bin": map[string]any{
				"type":        "string",
				"description": "Optional custom browser binary path or command (e.g. 'brave-browser', 'google-chrome').",
			},
			"profile": map[string]any{
				"type":        "string",
				"description": "Optional profile name ('brave', 'chrome', 'firefox', 'default' or any label) resolved under ~/.config/browser-mcp/profiles, or an absolute directory path. Relative paths are rejected.",
			},
			"persist": map[string]any{
				"type":        "boolean",
				"description": "Keep the profile (and its logins) after the browser closes. Default false: the browser gets an ephemeral profile under profiles/.ephemeral that is deleted on close. Set true with a profile name to keep a persistent profile.",
			},
			"headless": map[string]any{
				"type":        "boolean",
				"description": "Run the browser headless instead of headful (default follows manager configuration).",
			},
			"stealth": map[string]any{
				"type":        "boolean",
				"description": "Apply go-rod/stealth to every page (default follows manager configuration).",
			},
			"position": map[string]any{
				"type":        "string",
				"enum":        []string{"left", "right", "max", "normal"},
				"description": "Optional window position split screen preset: 'left' (left screen half), 'right' (right screen half), 'max', 'normal'.",
			},
			"proxy": map[string]any{
				"type": "string",
				"description": "Local proxy for intercept tools, for example 127.0.0.1:8080. " +
					"Accepts host:port or scheme://host:port. Credentials are never accepted here; " +
					"put them in ~/.config/browser-mcp/proxy.json (mode 0600), keyed by proxy host:port.",
			},
			"proxy_bypass": map[string]any{
				"type":        "string",
				"description": "Comma-separated hosts that skip the proxy, for example 'localhost,*.internal'.",
			},
			"ignore_certs": map[string]any{
				"type": "boolean",
				"description": "Accept invalid TLS certificates, for example an intercept proxy's MITM CA. " +
					"Independent of proxy: set it explicitly.",
			},
			"headers": map[string]any{
				"type":                 "object",
				"additionalProperties": map[string]any{"type": "string"},
				"description": "Request headers sent with every request from every page of this browser, the first navigation included. " +
					"Use it for program identity headers, e.g. {\"X-Bug-Bounty\": \"your-handle\"}. " +
					"Headers are applied at page creation and persist for the life of each page.",
			},
			"scope": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Request allowlist. A bare host (example.com) also covers its subdomains (www.example.com, api.example.com); a leading '*.example.com' glob is accepted, as is 'host:port' and '*' for all. Omit for no limit. Applies to navigation, replay, every redirect hop and intercepted subresources: an out-of-scope request is refused before it is sent.",
			},
			"scope_strict": map[string]any{
				"type":        "boolean",
				"description": "Opt-in strict mode: when true and scope is empty, every request is refused. Default false: an empty allowlist means no limit. The live state is shown by browser_list as scope and scope_strict.",
			},
			"rps": map[string]any{
				"type":        "number",
				"description": "Per-host sustained request rate in requests per second. Default 5. Every engine-issued request waits for a token; a request whose deadline cannot fit the wait fails as timeout naming the host and the remaining wait.",
			},
			"burst": map[string]any{
				"type":        "integer",
				"description": "Per-host token bucket depth, i.e. how many requests may go out at once before the rps pace applies. Default 10.",
			},
			"owner": map[string]any{
				"type": "string",
				"description": "Optional owner label for your agent (1 to 64 characters from A-Z, a-z, 0-9, '.', '_', '-'). " +
					"Different owners with identical options get separate browsers; the same owner with the same options reuses one. " +
					"Defaults to the session identity, else 'unowned'.",
			},
			"chromium_args": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Extra Chromium command-line switches for this browser only, for example [\"--disable-gpu\"]. Each must start with '--'. They are appended to the built-in quiet set and [browser] chromium_flags, deduplicated first-wins, and join the create key.",
			},
		},
	}
}

// browserCreateArgs is the strict decoding target for browser_create arguments.
type browserCreateArgs struct {
	Bin          string            `json:"bin,omitempty"`
	Profile      string            `json:"profile,omitempty"`
	Position     string            `json:"position,omitempty"`
	Proxy        string            `json:"proxy,omitempty"`
	ProxyBypass  string            `json:"proxy_bypass,omitempty"`
	Owner        string            `json:"owner,omitempty"`
	Headless     *bool             `json:"headless,omitempty"`
	Stealth      *bool             `json:"stealth,omitempty"`
	Persist      *bool             `json:"persist,omitempty"`
	IgnoreCerts  *bool             `json:"ignore_certs,omitempty"`
	ScopeStrict  *bool             `json:"scope_strict,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
	Scope        []string          `json:"scope,omitempty"`
	ChromiumArgs []string          `json:"chromium_args,omitempty"`
	RPS          *float64          `json:"rps,omitempty"`
	Burst        *int              `json:"burst,omitempty"`
}

// Execute parses options and creates a new distinct Browser via Manager.CreateBrowser.
func (t *BrowserCreate) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a browserCreateArgs
	if err := decodeArgs("browser_create", args, &a); err != nil {
		return failResult(err)
	}

	owner, err := normalizeOwner(a.Owner)
	if err != nil {
		return failResult(fmt.Errorf("browser_create: %w", err))
	}
	if owner != "" {
		ctx = browsers.WithExplicitOwner(ctx, owner)
	}

	var opts []browsers.Option
	if a.Bin != "" {
		opts = append(opts, browsers.WithBin(a.Bin))
	}
	if a.Profile != "" {
		opts = append(opts, browsers.WithProfileName(a.Profile))
	}
	if a.Persist != nil {
		opts = append(opts, browsers.WithPersist(*a.Persist))
	}
	if a.Position != "" {
		opts = append(opts, browsers.WithPosition(a.Position))
	}
	if a.Proxy != "" {
		opts = append(opts, browsers.WithProxy(a.Proxy))
	}
	if a.ProxyBypass != "" {
		opts = append(opts, browsers.WithProxyBypass(a.ProxyBypass))
	}
	if a.IgnoreCerts != nil {
		opts = append(opts, browsers.WithIgnoreCerts(*a.IgnoreCerts))
	}
	if len(a.Headers) > 0 {
		opts = append(opts, browsers.WithExtraHeaders(a.Headers))
	}
	if len(a.Scope) > 0 {
		opts = append(opts, browsers.WithScope(a.Scope))
	}
	if len(a.ChromiumArgs) > 0 {
		browser, err := config.ResolveBrowser(a.ChromiumArgs)
		if err != nil {
			return failResult(fmt.Errorf("browser_create: %w", err))
		}
		opts = append(opts, browsers.WithChromiumFlags(browser.ChromiumFlags))
	}
	if a.ScopeStrict != nil {
		opts = append(opts, browsers.WithScopeStrict(*a.ScopeStrict))
	}
	if a.RPS != nil || a.Burst != nil {
		limit := browsers.RateLimit{
			RPS:   browsers.DefaultRateRPS,
			Burst: browsers.DefaultRateBurst,
		}
		if a.RPS != nil {
			limit.RPS = *a.RPS
		}
		if a.Burst != nil {
			limit.Burst = *a.Burst
		}
		if err := limit.Validate(); err != nil {
			return failResult(fmt.Errorf("browser_create: %w", err))
		}
		opts = append(opts, browsers.WithRateLimit(limit))
	}
	if a.Headless != nil {
		opts = append(opts, browsers.WithHeadless(*a.Headless))
	}
	if a.Stealth != nil {
		opts = append(opts, browsers.WithStealth(*a.Stealth))
	}

	created, err := t.mgr.Create(ctx, opts...)
	if err != nil {
		return failResult(fmt.Errorf("browser_create: %w", err))
	}

	data, err := json.Marshal(newBrowserCreateResult(created))
	if err != nil {
		return failResult(fmt.Errorf("browser_create: %w", err))
	}
	return okResult(string(data))
}

// browserCreateResult is the compact create outcome: the browser_id plus the
// lifecycle facts an agent needs to decide when to close it.
type browserCreateResult struct {
	BrowserID   string               `json:"browser_id"`
	Owner       string               `json:"owner"`
	Persist     bool                 `json:"persist"`
	TTL         string               `json:"ttl"`
	Scope       string               `json:"scope"`
	ScopeStrict bool                 `json:"scope_strict"`
	Limits      browsers.LimitReport `json:"limits"`
	Reused      bool                 `json:"reused"`
}

// newBrowserCreateResult projects a create outcome into the bounded one-line
// reply shape.
func newBrowserCreateResult(created browsers.Created) browserCreateResult {
	info := created.Browser.Info()
	return browserCreateResult{
		BrowserID:   string(created.ID),
		Owner:       info.Owner,
		Persist:     info.Persist,
		TTL:         info.TTL,
		Scope:       info.Scope,
		ScopeStrict: info.ScopeStrict,
		Limits:      info.Limits,
		Reused:      created.Reused,
	}
}

// Ensure BrowserCreate satisfies the Tool interface at compile time.
var _ Tool = (*BrowserCreate)(nil)

// BrowserList implements the "browser_list" tool: it reports the manager's
// single Browser (browser_id) and its open pages, or an empty JSON array when
// no browser has been launched yet.
type BrowserList struct {
	// mgr is the shared browsers.Manager owning the single Browser.
	mgr *browsers.Manager
}

// NewBrowserList constructs the browser_list tool wired to the given
// browsers.Manager.
func NewBrowserList(mgr *browsers.Manager) *BrowserList {
	return &BrowserList{mgr: mgr}
}

// Name returns the MCP tool name "browser_list".
func (t *BrowserList) Name() string {
	return "browser_list"
}

// Description returns a customer/LLM-facing description of browser_list.
func (t *BrowserList) Description() string {
	return "Lists live browsers with their lifecycle state as compact JSON: browser_id, " +
		"owner, bin, profile_dir, persist, ephemeral, created_at, idle_for (how long the " +
		"browser has been unused), ttl (when idle cleanup closes it), scope (the request " +
		"allowlist: \"open\" when unrestricted), scope_strict (whether an empty allowlist " +
		"denies all requests), limits (the effective per-host budget: {rps, burst, hosts:[{host, " +
		"throttled}]}) and pages, each {id, url, title?, context?, stale?}. " +
		"Returns [] when none are running. Use it to see what you own and what to close; " +
		"idle browsers expire automatically. Example: {}." + resultEnvelopeDoc
}

// InputSchema returns the JSON Schema for browser_list's arguments: none.
func (t *BrowserList) InputSchema() models.InputSchema {
	return models.InputSchema{
		Type:       "object",
		Properties: map[string]any{},
	}
}

// browserEntry is the compact lifecycle read-out for one live browser.
type browserEntry struct {
	BrowserID   string               `json:"browser_id"`
	Owner       string               `json:"owner"`
	Bin         string               `json:"bin,omitempty"`
	ProfileDir  string               `json:"profile_dir,omitempty"`
	Persist     bool                 `json:"persist"`
	Ephemeral   bool                 `json:"ephemeral"`
	CreatedAt   time.Time            `json:"created_at"`
	IdleFor     string               `json:"idle_for"`
	TTL         string               `json:"ttl"`
	Scope       string               `json:"scope"`
	ScopeStrict bool                 `json:"scope_strict"`
	Limits      browsers.LimitReport `json:"limits"`
	Pages       []browsers.PageInfo  `json:"pages"`
}

// browserListEntry projects a BrowserInfo snapshot and its pages into the
// bounded listing shape.
func browserListEntry(info browsers.BrowserInfo, pages []browsers.PageInfo) browserEntry {
	if pages == nil {
		pages = []browsers.PageInfo{}
	}
	return browserEntry{
		BrowserID:   string(info.BrowserID),
		Owner:       info.Owner,
		Bin:         info.Bin,
		ProfileDir:  info.ProfileDir,
		Persist:     info.Persist,
		Ephemeral:   info.Ephemeral,
		CreatedAt:   info.CreatedAt,
		IdleFor:     info.IdleFor,
		TTL:         info.TTL,
		Scope:       info.Scope,
		ScopeStrict: info.ScopeStrict,
		Limits:      info.Limits,
		Pages:       pages,
	}
}

// Execute returns the snapshot of all live browsers and their open pages.
func (t *BrowserList) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	active := t.mgr.Browsers()
	if len(active) == 0 {
		return okResult("[]")
	}

	ids := make([]string, 0, len(active))
	for id := range active {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)

	result := make([]browserEntry, 0, len(ids))
	for _, id := range ids {
		b := active[browsers.BrowserID(id)]
		result = append(result, browserListEntry(b.Info(), b.Pages()))
	}

	data, err := json.Marshal(result)
	if err != nil {
		return failResult(fmt.Errorf("browser_list: %w", err))
	}
	return okResult(string(data))
}

// Ensure BrowserList satisfies the Tool interface at compile time.
var _ Tool = (*BrowserList)(nil)

// BrowserClose implements the "browser_close" tool: it tears down the manager's
// specified Browser and all of its open pages.
type BrowserClose struct {
	// mgr is the shared browsers.Manager owning the browsers.
	mgr *browsers.Manager
}

// NewBrowserClose constructs the browser_close tool wired to the given
// browsers.Manager.
func NewBrowserClose(mgr *browsers.Manager) *BrowserClose {
	return &BrowserClose{mgr: mgr}
}

// Name returns the MCP tool name "browser_close".
func (t *BrowserClose) Name() string {
	return "browser_close"
}

// Description returns a customer/LLM-facing description of browser_close.
func (t *BrowserClose) Description() string {
	return "Closes a live browser and all of its pages, releasing the Chromium process. " +
		"Pass browser_id to close one browser, or omit to close all. purge=true also " +
		"deletes that browser's profile directory (guarded: only paths under " +
		"~/.config/browser-mcp/profiles; this is the only way to delete a named persistent " +
		"profile). prune=true (with no browser_id) closes all browsers and deletes leftover " +
		"profiles under profiles/.ephemeral that no live browser owns — named persistent " +
		"profiles are never deleted by prune. Idle browsers expire on their own, but close " +
		"explicitly when done; an unknown browser_id returns a not-found error listing live " +
		"ids and owners. Example: { \"browser_id\": \"browser-2\", \"purge\": true }." +
		resultEnvelopeDoc
}

// InputSchema returns the JSON Schema for browser_close's arguments: an optional
// browser_id plus purge/prune cleanup flags.
func (t *BrowserClose) InputSchema() models.InputSchema {
	return models.InputSchema{
		Type: "object",
		Properties: map[string]any{
			"browser_id": map[string]any{
				"type":        "string",
				"description": "Optional browser_id returned by browser_create to close. Omit to close all browsers.",
			},
			"purge": map[string]any{
				"type":        "boolean",
				"description": "After closing browser_id, delete its profile directory (must resolve under ~/.config/browser-mcp/profiles). Refused for paths outside it. Requires browser_id.",
			},
			"prune": map[string]any{
				"type":        "boolean",
				"description": "Close all browsers, then delete leftover directories under profiles/.ephemeral that no live browser owns. Named persistent profiles are never deleted. Pass without browser_id.",
			},
		},
	}
}

// browserCloseArgs is the strict decoding target for browser_close arguments.
type browserCloseArgs struct {
	BrowserID string `json:"browser_id"`
	Purge     bool   `json:"purge"`
	Prune     bool   `json:"prune"`
}

// Execute closes the target browser or all browsers, optionally purging its
// profile or pruning orphan ephemeral profiles.
func (t *BrowserClose) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a browserCloseArgs
	if err := decodeArgs("browser_close", args, &a); err != nil {
		return failResult(err)
	}
	if a.Purge && a.Prune {
		return failResult(fmt.Errorf("browser_close: purge and prune are mutually exclusive — pass purge with a browser_id, or prune alone"))
	}
	if a.Prune {
		if a.BrowserID != "" {
			return failResult(fmt.Errorf("browser_close: prune takes no browser_id — it closes all browsers and cleans orphan ephemeral profiles; use purge with browser_id to delete one profile"))
		}
		removed, err := t.mgr.PruneProfiles(ctx)
		if err != nil {
			return failResult(fmt.Errorf("browser_close: %w", err))
		}
		return okResult(fmt.Sprintf("all browsers closed; pruned %d orphan ephemeral profile(s)", len(removed)))
	}
	if a.Purge {
		if a.BrowserID == "" {
			return failResult(fmt.Errorf("browser_close: purge requires a browser_id — pass the browser whose profile to delete, or use prune to clean orphan ephemeral profiles"))
		}
		if err := t.mgr.PurgeProfile(ctx, browsers.BrowserID(a.BrowserID)); err != nil {
			return failResult(fmt.Errorf("browser_close: %w", err))
		}
		return okResult(fmt.Sprintf("browser %s closed and its profile deleted", a.BrowserID))
	}
	if err := t.mgr.CloseBrowser(ctx, browsers.BrowserID(a.BrowserID)); err != nil {
		return failResult(fmt.Errorf("browser_close: %w", err))
	}
	if a.BrowserID == "" {
		return okResult("all browsers closed")
	}
	return okResult(fmt.Sprintf("browser %s closed", a.BrowserID))
}

// Ensure BrowserClose satisfies the Tool interface at compile time.
var _ Tool = (*BrowserClose)(nil)

// PageList implements the "page_list" tool: it lists every open page of the
// manager's single Browser as a JSON array, mirroring session_list but scoped
// to a browser_id.
type PageList struct {
	// mgr is the shared browsers.Manager owning the single Browser.
	mgr *browsers.Manager
}

// NewPageList constructs the page_list tool wired to the given
// browsers.Manager.
func NewPageList(mgr *browsers.Manager) *PageList {
	return &PageList{mgr: mgr}
}

// Name returns the MCP tool name "page_list".
func (t *PageList) Name() string {
	return "page_list"
}

// Description returns a customer/LLM-facing description of page_list.
func (t *PageList) Description() string {
	return "Lists every live page of the given browser as a JSON array. Each " +
		"entry is {id, url, context, title?, stale?}: url and title are read " +
		"from the live tab, so they follow navigations and redirects; context " +
		"is the incognito identity ('default' for the browser's own cookie " +
		"jar); stale marks an entry whose tab was busy or unreadable, in which " +
		"case url is the last known value. Use it to see which sessions/tabs " +
		"are still open inside the browser before closing them with " +
		"session_close. An unknown browser_id returns a not-found error listing " +
		"the running ids. " +
		"Example: { \"browser_id\": \"browser-1\" }." + resultEnvelopeDoc
}

// InputSchema returns the JSON Schema for page_list's arguments: a required
// browser_id.
func (t *PageList) InputSchema() models.InputSchema {
	return models.InputSchema{
		Type: "object",
		Properties: map[string]any{
			"browser_id": map[string]any{
				"type":        "string",
				"description": "The browser_id returned by browser_create whose open pages to list.",
			},
		},
		Required: []string{"browser_id"},
	}
}

// pageListArgs is the strict decoding target for page_list arguments.
type pageListArgs struct {
	// BrowserID identifies the browser whose pages to list (required).
	BrowserID string `json:"browser_id"`
}

// Execute validates the browser_id, resolves it against the manager's live
// browsers (without launching one), and returns its pages as an indented JSON
// array via Browser.Pages.
func (t *PageList) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a pageListArgs
	if err := decodeArgs("page_list", args, &a); err != nil {
		return failResult(err)
	}
	if a.BrowserID == "" {
		return failResult(fmt.Errorf("page_list: missing required argument %q", "browser_id"))
	}
	b, err := t.mgr.Browser(browsers.BrowserID(a.BrowserID))
	if err != nil {
		return failResult(fmt.Errorf("page_list: %w", err))
	}
	data, err := json.MarshalIndent(b.Pages(), "", "  ")
	if err != nil {
		return failResult(fmt.Errorf("page_list: %w", err))
	}
	return okResult(string(data))
}

// Ensure PageList satisfies the Tool interface at compile time.
var _ Tool = (*PageList)(nil)

// BrowserSetupAndEmulatePrompt is a co-located prompt definition for browser_create.
type BrowserSetupAndEmulatePrompt struct{}

// NewBrowserSetupAndEmulatePrompt constructs the browser_setup_and_emulate prompt template.
func NewBrowserSetupAndEmulatePrompt() *BrowserSetupAndEmulatePrompt {
	return &BrowserSetupAndEmulatePrompt{}
}

// Name returns the prompt template name "browser_setup_and_emulate".
func (p *BrowserSetupAndEmulatePrompt) Name() string { return "browser_setup_and_emulate" }

// Description returns a description of the prompt template.
func (p *BrowserSetupAndEmulatePrompt) Description() string {
	return "Provides guidance for initializing a browser session and setting up optional device emulation."
}

// Arguments returns the parameter list accepted by the prompt template.
func (p *BrowserSetupAndEmulatePrompt) Arguments() []models.PromptArgument {
	return []models.PromptArgument{
		{Name: "device", Description: "Optional device preset to emulate (e.g. IPhoneX, IPadPro)", Required: false},
	}
}

// GetMessages builds the prompt messages payload for the given argument map.
func (p *BrowserSetupAndEmulatePrompt) GetMessages(args map[string]string) ([]models.PromptMessage, error) {
	device := args["device"]
	text := "Please set up a live browser session:\n" +
		"1. Call browser_create to initialize the Chromium instance.\n" +
		"2. Call fetch_page with target URL to open an initial tab."
	if device != "" {
		text += fmt.Sprintf("\n3. Call page_act with action: \"emulate\" and device: %q to apply device settings.", device)
	}

	return []models.PromptMessage{
		{
			Role:    "user",
			Content: models.TextContent{Type: "text", Text: text},
		},
	}, nil
}

// Ensure BrowserSetupAndEmulatePrompt satisfies the Prompt interface at compile time.
var _ Prompt = (*BrowserSetupAndEmulatePrompt)(nil)

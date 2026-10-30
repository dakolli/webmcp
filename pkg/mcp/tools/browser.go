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
	return models.BrowserCreateDescription
}

// InputSchema returns the JSON Schema for browser_create's arguments: optional
// bin, profile, persist, headless, stealth, and position parameters.
func (t *BrowserCreate) InputSchema() models.InputSchema {
	return newBrowserCreateInputSchema()
}

// browserCreateArgs is the strict decoding target for browser_create arguments.
type browserCreateArgs struct {
	Bin          string            `json:"bin,omitempty"`
	Profile      string            `json:"profile,omitempty"`
	Position     string            `json:"position,omitempty"`
	Proxy        string            `json:"proxy,omitempty"`
	ProxyBypass  string            `json:"proxy_bypass,omitempty"`
	DenyHosts    []string          `json:"deny_hosts,omitempty"`
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
	if len(a.DenyHosts) > 0 {
		opts = append(opts, browsers.WithDenyHosts(a.DenyHosts))
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
	return models.BrowserListDescription
}

// InputSchema returns the JSON Schema for browser_list's arguments: none.
func (t *BrowserList) InputSchema() models.InputSchema {
	return newBrowserListInputSchema()
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
	return models.BrowserCloseDescription
}

// InputSchema returns the JSON Schema for browser_close's arguments: an optional
// browser_id plus purge/prune cleanup flags.
func (t *BrowserClose) InputSchema() models.InputSchema {
	return newBrowserCloseInputSchema()
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
	return models.PageListDescription
}

// InputSchema returns the JSON Schema for page_list's arguments: a required
// browser_id.
func (t *PageList) InputSchema() models.InputSchema {
	return newPageListInputSchema()
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
	return models.BrowserSetupAndEmulatePromptDescription
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

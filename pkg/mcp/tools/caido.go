package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dakolli/webmcp/pkg/caido"
	"github.com/dakolli/webmcp/pkg/config"
	"github.com/dakolli/webmcp/pkg/mcp/models"
)

// CaidoSearch implements the "caido_search" tool: it reads recorded HTTP
// history from a Caido instance and returns compact transactions.
type CaidoSearch struct {
	client *caido.Client
}

// NewCaidoSearch constructs the caido_search tool for the configured endpoint.
func NewCaidoSearch() *CaidoSearch {
	return &CaidoSearch{client: caido.NewClient(config.ActiveCaido().URL)}
}

// Name returns the MCP tool name "caido_search".
func (t *CaidoSearch) Name() string {
	return "caido_search"
}

// Description returns the LLM-facing description of caido_search.
func (t *CaidoSearch) Description() string {
	return caidoSearchDescription
}

// caidoSearchDescription is the LLM-facing description of caido_search.
const caidoSearchDescription = "Reads the Caido proxy history over GraphQL and returns HTTP transactions " +
	"as JSON. Caido must run on the same host. The tool reads recorded history " +
	"and never changes, replays, or blocks traffic. " +
	"Server-side filtering: pass httpql with a raw HTTPQL expression, or use " +
	"the typed keys host, method, status, status_gte and path_contains, which " +
	"compile to one expression joined with AND. A non-empty httpql wins and the " +
	"typed keys are ignored; the envelope reports httpql and httpql_source " +
	"(\"httpql\" or \"typed\"). scope_id selects the scope: \"\" uses the " +
	"configured focus scope, \"*\" searches every scope, and any other value is " +
	"a scope id from caido_status. The envelope reports scope_id, scope_name, " +
	"and all_scopes when every scope was searched. A scope the instance does " +
	"not hold returns invalid_args. " +
	"Only a search with no expression and no scope scans the newest 500 rows " +
	"(5 pages of 100); a filtered search pages until limit is met or the " +
	"instance stops returning rows, so a narrow expression beats raising limit. " +
	"url and since still filter the returned rows here. since defaults to 900 " +
	"seconds and applies only to the rows returned; total is the server count " +
	"for the expression and scope and ignores since, so count 0 beside a non-zero " +
	"total means the window hid the rows, not that none exist. The envelope " +
	"reports since_seconds and dropped_since, and when the window alone emptied " +
	"the result, note names the fix (pass since:0). count is the rows returned, " +
	"scanned is the rows examined, dropped is the rows the url or since filter " +
	"rejected. " +
	"limit clamps at 100 and body_bytes clamps at 65536, reported as " +
	"limit_applied and body_bytes_applied only when clamped. body_bytes 0 " +
	"returns metadata and headers with empty bodies and no request_cut or " +
	"response_cut. request_cut and response_cut appear only when a returned body " +
	"was shortened, or dropped with its headers because the captured message " +
	"exceeds 8 MiB. Redaction is on unless raw is true. bytes is the request " +
	"size and response_bytes is the raw captured response size. part selects " +
	"which side carries a body: request, response, or both. Results are newest " +
	"first. Empty history returns entries:[] and is not an error; call " +
	"caido_status first when a search returns nothing, because the wrong project " +
	"or scope is the usual cause. " +
	"Example: {\"httpql\":\"req.host.eq:\\\"www.gnu.org\\\"\"} returns only that " +
	"host. Example: {\"host\":\"www.gnu.org\",\"limit\":50} produces the same " +
	"rows. Example: {\"scope_id\":\"*\",\"method\":\"POST\",\"status_gte\":400} " +
	"searches every scope for POST errors." + models.ResultEnvelopeDoc

// InputSchema returns the JSON Schema for caido_search's arguments.
func (t *CaidoSearch) InputSchema() models.InputSchema {
	return newCaidoSearchInputSchema()
}

// caidoSearchArgs is the strict decoding target for caido_search arguments.
type caidoSearchArgs struct {
	URL          string `json:"url"`
	Method       string `json:"method"`
	Status       *int   `json:"status"`
	StatusGTE    *int   `json:"status_gte"`
	Host         string `json:"host"`
	PathContains string `json:"path_contains"`
	HTTPQL       string `json:"httpql"`
	ScopeID      string `json:"scope_id"`
	Since        *int   `json:"since"`
	Limit        *int   `json:"limit"`
	BodyBytes    *int   `json:"body_bytes"`
	Part         string `json:"part"`
	Raw          bool   `json:"raw"`
}

// Execute runs one history search against the configured Caido instance.
func (t *CaidoSearch) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a caidoSearchArgs
	if err := decodeArgs("caido_search", args, &a); err != nil {
		return failResult(err)
	}

	method := strings.ToUpper(strings.TrimSpace(a.Method))
	if method != "" && method != "GET" && method != "POST" {
		return failResult(fmt.Errorf("caido_search: invalid arguments: method must be GET, POST, or empty"))
	}

	status := 0
	if a.Status != nil {
		status = *a.Status
		if status < 0 {
			return failResult(fmt.Errorf("caido_search: invalid arguments: status must be >= 0"))
		}
	}

	statusGTE := 0
	if a.StatusGTE != nil {
		statusGTE = *a.StatusGTE
		if statusGTE < 0 {
			return failResult(fmt.Errorf("caido_search: invalid arguments: status_gte must be >= 0"))
		}
	}

	since := 900
	if a.Since != nil {
		since = *a.Since
		if since < 0 {
			return failResult(fmt.Errorf("caido_search: invalid arguments: since must be >= 0"))
		}
	}

	limit := 20
	if a.Limit != nil {
		limit = *a.Limit
		if limit < 0 {
			return failResult(fmt.Errorf("caido_search: invalid arguments: limit must be >= 0"))
		}
	}
	if limit == 0 {
		limit = 20
	}

	bodyBytes := 2000
	if a.BodyBytes != nil {
		bodyBytes = *a.BodyBytes
		if bodyBytes < 0 {
			return failResult(fmt.Errorf("caido_search: invalid arguments: body_bytes must be >= 0"))
		}
	}

	part := strings.ToLower(strings.TrimSpace(a.Part))
	switch part {
	case "", "both":
		part = "both"
	case "request", "response":
	default:
		return failResult(fmt.Errorf("caido_search: invalid arguments: part must be request, response, or both"))
	}

	active := config.ActiveCaido()
	if _, err := t.client.Login(
		ctx,
		active.AccessToken,
		active.PAT,
		active.AllowGuest,
	); err != nil {
		return failResult(err)
	}

	scopeID := strings.TrimSpace(a.ScopeID)
	scopeName := ""
	allScopes := scopeID == "*"
	if !allScopes {
		if scopeID == "" {
			_, scopeName = caido.Selection()
		}
		if scopeID != "" || scopeName != "" {
			scope, err := t.client.ResolveScope(ctx, scopeID, scopeName)
			if err != nil {
				return failResult(err)
			}
			scopeID, scopeName = scope.ID, scope.Name
		}
	}

	result, err := t.client.Search(ctx, caido.Query{
		URL:          strings.TrimSpace(a.URL),
		Method:       method,
		Status:       status,
		StatusGTE:    statusGTE,
		Host:         strings.TrimSpace(a.Host),
		PathContains: strings.TrimSpace(a.PathContains),
		HTTPQL:       strings.TrimSpace(a.HTTPQL),
		ScopeID:      scopeID,
		ScopeName:    scopeName,
		Limit:        limit,
		BodyBytes:    bodyBytes,
		Since:        time.Duration(since) * time.Second,
		Part:         part,
		Raw:          a.Raw,
	})
	if err != nil {
		return failResult(err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		return failResult(fmt.Errorf("caido_search: %w", err))
	}
	return okResult(string(data))
}

// Ensure CaidoSearch satisfies the Tool interface at compile time.
var _ Tool = (*CaidoSearch)(nil)

// caidoStatusDescription is the LLM-facing description of caido_status.
const caidoStatusDescription = "Reports which Caido project, scopes, capture mode and history size " +
	"caido_search reads. Call this FIRST when caido_search returns nothing: an " +
	"empty result usually means the wrong project, a scope that does not match, " +
	"or a capture filter, not an empty history. Read scopes[].matches before you " +
	"trust a row set: an empty allowlist renders as allowlist: [] with " +
	"matches \"all traffic\", and that scope records every host, while " +
	"matches \"allowlist\" records only its allowlist. project.selected is the " +
	"instance's project and project.configured is the [caido] project in " +
	"webmcp.toml, so a mismatch is visible. capture is the passthroughOptions " +
	"object, or {\"unfiltered\": true} when Caido filters nothing. Takes no " +
	"arguments. Example: {} returns " +
	`{"ok":true,"data":{"auth":"guest","url":"http://127.0.0.1:8080/graphql",` +
	`"project":{"id":"063ae3b3-...","name":"webmcp","path":"/tmp/webmcp",` +
	`"status":"READY","temporary":false,"read_only":false,` +
	`"selected":"webmcp","configured":"webmcp"},` +
	`"scopes":[{"id":"1","name":"target","allowlist":[],"denylist":[],` +
	`"matches":"all traffic","focus":true}],` +
	`"capture":{"unfiltered":true},"history":1879}}` + models.ResultEnvelopeDoc

// CaidoStatus implements the "caido_status" tool: it reports the project,
// scopes and capture settings that shape what caido_search can see.
type CaidoStatus struct {
	client *caido.Client
}

// NewCaidoStatus constructs the caido_status tool for the configured endpoint.
func NewCaidoStatus() *CaidoStatus {
	return &CaidoStatus{client: caido.NewClient(config.ActiveCaido().URL)}
}

// Name returns the MCP tool name "caido_status".
func (t *CaidoStatus) Name() string {
	return "caido_status"
}

// Description returns the LLM-facing description of caido_status.
func (t *CaidoStatus) Description() string {
	return caidoStatusDescription
}

// InputSchema returns the empty JSON Schema for caido_status's arguments.
func (t *CaidoStatus) InputSchema() models.InputSchema {
	return newCaidoStatusInputSchema()
}

// Execute reads the configured instance's status.
func (t *CaidoStatus) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	active := config.ActiveCaido()
	if _, err := t.client.Login(
		ctx,
		active.AccessToken,
		active.PAT,
		active.AllowGuest,
	); err != nil {
		return failResult(err)
	}
	project, scope := caido.Selection()
	status, err := t.client.Status(ctx, project, scope)
	if err != nil {
		return failResult(err)
	}
	data, err := json.Marshal(status)
	if err != nil {
		return failResult(fmt.Errorf("caido_status: %w", err))
	}
	return okResult(string(data))
}

// Ensure CaidoStatus satisfies the Tool interface at compile time.
var _ Tool = (*CaidoStatus)(nil)

// caidoSitemapDescription is the LLM-facing description of caido_sitemap.
const caidoSitemapDescription = "Lists the endpoints the Caido sitemap recorded for one host as JSON, " +
	"one row per method and path shape, so you can pick a candidate endpoint before " +
	"probing it. Reads the proxy sitemap over GraphQL and never changes, replays or " +
	"blocks traffic. host is a bare host such as www.gnu.org, with no scheme and no " +
	"path. path keeps only endpoints whose path starts with that prefix. max_depth " +
	"bounds the tree walk: default 5, clamped 1..5. scope_id selects the scope: \"\" " +
	"uses the configured focus scope, \"*\" reads every scope, any other value is a " +
	"scope id from caido_status. Each row carries method, path, kind (api, page or " +
	"static), the status of the latest hit, mime guessed from the path extension, the " +
	"requests count, sorted query params (at most 10) and shape. Rows collapse by " +
	"method and shape, where numeric and UUID segments become {id}, so /user/1 and " +
	"/user/2 are one row; requests is summed and the latest status is kept. Rows sort " +
	"by requests descending then path. The result caps at 200 rows and reports " +
	"truncated; scanned is the tree nodes walked and dropped is the rows collapsed by " +
	"shape. An empty result returns rows:[] with note naming the host, not an error. " +
	"When the walk stops at the depth cap, note reports the unvisited child count, so " +
	"a shallow read is never mistaken for the whole host. " +
	"Example: {\"host\":\"www.gnu.org\"} returns every endpoint shape on that host. " +
	"If it returns nothing, call caido_status to confirm the project and scope, " +
	"because a host outside the focus scope records no sitemap entry." + models.ResultEnvelopeDoc

// CaidoSitemap implements the "caido_sitemap" tool: it reads a host's Caido
// sitemap tree and returns the collapsed endpoint list.
type CaidoSitemap struct {
	client *caido.Client
}

// NewCaidoSitemap constructs the caido_sitemap tool for the configured endpoint.
func NewCaidoSitemap() *CaidoSitemap {
	return &CaidoSitemap{client: caido.NewClient(config.ActiveCaido().URL)}
}

// Name returns the MCP tool name "caido_sitemap".
func (t *CaidoSitemap) Name() string {
	return "caido_sitemap"
}

// Description returns the LLM-facing description of caido_sitemap.
func (t *CaidoSitemap) Description() string {
	return caidoSitemapDescription
}

// InputSchema returns the JSON Schema for caido_sitemap's arguments.
func (t *CaidoSitemap) InputSchema() models.InputSchema {
	return newCaidoSitemapInputSchema()
}

// caidoSitemapArgs is the strict decoding target for caido_sitemap arguments.
type caidoSitemapArgs struct {
	Host     string `json:"host"`
	Path     string `json:"path"`
	MaxDepth *int   `json:"max_depth"`
	ScopeID  string `json:"scope_id"`
}

// Execute reads one host's endpoint tree from the configured Caido instance.
func (t *CaidoSitemap) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a caidoSitemapArgs
	if err := decodeArgs("caido_sitemap", args, &a); err != nil {
		return failResult(err)
	}

	host := strings.TrimSpace(a.Host)
	if host == "" {
		return failResult(fmt.Errorf("caido_sitemap: invalid arguments: host is required"))
	}
	if strings.Contains(host, "://") || strings.ContainsAny(host, "/?#") {
		return failResult(fmt.Errorf(
			"caido_sitemap: invalid arguments: host must be a bare host such as www.gnu.org — no scheme and no path",
		))
	}

	depth := 5
	if a.MaxDepth != nil {
		depth = *a.MaxDepth
	}
	if depth < 1 {
		depth = 1
	}
	if depth > 5 {
		depth = 5
	}

	active := config.ActiveCaido()
	if _, err := t.client.Login(
		ctx,
		active.AccessToken,
		active.PAT,
		active.AllowGuest,
	); err != nil {
		return failResult(err)
	}

	scopeID := strings.TrimSpace(a.ScopeID)
	scopeName := ""
	allScopes := scopeID == "*"
	if !allScopes {
		if scopeID == "" {
			_, scopeName = caido.Selection()
		}
		if scopeID != "" || scopeName != "" {
			scope, err := t.client.ResolveScope(ctx, scopeID, scopeName)
			if err != nil {
				return failResult(err)
			}
			scopeID, scopeName = scope.ID, scope.Name
		}
	}

	result, err := t.client.Sitemap(ctx, caido.SitemapQuery{
		Host:    host,
		Path:    strings.TrimSpace(a.Path),
		Depth:   depth,
		ScopeID: scopeID,
	})
	if err != nil {
		return failResult(err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		return failResult(fmt.Errorf("caido_sitemap: %w", err))
	}
	return okResult(string(data))
}

// Ensure CaidoSitemap satisfies the Tool interface at compile time.
var _ Tool = (*CaidoSitemap)(nil)

// caidoFindingDescription is the LLM-facing description of caido_finding.
const caidoFindingDescription = "Reads or creates Caido findings so a proven bug outlives the chat. " +
	"A finding links a title and notes to one captured request id, and the Caido UI shows it. " +
	"action:list returns the newest findings as a findings array plus count. " +
	"action:get needs id and returns one finding. " +
	"action:create needs title and request_id and returns the new finding; request_id is a " +
	"request id from caido_search, not a URL. If caido_search returns entries:[] with dropped_since>0, " +
	"send it again with since:0 before you take a request_id. notes becomes the description and is cut at 4 KB. " +
	"Bodies in a returned description are redacted; there is no raw switch. " +
	"The tool never deletes or updates a finding. " +
	"Example: {\"action\":\"list\"}. Example: {\"action\":\"get\",\"id\":\"1\"}. " +
	"Example: {\"action\":\"create\",\"title\":\"IDOR on /api/orders\",\"request_id\":\"994\"," +
	"\"notes\":\"User A reads user B's order\"}. " +
	"If create returns an error, take request_id from a caido_search row; if list is empty, " +
	"call caido_status to confirm the project." + models.ResultEnvelopeDoc

// CaidoFinding implements the "caido_finding" tool: it lists, reads and creates
// findings that link a title and notes to one captured request.
type CaidoFinding struct {
	client *caido.Client
}

// NewCaidoFinding constructs the caido_finding tool for the configured endpoint.
func NewCaidoFinding() *CaidoFinding {
	return &CaidoFinding{client: caido.NewClient(config.ActiveCaido().URL)}
}

// Name returns the MCP tool name "caido_finding".
func (t *CaidoFinding) Name() string {
	return "caido_finding"
}

// Description returns the LLM-facing description of caido_finding.
func (t *CaidoFinding) Description() string {
	return caidoFindingDescription
}

// InputSchema returns the JSON Schema for caido_finding's arguments.
func (t *CaidoFinding) InputSchema() models.InputSchema {
	return newCaidoFindingInputSchema()
}

// caidoFindingArgs is the strict decoding target for caido_finding arguments.
type caidoFindingArgs struct {
	Action    string `json:"action"`
	ID        string `json:"id"`
	Title     string `json:"title"`
	RequestID string `json:"request_id"`
	Notes     string `json:"notes"`
}

// Execute runs one finding action against the configured Caido instance.
func (t *CaidoFinding) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a caidoFindingArgs
	if err := decodeArgs("caido_finding", args, &a); err != nil {
		return failResult(err)
	}
	action := strings.ToLower(strings.TrimSpace(a.Action))
	switch action {
	case "list", "get", "create":
	default:
		return failResult(fmt.Errorf(
			"caido_finding: invalid arguments: action must be list, get, or create",
		))
	}
	id := strings.TrimSpace(a.ID)
	title := strings.TrimSpace(a.Title)
	requestID := strings.TrimSpace(a.RequestID)
	if action == "get" && id == "" {
		return failResult(fmt.Errorf(
			"caido_finding: invalid arguments: get needs id",
		))
	}
	if action == "create" && (title == "" || requestID == "") {
		return failResult(fmt.Errorf(
			"caido_finding: invalid arguments: create needs title and request_id — take a request id from caido_search",
		))
	}

	active := config.ActiveCaido()
	if _, err := t.client.Login(
		ctx,
		active.AccessToken,
		active.PAT,
		active.AllowGuest,
	); err != nil {
		return failResult(err)
	}

	switch action {
	case "list":
		findings, err := t.client.Findings(ctx)
		if err != nil {
			return failResult(err)
		}
		payload := struct {
			Findings []caido.Finding `json:"findings"`
			Count    int             `json:"count"`
		}{Findings: findings, Count: len(findings)}
		data, err := json.Marshal(payload)
		if err != nil {
			return failResult(fmt.Errorf("caido_finding: %w", err))
		}
		return okResult(string(data))

	case "get":
		finding, err := t.client.Finding(ctx, id)
		if err != nil {
			return failResult(err)
		}
		data, err := json.Marshal(finding)
		if err != nil {
			return failResult(fmt.Errorf("caido_finding: %w", err))
		}
		return okResult(string(data))

	default:
		finding, err := t.client.CreateFinding(ctx, title, requestID, a.Notes)
		if err != nil {
			return failResult(err)
		}
		data, err := json.Marshal(finding)
		if err != nil {
			return failResult(fmt.Errorf("caido_finding: %w", err))
		}
		return okResult(string(data))
	}
}

// Ensure CaidoFinding satisfies the Tool interface at compile time.
var _ Tool = (*CaidoFinding)(nil)

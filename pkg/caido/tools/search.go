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
	"github.com/dakolli/webmcp/pkg/mcp/toolenv"
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
	"searches every scope for POST errors." + toolenv.ResultEnvelopeDoc

// InputSchema returns the JSON Schema for caido_search's arguments.
func (t *CaidoSearch) InputSchema() models.InputSchema {
	return models.InputSchema{
		Type: "object",
		Properties: map[string]any{
			"httpql": map[string]any{
				"type":        "string",
				"description": "Raw HTTPQL expression sent to the instance unchanged, e.g. req.host.eq:\"www.gnu.org\". When non-empty the typed keys are ignored. An instance error returns as an error.",
			},
			"scope_id": map[string]any{
				"type":        "string",
				"description": "Scope to search: \"\" uses the configured focus scope, \"*\" searches every scope, any other value is a scope id from caido_status.",
			},
			"host": map[string]any{
				"type":        "string",
				"description": "Typed host filter, compiled to req.host.eq:\"H\". Empty means every host.",
			},
			"url": map[string]any{
				"type":        "string",
				"description": "Substring matched case-insensitively against the composed URL, applied over the rows the instance returns. Empty means every URL.",
			},
			"method": map[string]any{
				"type":        "string",
				"enum":        []string{"GET", "POST", ""},
				"description": "HTTP method filter: GET, POST, or empty for all methods.",
			},
			"status": map[string]any{
				"type":        "integer",
				"description": "Exact status code, compiled to resp.code.eq:N. 0 for all codes.",
			},
			"status_gte": map[string]any{
				"type":        "integer",
				"description": "Minimum status code, compiled to resp.code.gte:N. 0 disables it.",
			},
			"path_contains": map[string]any{
				"type":        "string",
				"description": "Path substring, compiled to req.path.cont:\"P\". Empty disables it.",
			},
			"since": map[string]any{
				"type":        "integer",
				"description": "Lookback window in seconds applied to the returned rows. Default 900. 0 disables the time filter. total ignores it, so count 0 beside a non-zero total means pass since:0 to read the older rows.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Rows returned. Default 20, clamps at 100.",
			},
			"body_bytes": map[string]any{
				"type":        "integer",
				"description": "Cap per body in bytes. Default 2000, clamps at 65536. 0 returns metadata and headers with empty bodies.",
			},
			"part": map[string]any{
				"type":        "string",
				"enum":        []string{"request", "response", "both"},
				"description": "Which sides to return bodies for. Default both.",
			},
			"raw": map[string]any{
				"type":        "boolean",
				"description": "Disable credential redaction. Default false. The body_bytes cut still applies.",
			},
		},
	}
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
	if err := toolenv.DecodeArgs("caido_search", args, &a); err != nil {
		return toolenv.FailResult(err)
	}

	method := strings.ToUpper(strings.TrimSpace(a.Method))
	if method != "" && method != "GET" && method != "POST" {
		return toolenv.FailResult(fmt.Errorf("caido_search: invalid arguments: method must be GET, POST, or empty"))
	}

	status := 0
	if a.Status != nil {
		status = *a.Status
		if status < 0 {
			return toolenv.FailResult(fmt.Errorf("caido_search: invalid arguments: status must be >= 0"))
		}
	}

	statusGTE := 0
	if a.StatusGTE != nil {
		statusGTE = *a.StatusGTE
		if statusGTE < 0 {
			return toolenv.FailResult(fmt.Errorf("caido_search: invalid arguments: status_gte must be >= 0"))
		}
	}

	since := 900
	if a.Since != nil {
		since = *a.Since
		if since < 0 {
			return toolenv.FailResult(fmt.Errorf("caido_search: invalid arguments: since must be >= 0"))
		}
	}

	limit := 20
	if a.Limit != nil {
		limit = *a.Limit
		if limit < 0 {
			return toolenv.FailResult(fmt.Errorf("caido_search: invalid arguments: limit must be >= 0"))
		}
	}
	if limit == 0 {
		limit = 20
	}

	bodyBytes := 2000
	if a.BodyBytes != nil {
		bodyBytes = *a.BodyBytes
		if bodyBytes < 0 {
			return toolenv.FailResult(fmt.Errorf("caido_search: invalid arguments: body_bytes must be >= 0"))
		}
	}

	part := strings.ToLower(strings.TrimSpace(a.Part))
	switch part {
	case "", "both":
		part = "both"
	case "request", "response":
	default:
		return toolenv.FailResult(fmt.Errorf("caido_search: invalid arguments: part must be request, response, or both"))
	}

	active := config.ActiveCaido()
	if _, err := t.client.Login(
		ctx,
		active.AccessToken,
		active.PAT,
		active.AllowGuest,
	); err != nil {
		return toolenv.FailResult(err)
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
				return toolenv.FailResult(err)
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
		return toolenv.FailResult(err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		return toolenv.FailResult(fmt.Errorf("caido_search: %w", err))
	}
	return toolenv.OKResult(string(data))
}

// Ensure CaidoSearch satisfies the Tool interface at compile time.
var _ toolenv.Tool = (*CaidoSearch)(nil)

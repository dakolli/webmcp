package tools

import "github.com/dakolli/webmcp/pkg/mcp/models"

// BrowserCreateDescription is the LLM-facing description of the BrowserCreate tool.
const BrowserCreateDescription = "Creates or reuses a live Chromium browser and returns its browser_id. " +
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
	ResultEnvelopeDoc

// BrowserListDescription is the LLM-facing description of the BrowserList tool.
const BrowserListDescription = "Lists live browsers with their lifecycle state as compact JSON: browser_id, " +
	"owner, bin, profile_dir, persist, ephemeral, created_at, idle_for (how long the " +
	"browser has been unused), ttl (when idle cleanup closes it), scope (the request " +
	"allowlist: \"open\" when unrestricted), scope_strict (whether an empty allowlist " +
	"denies all requests), limits (the effective per-host budget: {rps, burst, hosts:[{host, " +
	"throttled}]}) and pages, each {id, url, title?, context?, stale?}. " +
	"Returns [] when none are running. Use it to see what you own and what to close; " +
	"idle browsers expire automatically. Example: {}." + ResultEnvelopeDoc

// BrowserCloseDescription is the LLM-facing description of the BrowserClose tool.
const BrowserCloseDescription = "Closes a live browser and all of its pages, releasing the Chromium process. " +
	"Pass browser_id to close one browser, or omit to close all. purge=true also " +
	"deletes that browser's profile directory (guarded: only paths under " +
	"~/.config/browser-mcp/profiles; this is the only way to delete a named persistent " +
	"profile). prune=true (with no browser_id) closes all browsers and deletes leftover " +
	"profiles under profiles/.ephemeral that no live browser owns — named persistent " +
	"profiles are never deleted by prune. Idle browsers expire on their own, but close " +
	"explicitly when done; an unknown browser_id returns a not-found error listing live " +
	"ids and owners. Example: { \"browser_id\": \"browser-2\", \"purge\": true }." +
	ResultEnvelopeDoc

// PageListDescription is the LLM-facing description of the PageList tool.
const PageListDescription = "Lists every live page of the given browser as a JSON array. Each " +
	"entry is {id, url, context, title?, stale?}: url and title are read " +
	"from the live tab, so they follow navigations and redirects; context " +
	"is the incognito identity ('default' for the browser's own cookie " +
	"jar); stale marks an entry whose tab was busy or unreadable, in which " +
	"case url is the last known value. Use it to see which sessions/tabs " +
	"are still open inside the browser before closing them with " +
	"session_close. An unknown browser_id returns a not-found error listing " +
	"the running ids. " +
	"Example: { \"browser_id\": \"browser-1\" }." + ResultEnvelopeDoc

// BrowserSetupAndEmulatePromptDescription is the LLM-facing description of the BrowserSetupAndEmulatePrompt tool.
const BrowserSetupAndEmulatePromptDescription = "Provides guidance for initializing a browser session and setting up optional device emulation."

// newBrowserCreateInputSchema returns the input schema of the browser_create tool.
func newBrowserCreateInputSchema() models.InputSchema {
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
			"deny_hosts": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
				"description": "Opt-in proxy deny list of host globs, for example [\"*.bsg.brave.com\", \"usage-ping.brave.com\"]. " +
					"Each host is routed outside the proxy and resolved to NOTFOUND, so browser telemetry never reaches the proxy history. " +
					"Default empty: behaviour is unchanged. Chromium and Chrome understand the same mechanism.",
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

// newBrowserListInputSchema returns the input schema of the browser_list tool.
func newBrowserListInputSchema() models.InputSchema {
	return models.InputSchema{
		Type:       "object",
		Properties: map[string]any{},
	}
}

// newBrowserCloseInputSchema returns the input schema of the browser_close tool.
func newBrowserCloseInputSchema() models.InputSchema {
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

// newPageListInputSchema returns the input schema of the page_list tool.
func newPageListInputSchema() models.InputSchema {
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

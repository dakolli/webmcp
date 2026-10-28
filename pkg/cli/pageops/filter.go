package pageops

import (
	"fmt"
	"strings"

	"github.com/dakolli/webmcp/pkg/browsers"
)

// networkFilterTypes lists the CDP request types the network filter accepts,
// lowercased and in the order a rejection message names them. Matching against
// an entry's type is case-insensitive.
var networkFilterTypes = []string{
	"document", "stylesheet", "image", "media", "font", "script",
	"texttrack", "xhr", "fetch", "prefetch", "eventsource", "websocket",
	"manifest", "signedexchange", "ping", "cspviolationreport", "preflight",
	"other",
}

// networkFilter is one parsed what=network filter: keep holds the types named
// without a prefix, drop the types named with "-". A nil filter keeps
// everything.
type networkFilter struct {
	keep map[string]bool
	drop map[string]bool
}

// parseNetworkFilter parses a comma list of request types. A bare name keeps
// that type, a "-" prefix drops it, and empty tokens are ignored. An empty list
// yields a nil filter, and an unknown name fails with the accepted list.
func parseNetworkFilter(raw string) (*networkFilter, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	known := make(map[string]bool, len(networkFilterTypes))
	for _, name := range networkFilterTypes {
		known[name] = true
	}
	f := &networkFilter{keep: make(map[string]bool), drop: make(map[string]bool)}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		dropped := strings.HasPrefix(part, "-")
		name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(part, "-")))
		if !known[name] {
			return nil, fmt.Errorf(
				"invalid arguments: what=network filter %q names unknown request type %q — accepted: %s",
				raw, name, strings.Join(networkFilterTypes, ", "),
			)
		}
		if dropped {
			f.drop[name] = true
		} else {
			f.keep[name] = true
		}
	}
	if len(f.keep) == 0 && len(f.drop) == 0 {
		return nil, nil
	}
	return f, nil
}

// keeps reports whether a request type survives the filter. With keep names
// only those types survive; drop names are removed either way.
func (f *networkFilter) keeps(requestType string) bool {
	if f == nil {
		return true
	}
	t := strings.ToLower(requestType)
	if len(f.keep) > 0 && !f.keep[t] {
		return false
	}
	return !f.drop[t]
}

// apply returns the entries the filter keeps and how many it removed.
func (f *networkFilter) apply(entries []browsers.NetworkEntry) ([]browsers.NetworkEntry, int) {
	if f == nil {
		return entries, 0
	}
	kept := make([]browsers.NetworkEntry, 0, len(entries))
	for _, entry := range entries {
		if f.keeps(entry.Type) {
			kept = append(kept, entry)
		}
	}
	return kept, len(entries) - len(kept)
}

// sinksCanaryPrefix marks every auto-minted inject_canary value, so a
// filter=canary read recognizes the injected marker in a sink value.
const sinksCanaryPrefix = "wm-canary-"

// sinksFilter is one parsed what=sinks filter: the empty value keeps every hit,
// canary keeps only values holding the injected marker, and known drops hits
// whose source is "unknown".
type sinksFilter string

const (
	sinksFilterCanary sinksFilter = "canary"
	sinksFilterKnown  sinksFilter = "known"
)

// parseSinksFilter parses the one sinks filter value. An empty value yields the
// empty filter and any other value fails with both accepted forms.
func parseSinksFilter(raw string) (sinksFilter, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return "", nil
	case string(sinksFilterCanary):
		return sinksFilterCanary, nil
	case string(sinksFilterKnown):
		return sinksFilterKnown, nil
	default:
		return "", fmt.Errorf(
			`invalid arguments: what=sinks filter %q is not accepted — use canary (only hits whose value holds the injected wm-canary-… marker) or known (drop hits whose source is "unknown")`,
			raw,
		)
	}
}

// keepsHit reports whether one sink hit survives the filter.
func (f sinksFilter) keepsHit(hit browsers.SinkHit) bool {
	switch f {
	case sinksFilterCanary:
		return strings.Contains(hit.Value, sinksCanaryPrefix)
	case sinksFilterKnown:
		return !strings.EqualFold(hit.Source, "unknown")
	default:
		return true
	}
}

// apply returns the hits the filter keeps and how many it removed.
func (f sinksFilter) apply(hits []browsers.SinkHit) ([]browsers.SinkHit, int) {
	if f == "" {
		return hits, 0
	}
	kept := make([]browsers.SinkHit, 0, len(hits))
	for _, hit := range hits {
		if f.keepsHit(hit) {
			kept = append(kept, hit)
		}
	}
	return kept, len(hits) - len(kept)
}

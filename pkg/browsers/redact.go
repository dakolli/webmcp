package browsers

import (
	"sort"
	"strings"

	"github.com/go-rod/rod/lib/proto"
)

const (
	// redactedValue replaces a sensitive header value on capture.
	redactedValue = "<redacted>"
	// networkHeaderCountLimit bounds how many headers one direction keeps.
	networkHeaderCountLimit = 32
	// networkHeaderValueLimit bounds one stored header value.
	networkHeaderValueLimit = 512
	// networkHeaderBudget bounds the stored name+value bytes per direction,
	// keeping 200 entries with both header sets well under 2 MB.
	networkHeaderBudget = 4 << 10
)

// sensitiveHeaders lists, lowercased, the header names whose values are
// replaced by redactedValue unless the page opted into secret capture.
var sensitiveHeaders = map[string]bool{
	"cookie":        true,
	"set-cookie":    true,
	"authorization": true,
	"x-csrf-token":  true,
}

// redactHeaders converts CDP headers into a bounded string map. Values of
// sensitiveHeaders become redactedValue unless keep is true; a set cut by the
// count, value or byte bound reports truncated.
func redactHeaders(h proto.NetworkHeaders, keep bool) (map[string]string, bool) {
	if len(h) == 0 {
		return nil, false
	}
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]string, min(len(h), networkHeaderCountLimit))
	used := 0
	for _, k := range keys {
		if len(out) >= networkHeaderCountLimit {
			return out, true
		}
		v := h[k].Str()
		if !keep && sensitiveHeaders[strings.ToLower(k)] {
			v = redactedValue
		}
		v = cutString(v, networkHeaderValueLimit)
		if used+len(k)+len(v) > networkHeaderBudget {
			return out, true
		}
		used += len(k) + len(v)
		out[k] = v
	}
	return out, false
}

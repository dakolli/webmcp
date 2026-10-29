package pageops

import (
	"context"

	"github.com/dakolli/webmcp/pkg/browsers"
)

// PageMutations returns the page's DOM mutation timeline as the shared log
// envelope {"entries":[{seq,ts,kind,target,attribute,added,removed,text}],
// "count","dropped","truncated","last_seq"}, newest 100 batches oldest first.
// since keeps only entries newer than that sequence, so a caller reads last_seq
// before an action and passes it back after the action to see exactly what the
// action changed. selector keeps only entries whose target contains it. The
// read is served from the page's in-memory ring, never from CDP, so it neither
// arms capture nor waits on the page lock.
func PageMutations(
	_ context.Context,
	p *browsers.Page,
	since int64,
	selector string,
) (string, error) {
	if err := guardRead(p); err != nil {
		return "", err
	}
	entries, dropped, lastSeq := p.Mutations(since, selector)
	return encodeLogEnvelope(entries, dropped, nil, &lastSeq, "mutations")
}

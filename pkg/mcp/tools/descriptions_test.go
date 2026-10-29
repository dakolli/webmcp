package tools

import (
	"strings"
	"testing"

	"github.com/dakolli/webmcp/pkg/mcp/models"
)

// TestEveryToolDescriptionCarriesEnvelopeDoc guards the moved description
// constants: every tool must still publish its prose plus the shared result
// envelope paragraph, and every prompt must publish a non-empty description.
func TestEveryToolDescriptionCarriesEnvelopeDoc(t *testing.T) {
	r := NewRegistry()

	for _, tool := range r.List() {
		if tool.Description == "" {
			t.Errorf("%s: empty description", tool.Name)
			continue
		}
		if !strings.HasSuffix(tool.Description, models.ResultEnvelopeDoc) {
			t.Errorf("%s: description does not end with ResultEnvelopeDoc", tool.Name)
		}
	}

	for _, prompt := range r.ListPrompts() {
		if prompt.Description == "" {
			t.Errorf("prompt %s: empty description", prompt.Name)
		}
	}
}

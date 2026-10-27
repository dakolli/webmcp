package tools

import (
	"github.com/dakolli/webmcp/pkg/mcp/models"
)

// Prompt represents an MCP prompt template implementation.
//
// Each prompt definition is co-located within its primary tool file and
// registered into Registry. When an agent calls prompts/list or prompts/get,
// these definitions generate structured prompt messages for the agent.
type Prompt interface {
	// Name returns the prompt template name, e.g. "fetch_and_summarize".
	Name() string
	// Description returns an LLM/user-facing explanation of what the prompt template does.
	Description() string
	// Arguments returns the argument schemas accepted by the prompt template.
	Arguments() []models.PromptArgument
	// GetMessages builds the prompt messages payload for the given argument map.
	GetMessages(args map[string]string) ([]models.PromptMessage, error)
}

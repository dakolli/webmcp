package tools

import "github.com/dakolli/webmcp/pkg/mcp/toolenv"

// Tool aliases the shared MCP tool contract.
type Tool = toolenv.Tool

// resultEnvelope aliases the shared result envelope.
type resultEnvelope = toolenv.ResultEnvelope

// envelopeError aliases the shared failure branch.
type envelopeError = toolenv.EnvelopeError

const (
	// ResultEnvelopeDoc aliases the shared envelope documentation paragraph.
	ResultEnvelopeDoc = toolenv.ResultEnvelopeDoc

	errInvalidArgs     = toolenv.ErrInvalidArgs
	errNotFound        = toolenv.ErrNotFound
	errElementNotFound = toolenv.ErrElementNotFound
	errNotInteractable = toolenv.ErrNotInteractable
	errAntiBot         = toolenv.ErrAntiBot
	errPageClosed      = toolenv.ErrPageClosed
	errTimeout         = toolenv.ErrTimeout
	errNavFailed       = toolenv.ErrNavFailed
	errUnknown         = toolenv.ErrUnknown
)

var (
	okResult          = toolenv.OKResult
	failResult        = toolenv.FailResult
	failResultForPage = toolenv.FailResultForPage
	marshalEnvelope   = toolenv.MarshalEnvelope
	decodeArgs        = toolenv.DecodeArgs
)

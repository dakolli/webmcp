package tools

import "strings"

// genericReconPrefix is the lane-wide prefix pkg/recon puts on a guard refusal
// before any tool claims the call.
const genericReconPrefix = "recon: "

// namedError attributes err to the calling tool exactly once. It replaces the
// lane's generic "recon: " prefix rather than stacking on top of it, leaves an
// error that already names the tool untouched, and keeps the original reachable
// through errors.Is and errors.As.
func namedError(tool string, err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.HasPrefix(msg, tool+": ") {
		return err
	}
	return &toolError{
		tool: tool,
		body: strings.TrimPrefix(msg, genericReconPrefix),
		err:  err,
	}
}

// toolError renders a wrapped error under its calling tool's name.
type toolError struct {
	tool string
	body string
	err  error
}

// Error renders "tool: body".
func (e *toolError) Error() string {
	return e.tool + ": " + e.body
}

// Unwrap exposes the original error to errors.Is and errors.As.
func (e *toolError) Unwrap() error {
	return e.err
}

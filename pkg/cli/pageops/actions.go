package pageops

import "strings"

// Action describes one page-act action: its canonical name, the inputs it
// consumes, and a one-line summary. The table below is the single source of
// truth for the page_act enum, the CLI action vocabulary and the IPC allow-list.
type Action struct {
	Name          string
	NeedsSelector bool
	NeedsValue    bool
	NeedsPaths    bool
	Summary       string
}

// actions is the page-act vocabulary in schema order. Every surface derives its
// action names from this table, so adding an action means editing one row.
var actions = []Action{
	{Name: "click", NeedsSelector: true, Summary: "Click an element matched by selector or handle."},
	{Name: "input", NeedsSelector: true, NeedsValue: true, Summary: "Set a field's text directly."},
	{Name: "type", NeedsSelector: true, NeedsValue: true, Summary: "Type keystrokes into a field."},
	{Name: "press_key", NeedsSelector: true, NeedsValue: true, Summary: "Send a key or key combination."},
	{Name: "click_at", Summary: "Click at viewport coordinates."},
	{Name: "hover_at", Summary: "Move the pointer to viewport coordinates."},
	{Name: "drag_and_drop", NeedsSelector: true, Summary: "Drag from a point or element to a target."},
	{Name: "upload_file", NeedsSelector: true, NeedsValue: true, NeedsPaths: true, Summary: "Set files on a file input."},
	{Name: "eval", NeedsValue: true, Summary: "Run JavaScript in the page."},
	{Name: "navigate", NeedsValue: true, Summary: "Navigate the page to a URL."},
	{Name: "back", Summary: "Go back in history."},
	{Name: "forward", Summary: "Go forward in history."},
	{Name: "reload", Summary: "Reload the page."},
	{Name: "hover", NeedsSelector: true, Summary: "Hover an element matched by selector or handle."},
	{Name: "set_style", NeedsSelector: true, NeedsValue: true, Summary: "Set inline CSS declarations on an element, or clear them with an empty value."},
	{Name: "scroll", Summary: "Scroll the page by x and y."},
	{Name: "select", NeedsSelector: true, NeedsValue: true, Summary: "Select an option in a select element."},
	{Name: "wait", Summary: "Wait for a readiness mode."},
	{Name: "set_viewport", Summary: "Set the viewport size."},
	{Name: "emulate", Summary: "Apply a device preset."},
	{Name: "clear_logs", Summary: "Empty the network, console and websocket buffers."},
	{Name: "sinks_install", Summary: "Arm the source-to-sink tracer."},
	{Name: "sinks_clear", Summary: "Empty the source-to-sink ring."},
	{Name: "replay", Summary: "Resend one captured network request."},
	{Name: "inject_canary", Summary: "Inject a canary into page sinks."},
	{Name: "postmessage_install", Summary: "Arm the postMessage recorder."},
	{Name: "postmessage_clear", Summary: "Empty the postMessage rings."},
	{Name: "post_message", NeedsValue: true, Summary: "Deliver a message to a window or frame."},
	{Name: "wait_open", Summary: "Wait for the page to open a popup."},
	{Name: "intercept_add", Summary: "Arm in-flight request interception rules."},
	{Name: "intercept_list", Summary: "List the page's interception rules."},
	{Name: "intercept_clear", Summary: "Remove every interception rule."},
	{Name: "auth_save", Summary: "Save the page's cookies and storage."},
	{Name: "auth_restore", Summary: "Restore a saved auth snapshot."},
	{Name: "auth_list", Summary: "List saved auth snapshots."},
	{Name: "replay_fuzz", Summary: "Run a bounded request-variation matrix."},
	{Name: "poc_bundle", NeedsValue: true, NeedsPaths: true, Summary: "Package a finding as an evidence directory."},
	{Name: "dialog_accept", Summary: "Accept a JavaScript dialog, using value as the prompt text."},
	{Name: "dialog_dismiss", Summary: "Dismiss a JavaScript dialog."},
	{Name: "http_auth", NeedsValue: true, Summary: "Set or clear a per-page HTTP basic-auth credential given as user:password."},
}

// Actions returns a copy of the page-act action table in schema order.
func Actions() []Action {
	return append([]Action(nil), actions...)
}

// ActionNames returns the canonical action names in table order.
func ActionNames() []string {
	names := make([]string, len(actions))
	for i, a := range actions {
		names[i] = a.Name
	}
	return names
}

// LookupAction finds an action by name. It accepts the CLI dashed spelling by
// normalising '-' to '_', so "poc-bundle" resolves to the "poc_bundle" row.
func LookupAction(name string) (Action, bool) {
	if a, ok := findAction(name); ok {
		return a, true
	}
	return findAction(strings.ReplaceAll(name, "-", "_"))
}

// findAction returns the row whose name matches exactly.
func findAction(name string) (Action, bool) {
	for _, a := range actions {
		if a.Name == name {
			return a, true
		}
	}
	return Action{}, false
}

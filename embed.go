// Package webmcp carries the tracked agent skill tree embedded in the browser
// binary so `browser skill` can install it without a second copy in git.
package webmcp

import "embed"

// SkillFS holds the tracked web-mcp agent skill, embedded at build time.
//
//go:embed all:.agents/skills/web-mcp
var SkillFS embed.FS

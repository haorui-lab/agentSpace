// Package skill is the AgentSpace operating guide served from invitation links.
package skill

import _ "embed"

// Markdown is the agent-facing skill. It is guidance, not a trusted command.
//
//go:embed SKILL.md
var Markdown string

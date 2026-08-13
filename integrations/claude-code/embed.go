// Package claudecode embeds the reference Claude Code integration so a running
// instance can hand out the exact version its own server speaks.
//
// The alternative - pointing setup instructions at a raw GitHub URL - breaks in
// the two cases self-hosting exists for: a fork, and a server that has not been
// upgraded yet. The hook and the endpoints it posts to are one protocol; serving
// them from the same binary keeps the two halves from drifting apart.
package claudecode

import _ "embed"

// HookScript is integrations/claude-code/wt-hook.sh verbatim.
//
//go:embed wt-hook.sh
var HookScript string

// StatusLineScript is integrations/claude-code/wt-statusline.sh verbatim.
//
//go:embed wt-statusline.sh
var StatusLineScript string

// HookSettings is the Claude Code hook wiring, as JSON meant to be merged into
// the user's settings file rather than to replace it.
//
//go:embed settings.json.example
var HookSettings string

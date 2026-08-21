package common

import "strings"

// NormalizeStopReason maps an upstream Kiro/CodeWhisperer stop reason onto the
// Anthropic Messages API vocabulary.
//
// The Kiro event stream reports the AWS enum spelling ("END_TURN", "TOOL_USE"),
// but clients match the lowercase spec values — the Claude Agent SDK decides
// whether to run a tool by comparing stop_reason against "tool_use", and the
// OpenAI-compatible surface maps "tool_use" to finish_reason "tool_calls".
// Passing the AWS spelling through makes both silently take the wrong branch,
// so every stop reason is folded to its spec spelling here before it reaches a
// response builder.
//
// Unrecognized values are lowercased rather than replaced: downstream mappers
// (e.g. "content_filtered" -> OpenAI "content_filter") own their own vocabulary,
// and dropping an unknown reason would hide it from them.
func NormalizeStopReason(stopReason string) string {
	normalized := strings.ToLower(strings.TrimSpace(stopReason))
	switch normalized {
	case "endturn":
		return "end_turn"
	case "tooluse":
		return "tool_use"
	case "maxtokens":
		return "max_tokens"
	case "stopsequence":
		return "stop_sequence"
	case "pauseturn":
		return "pause_turn"
	default:
		return normalized
	}
}

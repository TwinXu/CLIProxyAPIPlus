package common

import (
	"strings"
	"unicode"
)

// stopReasonSpellings maps a stop reason, folded to lowercase with every
// non-alphanumeric rune removed, onto its canonical snake_case spelling.
//
// Most rows are Anthropic Messages API values. Two are not: content_filtered
// and guardrail_intervened are Bedrock Converse reasons with no Anthropic
// equivalent. They are canonicalized rather than translated because the
// OpenAI mapper matches content_filtered by name, and because dropping a
// reason would hide it from a downstream mapper that does understand it.
//
// The map is read-only after init — it is never assigned to outside this
// literal, and NormalizeStopReason reads it on the per-response hot path
// from both the streaming and non-streaming executors.
//
// The key is separator-free on purpose: the same reason reaches us in at least
// three spellings depending on which upstream surface produced it — the AWS
// enum ("END_TURN"), the Converse camelCase ("endTurn"), and the spec spelling
// itself ("end_turn"). Folding the separators out collapses all three onto one
// key, so a reason needs exactly one entry here instead of one per spelling.
//
// Single-word reasons ("refusal") need no entry: lowercasing alone already
// produces the spec spelling, and the fallthrough returns it unchanged.
var stopReasonSpellings = map[string]string{
	"endturn":                    "end_turn",
	"tooluse":                    "tool_use",
	"maxtokens":                  "max_tokens",
	"stopsequence":               "stop_sequence",
	"pauseturn":                  "pause_turn",
	"contentfiltered":            "content_filtered",
	"guardrailintervened":        "guardrail_intervened",
	"modelcontextwindowexceeded": "model_context_window_exceeded",
}

// foldStopReasonKey drops every non-alphanumeric rune. Enumerating separators
// ("_", "-", " ") left tabs, dots and NBSP unfolded, so a spelling like
// "END\tTURN" missed the table and reached clients verbatim.
func foldStopReasonKey(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return -1
	}, s)
}

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
// Reasons outside the table are lowercased but otherwise returned as they came:
// downstream mappers own their own vocabulary, and dropping an unknown reason
// would hide it from them. Note this includes emitting a value the caller's
// own vocabulary may not model — mapKiroStopReasonToOpenAI passes such a
// reason through as finish_reason, which is a closed enum on that surface.
func NormalizeStopReason(stopReason string) string {
	normalized := strings.ToLower(strings.TrimSpace(stopReason))
	if spelled, ok := stopReasonSpellings[foldStopReasonKey(normalized)]; ok {
		return spelled
	}
	return normalized
}

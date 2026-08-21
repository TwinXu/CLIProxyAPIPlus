package common

import "strings"

// stopReasonSpellings maps a stop reason, folded to lowercase with every
// separator removed, onto its Anthropic Messages API spelling.
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

var stopReasonSeparators = strings.NewReplacer("_", "", "-", "", " ", "")

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
// would hide it from them.
func NormalizeStopReason(stopReason string) string {
	normalized := strings.ToLower(strings.TrimSpace(stopReason))
	if spelled, ok := stopReasonSpellings[stopReasonSeparators.Replace(normalized)]; ok {
		return spelled
	}
	return normalized
}

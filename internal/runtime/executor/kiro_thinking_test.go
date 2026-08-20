package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

// Kiro was the only executor that never ran thinking.ApplyThinking, so a thinking
// suffix was parsed for routing and then discarded and nothing reconciled the
// client's request with what the backend accepts. These cases pin the behaviour
// that wiring it in is supposed to produce, using what ListAvailableModels on
// q.us-east-1.amazonaws.com reports: claude-opus-5 accepts low/medium/high/xhigh/max,
// claude-opus-4.6 accepts low/medium/high/max, and neither takes a token budget.
func TestApplyKiroThinkingHonoursSuffix(t *testing.T) {
	const body = `{"model":"kiro-claude-opus-5","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`

	for _, tc := range []struct {
		name       string
		model      string
		wantEffort string
	}{
		{"high", "kiro-claude-opus-5(high)", "high"},
		{"low", "kiro-claude-opus-5(low)", "low"},
		// xhigh and max are distinct levels on Claude 5; xhigh must not be promoted.
		{"xhigh stays xhigh", "kiro-claude-opus-5(xhigh)", "xhigh"},
		{"max", "kiro-claude-opus-5(max)", "max"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := applyKiroThinking([]byte(body), tc.model, "claude")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := gjson.GetBytes(out, "output_config.effort").String(); got != tc.wantEffort {
				t.Fatalf("output_config.effort = %q, want %q\nbody: %s", got, tc.wantEffort, out)
			}
			if got := gjson.GetBytes(out, "thinking.type").String(); got != "adaptive" {
				t.Fatalf("thinking.type = %q, want adaptive\nbody: %s", got, out)
			}
			if gjson.GetBytes(out, "thinking.budget_tokens").Exists() {
				t.Fatalf("budget_tokens must not survive; the backend takes no budget\nbody: %s", out)
			}
		})
	}
}

func TestApplyKiroThinkingRejectsLevelUnsupportedByModel(t *testing.T) {
	// claude-opus-4.6's enum has no xhigh. Reporting that is the point: silently
	// dropping the config would leave the backend applying its own default, which
	// is exactly the quiet substitution 0dabffed set out to stop.
	const body = `{"model":"kiro-claude-opus-4-6","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`
	out, err := applyKiroThinking([]byte(body), "kiro-claude-opus-4-6(xhigh)", "claude")
	if err == nil {
		t.Fatalf("expected an error for an unsupported level, got none\nbody: %s", out)
	}
	if gjson.GetBytes(out, "output_config.effort").Exists() {
		t.Fatalf("no effort should have been written\nbody: %s", out)
	}
}

func TestApplyKiroThinkingConvertsBudgetToLevel(t *testing.T) {
	// A Claude-style budget request must be reconciled into an effort level rather
	// than rejected or forwarded as a budget the backend does not accept.
	const body = `{"model":"kiro-claude-opus-5","max_tokens":1024,"thinking":{"type":"enabled","budget_tokens":30000},"messages":[{"role":"user","content":"hi"}]}`
	out, err := applyKiroThinking([]byte(body), "kiro-claude-opus-5", "claude")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if effort := gjson.GetBytes(out, "output_config.effort").String(); effort == "" {
		t.Fatalf("expected a derived effort level, got none\nbody: %s", out)
	}
	if gjson.GetBytes(out, "thinking.budget_tokens").Exists() {
		t.Fatalf("budget_tokens must not survive to a backend that takes no budget\nbody: %s", out)
	}
}

func TestApplyKiroThinkingLeavesUnspecifiedRequestsAlone(t *testing.T) {
	// With no thinking intent from the client we send no effort, leaving the
	// backend's own default in force (high for opus-5, xhigh for opus-4.7).
	const body = `{"model":"kiro-claude-opus-5","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`
	out, err := applyKiroThinking([]byte(body), "kiro-claude-opus-5", "claude")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gjson.GetBytes(out, "output_config.effort").Exists() {
		t.Fatalf("effort must be absent when the client did not ask for one\nbody: %s", out)
	}
}

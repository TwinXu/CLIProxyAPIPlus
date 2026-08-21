package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	kiroclaude "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/kiro/claude"
	"github.com/tidwall/gjson"
)

// Kiro was the only executor that never ran thinking.ApplyThinking, so a thinking
// suffix was parsed for routing and then discarded. These cases pin what wiring it
// in does -- and, just as importantly, what it must not do. They use what
// ListAvailableModels on the Kiro backend reports: claude-opus-5 accepts
// low/medium/high/xhigh/max, claude-opus-4.6 accepts low/medium/high/max, and
// neither takes a token budget.
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
				t.Fatalf("applyKiroThinking: %v", err)
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

// The effort applyKiroThinking writes now survives translation: BuildKiroPayload
// moves it into additionalModelRequestFields, the field the model's own schema
// declares for it, while the Claude-shaped fields it came from are stripped.
//
// This replaces a tripwire that asserted the opposite -- that the effort was
// dropped and never reached the backend.
func TestKiroEffortReachesTheBackend(t *testing.T) {
	const body = `{"model":"kiro-claude-opus-5","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`

	applied, err := applyKiroThinking([]byte(body), "kiro-claude-opus-5(xhigh)", "claude")
	if err != nil {
		t.Fatalf("applyKiroThinking: %v", err)
	}

	payload, thinkingEnabled := kiroclaude.BuildKiroPayload(applied, "claude-opus-5", "arn:test", "AI_EDITOR", false, false, nil, nil)

	// Assert the payload was really built before reading fields out of it: an
	// empty or bailed-out payload would fail these checks for the wrong reason.
	if !gjson.GetBytes(payload, "conversationState.currentMessage").Exists() {
		t.Fatalf("payload was not built\npayload: %s", payload)
	}
	if !thinkingEnabled {
		t.Fatalf("adaptive thinking should register as enabled\npayload: %s", payload)
	}
	if got := gjson.GetBytes(payload, "additionalModelRequestFields.output_config.effort").String(); got != "xhigh" {
		t.Fatalf("forwarded effort = %q, want xhigh\npayload: %s", got, payload)
	}
	if got := gjson.GetBytes(payload, "additionalModelRequestFields.thinking.type").String(); got != "adaptive" {
		t.Fatalf("forwarded thinking.type = %q, want adaptive\npayload: %s", got, payload)
	}
	// Without display the backend suppresses reasoningContentEvent entirely, so
	// enabling thinking without it would be worse than not enabling it at all.
	if got := gjson.GetBytes(payload, "additionalModelRequestFields.thinking.display").String(); got != "summarized" {
		t.Fatalf("forwarded thinking.display = %q, want summarized\npayload: %s", got, payload)
	}
	// The Claude-shaped originals must still be stripped -- Kiro rejects them.
	if gjson.GetBytes(payload, "output_config").Exists() || gjson.GetBytes(payload, "thinking").Exists() {
		t.Fatalf("Claude-shaped thinking fields must not survive at the top level\npayload: %s", payload)
	}
}

// additionalModelRequestFields is validated against a per-model schema declared
// with additionalProperties:false, so attaching it to a model that does not
// declare the capability turns a working request into a 400. Two cases must stay
// clean: a model with no thinking support at all, and a supported model the client
// never asked to think.
func TestKiroAdditionalFieldsOmittedWhenNotApplicable(t *testing.T) {
	for _, tc := range []struct{ name, model, backendID, body string }{
		{
			name: "model declares no thinking support", model: "kiro-gpt-5-6-sol", backendID: "gpt-5-6-sol",
			body: `{"model":"kiro-gpt-5-6-sol","max_tokens":1024,"thinking":{"type":"enabled","budget_tokens":8000},"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "client asked for no thinking", model: "kiro-claude-opus-5", backendID: "claude-opus-5",
			body: `{"model":"kiro-claude-opus-5","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			applied, err := applyKiroThinking([]byte(tc.body), tc.model, "claude")
			if err != nil {
				t.Fatalf("applyKiroThinking: %v", err)
			}
			payload, _ := kiroclaude.BuildKiroPayload(applied, tc.backendID, "arn:test", "AI_EDITOR", false, false, nil, nil)
			if !gjson.GetBytes(payload, "conversationState.currentMessage").Exists() {
				t.Fatalf("payload was not built\npayload: %s", payload)
			}
			if gjson.GetBytes(payload, "additionalModelRequestFields").Exists() {
				t.Fatalf("additionalModelRequestFields must be absent\npayload: %s", payload)
			}
		})
	}
}

// A level the client named explicitly, via suffix, must be refused when the model
// does not offer it: the level reaches the backend now, so serving a different
// strength than the one that was typed is a silent substitution.
//
// The rejection surfaces as a ThinkingError, which is a 400 whose message carries
// none of the substrings isRequestInvalidError matches, so on this branch the
// conductor treats it as retryable and marks each credential it walks past as
// errored for that model. That is a conductor-level problem with a conductor-level
// fix already written on upstream-sync/batch2 (f52c679c routes 400s through
// internal/clienterror; 08a4ec02 exempts them inside MarkResult), which is why
// there is deliberately no local patch for it here.
func TestApplyKiroThinkingRejectsUnsupportedSuffixLevel(t *testing.T) {
	// claude-opus-4.6's enum has no xhigh.
	const body = `{"model":"kiro-claude-opus-4-6","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`
	out, err := applyKiroThinking([]byte(body), "kiro-claude-opus-4-6(xhigh)", "claude")
	if err == nil {
		t.Fatalf("xhigh on a model that does not list it must be rejected\nbody: %s", out)
	}
}

// Cross-format requests that used to pass through must keep working. Before this
// wiring existed, Kiro never validated thinking config at all; a generic OpenAI
// client sending a level Claude does not have must not start failing.
func TestApplyKiroThinkingKeepsCrossFormatRequestsAlive(t *testing.T) {
	for _, tc := range []struct{ name, model, body, format string }{
		{"openai minimal", "kiro-claude-opus-4-6", `{"model":"kiro-claude-opus-4-6","reasoning_effort":"minimal","messages":[{"role":"user","content":"hi"}]}`, "openai"},
		{"openai xhigh on 4.6", "kiro-claude-opus-4-6", `{"model":"kiro-claude-opus-4-6","reasoning_effort":"xhigh","messages":[{"role":"user","content":"hi"}]}`, "openai"},
		{"claude oversized budget", "kiro-claude-sonnet-4-5", `{"model":"kiro-claude-sonnet-4-5","max_tokens":1024,"thinking":{"type":"enabled","budget_tokens":64000},"messages":[{"role":"user","content":"hi"}]}`, "claude"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := applyKiroThinking([]byte(tc.body), tc.model, tc.format)
			if err != nil {
				t.Fatalf("applyKiroThinking: %v", err)
			}
			if !gjson.GetBytes(out, "messages").Exists() {
				t.Fatalf("request body was destroyed\nbody: %s", out)
			}
			// Surviving is the point, but it must not survive carrying a level the
			// model does not list -- passing through and forwarding something
			// unsupported would just move the rejection upstream.
			if effort := gjson.GetBytes(out, "output_config.effort").String(); effort != "" {
				info := registry.LookupModelInfo(tc.model, "kiro")
				if info == nil || info.Thinking == nil || !thinking.HasLevel(info.Thinking.Levels, effort) {
					t.Fatalf("effort %q is not a level %s declares\nbody: %s", effort, tc.model, out)
				}
			}
		})
	}
}

// A model whose registry entry declares no thinking support must be left exactly
// as the client sent it. ApplyThinking would strip the config here, silently and
// with no error the caller could react to -- and every amazonq-* and
// kiro-gpt-5-6-* entry routes through this executor in exactly that state.
func TestApplyKiroThinkingLeavesModelsWithoutThinkingSupportAlone(t *testing.T) {
	for _, model := range []string{"kiro-gpt-5-6-sol", "amazonq-claude-opus-4-8"} {
		t.Run(model, func(t *testing.T) {
			body := `{"model":"` + model + `","max_tokens":1024,"thinking":{"type":"enabled","budget_tokens":8000},"messages":[{"role":"user","content":"hi"}]}`
			out, err := applyKiroThinking([]byte(body), model, "claude")
			if err != nil {
				t.Fatalf("applyKiroThinking: %v", err)
			}
			if !gjson.GetBytes(out, "thinking").Exists() {
				t.Fatalf("thinking config was stripped for a model with no declared support\nbody: %s", out)
			}
		})
	}
}

func TestApplyKiroThinkingConvertsBudgetToLevel(t *testing.T) {
	// A Claude-style budget request must be reconciled into an effort level rather
	// than rejected or forwarded as a budget the backend does not accept. 30000 is
	// above ThresholdHigh (24576), so it maps to xhigh -- assert the level, not
	// merely that some level exists, or a wrong mapping passes unnoticed.
	const body = `{"model":"kiro-claude-opus-5","max_tokens":1024,"thinking":{"type":"enabled","budget_tokens":30000},"messages":[{"role":"user","content":"hi"}]}`
	out, err := applyKiroThinking([]byte(body), "kiro-claude-opus-5", "claude")
	if err != nil {
		t.Fatalf("applyKiroThinking: %v", err)
	}

	if effort := gjson.GetBytes(out, "output_config.effort").String(); effort != "xhigh" {
		t.Fatalf("output_config.effort = %q, want xhigh\nbody: %s", effort, out)
	}
	if gjson.GetBytes(out, "thinking.budget_tokens").Exists() {
		t.Fatalf("budget_tokens must not survive to a backend that takes no budget\nbody: %s", out)
	}
}

func TestApplyKiroThinkingLeavesUnspecifiedRequestsAlone(t *testing.T) {
	// With no thinking intent from the client we send no effort, leaving the
	// backend's own default in force.
	const body = `{"model":"kiro-claude-opus-5","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`
	out, err := applyKiroThinking([]byte(body), "kiro-claude-opus-5", "claude")
	if err != nil {
		t.Fatalf("applyKiroThinking: %v", err)
	}

	if gjson.GetBytes(out, "output_config.effort").Exists() {
		t.Fatalf("effort must be absent when the client did not ask for one\nbody: %s", out)
	}
}

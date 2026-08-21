package registry

import (
	"strings"
	"testing"
)

func TestGitHubCopilotGeminiModelsAreChatOnly(t *testing.T) {
	models := GetGitHubCopilotModels()
	required := map[string]bool{
		"gemini-2.5-pro":         false,
		"gemini-3-pro-preview":   false,
		"gemini-3.1-pro-preview": false,
		"gemini-3-flash-preview": false,
	}

	for _, model := range models {
		if _, ok := required[model.ID]; !ok {
			continue
		}
		required[model.ID] = true
		if len(model.SupportedEndpoints) != 1 || model.SupportedEndpoints[0] != "/chat/completions" {
			t.Fatalf("model %q supported endpoints = %v, want [/chat/completions]", model.ID, model.SupportedEndpoints)
		}
	}

	for modelID, found := range required {
		if !found {
			t.Fatalf("expected GitHub Copilot model %q in definitions", modelID)
		}
	}
}

func TestCodexStaticModelsIncludeGPT55(t *testing.T) {
	tierModels := map[string][]*ModelInfo{
		"free": GetCodexFreeModels(),
		"team": GetCodexTeamModels(),
		"plus": GetCodexPlusModels(),
		"pro":  GetCodexProModels(),
	}

	for tier, models := range tierModels {
		t.Run(tier, func(t *testing.T) {
			model := findModelInfo(models, "gpt-5.5")
			if model == nil {
				t.Fatalf("expected codex %s tier to include gpt-5.5", tier)
			}
			assertGPT55ModelInfo(t, tier, model)
		})
	}

	model := LookupStaticModelInfo("gpt-5.5")
	if model == nil {
		t.Fatal("expected LookupStaticModelInfo to find gpt-5.5")
	}
	assertGPT55ModelInfo(t, "lookup", model)
}

func TestKiroStaticModelsIncludeOpus48(t *testing.T) {
	model := findModelInfo(GetKiroModels(), "kiro-claude-opus-4-8")
	if model == nil {
		t.Fatal("expected Kiro models to include kiro-claude-opus-4-8")
	}
	assertKiroOpus48ModelInfo(t, model, "Kiro Claude Opus 4.8", "Claude Opus 4.8 via Kiro (2.2x credit)")

	agentic := findModelInfo(GetKiroModels(), "kiro-claude-opus-4-8-agentic")
	if agentic == nil {
		t.Fatal("expected Kiro models to include kiro-claude-opus-4-8-agentic")
	}
	assertKiroOpus48ModelInfo(t, agentic, "Kiro Claude Opus 4.8 (Agentic)", "Claude Opus 4.8 optimized for coding agents (chunked writes)")

	lookup := LookupStaticModelInfo("kiro-claude-opus-4-8")
	if lookup == nil {
		t.Fatal("expected LookupStaticModelInfo to find kiro-claude-opus-4-8")
	}
	assertKiroOpus48ModelInfo(t, lookup, "Kiro Claude Opus 4.8", "Claude Opus 4.8 via Kiro (2.2x credit)")
}

func TestAmazonQStaticModelsIncludeOpus48(t *testing.T) {
	model := findModelInfo(GetAmazonQModels(), "amazonq-claude-opus-4.8")
	if model == nil {
		t.Fatal("expected Amazon Q models to include amazonq-claude-opus-4.8")
	}
	assertAmazonQOpus48ModelInfo(t, model, "Amazon Q Claude Opus 4.8", "Claude Opus 4.8 via Amazon Q (2.2x credit)")

	hyphenModel := findModelInfo(GetAmazonQModels(), "amazonq-claude-opus-4-8")
	if hyphenModel == nil {
		t.Fatal("expected Amazon Q models to include amazonq-claude-opus-4-8")
	}
	assertAmazonQOpus48ModelInfo(t, hyphenModel, "Amazon Q Claude Opus 4.8", "Claude Opus 4.8 via Amazon Q (2.2x credit)")

	lookup := LookupStaticModelInfo("amazonq-claude-opus-4.8")
	if lookup == nil {
		t.Fatal("expected LookupStaticModelInfo to find amazonq-claude-opus-4.8")
	}
	assertAmazonQOpus48ModelInfo(t, lookup, "Amazon Q Claude Opus 4.8", "Claude Opus 4.8 via Amazon Q (2.2x credit)")

	hyphenLookup := LookupStaticModelInfo("amazonq-claude-opus-4-8")
	if hyphenLookup == nil {
		t.Fatal("expected LookupStaticModelInfo to find amazonq-claude-opus-4-8")
	}
	assertAmazonQOpus48ModelInfo(t, hyphenLookup, "Amazon Q Claude Opus 4.8", "Claude Opus 4.8 via Amazon Q (2.2x credit)")
}

func findModelInfo(models []*ModelInfo, id string) *ModelInfo {
	for _, model := range models {
		if model != nil && model.ID == id {
			return model
		}
	}
	return nil
}

// Every Claude entry in the Kiro table needs an -agentic twin. The static table is
// the entire catalogue whenever the dynamic fetch fails, so a missing entry means
// a model that executes (canonicalKiroModelName strips -agentic before routing)
// but is never advertised, and whose capabilities nothing describes.
//
// The scope is kiro-claude-* because that is where the table is currently
// complete. It is not complete elsewhere: the kiro-gpt-5-6-* entries have no
// -agentic twins, and minimax-m2-5 and glm-5 route with no entry at all.
func TestKiroClaudeModelsAllHaveAgenticVariants(t *testing.T) {
	models := GetKiroModels()
	for _, model := range models {
		if model == nil || !strings.HasPrefix(model.ID, "kiro-claude-") {
			continue
		}
		if strings.HasSuffix(model.ID, "-agentic") || strings.Contains(model.ID, "-auto") {
			continue
		}
		if findModelInfo(models, model.ID+"-agentic") == nil {
			t.Errorf("%s has no -agentic variant", model.ID)
		}
	}
}

// kiro-claude-opus-4-7 was added with the same 1M/128K/five-level shape as 4.8 but
// no coverage; a typo in those numbers, or the entry going missing, shipped silently.
func TestKiroStaticModelsIncludeOpus47(t *testing.T) {
	for _, tc := range []struct{ id, displayName string }{
		{"kiro-claude-opus-4-7", "Kiro Claude Opus 4.7"},
		{"kiro-claude-opus-4-7-agentic", "Kiro Claude Opus 4.7 (Agentic)"},
	} {
		model := findModelInfo(GetKiroModels(), tc.id)
		if model == nil {
			t.Fatalf("expected Kiro models to include %s", tc.id)
		}
		if model.DisplayName != tc.displayName {
			t.Errorf("%s display name: got %q, want %q", tc.id, model.DisplayName, tc.displayName)
		}
		if model.ContextLength != KiroModernContextLength {
			t.Errorf("%s context length: got %d, want %d", tc.id, model.ContextLength, KiroModernContextLength)
		}
		if model.MaxCompletionTokens != KiroModernMaxOutputLarge {
			t.Errorf("%s max completion tokens: got %d, want %d", tc.id, model.MaxCompletionTokens, KiroModernMaxOutputLarge)
		}
		if model.Thinking == nil || len(model.Thinking.Levels) != 5 {
			t.Fatalf("%s should declare the five-level effort set, got %+v", tc.id, model.Thinking)
		}
	}
}

// The Claude 5 pair uses effort levels, so their ThinkingSupport must carry Levels
// and no budget: a non-zero Min/Max would make detectModelCapability read them as
// budget-capable and send a budget the backend does not accept.
func TestKiroClaude5ModelsDeclareEffortLevelsNotBudgets(t *testing.T) {
	for _, id := range []string{
		"kiro-claude-opus-5", "kiro-claude-sonnet-5",
		"kiro-claude-opus-5-agentic", "kiro-claude-sonnet-5-agentic",
	} {
		model := findModelInfo(GetKiroModels(), id)
		if model == nil {
			t.Fatalf("expected Kiro models to include %s", id)
		}
		if model.Thinking == nil {
			t.Fatalf("%s: missing thinking support", id)
		}
		if model.Thinking.Min != 0 || model.Thinking.Max != 0 {
			t.Errorf("%s must not declare a token budget: %+v", id, model.Thinking)
		}
		if len(model.Thinking.Levels) == 0 {
			t.Errorf("%s must declare effort levels", id)
		}
	}
}

// KiroEffortThinking hands out copies. Sharing the package-level level slices
// would let one model's registration mutate every other model's levels.
func TestKiroEffortThinkingDoesNotAliasSharedLevels(t *testing.T) {
	first := KiroThinkingWithXHigh()
	first.Levels[0] = "mutated"
	if second := KiroThinkingWithXHigh(); second.Levels[0] != "low" {
		t.Fatalf("level slice is shared between callers: got %v", second.Levels)
	}
}

// assertKiroOpus48ModelInfo checks the Kiro entry against what ListAvailableModels
// on q.us-east-1.amazonaws.com reports for claude-opus-4.8: a 1M context, a 128K
// output ceiling, and effort levels rather than a thinking budget. These differ
// from the Amazon Q entry for the same Claude release, so the limits are asserted
// here rather than in the shared helper.
func assertKiroOpus48ModelInfo(t *testing.T, model *ModelInfo, displayName, description string) {
	t.Helper()

	assertAWSOpus48ModelInfo(t, model, displayName, description)
	if model.ContextLength != 1000000 {
		t.Fatalf("context length mismatch: got %d, want 1000000", model.ContextLength)
	}
	if model.MaxCompletionTokens != 128000 {
		t.Fatalf("max completion tokens mismatch: got %d, want 128000", model.MaxCompletionTokens)
	}
	if model.Thinking == nil {
		t.Fatal("missing thinking support")
	}
	if model.Thinking.Min != 0 || model.Thinking.Max != 0 {
		t.Fatalf("thinking must not declare a token budget; the backend accepts none: %+v", model.Thinking)
	}
	if !model.Thinking.ZeroAllowed || !model.Thinking.DynamicAllowed {
		t.Fatalf("thinking support mismatch: %+v", model.Thinking)
	}
	want := []string{"low", "medium", "high", "xhigh", "max"}
	if len(model.Thinking.Levels) != len(want) {
		t.Fatalf("effort levels mismatch: got %v, want %v", model.Thinking.Levels, want)
	}
	for i := range want {
		if model.Thinking.Levels[i] != want[i] {
			t.Fatalf("effort levels mismatch: got %v, want %v", model.Thinking.Levels, want)
		}
	}
}

func assertAmazonQOpus48ModelInfo(t *testing.T, model *ModelInfo, displayName, description string) {
	t.Helper()

	assertAWSOpus48ModelInfo(t, model, displayName, description)
	if model.ContextLength != 200000 {
		t.Fatalf("context length mismatch: got %d, want 200000", model.ContextLength)
	}
	if model.MaxCompletionTokens != 64000 {
		t.Fatalf("max completion tokens mismatch: got %d, want 64000", model.MaxCompletionTokens)
	}
}

func assertAWSOpus48ModelInfo(t *testing.T, model *ModelInfo, displayName, description string) {
	t.Helper()

	if model.Created != 1780012800 {
		t.Fatalf("created timestamp mismatch: got %d", model.Created)
	}
	if model.OwnedBy != "aws" {
		t.Fatalf("owned_by mismatch: got %q", model.OwnedBy)
	}
	if model.Type != "kiro" {
		t.Fatalf("type mismatch: got %q", model.Type)
	}
	if model.DisplayName != displayName {
		t.Fatalf("display name mismatch: got %q, want %q", model.DisplayName, displayName)
	}
	if model.Description != description {
		t.Fatalf("description mismatch: got %q, want %q", model.Description, description)
	}
}

func assertGPT55ModelInfo(t *testing.T, source string, model *ModelInfo) {
	t.Helper()

	if model.ID != "gpt-5.5" {
		t.Fatalf("%s id mismatch: got %q", source, model.ID)
	}
	if model.Object != "model" {
		t.Fatalf("%s object mismatch: got %q", source, model.Object)
	}
	if model.Created != 1776902400 {
		t.Fatalf("%s created timestamp mismatch: got %d", source, model.Created)
	}
	if model.OwnedBy != "openai" {
		t.Fatalf("%s owned_by mismatch: got %q", source, model.OwnedBy)
	}
	if model.Type != "openai" {
		t.Fatalf("%s type mismatch: got %q", source, model.Type)
	}
	if model.DisplayName != "GPT 5.5" {
		t.Fatalf("%s display name mismatch: got %q", source, model.DisplayName)
	}
	if model.Version != "gpt-5.5" {
		t.Fatalf("%s version mismatch: got %q", source, model.Version)
	}
	if model.Description != "Frontier model for complex coding, research, and real-world work." {
		t.Fatalf("%s description mismatch: got %q", source, model.Description)
	}
	if model.ContextLength != 272000 {
		t.Fatalf("%s context length mismatch: got %d", source, model.ContextLength)
	}
	if model.MaxCompletionTokens != 128000 {
		t.Fatalf("%s max completion tokens mismatch: got %d", source, model.MaxCompletionTokens)
	}
	if len(model.SupportedParameters) != 1 || model.SupportedParameters[0] != "tools" {
		t.Fatalf("%s supported parameters mismatch: got %v", source, model.SupportedParameters)
	}
	if model.Thinking == nil {
		t.Fatalf("%s missing thinking support", source)
	}

	want := []string{"low", "medium", "high", "xhigh"}
	if len(model.Thinking.Levels) != len(want) {
		t.Fatalf("%s thinking level count mismatch: got %d, want %d", source, len(model.Thinking.Levels), len(want))
	}
	for i, level := range want {
		if model.Thinking.Levels[i] != level {
			t.Fatalf("%s thinking level %d mismatch: got %q, want %q", source, i, model.Thinking.Levels[i], level)
		}
	}
}

// The dynamic converter and the static table are two descriptions of the same
// models, and only one wins per model at runtime (MergeWithStaticMetadata
// restores static metadata for opus-4-8 alone). A disagreement decides whether
// /v1/models advertises thinking and whether ApplyThinking normalises a client's
// config or strips it, so pin that they cannot drift.
//
// The gpt-5-6 entries are the ones that matter here: they are the only Kiro
// models the static table gives no thinking support at all, so a blanket default
// in the converter would invent support for exactly them.
func TestKiroDynamicThinkingMatchesStaticTable(t *testing.T) {
	for _, static := range GetKiroModels() {
		if static == nil {
			continue
		}
		t.Run(static.ID, func(t *testing.T) {
			// No effort levels: the backend said nothing, so the static table decides.
			got := KiroThinkingForModel(static.ID, nil)

			if (got == nil) != (static.Thinking == nil) {
				t.Fatalf("thinking presence differs: dynamic=%+v static=%+v", got, static.Thinking)
			}
			if got == nil {
				return
			}
			if got.Min != static.Thinking.Min || got.Max != static.Thinking.Max ||
				got.ZeroAllowed != static.Thinking.ZeroAllowed ||
				got.DynamicAllowed != static.Thinking.DynamicAllowed ||
				len(got.Levels) != len(static.Thinking.Levels) {
				t.Fatalf("thinking differs: dynamic=%+v static=%+v", got, static.Thinking)
			}
			for i := range got.Levels {
				if got.Levels[i] != static.Thinking.Levels[i] {
					t.Fatalf("levels differ: dynamic=%v static=%v", got.Levels, static.Thinking.Levels)
				}
			}
		})
	}
}

// A model the static table does not describe still needs sensible metadata, and
// the backend's own enum must win over both.
func TestKiroThinkingForModelFallbacks(t *testing.T) {
	// A model neither the backend nor the catalogue describes gets nil, not a
	// fabricated budget: applyKiroThinking reads that as "leave the client's
	// config alone", which is the only honest answer for a model we know nothing
	// about.
	if unknown := KiroThinkingForModel("kiro-brand-new-model", nil); unknown != nil {
		t.Fatalf("an undescribed model must not be asserted thinking-capable, got %+v", unknown)
	}

	declared := KiroThinkingForModel("kiro-gpt-5-6-sol", []string{"low", "high"})
	if declared == nil || len(declared.Levels) != 2 {
		t.Fatalf("a backend-declared enum must win over the static table, got %+v", declared)
	}

	// Mutating a returned value must not be visible to the next caller. This holds
	// because LookupStaticModelInfo already deep-copies and GetKiroModels rebuilds
	// from literals, so it is a guard against either of those changing rather than
	// against the current code.
	first := KiroThinkingForModel("kiro-claude-sonnet-4-5", nil)
	if first == nil {
		t.Fatal("expected thinking support for kiro-claude-sonnet-4-5")
	}
	first.Max = 1
	first.Levels = append(first.Levels, "injected")
	second := KiroThinkingForModel("kiro-claude-sonnet-4-5", nil)
	if second.Max == 1 {
		t.Fatal("KiroThinkingForModel leaks Max mutations to later callers")
	}
	for _, level := range second.Levels {
		if level == "injected" {
			t.Fatal("KiroThinkingForModel leaks Levels mutations to later callers")
		}
	}
}

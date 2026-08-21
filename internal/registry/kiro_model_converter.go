// Package registry provides Kiro model conversion utilities.
// This file handles converting dynamic Kiro API model lists to the internal ModelInfo format,
// and merging with static metadata for thinking support and other capabilities.
package registry

// DefaultKiroThinkingSupport defines the default thinking configuration for Kiro models.
// All Kiro models support thinking with the following budget range.
var DefaultKiroThinkingSupport = &ThinkingSupport{
	Min:            1024,  // Minimum thinking budget tokens
	Max:            32000, // Maximum thinking budget tokens
	ZeroAllowed:    true,  // Allow disabling thinking with 0
	DynamicAllowed: true,  // Allow dynamic thinking budget (-1)
}

// Capability numbers for the Claude 4.6-and-newer generation on Kiro, as
// reported by ListAvailableModels on the Kiro backend. Read them yourself with:
//
//	go run ./cmd/kiroprobe -token <auth file> -model claude
//
// Recorded 2026-08-20. The Default* values above describe the Claude 4.5-era
// backend and understate this generation on every axis, which is why these exist
// separately rather than as new defaults. Re-probe when a new model lands: the
// backend is the only authority for these, and a guess here silently misreports
// every client's context and output budget.
const (
	// KiroModernContextLength is the context window Claude 4.6+ reports on Kiro.
	KiroModernContextLength = 1000000
	// KiroModernMaxOutputLarge is the output ceiling for the Opus 4.7/4.8/5 tier.
	KiroModernMaxOutputLarge = 128000
	// KiroModernMaxOutputStandard is the output ceiling for Sonnet and Opus 4.6.
	KiroModernMaxOutputStandard = 64000
)

// Effort levels the backend accepts, from each model's
// additionalModelRequestFieldsSchema. Claude 4.7 and newer added "xhigh"
// between "high" and "max"; 4.6 has no such level and rejects it.
var (
	kiroEffortLevelsWithXHigh = []string{"low", "medium", "high", "xhigh", "max"}
	kiroEffortLevels46        = []string{"low", "medium", "high", "max"}
)

// KiroEffortThinking builds a ThinkingSupport for a Kiro model whose strength is
// a discrete effort level rather than a token budget. It copies the level slice
// so callers cannot alias the package-level defaults above, and deliberately
// leaves Min/Max at zero: the backend accepts no budget, and a non-zero range
// here would make detectModelCapability read the model as budget-capable.
func KiroEffortThinking(levels []string) *ThinkingSupport {
	return &ThinkingSupport{
		ZeroAllowed:    true,
		DynamicAllowed: true,
		Levels:         append([]string(nil), levels...),
	}
}

// KiroThinkingWithXHigh returns the effort support shared by Claude 4.7, 4.8 and 5.
func KiroThinkingWithXHigh() *ThinkingSupport { return KiroEffortThinking(kiroEffortLevelsWithXHigh) }

// KiroThinking46 returns the effort support for the Claude 4.6 pair.
func KiroThinking46() *ThinkingSupport { return KiroEffortThinking(kiroEffortLevels46) }

// KiroThinkingForModel decides the ThinkingSupport a dynamically discovered Kiro
// model should carry, given the effort levels the backend declared for it (empty
// if it declared none).
//
// The whole point is that the dynamic path and the static table cannot disagree.
// They are two descriptions of the same models, and only one of them wins per
// model at runtime -- MergeWithStaticMetadata restores static metadata for
// opus-4-8 alone, so for everything else whatever this returns is what the
// registry believes. A disagreement is not cosmetic: it decides whether
// /v1/models advertises thinking, and whether ApplyThinking normalises a client's
// config or strips it.
//
// So the order is: the backend's own effort enum first, then whatever the static
// table says for this exact model (including nothing, which is the honest answer
// for the gpt-5-6 entries), and the Claude 4.5-era default only for a model no
// static entry describes. TestKiroDynamicThinkingMatchesStaticTable pins it.
func KiroThinkingForModel(modelID string, effortLevels []string) *ThinkingSupport {
	if len(effortLevels) > 0 {
		return KiroEffortThinking(effortLevels)
	}
	if static := LookupStaticModelInfo(modelID); static != nil {
		// LookupStaticModelInfo already returns a clone, so static.Thinking is ours
		// to hand back directly. Nil is a real answer here, not a missing one: the
		// gpt-5-6 entries declare no thinking, and inventing some for them is the
		// bug this function exists to prevent.
		return static.Thinking
	}
	// A model neither the backend nor the catalogue describes. Nil says exactly
	// that, and applyKiroThinking's guard turns it into "leave the client's config
	// alone". The alternative -- asserting the Claude 4.5-era 1024-32000 budget --
	// would be a guess about a model we have never seen, and would make us rewrite
	// requests into a budget shape for a backend that may well take effort levels.
	return nil
}

// DefaultKiroContextLength is the default context window size for Kiro models.
const DefaultKiroContextLength = 200000

// DefaultKiroMaxCompletionTokens is the default max completion tokens for Kiro models.
const DefaultKiroMaxCompletionTokens = 64000

// MergeWithStaticMetadata merges dynamic models with static metadata.
// Static metadata takes priority for any overlapping fields.
// This allows manual overrides for specific models while keeping dynamic discovery.
//
// Parameters:
//   - dynamicModels: Models from Kiro API (converted to ModelInfo)
//   - staticModels: Predefined model metadata (from GetKiroModels())
//
// Returns:
//   - []*ModelInfo: Merged model list with static metadata taking priority
func MergeWithStaticMetadata(dynamicModels, staticModels []*ModelInfo) []*ModelInfo {
	if len(dynamicModels) == 0 && len(staticModels) == 0 {
		return nil
	}

	// Build a map of static models for quick lookup
	staticMap := make(map[string]*ModelInfo, len(staticModels))
	for _, sm := range staticModels {
		if sm != nil && sm.ID != "" {
			staticMap[sm.ID] = sm
		}
	}

	// Build result, preferring static metadata where available
	seenIDs := make(map[string]struct{})
	result := make([]*ModelInfo, 0, len(dynamicModels)+len(staticModels))

	// First, process dynamic models and merge with static if available
	for _, dm := range dynamicModels {
		if dm == nil || dm.ID == "" {
			continue
		}

		// Skip duplicates
		if _, seen := seenIDs[dm.ID]; seen {
			continue
		}
		seenIDs[dm.ID] = struct{}{}

		// Check if static metadata exists for this model
		if sm, exists := staticMap[dm.ID]; exists {
			// Static metadata takes priority - use static model
			result = append(result, sm)
		} else {
			// No static metadata - use dynamic model
			result = append(result, dm)
		}
	}

	// Add any static models not in dynamic list
	for _, sm := range staticModels {
		if sm == nil || sm.ID == "" {
			continue
		}
		if _, seen := seenIDs[sm.ID]; seen {
			continue
		}
		seenIDs[sm.ID] = struct{}{}
		result = append(result, sm)
	}

	return result
}

// cloneThinkingSupport creates a deep copy of ThinkingSupport.
// Returns nil if input is nil.
func cloneThinkingSupport(ts *ThinkingSupport) *ThinkingSupport {
	if ts == nil {
		return nil
	}

	clone := &ThinkingSupport{
		Min:            ts.Min,
		Max:            ts.Max,
		ZeroAllowed:    ts.ZeroAllowed,
		DynamicAllowed: ts.DynamicAllowed,
	}

	// Deep copy Levels slice if present
	if len(ts.Levels) > 0 {
		clone.Levels = make([]string, len(ts.Levels))
		copy(clone.Levels, ts.Levels)
	}

	return clone
}

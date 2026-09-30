package registry

import "testing"

// mapModelToKiro hands the backend spelling to the payload builder, and the
// backend spells versions with dots while the catalogue spells them with dashes.
// Without folding, effort forwarding silently does nothing for every model whose
// backend id carries a dot -- opus-4.7 and opus-4.8 among them.
func TestLookupKiroModelInfoFoldsBackendSpelling(t *testing.T) {
	for _, name := range []string{
		"claude-opus-4.8", "claude-opus-4.7", "claude-opus-4.6",
		"kiro-claude-opus-4-8", "KIRO-CLAUDE-OPUS-4-8",
		"claude-opus-5", "kiro-claude-opus-5-agentic", "amazonq-claude-opus-4-8",
		"claude-opus-5.5", "kiro-claude-opus-5-5-agentic",
		"claude-sonnet-5.5", "amazonq-claude-sonnet-5-5", "kiro-claude-sonnet-5-5-agentic",
	} {
		t.Run(name, func(t *testing.T) {
			info := LookupKiroModelInfo(name)
			if info == nil {
				t.Fatalf("LookupKiroModelInfo(%q) = nil, want a catalogue entry", name)
			}
			if !isKiroCatalogueID(info.ID) {
				t.Fatalf("LookupKiroModelInfo(%q) resolved to %q, which is not a Kiro entry", name, info.ID)
			}
		})
	}
}

// LookupModelInfo's static fallback ignores its provider argument, so a bare name
// can match a same-named model from another provider whose effort levels are not
// the ones Kiro offers. Validating a Kiro request against those would enforce the
// wrong enum, so only Kiro-prefixed entries may be returned.
func TestLookupKiroModelInfoRejectsOtherProviders(t *testing.T) {
	// The Kiro backend offers four levels on opus-4.6 (low/medium/high/max);
	// the same-named entry on another provider does not agree.
	info := LookupKiroModelInfo("claude-opus-4.6")
	if info == nil || info.Thinking == nil {
		t.Fatalf("claude-opus-4.6 should resolve with thinking metadata, got %v", info)
	}
	if got := len(info.Thinking.Levels); got != 4 {
		t.Fatalf("claude-opus-4.6 levels = %d (%v), want 4 -- a different count means a "+
			"non-Kiro entry was matched", got, info.Thinking.Levels)
	}

	// A model that exists elsewhere but has no Kiro entry must resolve to nothing
	// rather than borrowing the other provider's capabilities.
	if info = LookupKiroModelInfo("gemini-3-pro"); info != nil {
		t.Fatalf("gemini-3-pro resolved to Kiro entry %q, want nil", info.ID)
	}
}

func TestLookupKiroModelInfoRejectsEmpty(t *testing.T) {
	for _, name := range []string{"", "   ", "kiro-", "amazonq-", "-agentic"} {
		if info := LookupKiroModelInfo(name); info != nil {
			t.Errorf("LookupKiroModelInfo(%q) = %q, want nil", name, info.ID)
		}
	}
}

// A model awaiting the backend must be described but not advertised. Lookups
// that miss the backend's registration -- the executor's, and ApplyThinking's
// by the resolved id -- must find its capabilities rather than the 200K/32K
// defaults, while the lists served as the static fallback must leave it out.
func TestKiroModelsAwaitingBackendAreDescribedNotAdvertised(t *testing.T) {
	for _, m := range kiroModelsAwaitingBackend() {
		for _, list := range [][]*ModelInfo{GetKiroModels(), GetStaticModelDefinitionsByChannel("kiro")} {
			for _, advertised := range list {
				if advertised.ID == m.ID {
					t.Errorf("%s is advertised before the backend serves it", m.ID)
				}
			}
		}
		for _, info := range []*ModelInfo{LookupKiroModelInfo(m.ID), LookupModelInfo(m.ID, "kiro")} {
			if info == nil || info.ID != m.ID || info.ContextLength != KiroModernContextLength || info.Thinking == nil {
				t.Errorf("lookup of %s = %+v, want its own entry with a 1M window and thinking", m.ID, info)
			}
		}
	}
}

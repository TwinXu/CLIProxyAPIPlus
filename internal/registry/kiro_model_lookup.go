package registry

import "strings"

// LookupKiroModelInfo resolves a Kiro model name -- as a client spelled it, or as
// it was mapped for the backend -- to its Kiro catalogue entry.
//
// The name is folded the same way the executor's canonicalKiroModelName folds it
// for routing, so a name that reaches the backend also finds its capabilities:
// case is irrelevant, -agentic/-chat select a request shape rather than a
// different model, either vendor prefix addresses the same API, and dots collapse
// to dashes. That last one matters most for a backend id: mapModelToKiro emits the
// dotted spelling ("claude-opus-4.8") while the catalogue stores the dashed one
// ("kiro-claude-opus-4-8"), and without folding, nine of the models this executor
// routes would silently resolve to nothing.
//
// Only an entry that carries a Kiro prefix is accepted. LookupModelInfo's static
// fallback ignores the provider argument, so a bare lookup can return a
// same-named model belonging to another provider -- "claude-opus-4.5" matches an
// Anthropic entry whose effort levels are not the ones Kiro offers. Validating a
// request against those would mean enforcing the wrong enum.
func LookupKiroModelInfo(name string) *ModelInfo {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.TrimSuffix(name, "-agentic")
	name = strings.TrimSuffix(name, "-chat")
	name = strings.TrimPrefix(name, "kiro-")
	name = strings.TrimPrefix(name, "amazonq-")
	name = strings.ReplaceAll(name, ".", "-")
	if name == "" {
		return nil
	}
	// amazonq-* entries are stored under their own prefix and describe the same
	// backend model with a different quota, so both spellings are tried.
	for _, candidate := range []string{"kiro-" + name, "amazonq-" + name} {
		if info := LookupModelInfo(candidate, "kiro"); info != nil && isKiroCatalogueID(info.ID) {
			return info
		}
	}
	return nil
}

// isKiroCatalogueID reports whether an entry belongs to the Kiro catalogue.
func isKiroCatalogueID(id string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	return strings.HasPrefix(id, "kiro-") || strings.HasPrefix(id, "amazonq-")
}

package cliproxy

import (
	"slices"
	"testing"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

// claude-sonnet-5-5 was wired up on 2026-09-30, before the backend served it,
// and deliberately is not in GetKiroModels. That is only safe if nothing
// advertises it until ListAvailableModels lists claude-sonnet-5.5 -- not the
// live list, not the static fallback, not the default alias -- and if, once it
// is listed, it arrives under both ids with the backend's own numbers.
func TestKiroSonnet55AppearsOnlyWhenTheBackendListsIt(t *testing.T) {
	cfg := &config.Config{}
	cfg.SanitizeOAuthModelAlias() // injects the default Kiro aliases

	withAliases := func(models []*ModelInfo) map[string]*ModelInfo {
		byID := map[string]*ModelInfo{}
		for _, m := range applyOAuthModelAlias(cfg, "kiro", "oauth", models) {
			byID[m.ID] = m
		}
		return byID
	}
	fromBackend := func(apiModels ...*kiroauth.KiroModel) map[string]*ModelInfo {
		return withAliases(mergeKiroDynamicWithStaticModels(generateKiroAgenticVariants(convertKiroAPIModels(apiModels))))
	}
	sonnet5 := &kiroauth.KiroModel{ModelID: "claude-sonnet-5", ModelName: "Claude Sonnet 5", MaxInputTokens: 1000000, MaxOutputTokens: 64000}
	ids := []string{"kiro-claude-sonnet-5-5", "kiro-claude-sonnet-5-5-agentic", "claude-sonnet-5-5"}

	for source, byID := range map[string]map[string]*ModelInfo{
		"backend without it": fromBackend(sonnet5),
		"static fallback":    withAliases(registry.GetKiroModels()),
	} {
		for _, id := range ids {
			if byID[id] != nil {
				t.Errorf("%s: %s is advertised, but the backend does not serve it", source, id)
			}
		}
	}

	// A subset of levels, so a match can only have come from the backend.
	levels := []string{"low", "medium", "high"}
	byID := fromBackend(sonnet5, &kiroauth.KiroModel{
		ModelID: "claude-sonnet-5.5", ModelName: "Claude Sonnet 5.5",
		MaxInputTokens: 1000000, MaxOutputTokens: 128000, EffortLevels: levels,
	})
	for _, id := range ids {
		m := byID[id]
		if m == nil {
			t.Errorf("%s is not advertised once the backend lists claude-sonnet-5.5", id)
			continue
		}
		if m.ContextLength != 1000000 || m.MaxCompletionTokens != 128000 {
			t.Errorf("%s limits = %d/%d, want the backend's 1000000/128000", id, m.ContextLength, m.MaxCompletionTokens)
		}
		if m.Thinking == nil || !slices.Equal(m.Thinking.Levels, levels) {
			t.Errorf("%s thinking = %+v, want the backend's levels %v", id, m.Thinking, levels)
		}
	}
}

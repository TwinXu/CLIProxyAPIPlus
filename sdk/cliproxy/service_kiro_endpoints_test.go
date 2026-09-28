package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

// A Kiro model that declares no endpoints leaves /v1/responses on its raw path,
// and the Kiro executor has no Responses translator: the body used to reach the
// Claude builder untranslated and the user's input was replaced by a placeholder.
// Every registered Kiro model -- including the alias clones clients actually
// address, such as claude-opus-5-5 -- must declare chat-only so the Responses
// handler bridges it through Chat Completions.
func TestRegisterModelsForAuth_KiroModelsDeclareChatOnly(t *testing.T) {
	cfg := &config.Config{}
	cfg.SanitizeOAuthModelAlias() // injects the default Kiro aliases
	service := &Service{cfg: cfg}
	// No access token, so fetchKiroModels serves the static catalogue without a
	// network call.
	auth := &coreauth.Auth{ID: "kiro-endpoints-test.json", Provider: "kiro", Status: coreauth.StatusActive}

	reg := registry.GetGlobalRegistry()
	reg.UnregisterClient(auth.ID)
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	service.registerModelsForAuth(auth)
	models := reg.GetModelsForClient(auth.ID)
	if len(models) == 0 {
		t.Fatal("expected Kiro models to be registered")
	}
	seen := map[string]bool{}
	for _, model := range models {
		seen[model.ID] = true
		if len(model.SupportedEndpoints) != 1 || model.SupportedEndpoints[0] != "/chat/completions" {
			t.Errorf("%s SupportedEndpoints = %v, want [/chat/completions]", model.ID, model.SupportedEndpoints)
		}
	}
	for _, id := range []string{"kiro-claude-opus-5-5", "claude-opus-5-5", "kiro-claude-opus-5", "claude-opus-5"} {
		if !seen[id] {
			t.Errorf("expected %s to be registered", id)
		}
	}
}

// Declaring chat-only must not overwrite a model that already states its own
// endpoints.
func TestMarkKiroModelsChatOnlyKeepsDeclaredEndpoints(t *testing.T) {
	declared := &ModelInfo{ID: "declared", SupportedEndpoints: []string{"/responses"}}
	out := markKiroModelsChatOnly([]*ModelInfo{declared, nil, {ID: "bare"}})
	if got := out[0].SupportedEndpoints; len(got) != 1 || got[0] != "/responses" {
		t.Fatalf("declared endpoints overwritten: %v", got)
	}
	if got := out[2].SupportedEndpoints; len(got) != 1 || got[0] != "/chat/completions" {
		t.Fatalf("bare model endpoints = %v, want [/chat/completions]", got)
	}
}

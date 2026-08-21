// Command kiroprobe prints what the Kiro (AWS CodeWhisperer) backend reports for
// the models it serves.
//
// The registry's Kiro capability numbers -- DefaultKiroContextLength,
// DefaultKiroMaxCompletionTokens and DefaultKiroThinkingSupport -- were all
// introduced together in January 2026 and applied unchanged to every model added
// since, with nothing in the tree recording where they came from. The
// KiroModern* constants that now cover Claude 4.6 and newer came from this tool;
// re-run it rather than guessing when a new model lands.
//
// This asks the backend directly. Point it at a Kiro auth file:
//
//	go run ./cmd/kiroprobe -token ~/.aws/sso/cache/kiro-auth-token.json
//	go run ./cmd/kiroprobe -token auths/kiro-<account>.json -raw
//
// Without -raw it prints one line per model: what the backend sent, plus what our
// own parser extracted from it, so a disagreement between the two is visible.
// With -raw it prints the whole response, which is the only way to see fields
// neither the parser nor the summary models yet.
//
// It is also the way to answer the open question in applyKiroThinking: nothing in
// this tree knows the wire format for sending an effort level to Kiro, so
// output_config.effort is currently dropped at translation. The schema this tool
// prints (additionalModelRequestFieldsSchema) is the backend describing the field
// it expects; capturing a real Kiro IDE request is what would confirm where it
// belongs in the payload.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func main() {
	tokenFile := flag.String("token", "", "path to a Kiro auth/token JSON file (required)")
	raw := flag.Bool("raw", false, "print the unparsed response body instead of a summary")
	filter := flag.String("model", "", "only show models whose id contains this substring")
	timeout := flag.Duration("timeout", 30*time.Second, "request timeout")
	noRefresh := flag.Bool("no-refresh", false, "do not refresh an expired token; send it as-is")
	flag.Parse()

	if *tokenFile == "" {
		fmt.Fprintln(os.Stderr, "-token is required")
		flag.Usage()
		os.Exit(2)
	}

	cfg := &config.Config{}
	auth := kiro.NewKiroAuth(cfg)
	tokenData, err := loadToken(*tokenFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load token: %v\n", err)
		os.Exit(1)
	}

	// Warn before the request, for whichever layout the token came from, and via
	// the same check the proxy uses -- IsTokenExpired also understands the
	// non-RFC3339 timestamp the IDE sometimes writes. Otherwise a stale token
	// surfaces as an opaque "status 400" about an invalid token.
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	// Guarded on a non-empty ExpiresAt: IsTokenExpired treats "no expiry recorded"
	// as expired, which would false-alarm on any credential that carries none --
	// noise on exactly the path this warning exists to make clearer.
	if strings.TrimSpace(tokenData.ExpiresAt) != "" && auth.IsTokenExpired(tokenData) {
		if *noRefresh {
			fmt.Fprintf(os.Stderr, "warning: token expired at %s and -no-refresh is set; the backend will reject this\n", tokenData.ExpiresAt)
		} else {
			fmt.Fprintf(os.Stderr, "note: token expired at %s, refreshing in memory\n", tokenData.ExpiresAt)
			refreshed, errRefresh := refreshToken(ctx, cfg, tokenData)
			if errRefresh != nil {
				fmt.Fprintf(os.Stderr, "refresh failed: %v\n(re-authenticate, or pass -no-refresh to send the stale token anyway)\n", errRefresh)
				os.Exit(1)
			}
			tokenData = refreshed
			fmt.Fprintf(os.Stderr, "note: refreshed, now valid until %s (the credential file was not modified)\n", tokenData.ExpiresAt)
		}
	}

	body, err := auth.ListAvailableModelsRaw(ctx, tokenData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ListAvailableModels: %v\n", err)
		os.Exit(1)
	}

	if *raw {
		var pretty any
		if json.Unmarshal(body, &pretty) == nil {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(pretty)
			return
		}
		os.Stdout.Write(body)
		return
	}

	var parsed struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		fmt.Fprintf(os.Stderr, "parse response: %v\n(use -raw to see the body)\n", err)
		os.Exit(1)
	}

	// Also run the body through the real parser and show what it extracted. The
	// raw dump says what the backend sent; this says what the registry will
	// actually believe, and the two disagreeing is the bug this tool exists to
	// catch. A failure here is not fatal -- the raw summary is still useful.
	extracted := make(map[string]*kiro.KiroModel)
	if models, errParse := kiro.ParseAvailableModels(body); errParse != nil {
		fmt.Fprintf(os.Stderr, "note: the parsed path failed (%v); showing raw fields only\n", errParse)
	} else {
		for _, m := range models {
			extracted[m.ModelID] = m
		}
	}

	for _, m := range parsed.Models {
		id, _ := m["modelId"].(string)
		if *filter != "" && !strings.Contains(id, *filter) {
			continue
		}
		fmt.Printf("%s\n", id)
		// tokenLimits is where the backend states the numbers the registry guesses at.
		if limits, ok := m["tokenLimits"].(map[string]any); ok {
			for _, k := range sortedKeys(limits) {
				fmt.Printf("    tokenLimits.%-24s %v\n", k, limits[k])
			}
		} else {
			fmt.Printf("    tokenLimits              <absent>\n")
		}
		// Anything else the backend sends that we do not model yet.
		for _, k := range sortedKeys(m) {
			switch k {
			case "modelId", "tokenLimits", "modelName", "description":
			default:
				fmt.Printf("    %-33s %v\n", k, m[k])
			}
		}
		// What the registry will actually believe about this model.
		if got, ok := extracted[id]; ok {
			levels := "<none declared>"
			if len(got.EffortLevels) > 0 {
				levels = strings.Join(got.EffortLevels, ", ")
			}
			fmt.Printf("    parsed.effortLevels               %s\n", levels)
			if got.DefaultEffort != "" {
				// The level the backend applies when a request sends none. We
				// deliberately send none, so this is what a plain request gets.
				fmt.Printf("    parsed.defaultEffort              %s\n", got.DefaultEffort)
			}
			fmt.Printf("    parsed.maxInput/maxOutput         %d / %d\n", got.MaxInputTokens, got.MaxOutputTokens)
		}
		fmt.Println()
	}
}

// refreshToken exchanges a stale token for a fresh one, in memory only -- the
// credential file on disk is never rewritten. A probe that dies on a day-old
// token is not much of a probe, and the proxy refreshes automatically, so doing
// the same here keeps the tool on its own rule: issue the request production
// issues.
//
// The three-way dispatch mirrors refreshTokenData in oauth_web.go (which in turn
// mirrors KiroExecutor.Refresh). Kept in step deliberately rather than
// simplified: which endpoint refreshes a credential is a property of how it was
// issued, and guessing wrong fails in a way that looks like a dead token.
func refreshToken(ctx context.Context, cfg *config.Config, token *kiro.KiroTokenData) (*kiro.KiroTokenData, error) {
	var (
		refreshed *kiro.KiroTokenData
		err       error
	)
	switch {
	case token.ClientID != "" && token.ClientSecret != "" && token.AuthMethod == "idc" && token.Region != "":
		refreshed, err = kiro.NewSSOOIDCClient(cfg).RefreshTokenWithRegion(ctx, token.ClientID, token.ClientSecret, token.RefreshToken, token.Region, token.StartURL)
	case token.ClientID != "" && token.ClientSecret != "" && token.AuthMethod == "builder-id":
		refreshed, err = kiro.NewSSOOIDCClient(cfg).RefreshToken(ctx, token.ClientID, token.ClientSecret, token.RefreshToken)
	default:
		refreshed, err = kiro.NewKiroOAuth(cfg).RefreshToken(ctx, token.RefreshToken)
	}
	if err != nil {
		return nil, err
	}
	if refreshed == nil {
		return nil, fmt.Errorf("refresh returned no token")
	}
	// The refresh response carries credentials, not the profile binding, so keep
	// the one we loaded -- ListAvailableModels is scoped by it and fails without.
	if strings.TrimSpace(refreshed.ProfileArn) == "" {
		refreshed.ProfileArn = token.ProfileArn
	}
	return refreshed, nil
}

// loadToken accepts both Kiro credential layouts by delegating to the loaders the
// proxy itself uses. Kiro IDE's own kiro-auth-token.json is camelCase and is what
// LoadKiroTokenFromPath reads -- along with the ~ expansion, the AuthMethod
// normalisation ("IdC" -> "idc") and the IDC device-registration lookup that an
// Enterprise token needs. The files this proxy writes under auths/ are snake_case
// and belong to LoadFromFile.
//
// Both layouts unmarshal cleanly into each other's struct and silently yield
// empty fields, which reaches the backend as an empty bearer token and returns a
// 400 about an invalid token. So the choice cannot be made on which one parses --
// it is made on which one actually carries an access token. LoadKiroTokenFromPath
// enforces that itself; LoadFromFile does not, only reading and unmarshalling, so
// the explicit check below is doing that work and must not be removed.
func loadToken(path string) (*kiro.KiroTokenData, error) {
	path, err := expandHome(path)
	if err != nil {
		return nil, err
	}

	camel, camelErr := kiro.LoadKiroTokenFromPath(path)
	if camelErr == nil && camel != nil && strings.TrimSpace(camel.AccessToken) != "" {
		return camel, nil
	}

	storage, snakeErr := kiro.LoadFromFile(path)
	if snakeErr != nil {
		return nil, fmt.Errorf("recognised neither credential layout in %s (camelCase: %v; snake_case: %v)", path, camelErr, snakeErr)
	}
	if strings.TrimSpace(storage.AccessToken) == "" {
		return nil, fmt.Errorf("no access token found in %s (recognised neither the camelCase IDE layout nor the snake_case auths/ layout)", path)
	}
	return storage.ToTokenData(), nil
}

// expandHome expands a leading ~ once, up front, so the loaders below are handed
// an already-absolute path and their own tilde handling never runs.
//
// "~other/x" is rejected rather than passed along. It names another user's home,
// and LoadKiroTokenFromPath would expand it as $HOME + "other/x" -- which, if
// that path happens to exist, means silently probing with different credentials
// than the flag names.
func expandHome(path string) (string, error) {
	if !strings.HasPrefix(path, "~") {
		return path, nil
	}
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return "", fmt.Errorf("cannot expand %q: another user's home directory is not supported", path)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot expand %q: %w", path, err)
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~")), nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

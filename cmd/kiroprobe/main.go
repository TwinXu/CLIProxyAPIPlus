// Command kiroprobe prints what the Kiro (AWS CodeWhisperer) backend reports for
// the models it serves.
//
// The registry's Kiro capability numbers -- DefaultKiroContextLength,
// DefaultKiroMaxCompletionTokens and DefaultKiroThinkingSupport -- were all
// introduced together in January 2026 and have been applied unchanged to every
// model added since, including claude-opus-5 and claude-sonnet-5. Nothing in the
// tree records where they came from, and ConvertKiroAPIModels, the one path that
// would have taken maxInputTokens from the backend, has no callers.
//
// This asks the backend directly. Point it at a Kiro auth file:
//
//	go run ./cmd/kiroprobe -token ~/.aws/sso/cache/kiro-auth-token.json
//	go run ./cmd/kiroprobe -token auths/kiro-<account>.json -raw
//
// Without -raw it prints one line per model with the fields that bear on the
// registry entries. With -raw it prints the whole response, which is the only way
// to see tokenLimits fields the parser drops.
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
	flag.Parse()

	if *tokenFile == "" {
		fmt.Fprintln(os.Stderr, "-token is required")
		flag.Usage()
		os.Exit(2)
	}

	auth := kiro.NewKiroAuth(&config.Config{})
	tokenData, err := loadToken(*tokenFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load token: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

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
		fmt.Println()
	}
}

// loadToken accepts both Kiro credential layouts. Kiro IDE's own
// kiro-auth-token.json is camelCase and matches kiro.KiroTokenData; the files
// this proxy writes under auths/ are snake_case and match kiro.KiroTokenStorage.
// Unmarshalling the wrong one succeeds and silently yields empty fields, which
// reaches the backend as an empty bearer token and comes back as a 400 about an
// invalid token -- so decide by which layout actually carries an access token.
func loadToken(path string) (*kiro.KiroTokenData, error) {
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(home, path[1:])
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var camel kiro.KiroTokenData
	if err := json.Unmarshal(raw, &camel); err == nil && strings.TrimSpace(camel.AccessToken) != "" {
		return &camel, nil
	}

	var snake kiro.KiroTokenStorage
	if err := json.Unmarshal(raw, &snake); err != nil {
		return nil, err
	}
	if strings.TrimSpace(snake.AccessToken) == "" {
		return nil, fmt.Errorf("no access token found in %s (recognised neither the camelCase IDE layout nor the snake_case auths/ layout)", path)
	}
	if expiry := strings.TrimSpace(snake.ExpiresAt); expiry != "" {
		if at, errParse := time.Parse(time.RFC3339, expiry); errParse == nil && time.Now().After(at) {
			fmt.Fprintf(os.Stderr, "warning: token expired at %s; refresh it or the backend will reject the request\n", expiry)
		}
	}
	return &kiro.KiroTokenData{
		AccessToken:  snake.AccessToken,
		RefreshToken: snake.RefreshToken,
		ProfileArn:   snake.ProfileArn,
		ExpiresAt:    snake.ExpiresAt,
		AuthMethod:   snake.AuthMethod,
		Provider:     snake.Provider,
		ClientID:     snake.ClientID,
		ClientSecret: snake.ClientSecret,
		Email:        snake.Email,
		StartURL:     snake.StartURL,
		Region:       snake.Region,
	}, nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

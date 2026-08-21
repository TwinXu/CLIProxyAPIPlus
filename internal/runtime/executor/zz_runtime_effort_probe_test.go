package executor

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

// TestProbeRuntimeEffort is a manual probe (not part of CI): it sends the real
// wire payload to the primary runtime.kiro.dev endpoint with an explicit
// top-level additionalModelRequestFields block, to confirm the effort format
// proven against the legacy endpoint also holds there.
// Run with:
//
//	KIRO_PROBE=/path/to/kiro-token.json go test ./internal/runtime/executor \
//	    -run ProbeRuntimeEffort -v
func TestProbeRuntimeEffort(t *testing.T) {
	tokenPath := os.Getenv("KIRO_PROBE")
	if tokenPath == "" {
		t.Skip("set KIRO_PROBE to a Kiro token file to run this network probe")
	}
	raw, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("read credential: %v", err)
	}
	var meta map[string]any
	if err = json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("parse credential: %v", err)
	}
	auth := &cliproxyauth.Auth{ID: "probe", Metadata: meta}
	accessToken, profileArn := kiroCredentials(auth)
	if accessToken == "" {
		t.Fatal("no access token")
	}

	ep := buildKiroEndpointConfigs("us-east-1")[0]
	t.Logf("endpoint: %s (%s)", ep.URL, ep.Name)

	const modelID = "claude-opus-5"
	from := sdktranslator.FromString("claude")
	to := sdktranslator.FromString("kiro")

	type variant struct {
		label string
		amrf  map[string]any
	}
	variants := []variant{
		{"no amrf", nil},
		{"adaptive only", map[string]any{"thinking": map[string]any{"type": "adaptive"}}},
		{"adaptive+summarized", map[string]any{"thinking": map[string]any{"type": "adaptive", "display": "summarized"}}},
		{"adaptive+omitted", map[string]any{"thinking": map[string]any{"type": "adaptive", "display": "omitted"}}},
		{"xhigh, no display", map[string]any{"output_config": map[string]any{"effort": "xhigh"},
			"thinking": map[string]any{"type": "adaptive"}}},
		{"xhigh+summarized", map[string]any{"output_config": map[string]any{"effort": "xhigh"},
			"thinking": map[string]any{"type": "adaptive", "display": "summarized"}}},
		{"max+summarized", map[string]any{"output_config": map[string]any{"effort": "max"},
			"thinking": map[string]any{"type": "adaptive", "display": "summarized"}}},
	}
	for _, v := range variants {
		effort := v.label
		body := []byte(`{"model":"claude-opus-5","max_tokens":400,"messages":[{"role":"user","content":"What is 17*23? Reason it out step by step."}]}`)
		translated := sdktranslator.TranslateRequest(from, to, modelID, bytes.Clone(body), true)
		payload, _ := buildKiroPayloadForFormat(translated, modelID, profileArn, ep.Origin, false, false, from, nil)

		if v.amrf != nil {
			var m map[string]json.RawMessage
			if err = json.Unmarshal(payload, &m); err != nil {
				t.Fatalf("unmarshal payload: %v", err)
			}
			amrf, _ := json.Marshal(v.amrf)
			m["additionalModelRequestFields"] = amrf
			if payload, err = json.Marshal(m); err != nil {
				t.Fatalf("remarshal payload: %v", err)
			}
		}

		httpReq, _ := http.NewRequest(http.MethodPost, ep.URL, bytes.NewReader(payload))
		httpReq.Header.Set("Content-Type", kiroContentType)
		httpReq.Header.Set("Accept", kiroAcceptStream)
		if ep.AmzTarget != "" {
			httpReq.Header.Set("X-Amz-Target", ep.AmzTarget)
		}
		httpReq.Header.Set("x-amzn-kiro-agent-mode", kiroIDEAgentMode)
		httpReq.Header.Set("x-amzn-codewhisperer-optout", "true")
		applyDynamicFingerprintForEndpoint(httpReq, auth, ep)
		httpReq.Header.Set("Amz-Sdk-Request", "attempt=1; max=3")
		httpReq.Header.Set("Amz-Sdk-Invocation-Id", uuid.New().String())
		httpReq.Header.Set("Authorization", "Bearer "+accessToken)

		client := &http.Client{Timeout: 120 * time.Second}
		resp, errDo := client.Do(httpReq)
		if errDo != nil {
			t.Errorf("effort=%-6s transport error: %v", effort, errDo)
			continue
		}
		rb, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		s := string(rb)
		label := effort
		if label == "" {
			label = "<none>"
		}
		if resp.StatusCode != http.StatusOK {
			snippet := s
			if len(snippet) > 300 {
				snippet = snippet[:300]
			}
			t.Logf("effort=%-7s status=%d body=%s", label, resp.StatusCode, snippet)
			continue
		}
		t.Logf("effort=%-7s status=%d reasoningEvents=%d answerEvents=%d bytes=%d",
			label, resp.StatusCode,
			strings.Count(s, "reasoningContentEvent"),
			strings.Count(s, "assistantResponseEvent"),
			len(rb))
	}
}

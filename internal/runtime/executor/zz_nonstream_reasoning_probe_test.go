package executor

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	kiroclaude "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/kiro/claude"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

// TestProbeNonStreamReasoning is a manual probe (not part of CI): it sends one real
// thinking request and prints the order the backend emits its events in, then the
// content blocks the buffered path builds out of them.
//
// The order is what decides whether merging every reasoningContentEvent into a single
// leading thinking block is lossless. A stream:false response is one flat string, so
// reasoning that genuinely interleaved with text could not be reconstructed in place;
// re-run this before assuming it never does.
//
//	KIRO_PROBE=/path/to/kiro-token.json go test ./internal/runtime/executor \
//	    -run ProbeNonStreamReasoning -v
func TestProbeNonStreamReasoning(t *testing.T) {
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
	const modelID = "claude-opus-5"
	from := sdktranslator.FromString("claude")
	to := sdktranslator.FromString("kiro")

	body := []byte(`{"model":"claude-opus-5","max_tokens":600,"thinking":{"type":"adaptive"},` +
		`"messages":[{"role":"user","content":"What is 17*23? Reason it out, answer, then double-check the answer."}]}`)
	translated := sdktranslator.TranslateRequest(from, to, modelID, bytes.Clone(body), false)
	payload, _ := buildKiroPayloadForFormat(translated, modelID, profileArn, ep.Origin, false, false, from, nil)

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
	resp, err := client.Do(httpReq)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	rb, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet := string(rb)
		if len(snippet) > 400 {
			snippet = snippet[:400]
		}
		t.Fatalf("status=%d body=%s", resp.StatusCode, snippet)
	}
	t.Logf("endpoint: %s (%s), %d bytes", ep.URL, ep.Name, len(rb))

	e := &KiroExecutor{}
	t.Logf("event order: %s", probeEventOrder(t, e, rb))

	parsed, err := e.parseEventStream(bytes.NewReader(rb), modelID)
	if err != nil {
		t.Fatalf("parseEventStream: %v", err)
	}
	t.Logf("reasoning: present=%t len=%d upstreamSignature=%t",
		parsed.Reasoning.Present, len(parsed.Reasoning.Text), parsed.Reasoning.Signature != "")

	built := kiroclaude.BuildClaudeResponseWithReasoning(parsed.Content, parsed.Reasoning, parsed.ToolUses,
		modelID, usage.Detail{}, parsed.StopReason)
	var out struct {
		Content []map[string]any `json:"content"`
	}
	if err = json.Unmarshal(built, &out); err != nil {
		t.Fatalf("unmarshal built response: %v", err)
	}
	for i, block := range out.Content {
		name, _ := block["type"].(string)
		field := "text"
		if name == "thinking" {
			field = "thinking"
		}
		text, _ := block[field].(string)
		t.Logf("block[%d] type=%s len=%d head=%q", i, name, len(text), truncateProbe(text, 80))
	}
}

// probeEventOrder walks the frames and returns the event types in arrival order, with
// runs of the same type collapsed to "type xN" so interleaving is visible at a glance.
func probeEventOrder(t *testing.T, e *KiroExecutor, rb []byte) string {
	t.Helper()
	reader := bufio.NewReader(bytes.NewReader(rb))
	var parts []string
	var last string
	count := 0
	flush := func() {
		if count == 0 {
			return
		}
		if count == 1 {
			parts = append(parts, last)
		} else {
			parts = append(parts, fmt.Sprintf("%s x%d", last, count))
		}
	}
	for {
		msg, errEvent := e.readEventStreamMessage(reader)
		if errEvent != nil || msg == nil {
			break
		}
		if msg.EventType == last {
			count++
			continue
		}
		flush()
		last, count = msg.EventType, 1
	}
	flush()
	return strings.Join(parts, " -> ")
}

func truncateProbe(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

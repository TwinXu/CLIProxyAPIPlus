package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestListAuthFilesExposesPerAuthRequestCounts(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	auth := &coreauth.Auth{
		ID:       "kiro-user.json",
		FileName: "kiro-user.json",
		Provider: "kiro",
		Attributes: map[string]string{
			"path": filepath.Join(authDir, "kiro-user.json"),
		},
	}
	manager := coreauth.NewManager(nil, nil, nil)
	registered, errRegister := manager.Register(context.Background(), auth)
	if errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}

	stats := usage.NewRequestStatistics()
	now := time.Now().UTC()
	stats.Record(context.Background(), coreusage.Record{AuthIndex: registered.Index, RequestedAt: now.Add(-10 * time.Minute)})
	stats.Record(context.Background(), coreusage.Record{AuthIndex: registered.Index, RequestedAt: now})
	stats.Record(context.Background(), coreusage.Record{AuthIndex: registered.Index, RequestedAt: now, Failed: true})
	stats.Record(context.Background(), coreusage.Record{AuthIndex: "other-auth", RequestedAt: now, Failed: true})

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.SetUsageStatistics(stats)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)

	h.ListAuthFiles(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Files []struct {
			AuthIndex string `json:"auth_index"`
			Success   int64  `json:"success"`
			Failed    int64  `json:"failed"`
			Recent    []struct {
				Time    string `json:"time"`
				Success int64  `json:"success"`
				Failed  int64  `json:"failed"`
			} `json:"recent_requests"`
			Stats struct {
				Success int64 `json:"success"`
				Failure int64 `json:"failure"`
			} `json:"stats"`
		} `json:"files"`
	}
	if errDecode := json.Unmarshal(recorder.Body.Bytes(), &payload); errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if len(payload.Files) != 1 {
		t.Fatalf("files len = %d, want 1", len(payload.Files))
	}
	file := payload.Files[0]
	if file.AuthIndex != registered.Index {
		t.Fatalf("auth_index = %q, want %q", file.AuthIndex, registered.Index)
	}
	if file.Success != 2 || file.Failed != 1 {
		t.Fatalf("root counts = success:%d failed:%d, want 2/1", file.Success, file.Failed)
	}
	if file.Stats.Success != 2 || file.Stats.Failure != 1 {
		t.Fatalf("legacy stats = success:%d failure:%d, want 2/1", file.Stats.Success, file.Stats.Failure)
	}
	if len(file.Recent) != 20 {
		t.Fatalf("recent_requests len = %d, want 20", len(file.Recent))
	}
	for i, window := range file.Recent {
		parsed, errParse := time.Parse(time.RFC3339, window.Time)
		if errParse != nil {
			t.Fatalf("recent_requests[%d].time = %q: %v", i, window.Time, errParse)
		}
		if i > 0 {
			previous, _ := time.Parse(time.RFC3339, file.Recent[i-1].Time)
			if parsed.Sub(previous) != 10*time.Minute {
				t.Fatalf("window spacing at %d = %s, want 10m", i, parsed.Sub(previous))
			}
		}
	}
	previous := file.Recent[len(file.Recent)-2]
	if previous.Success != 1 || previous.Failed != 0 {
		t.Fatalf("previous window = success:%d failed:%d, want 1/0", previous.Success, previous.Failed)
	}
	current := file.Recent[len(file.Recent)-1]
	if current.Success != 1 || current.Failed != 1 {
		t.Fatalf("current window = success:%d failed:%d, want 1/1", current.Success, current.Failed)
	}
}

package usage

import (
	"context"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestRequestStatisticsRecordIncludesLatency(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-key",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC),
		Latency:     1500 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
		},
	})

	snapshot := stats.Snapshot()
	details := snapshot.APIs["test-key"].Models["gpt-5.4"].Details
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1", len(details))
	}
	if details[0].LatencyMs != 1500 {
		t.Fatalf("latency_ms = %d, want 1500", details[0].LatencyMs)
	}
}

func TestRequestStatisticsMergeSnapshotDedupIgnoresLatency(t *testing.T) {
	stats := NewRequestStatistics()
	timestamp := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	first := StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"test-key": {
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						Details: []RequestDetail{{
							Timestamp: timestamp,
							LatencyMs: 0,
							Source:    "user@example.com",
							AuthIndex: "0",
							Tokens: TokenStats{
								InputTokens:  10,
								OutputTokens: 20,
								TotalTokens:  30,
							},
						}},
					},
				},
			},
		},
	}
	second := StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"test-key": {
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						Details: []RequestDetail{{
							Timestamp: timestamp,
							LatencyMs: 2500,
							Source:    "user@example.com",
							AuthIndex: "0",
							Tokens: TokenStats{
								InputTokens:  10,
								OutputTokens: 20,
								TotalTokens:  30,
							},
						}},
					},
				},
			},
		},
	}

	result := stats.MergeSnapshot(first)
	if result.Added != 1 || result.Skipped != 0 {
		t.Fatalf("first merge = %+v, want added=1 skipped=0", result)
	}

	result = stats.MergeSnapshot(second)
	if result.Added != 0 || result.Skipped != 1 {
		t.Fatalf("second merge = %+v, want added=0 skipped=1", result)
	}

	snapshot := stats.Snapshot()
	details := snapshot.APIs["test-key"].Models["gpt-5.4"].Details
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1", len(details))
	}
}

func TestAuthIndexCountsSnapshotAggregatesRecentWindows(t *testing.T) {
	stats := NewRequestStatistics()
	now := time.Now().UTC()
	currentWindow := now.Truncate(authIndexRequestWindowDuration)

	stats.Record(context.Background(), coreusage.Record{
		AuthIndex:   "auth-a",
		RequestedAt: now.Add(-authIndexRequestWindowDuration),
	})
	stats.Record(context.Background(), coreusage.Record{
		AuthIndex:   "auth-a",
		RequestedAt: now,
	})
	stats.Record(context.Background(), coreusage.Record{
		AuthIndex:   "auth-a",
		RequestedAt: now,
		Failed:      true,
	})
	stats.Record(context.Background(), coreusage.Record{
		AuthIndex:   "auth-b",
		RequestedAt: now,
		Failed:      true,
	})

	snapshot := stats.AuthIndexCountsSnapshot()
	authA := snapshot["auth-a"]
	if authA.Success != 2 || authA.Failure != 1 {
		t.Fatalf("auth-a counts = success:%d failure:%d, want 2/1", authA.Success, authA.Failure)
	}
	if len(authA.RecentRequests) != authIndexRequestWindowCount {
		t.Fatalf("recent requests len = %d, want %d", len(authA.RecentRequests), authIndexRequestWindowCount)
	}
	for i, window := range authA.RecentRequests {
		expectedTime := currentWindow.Add(time.Duration(i-(authIndexRequestWindowCount-1)) * authIndexRequestWindowDuration)
		if !window.Time.Equal(expectedTime) {
			t.Fatalf("window %d time = %s, want %s", i, window.Time, expectedTime)
		}
	}
	previous := authA.RecentRequests[authIndexRequestWindowCount-2]
	if previous.Success != 1 || previous.Failed != 0 {
		t.Fatalf("previous window = success:%d failed:%d, want 1/0", previous.Success, previous.Failed)
	}
	current := authA.RecentRequests[authIndexRequestWindowCount-1]
	if current.Success != 1 || current.Failed != 1 {
		t.Fatalf("current window = success:%d failed:%d, want 1/1", current.Success, current.Failed)
	}
	authB := snapshot["auth-b"]
	if authB.Success != 0 || authB.Failure != 1 {
		t.Fatalf("auth-b counts = success:%d failure:%d, want 0/1", authB.Success, authB.Failure)
	}
	if currentB := authB.RecentRequests[authIndexRequestWindowCount-1]; currentB.Success != 0 || currentB.Failed != 1 {
		t.Fatalf("auth-b current window = success:%d failed:%d, want 0/1", currentB.Success, currentB.Failed)
	}
}

func TestMergeSnapshotUpdatesAuthIndexWindows(t *testing.T) {
	stats := NewRequestStatistics()
	now := time.Now().UTC()
	result := stats.MergeSnapshot(StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"test-key": {
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						Details: []RequestDetail{
							{Timestamp: now, AuthIndex: "auth-a"},
							{Timestamp: now, AuthIndex: "auth-a", Failed: true},
						},
					},
				},
			},
		},
	})
	if result.Added != 2 || result.Skipped != 0 {
		t.Fatalf("merge = %+v, want added=2 skipped=0", result)
	}

	authA := stats.AuthIndexCountsSnapshot()["auth-a"]
	if authA.Success != 1 || authA.Failure != 1 {
		t.Fatalf("counts = success:%d failure:%d, want 1/1", authA.Success, authA.Failure)
	}
	current := authA.RecentRequests[authIndexRequestWindowCount-1]
	if current.Success != 1 || current.Failed != 1 {
		t.Fatalf("current window = success:%d failed:%d, want 1/1", current.Success, current.Failed)
	}
}

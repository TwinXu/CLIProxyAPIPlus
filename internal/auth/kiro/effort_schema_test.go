package kiro

import (
	"encoding/json"
	"testing"
)

// The effort schema is the backend's data, and the model list rides on the same
// decode. These cases pin that an unfamiliar schema costs us the levels for one
// model and nothing else -- before, decoding it into fixed structs meant any
// surprise here made json.Unmarshal fail for the whole response, and
// fetchKiroModels silently fell back to the static catalogue for every account.
func TestParseEffortSchema(t *testing.T) {
	const objectSchema = `{"properties":{"output_config":{"properties":{"effort":{"enum":["low","medium","high","max"],"default":"high"}}}}}`

	for _, tc := range []struct {
		name        string
		raw         string
		wantLevels  []string
		wantDefault string
	}{
		{
			name:        "object schema",
			raw:         objectSchema,
			wantLevels:  []string{"low", "medium", "high", "max"},
			wantDefault: "high",
		},
		{
			// AWS is not consistent about this: the schema sometimes arrives as a
			// JSON string that itself contains the object.
			name:        "schema delivered as a JSON string",
			raw:         mustQuote(t, objectSchema),
			wantLevels:  []string{"low", "medium", "high", "max"},
			wantDefault: "high",
		},
		{
			name:        "enum holding non-strings skips them rather than failing",
			raw:         `{"properties":{"output_config":{"properties":{"effort":{"enum":["low",7,null,"high"]}}}}}`,
			wantLevels:  []string{"low", "high"},
			wantDefault: "",
		},
		{
			name:       "absent schema",
			raw:        "",
			wantLevels: nil,
		},
		{
			name:       "schema present but shaped differently",
			raw:        `{"properties":{"something_else":{"type":"object"}}}`,
			wantLevels: nil,
		},
		{
			name:       "effort is not an object",
			raw:        `{"properties":{"output_config":{"properties":{"effort":"high"}}}}`,
			wantLevels: nil,
		},
		{
			name:       "outright garbage",
			raw:        `not json at all`,
			wantLevels: nil,
		},
		{
			name:       "string that is not itself JSON",
			raw:        `"just a description"`,
			wantLevels: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			levels, defaultEffort := parseEffortSchema(json.RawMessage(tc.raw), "test-model")

			if len(levels) != len(tc.wantLevels) {
				t.Fatalf("levels = %v, want %v", levels, tc.wantLevels)
			}
			for i := range tc.wantLevels {
				if levels[i] != tc.wantLevels[i] {
					t.Fatalf("levels = %v, want %v", levels, tc.wantLevels)
				}
			}
			if defaultEffort != tc.wantDefault {
				t.Fatalf("defaultEffort = %q, want %q", defaultEffort, tc.wantDefault)
			}
		})
	}
}

func mustQuote(t *testing.T, s string) string {
	t.Helper()
	quoted, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(quoted)
}

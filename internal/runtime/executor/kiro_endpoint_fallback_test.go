package executor

import "testing"

// Only INVALID_MODEL_ID may advance to the next endpoint. Treating any other 400
// that way would send a genuinely malformed request three times and report the
// last endpoint's rejection instead of the first's.
func TestIsKiroModelUnavailableAtEndpoint(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "model not served here",
			body: `{"message":"Invalid model ID. Please select a different model to continue.","reason":"INVALID_MODEL_ID"}`,
			want: true,
		},
		{
			// The effort enum rejection is a real 400 this must not retry: the body
			// is valid, the value simply is not offered, and every endpoint agrees.
			name: "effort not in the model's enum",
			body: `{"message":"Invalid additionalModelRequestFields: does not have a value in the enumeration [\"low\", \"medium\", \"high\", \"xhigh\", \"max\"]","reason":"REQUEST_BODY_INVALID"}`,
			want: false,
		},
		{name: "other validation failure", body: `{"message":"malformed","reason":"REQUEST_BODY_INVALID"}`, want: false},
		{name: "no reason field", body: `{"message":"Invalid model ID."}`, want: false},
		{name: "reason nested elsewhere", body: `{"error":{"reason":"INVALID_MODEL_ID"}}`, want: false},
		{name: "not json", body: `Bad Gateway`, want: false},
		{name: "empty", body: ``, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isKiroModelUnavailableAtEndpoint([]byte(tc.body)); got != tc.want {
				t.Errorf("isKiroModelUnavailableAtEndpoint(%s) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

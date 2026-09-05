package errors

import "testing"

func TestIsTerminalUpstreamError(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		{
			"content moderation",
			"<400> InternalError.Algo.DataInspectionFailed: Input text data may contain inappropriate content.",
			true,
		},
		{
			"moderation case-insensitive",
			"Input text data may contain INAPPROPRIATE content.",
			true,
		},
		{"model permission denial", "Model access denied.", false},
		{"key-invalid 401", "Incorrect API key provided.", false},
		{"quota", "Free quota exhausted.", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		if got := IsTerminalUpstreamError(tc.msg); got != tc.want {
			t.Errorf("%s: IsTerminalUpstreamError(%q) = %v, want %v", tc.name, tc.msg, got, tc.want)
		}
	}
}
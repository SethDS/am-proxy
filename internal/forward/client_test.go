package forward

import (
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		status int
		hdr    string
		want   Outcome
		after  time.Duration
	}{
		{200, "", OutcomeOK, 0},
		{400, "", OutcomeBadRequest, 0},
		{401, "", OutcomeAuth, 0},
		{403, "", OutcomeAuth, 0},
		{404, "", OutcomeNotFound, 0},
		{408, "", OutcomeTimeout, 0},
		{429, "10", OutcomeRateLimited, 10 * time.Second},
		{500, "", OutcomeRetryable, 0},
		{503, "", OutcomeRetryable, 0},
		{422, "", OutcomeOther4xx, 0},
	}
	for _, tc := range cases {
		r := Classify(tc.status, tc.hdr)
		if r.Outcome != tc.want {
			t.Fatalf("status %d: got %v want %v", tc.status, r.Outcome, tc.want)
		}
		if r.RetryAfter != tc.after {
			t.Fatalf("status %d RetryAfter=%v want %v", tc.status, r.RetryAfter, tc.after)
		}
	}
}

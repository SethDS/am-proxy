package forward

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/SethDS/am-proxy/internal/model"
)

// Outcome classifies an Alertmanager POST result for the custody failure policy.
type Outcome int

const (
	OutcomeOK Outcome = iota
	OutcomeBadRequest      // 400 → dead-letter
	OutcomeAuth            // 401/403 → hold, stop rapid retries, alert ops
	OutcomeNotFound        // 404 → bounded retry after config check
	OutcomeTimeout         // 408 → exp backoff + jitter
	OutcomeRateLimited     // 429 → hold, respect Retry-After
	OutcomeRetryable       // 5xx / network → retry with backoff
	OutcomeOther4xx        // other 4xx → dead-letter
)

func (o Outcome) String() string {
	switch o {
	case OutcomeOK:
		return "ok"
	case OutcomeBadRequest:
		return "bad_request"
	case OutcomeAuth:
		return "auth"
	case OutcomeNotFound:
		return "not_found"
	case OutcomeTimeout:
		return "timeout"
	case OutcomeRateLimited:
		return "rate_limited"
	case OutcomeRetryable:
		return "retryable"
	case OutcomeOther4xx:
		return "other_4xx"
	default:
		return "unknown"
	}
}

type Result struct {
	Outcome    Outcome
	StatusCode int
	RetryAfter time.Duration
	Err        error
}

type Client struct {
	HTTP *http.Client
}

func NewClient(timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Client{
		HTTP: &http.Client{Timeout: timeout},
	}
}

// PostAlerts POSTs a batch to baseURL + /api/v2/alerts.
func (c *Client) PostAlerts(ctx context.Context, baseURL string, alerts []model.Alert) Result {
	u := strings.TrimRight(baseURL, "/") + "/api/v2/alerts"
	body, err := json.Marshal(alerts)
	if err != nil {
		return Result{Outcome: OutcomeBadRequest, Err: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return Result{Outcome: OutcomeRetryable, Err: err}
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Result{Outcome: OutcomeRetryable, Err: err}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))

	return Classify(resp.StatusCode, resp.Header.Get("Retry-After"))
}

func Classify(status int, retryAfterHdr string) Result {
	r := Result{StatusCode: status}
	switch {
	case status >= 200 && status < 300:
		r.Outcome = OutcomeOK
	case status == http.StatusBadRequest:
		r.Outcome = OutcomeBadRequest
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		r.Outcome = OutcomeAuth
	case status == http.StatusNotFound:
		r.Outcome = OutcomeNotFound
	case status == http.StatusRequestTimeout:
		r.Outcome = OutcomeTimeout
	case status == http.StatusTooManyRequests:
		r.Outcome = OutcomeRateLimited
		r.RetryAfter = parseRetryAfter(retryAfterHdr)
	case status >= 500:
		r.Outcome = OutcomeRetryable
	case status >= 400:
		r.Outcome = OutcomeOther4xx
	default:
		r.Outcome = OutcomeRetryable
		r.Err = fmt.Errorf("unexpected status %d", status)
	}
	return r
}

func parseRetryAfter(h string) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	if sec, err := strconv.Atoi(h); err == nil {
		if sec < 0 {
			return 0
		}
		return time.Duration(sec) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		d := time.Until(t)
		if d < 0 {
			return 0
		}
		return d
	}
	return 0
}

package torbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const DailyBandwidthLimitExceeded = "DAILY_BANDWIDTH_LIMIT_EXCEEDED"

var errDailyBandwidthLimit = errors.New("TorBox daily bandwidth limit exceeded")

// APIError preserves machine-readable upstream errors without exposing request
// URLs or credentials. Bandwidth values are the last values reported by TorBox.
type APIError struct {
	StatusCode int
	Code       string
	RetryAt    time.Time
	LimitBytes *int64
	UsageBytes *int64
	message    string
}

func (e *APIError) Error() string {
	if e.message != "" {
		return e.message
	}
	return fmt.Sprintf("torbox: unexpected status %d (%s)", e.StatusCode, e.Code)
}

func (e *APIError) Is(target error) bool {
	return target == errDailyBandwidthLimit && e.Code == DailyBandwidthLimitExceeded
}

func IsDailyBandwidthLimit(err error) bool {
	return errors.Is(err, errDailyBandwidthLimit)
}

func newAPIError(body []byte, status int, retryAt time.Time, prefix string) *APIError {
	var env struct {
		Error string          `json:"error"`
		Data  json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(body, &env)
	e := &APIError{StatusCode: status, Code: env.Error, RetryAt: retryAt}
	if status == http.StatusOK {
		e.message = prefix + env.Error
	} else {
		e.message = fmt.Sprintf("torbox: unexpected status %d", status)
		if e.Code != "" {
			e.message += " (" + e.Code + ")"
		}
	}
	if e.Code == DailyBandwidthLimitExceeded {
		var data struct {
			Limit *int64 `json:"daily_bandwidth_limit"`
			Usage *int64 `json:"daily_bandwidth_usage"`
		}
		if json.Unmarshal(env.Data, &data) == nil {
			if data.Limit != nil && *data.Limit >= 0 {
				e.LimitBytes = data.Limit
			}
			if data.Usage != nil && *data.Usage >= 0 {
				e.UsageBytes = data.Usage
			}
		}
	}
	return e
}

// downloadEnvelopeError runs before decoding data as a URL: error responses
// can contain an object in data, including on an HTTP 200 response.
func downloadEnvelopeError(body []byte, prefix string, retryAt time.Time) error {
	e := newAPIError(body, http.StatusOK, retryAt, prefix)
	if e.Code == "" {
		return nil
	}
	return e
}

func parseRetryAfter(value string, now time.Time) time.Time {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		// Reject negative values and durations that overflow time.Duration.
		if seconds > 0 && seconds <= int64((1<<63-1)/time.Second) {
			return now.Add(time.Duration(seconds) * time.Second)
		}
		return time.Time{}
	}
	if until, err := http.ParseTime(value); err == nil && until.After(now) {
		return until
	}
	return time.Time{}
}

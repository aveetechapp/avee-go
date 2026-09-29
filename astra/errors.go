package astra

import (
	"errors"
	"fmt"
	"time"
)

var ErrTimeout = errors.New("astra: timeout")

type Problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
}

type HTTPError struct {
	Status     int
	URL        string
	Body       string
	Detail     string
	Problem    *Problem
	RetryAfter time.Duration
}

func (e *HTTPError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("astra: %d from %s", e.Status, e.URL)
	}

	return fmt.Sprintf("astra: %d from %s: %s", e.Status, e.URL, e.Detail)
}

func (e *HTTPError) Retryable() bool {
	return retryableStatus(e.Status)
}

type ValidationError struct {
	Reason string
}

func (e *ValidationError) Error() string {
	return "astra: " + e.Reason
}

type SubscriptionError struct {
	Reason string
}

func (e *SubscriptionError) Error() string {
	return "astra: subscription refused: " + e.Reason
}

type ServerError struct {
	Reason string
}

func (e *ServerError) Error() string {
	return "astra: server: " + e.Reason
}

func invalidf(format string, args ...any) error {
	return &ValidationError{Reason: fmt.Sprintf(format, args...)}
}

func retryableStatus(status int) bool {
	switch status {
	case 408, 429, 502, 503, 504:
		return true
	default:
		return false
	}
}

func clip(s string) string {
	const limit = 200
	if len(s) <= limit {
		return s
	}

	return s[:limit] + "…"
}

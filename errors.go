package avee

import (
	"errors"
	"fmt"
	"net/http"
	"time"
	"unicode/utf8"
)

var (
	ErrTimeout          = errors.New("avee: timeout")
	ErrPaymentDeclined  = errors.New("avee: the payer declined the payment")
	ErrNoPayableNetwork = errors.New("avee: no accepted network the payer supports")
)

type APIError struct {
	Problem         *Problem
	PaymentRequired *PaymentRequired
	Payment         *PaymentReceipt
	Header          http.Header
	Operation       string
	Method          string
	URL             string
	Code            string
	Detail          string
	Param           string
	RequestID       string
	Body            string
	RateLimit       RateLimit
	Status          int
	RetryAfter      time.Duration
	Paid            bool
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("avee: %s %s answered %d", e.Method, e.URL, e.Status)
	if e.Code != "" {
		msg += " " + e.Code
	}

	if e.Detail != "" {
		msg += ": " + e.Detail
	}

	if e.RequestID != "" {
		msg += " (request " + e.RequestID + ")"
	}

	return msg
}

func (e *APIError) Retryable() bool {
	return retryableStatus(e.Status)
}

type ValidationError struct {
	Reason string
}

func (e *ValidationError) Error() string {
	return "avee: " + e.Reason
}

type PaymentError struct {
	Err       error
	Required  *PaymentRequired
	Operation string
	Reason    string
}

func (e *PaymentError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("avee: payment for %s: %s: %v", e.Operation, e.Reason, e.Err)
	}

	return fmt.Sprintf("avee: payment for %s: %s", e.Operation, e.Reason)
}

func (e *PaymentError) Unwrap() error {
	return e.Err
}

func invalidf(format string, args ...any) error {
	return &ValidationError{Reason: fmt.Sprintf(format, args...)}
}

func retryableStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
}

const clipLimit = 200

func clip(s string) string {
	if len(s) <= clipLimit {
		return s
	}

	cut := clipLimit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}

	return s[:cut] + "…"
}

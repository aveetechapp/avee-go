package avee

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	retryBaseDelay  = 250 * time.Millisecond
	pooledBufferCap = 1 << 20
)

var bufferPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

type reply struct {
	header http.Header
	body   *bytes.Buffer
	status int
}

func (rep *reply) release() {
	if rep.body.Cap() <= pooledBufferCap {
		rep.body.Reset()
		bufferPool.Put(rep.body)
	}
}

func (c *Client) target(r *call) string {
	u := *c.base
	base := strings.TrimRight(c.base.Path, "/")
	rawBase := strings.TrimRight(c.base.EscapedPath(), "/")
	escaped := make([]string, len(r.segments))

	for i, s := range r.segments {
		escaped[i] = url.PathEscape(s)
	}

	u.Path = base + "/" + strings.Join(r.segments, "/")
	u.RawPath = rawBase + "/" + strings.Join(escaped, "/")
	u.RawQuery = r.query.Encode()

	return u.String()
}

func (c *Client) do(ctx context.Context, r *call, out any) error {
	if r.err != nil {
		return r.err
	}

	var body []byte

	if r.body != nil {
		encoded, err := json.Marshal(r.body)
		if err != nil {
			return invalidf("%s: encode body: %v", r.operation, err)
		}

		body = encoded
	}

	target := c.target(r)
	signature := ""

	for attempt := 0; ; {
		rep, err := c.roundTrip(ctx, r, target, body, signature)
		if err != nil {
			delay, retry := c.retryDelay(ctx, r, err, attempt, signature != "")
			if !retry {
				return err
			}

			if sleepContext(ctx, delay) != nil {
				return err
			}

			attempt++

			continue
		}

		if rep.status >= 200 && rep.status < 300 {
			err = json.Unmarshal(rep.body.Bytes(), out)
			rep.release()

			if err != nil {
				return invalidf("%s: response from %s does not match the contract: %s", r.operation, target, clip(err.Error()))
			}

			return nil
		}

		if rep.status == http.StatusPaymentRequired && c.payer != nil && signature == "" {
			signature, err = c.pay(ctx, r, target, rep)
			rep.release()

			if err != nil {
				return err
			}

			continue
		}

		apiErr := c.apiError(r, target, rep, signature != "")
		rep.release()

		delay, retry := c.retryDelay(ctx, r, apiErr, attempt, signature != "")
		if !retry {
			return apiErr
		}

		if sleepContext(ctx, delay) != nil {
			return apiErr
		}

		attempt++
	}
}

func (c *Client) roundTrip(ctx context.Context, r *call, target string, body []byte, signature string) (*reply, error) {
	actx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(actx, r.method, target, reader)
	if err != nil {
		return nil, fmt.Errorf("avee: build request: %w", err)
	}

	for k, vs := range c.header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}

	if c.payer != nil {
		req.Header.Set("Accept-Payment", "x402")
	}

	if signature != "" {
		req.Header.Set("PAYMENT-SIGNATURE", signature)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, c.transportError(ctx, actx, r, target, err)
	}
	defer resp.Body.Close()

	buf, ok := bufferPool.Get().(*bytes.Buffer)
	if !ok {
		buf = new(bytes.Buffer)
	}

	buf.Reset()

	rep := &reply{header: resp.Header, body: buf, status: resp.StatusCode}

	n, err := buf.ReadFrom(io.LimitReader(resp.Body, c.maxResponseBytes+1))
	if err != nil {
		rep.release()

		return nil, c.transportError(ctx, actx, r, target, err)
	}

	c.record(ResponseInfo{
		Operation: r.operation,
		Status:    resp.StatusCode,
		RequestID: resp.Header.Get("X-Request-Id"),
		RateLimit: parseRateLimit(resp.Header, time.Now()),
		Payment:   parseReceipt(resp.Header),
	})

	if n > c.maxResponseBytes {
		rep.release()

		return nil, invalidf("%s: response from %s exceeds %d bytes", r.operation, target, c.maxResponseBytes)
	}

	return rep, nil
}

func (c *Client) transportError(parent, attempt context.Context, r *call, target string, err error) error {
	if parent.Err() != nil {
		return fmt.Errorf("avee: %s %s: %w", r.method, target, parent.Err())
	}

	if errors.Is(attempt.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: %s %s after %s", ErrTimeout, r.method, target, c.timeout)
	}

	return fmt.Errorf("avee: %s %s: %w", r.method, target, err)
}

func (c *Client) apiError(r *call, target string, rep *reply, paid bool) *APIError {
	now := time.Now()
	raw := rep.body.Bytes()
	body := string(raw[:min(len(raw), 4*clipLimit)])
	e := &APIError{
		Operation:  r.operation,
		Method:     r.method,
		URL:        target,
		Status:     rep.status,
		Header:     rep.header,
		Body:       clip(body),
		RequestID:  rep.header.Get("X-Request-Id"),
		RateLimit:  parseRateLimit(rep.header, now),
		RetryAfter: parseRetryAfter(rep.header.Get("Retry-After"), now),
		Payment:    parseReceipt(rep.header),
		Paid:       paid,
	}
	e.Detail = clip(strings.TrimSpace(body))

	if rep.status == http.StatusPaymentRequired {
		if required, _, err := decodeRequired(rep); err == nil {
			e.PaymentRequired = required
		}
	}

	if !strings.Contains(rep.header.Get("Content-Type"), "json") {
		return e
	}

	var p Problem
	if json.Unmarshal(rep.body.Bytes(), &p) != nil || (p.Code == "" && p.Title == "") {
		return e
	}

	e.Problem = &p
	e.Code = string(p.Code)
	e.Detail = clip(p.Title)

	if p.Detail != nil && *p.Detail != "" {
		e.Detail = clip(*p.Detail)
	}

	if p.Param != nil {
		e.Param = *p.Param
	}

	if p.RequestID != "" {
		e.RequestID = p.RequestID
	}

	return e
}

func (c *Client) retryDelay(ctx context.Context, r *call, err error, attempt int, paid bool) (time.Duration, bool) {
	if paid || ctx.Err() != nil || attempt >= c.maxRetries || r.method != http.MethodGet {
		return 0, false
	}

	backoff := fullJitter(attempt, retryBaseDelay, c.maxRetryDelay)

	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if !apiErr.Retryable() {
			return 0, false
		}

		wait := apiErr.RetryAfter
		if wait == 0 && apiErr.Status == http.StatusTooManyRequests && apiErr.RateLimit.Remaining == 0 {
			wait = apiErr.RateLimit.Reset
		}

		if wait == 0 {
			return backoff, true
		}

		return wait, wait <= c.maxRetryDelay
	}

	var validation *ValidationError
	if errors.As(err, &validation) {
		return 0, false
	}

	return backoff, true
}

func parseRateLimit(h http.Header, now time.Time) RateLimit {
	rl := RateLimit{Policy: h.Get("RateLimit-Policy"), RetryAfter: parseRetryAfter(h.Get("Retry-After"), now)}

	if v, err := strconv.Atoi(strings.TrimSpace(h.Get("X-RateLimit-Limit"))); err == nil {
		rl.Limit = v
		rl.Known = true
	}

	remaining, reset := -1, -1

	if v, err := strconv.Atoi(strings.TrimSpace(h.Get("X-RateLimit-Remaining"))); err == nil {
		remaining = v
	}

	if v, err := strconv.Atoi(strings.TrimSpace(h.Get("X-RateLimit-Reset"))); err == nil {
		reset = v
	}

	for _, part := range strings.Split(h.Get("RateLimit"), ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}

		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			continue
		}

		switch k {
		case "r":
			if remaining < 0 {
				remaining = n
			}
		case "t":
			if reset < 0 {
				reset = n
			}
		}
	}

	if remaining >= 0 {
		rl.Remaining = remaining
		rl.Known = true
	}

	if reset >= 0 {
		rl.Reset = time.Duration(reset) * time.Second
		rl.Known = true
	}

	return rl
}

func fullJitter(attempt int, base, ceiling time.Duration) time.Duration {
	limit := ceiling
	if attempt < 30 {
		if d := base << attempt; d > 0 && d < ceiling {
			limit = d
		}
	}

	if limit <= 0 {
		return 0
	}

	return rand.N(limit)
}

func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}

	if n, err := strconv.ParseUint(v, 10, 32); err == nil {
		return time.Duration(n) * time.Second
	}

	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now)
	}

	return 0
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}

	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type present bool

func (p *present) UnmarshalJSON([]byte) error {
	*p = true

	return nil
}

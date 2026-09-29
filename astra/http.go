package astra

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	errorBodyLimit  = 64 << 10
	retryBaseDelay  = 250 * time.Millisecond
	pooledBufferCap = 256 << 10
)

var bufferPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

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

func readHTTPError(resp *http.Response, rawURL string) *HTTPError {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
	e := &HTTPError{
		Status:     resp.StatusCode,
		URL:        rawURL,
		Body:       string(body),
		Detail:     clip(strings.TrimSpace(string(body))),
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
	}

	if !strings.Contains(resp.Header.Get("Content-Type"), "json") {
		return e
	}

	var parsed struct {
		Problem
		Errmsg string `json:"errmsg"`
	}

	if json.Unmarshal(body, &parsed) != nil {
		return e
	}

	switch {
	case parsed.Title != "" && parsed.Status != 0:
		p := parsed.Problem
		e.Problem = &p
		e.Detail = clip(p.Detail)

		if e.Detail == "" {
			e.Detail = clip(p.Title)
		}
	case parsed.Errmsg != "":
		e.Detail = clip(parsed.Errmsg)
	}

	return e
}

func (c *Client) resolve(path string, query url.Values) *url.URL {
	u := c.base.JoinPath(path)
	u.RawQuery = query.Encode()

	return u
}

func (c *Client) getJSON(ctx context.Context, path string, query url.Values, dst any, accept ...int) error {
	target := c.resolve(path, query).String()

	for attempt := 0; ; attempt++ {
		err := c.attempt(ctx, target, dst, accept)
		if err == nil {
			return nil
		}

		delay, retry := c.retryDelay(ctx, err, attempt)
		if !retry {
			return err
		}

		if sleepContext(ctx, delay) != nil {
			return err
		}
	}
}

func (c *Client) retryDelay(ctx context.Context, err error, attempt int) (time.Duration, bool) {
	if ctx.Err() != nil || attempt >= c.maxRetries {
		return 0, false
	}

	backoff := fullJitter(attempt, retryBaseDelay, c.maxRetryDelay)

	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		if !httpErr.Retryable() {
			return 0, false
		}

		if httpErr.RetryAfter == 0 {
			return backoff, true
		}

		return httpErr.RetryAfter, httpErr.RetryAfter <= c.maxRetryDelay
	}

	var validation *ValidationError
	if errors.As(err, &validation) {
		return 0, false
	}

	return backoff, true
}

func (c *Client) attempt(ctx context.Context, target string, dst any, accept []int) error {
	actx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(actx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("astra: build request: %w", err)
	}

	c.decorate(req.Header)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return c.transportError(ctx, actx, target, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && !acceptedReport(resp, accept) {
		return readHTTPError(resp, target)
	}

	buf, ok := bufferPool.Get().(*bytes.Buffer)
	if !ok {
		buf = new(bytes.Buffer)
	}

	defer func() {
		if buf.Cap() <= pooledBufferCap {
			buf.Reset()
			bufferPool.Put(buf)
		}
	}()

	buf.Reset()

	n, err := buf.ReadFrom(io.LimitReader(resp.Body, c.maxResponseBytes+1))
	if err != nil {
		return c.transportError(ctx, actx, target, err)
	}

	if n > c.maxResponseBytes {
		return invalidf("response from %s exceeds %d bytes", target, c.maxResponseBytes)
	}

	if err = json.Unmarshal(buf.Bytes(), dst); err != nil {
		return invalidf("response from %s does not match the contract: %s", target, clip(err.Error()))
	}

	return nil
}

func (c *Client) transportError(parent, attempt context.Context, target string, err error) error {
	if parent.Err() != nil {
		return fmt.Errorf("astra: GET %s: %w", target, parent.Err())
	}

	if errors.Is(attempt.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: GET %s after %s", ErrTimeout, target, c.timeout)
	}

	return fmt.Errorf("astra: GET %s: %w", target, err)
}

func (c *Client) decorate(h http.Header) {
	for k, vs := range c.header {
		for _, v := range vs {
			h.Add(k, v)
		}
	}
}

func acceptedReport(resp *http.Response, accept []int) bool {
	if !slices.Contains(accept, resp.StatusCode) {
		return false
	}

	mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))

	return err == nil && mt == "application/json"
}

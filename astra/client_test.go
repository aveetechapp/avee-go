package astra

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var feedMetaJSON = map[string]any{
	"id": btc,
	"attributes": map[string]any{
		"asset_type": "Crypto", "description": "d", "display_symbol": "BTC/USD", "quote_currency": "USD",
		"symbol": "Crypto.BTC/USD", "min_channel": "real_time", "astra_id": btcAstra,
	},
	"market_hours": map[string]any{"is_open": true, "next_open": nil, "next_close": nil},
}

func TestPriceFeedsSendsFilters(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, []any{feedMetaJSON}) })
	c := newTestClient(t, f.URL, Options{})

	feeds, err := c.PriceFeeds(t.Context(), PriceFeedsQuery{Query: "btc", AssetType: "crypto"})
	if err != nil || len(feeds) != 1 || feeds[0].Symbol != "Crypto.BTC/USD" {
		t.Fatalf("got %+v, %v", feeds, err)
	}

	q := f.recorded()[0].URL.Query()
	if f.recorded()[0].URL.Path != "/v2/price_feeds" || q.Get("query") != "btc" || q.Get("asset_type") != "crypto" {
		t.Fatalf("request %s", f.recorded()[0].URL)
	}
}

func TestPriceFeedKeepsBasePathAndNormalizesID(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, feedMetaJSON) })
	c := newTestClient(t, f.URL+"/hermes", Options{})

	if _, err := c.PriceFeed(t.Context(), "0x"+strings.ToUpper(btc)); err != nil {
		t.Fatal(err)
	}

	if got := f.recorded()[0].URL.Path; got != "/hermes/v2/price_feeds/"+btc {
		t.Fatalf("path %s", got)
	}
}

func TestLatestPricesBatchesIDsInOneRequest(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, envelopeJSON(parsedJSON(btc, "1", 5), parsedJSON(eth, "2", 5)))
	})
	c := newTestClient(t, f.URL, Options{})

	got, err := c.LatestPrices(t.Context(), []string{btc, "0x" + eth, btc}, PriceQuery{IgnoreInvalid: true})
	if err != nil || len(got) != 2 {
		t.Fatalf("got %+v, %v", got, err)
	}

	reqs := f.recorded()
	if len(reqs) != 1 {
		t.Fatalf("%d requests", len(reqs))
	}

	q := reqs[0].URL.Query()
	if ids := q["ids[]"]; len(ids) != 2 || ids[0] != btc || ids[1] != eth || q.Get("ignore_invalid_price_ids") != "true" {
		t.Fatalf("query %v", q)
	}
}

func TestInputsAreValidatedBeforeAnyRequest(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, envelopeJSON()) })
	c := newTestClient(t, f.URL, Options{})
	ctx := t.Context()

	many := make([]string, 101)
	for i := range many {
		many[i] = strings.Repeat("0", 62) + string("0123456789abcdef"[i%16]) + string("0123456789abcdef"[i/16])
	}

	calls := []func() error{
		func() error { _, err := c.LatestPrices(ctx, []string{"nope"}, PriceQuery{}); return err },
		func() error { _, err := c.PricesAt(ctx, 1, many, PriceQuery{}); return err },
		func() error { _, err := c.PricesAt(ctx, -1, []string{btc}, PriceQuery{}); return err },
		func() error { _, err := c.PricesInInterval(ctx, 1, 61, []string{btc}, IntervalQuery{}); return err },
		func() error {
			_, err := c.Candles(ctx, CandlesQuery{Feed: "x", Resolution: "60", From: 10, To: 5})
			return err
		},
		func() error { _, err := c.Candles(ctx, CandlesQuery{Resolution: "60"}); return err },
		func() error { _, err := c.Candles(ctx, CandlesQuery{Feed: "x", Resolution: "1 D", To: 1}); return err },
		func() error { _, err := c.Candles(ctx, CandlesQuery{Feed: "x", Resolution: "١", To: 1}); return err },
		func() error {
			_, err := c.Candles(ctx, CandlesQuery{Feed: "x", Resolution: "123456789", To: 1})
			return err
		},
	}

	var ve *ValidationError

	for i, call := range calls {
		if err := call(); !errors.As(err, &ve) {
			t.Errorf("call %d: err = %v", i, err)
		}
	}

	if n := len(f.recorded()); n != 0 {
		t.Fatalf("%d requests sent", n)
	}
}

func TestHistoricalRoutes(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/updates/price/100" {
			writeJSON(w, 200, envelopeJSON(parsedJSON(btc, "1", 100)))

			return
		}

		writeJSON(w, 200, []any{envelopeJSON(parsedJSON(btc, "1", 100)), envelopeJSON(parsedJSON(btc, "2", 101))})
	})
	c := newTestClient(t, f.URL, Options{})

	at, err := c.PricesAt(t.Context(), 100, []string{btc}, PriceQuery{})
	if err != nil || at[0].Price.PublishTime != 100 {
		t.Fatalf("at: %+v, %v", at, err)
	}

	series, err := c.PricesInInterval(t.Context(), 100, 1, []string{btc}, IntervalQuery{KeepAllUpdatesPerSecond: true})
	if err != nil || len(series) != 2 || series[1].Price.Price != "2" {
		t.Fatalf("interval: %+v, %v", series, err)
	}

	req := f.recorded()[1]
	if req.URL.Path != "/v2/updates/price/100/1" || req.URL.Query().Get("unique") != "false" {
		t.Fatalf("request %s", req.URL)
	}
}

func TestNativeRoutes(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/feeds":
			writeJSON(w, 200, []any{map[string]any{
				"id": btcAstra, "pyth_id": btc, "symbol": "Crypto.BTC/USD", "category": "crypto", "attributes": map[string]any{},
				"live": map[string]any{"status": "trading", "price": "1", "conf": "0", "expo": -2, "publish_time": 1, "timestamp_ms": 1000, "served_publish_time": 1, "sources": 3},
			}})
		case "/v1/status":
			writeJSON(w, 200, map[string]any{"ready": true, "feeds": []any{}})
		default:
			writeJSON(w, 200, map[string]any{"s": "ok", "t": []int{0}, "o": []float64{1}, "h": []float64{2}, "l": []float64{0.5}, "c": []float64{1.5}, "v": []int{0}})
		}
	})
	c := newTestClient(t, f.URL, Options{})
	ctx := t.Context()

	feeds, err := c.Feeds(ctx, "crypto")
	if err != nil || feeds[0].Live.Price.Float64() != 0.01 {
		t.Fatalf("feeds: %+v, %v", feeds, err)
	}

	status, err := c.Status(ctx)
	if err != nil || !status.Ready {
		t.Fatalf("status: %+v, %v", status, err)
	}

	candles, err := c.Candles(ctx, CandlesQuery{Feed: "Crypto.BTC/USD", Resolution: "60", From: 0, To: 60})
	if err != nil || len(candles) != 1 {
		t.Fatalf("candles: %+v, %v", candles, err)
	}

	q := f.recorded()[2].URL.Query()
	if q.Get("feed") != "Crypto.BTC/USD" || q.Get("resolution") != "60" || q.Get("from") != "0" || q.Get("to") != "60" {
		t.Fatalf("candles query %v", q)
	}
}

func TestHermesTextErrorIsNotRetried(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) { writeText(w, 404, "Price ids not found: "+btc) })
	c := newTestClient(t, f.URL, Options{})

	_, err := c.LatestPrices(t.Context(), []string{btc}, PriceQuery{})

	var he *HTTPError
	if !errors.As(err, &he) || he.Status != 404 || !strings.Contains(he.Body, "Price ids not found") || he.Retryable() {
		t.Fatalf("err = %v", err)
	}

	if n := len(f.recorded()); n != 1 {
		t.Fatalf("%d requests", n)
	}
}

func TestUDFErrorMessage(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 404, map[string]any{"s": "error", "errmsg": "unknown feed"})
	})
	c := newTestClient(t, f.URL, Options{})

	_, err := c.Candles(t.Context(), CandlesQuery{Feed: "nope", Resolution: "60", To: 1})
	if err == nil || !strings.Contains(err.Error(), "unknown feed") {
		t.Fatalf("err = %v", err)
	}
}

func TestProblemIsRetriedThenReturned(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"type":"about:blank","title":"History unavailable","status":503,"detail":"charts database down"}`))
	})
	c := newTestClient(t, f.URL, Options{MaxRetries: 2, MaxRetryDelay: 50 * time.Millisecond})

	_, err := c.PricesAt(t.Context(), 1, []string{btc}, PriceQuery{})

	var he *HTTPError
	if !errors.As(err, &he) || he.Problem == nil || he.Problem.Detail != "charts database down" {
		t.Fatalf("err = %v", err)
	}

	if n := len(f.recorded()); n != 3 {
		t.Fatalf("%d requests, want 3", n)
	}
}

func TestRetryAfterIsHonoured(t *testing.T) {
	var calls atomic.Int32

	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			writeText(w, 429, "slow down")

			return
		}

		writeJSON(w, 200, envelopeJSON(parsedJSON(btc, "1", 1)))
	})
	c := newTestClient(t, f.URL, Options{MaxRetryDelay: 2 * time.Second})

	start := time.Now()
	if _, err := c.LatestPrices(t.Context(), []string{btc}, PriceQuery{}); err != nil {
		t.Fatal(err)
	}

	if elapsed := time.Since(start); elapsed < 950*time.Millisecond || calls.Load() != 2 {
		t.Fatalf("elapsed %s, calls %d", elapsed, calls.Load())
	}
}

func TestRetryAfterBeyondCapIsNotAwaited(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "120")
		writeText(w, 429, "later")
	})
	c := newTestClient(t, f.URL, Options{MaxRetryDelay: time.Second})

	_, err := c.LatestPrices(t.Context(), []string{btc}, PriceQuery{})

	var he *HTTPError
	if !errors.As(err, &he) || he.RetryAfter != 120*time.Second || len(f.recorded()) != 1 {
		t.Fatalf("err = %v, requests %d", err, len(f.recorded()))
	}
}

func TestSlowRequestTimesOutAndIsRetried(t *testing.T) {
	var calls atomic.Int32

	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	})
	c := newTestClient(t, f.URL, Options{Timeout: 100 * time.Millisecond, MaxRetries: 1, MaxRetryDelay: 20 * time.Millisecond})

	_, err := c.Status(t.Context())
	if !errors.Is(err, ErrTimeout) || calls.Load() != 2 {
		t.Fatalf("err = %v, calls %d", err, calls.Load())
	}
}

func TestCallerCancellationIsNotRetried(t *testing.T) {
	f := newFake(t, func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	c := newTestClient(t, f.URL, Options{})

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	_, err := c.Status(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrTimeout) || len(f.recorded()) != 1 {
		t.Fatalf("err = %v, requests %d", err, len(f.recorded()))
	}
}

func TestResponseSizeIsBounded(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, []any{feedMetaJSON, feedMetaJSON, feedMetaJSON})
	})
	c := newTestClient(t, f.URL, Options{MaxResponseBytes: 256})

	_, err := c.PriceFeeds(t.Context(), PriceFeedsQuery{})
	if err == nil || !strings.Contains(err.Error(), "exceeds 256 bytes") {
		t.Fatalf("err = %v", err)
	}
}

func TestMalformedBodiesAreRejected(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/status" {
			writeText(w, 200, "<html>")

			return
		}

		writeJSON(w, 200, map[string]any{"parsed": []any{map[string]any{"id": btc, "price": map[string]any{"price": "1.5", "conf": "0", "expo": -8, "publish_time": 1}}}})
	})
	c := newTestClient(t, f.URL, Options{})

	var ve *ValidationError
	if _, err := c.Status(t.Context()); !errors.As(err, &ve) {
		t.Errorf("status err = %v", err)
	}

	if _, err := c.LatestPrices(t.Context(), []string{btc}, PriceQuery{}); !errors.As(err, &ve) {
		t.Errorf("latest err = %v", err)
	}
}

func TestNewValidatesOptions(t *testing.T) {
	for _, opts := range []Options{{BaseURL: "ftp://x"}, {BaseURL: "not a url"}, {Timeout: -1}} {
		if _, err := New(opts); err == nil {
			t.Errorf("New(%+v) accepted", opts)
		}
	}

	c, err := New(Options{})
	if err != nil || c.base.String() != DefaultBaseURL {
		t.Fatalf("default client: %v", err)
	}
}

func TestFullJitterAndRetryAfter(t *testing.T) {
	for attempt := range 40 {
		if d := fullJitter(attempt, 500*time.Millisecond, 30*time.Second); d < 0 || d >= 30*time.Second {
			t.Fatalf("attempt %d: %s", attempt, d)
		}
	}

	if d := fullJitter(0, 500*time.Millisecond, 30*time.Second); d >= 500*time.Millisecond {
		t.Fatalf("first attempt %s", d)
	}

	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	if parseRetryAfter("3", now) != 3*time.Second || parseRetryAfter("soon", now) != 0 {
		t.Fatal("seconds form")
	}

	if parseRetryAfter("Mon, 28 Sep 2026 00:00:05 GMT", now) != 5*time.Second {
		t.Fatal("date form")
	}
}

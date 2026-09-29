package astra

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const (
	btc      = "e62df6c8b4a85fe1a67db44dc12de5db330f7ac66b72dc658afedf0f4a415b43"
	eth      = "ff61491a931112ddf1bd8147cd1b641375f79f5825126d665480874634fd0ace"
	btcAstra = "1de769477ecf66f69ca287676658956a211c207a9ca608ad5235bd095b02f4b2"
)

func priceJSON(value string, publishTime int64) map[string]any {
	return map[string]any{"price": value, "conf": "1000", "expo": -8, "publish_time": publishTime}
}

func parsedJSON(id, value string, publishTime int64) map[string]any {
	return map[string]any{
		"id":        id,
		"price":     priceJSON(value, publishTime),
		"ema_price": priceJSON(value, publishTime),
		"metadata":  map[string]any{"slot": 0, "proof_available_time": publishTime, "prev_publish_time": publishTime - 1},
	}
}

func envelopeJSON(items ...map[string]any) map[string]any {
	return map[string]any{"binary": map[string]any{"encoding": "hex", "data": []string{}}, "parsed": items}
}

func wsUpdateJSON(id, value string, publishTime int64) []byte {
	b, _ := json.Marshal(map[string]any{
		"type": "price_update",
		"price_feed": map[string]any{
			"id":        id,
			"price":     priceJSON(value, publishTime),
			"ema_price": priceJSON(value, publishTime),
			"metadata":  map[string]any{"emitter_chain": 0, "price_service_receive_time": publishTime, "prev_publish_time": publishTime - 1},
		},
	})

	return b
}

var ackJSON = []byte(`{"type":"response","status":"success"}`)

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeText(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = fmt.Fprint(w, body)
}

type fakeAstra struct {
	*httptest.Server
	mu       sync.Mutex
	requests []*http.Request
	wsConns  atomic.Int32
}

func (f *fakeAstra) recorded() []*http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]*http.Request(nil), f.requests...)
}

func newFake(t *testing.T, handler http.HandlerFunc) *fakeAstra {
	t.Helper()

	f := &fakeAstra{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.Clone(context.Background()))
		f.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(f.Close)

	return f
}

func newWSFake(t *testing.T, serve func(ctx context.Context, conn *websocket.Conn, n int32)) *fakeAstra {
	t.Helper()

	var f *fakeAstra

	f = newFake(t, func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()

		serve(r.Context(), conn, f.wsConns.Add(1))
	})

	return f
}

func readSubscribe(ctx context.Context, t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()

	_, data, err := conn.Read(ctx)
	if err != nil {
		return nil
	}

	var m map[string]any
	if err = json.Unmarshal(data, &m); err != nil {
		t.Errorf("subscribe is not JSON: %v", err)
	}

	return m
}

func newTestClient(t *testing.T, baseURL string, opts Options) *Client {
	t.Helper()

	opts.BaseURL = baseURL
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Transport: &http.Transport{}}
	}

	c, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(opts.HTTPClient.CloseIdleConnections)

	return c
}

func receive(t *testing.T, sub *Subscription, n int) []PriceUpdate {
	t.Helper()

	out := make([]PriceUpdate, 0, n)
	timeout := time.After(10 * time.Second)

	for len(out) < n {
		select {
		case u, ok := <-sub.Updates():
			if !ok {
				return out
			}

			out = append(out, u)
		case <-timeout:
			t.Fatalf("timed out after %d of %d updates", len(out), n)
		}
	}

	return out
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}

		time.Sleep(5 * time.Millisecond)
	}
}

func settleGoroutines(t *testing.T, baseline int) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > baseline {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<20)
			t.Fatalf("goroutines leaked: %d > %d\n%s", runtime.NumGoroutine(), baseline, buf[:runtime.Stack(buf, true)])
		}

		time.Sleep(10 * time.Millisecond)
	}
}

var fastReconnect = SubscribeOptions{ReconnectBaseDelay: 10 * time.Millisecond, ReconnectMaxDelay: 30 * time.Millisecond}

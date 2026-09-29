package astra

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func sseHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()
}

func sseEvent(w http.ResponseWriter, body any) {
	b, _ := json.Marshal(body)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
	w.(http.Flusher).Flush()
}

type recorder struct {
	mu     sync.Mutex
	errs   []error
	states []ConnectionState
}

func (r *recorder) options(base SubscribeOptions) SubscribeOptions {
	base.OnError = func(err error) {
		r.mu.Lock()
		r.errs = append(r.errs, err)
		r.mu.Unlock()
	}
	base.OnStateChange = func(s ConnectionState) {
		r.mu.Lock()
		r.states = append(r.states, s)
		r.mu.Unlock()
	}

	return base
}

func (r *recorder) errors() []error {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]error(nil), r.errs...)
}

func (r *recorder) stateList() []ConnectionState {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]ConnectionState(nil), r.states...)
}

func TestSSEReaderHandlesFramingAndLimits(t *testing.T) {
	in := ": keepalive\r\n\r\ndata: {\"a\":1}\r\n\r\ndata: x\ndata:y\nevent: e\nid: 1\n\n"
	r := sseReader{br: bufio.NewReaderSize(strings.NewReader(in), 16), max: 1024, touch: func() {}}

	var got []string

	for {
		data, err := r.next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			t.Fatal(err)
		}

		got = append(got, string(data))
	}

	if strings.Join(got, "|") != `{"a":1}|x`+"\n"+`y` {
		t.Fatalf("got %q", got)
	}

	long := sseReader{br: bufio.NewReaderSize(strings.NewReader("data: 123456789\n\n"), 16), max: 8, touch: func() {}}
	if _, err := long.next(); !errors.Is(err, errMessageTooLarge) {
		t.Fatalf("long line: %v", err)
	}

	multi := sseReader{br: bufio.NewReaderSize(strings.NewReader("data: 12345\ndata: 12345\n\n"), 16), max: 8, touch: func() {}}
	if _, err := multi.next(); !errors.Is(err, errMessageTooLarge) {
		t.Fatalf("long event: %v", err)
	}
}

func TestSSEStreamReconnectsAndDropsTheReplayedValue(t *testing.T) {
	var connects atomic.Int32

	release := make(chan struct{})

	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		sseHeaders(w)

		if connects.Add(1) == 1 {
			sseEvent(w, envelopeJSON(parsedJSON(btc, "1", 10)))

			return
		}

		select {
		case <-release:
		case <-r.Context().Done():
			return
		}

		sseEvent(w, envelopeJSON(parsedJSON(btc, "1", 10)))
		sseEvent(w, envelopeJSON(parsedJSON(btc, "2", 11)))
		<-r.Context().Done()
	})
	c := newTestClient(t, f.URL, Options{})
	rec := &recorder{}

	opts := fastReconnect
	opts.Transport = TransportSSE
	opts.Channel = ChannelRealTime

	sub, err := c.Subscribe(t.Context(), []string{btc}, rec.options(opts))
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	if got := receive(t, sub, 1); got[0].Price.Price != "1" {
		t.Fatalf("first %+v", got)
	}

	close(release)

	if got := receive(t, sub, 1); got[0].Price.Price != "2" {
		t.Fatalf("second %+v", got)
	}

	st := sub.Stats()
	if st.Duplicates != 1 || st.Reconnects != 1 || st.Connects != 2 {
		t.Fatalf("stats %+v", st)
	}

	q := f.recorded()[0].URL.Query()
	if q["ids[]"][0] != btc || q.Get("channel") != "real_time" {
		t.Fatalf("query %v", q)
	}

	if s := rec.stateList(); len(s) < 3 || s[0] != StateOpen || s[1] != StateReconnecting || s[2] != StateOpen {
		t.Fatalf("states %v", s)
	}
}

func TestSSENotFoundIsFatal(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) { writeText(w, 404, "Price ids not found: "+btc) })
	c := newTestClient(t, f.URL, Options{})

	opts := fastReconnect
	opts.Transport = TransportSSE

	sub, err := c.Subscribe(t.Context(), []string{btc}, opts)
	if err != nil {
		t.Fatal(err)
	}

	<-sub.Done()

	if _, open := <-sub.Updates(); open {
		t.Fatal("updates channel still open")
	}

	var he *HTTPError
	if !errors.As(sub.Err(), &he) || he.Status != 404 || len(f.recorded()) != 1 {
		t.Fatalf("err = %v, requests %d", sub.Err(), len(f.recorded()))
	}
}

func TestSSERetryAfterDelaysTheReconnect(t *testing.T) {
	var calls atomic.Int32

	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			writeText(w, 429, "too many streams")

			return
		}

		sseHeaders(w)
		sseEvent(w, envelopeJSON(parsedJSON(btc, "1", 1)))
		<-r.Context().Done()
	})
	c := newTestClient(t, f.URL, Options{})

	opts := fastReconnect
	opts.Transport = TransportSSE
	start := time.Now()

	sub, err := c.Subscribe(t.Context(), []string{btc}, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	receive(t, sub, 1)

	if elapsed := time.Since(start); elapsed < 950*time.Millisecond {
		t.Fatalf("reconnected after %s", elapsed)
	}
}

func TestSSEIdleStreamIsReconnected(t *testing.T) {
	var calls atomic.Int32

	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		sseHeaders(w)

		if calls.Add(1) > 1 {
			sseEvent(w, envelopeJSON(parsedJSON(btc, "1", 1)))
		}

		<-r.Context().Done()
	})
	c := newTestClient(t, f.URL, Options{})
	rec := &recorder{}

	opts := fastReconnect
	opts.Transport = TransportSSE
	opts.IdleTimeout = 150 * time.Millisecond

	sub, err := c.Subscribe(t.Context(), []string{btc}, rec.options(opts))
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	receive(t, sub, 1)

	if errs := rec.errors(); len(errs) == 0 || !errors.Is(errs[0], ErrTimeout) {
		t.Fatalf("errors %v", errs)
	}
}

func TestSSEMalformedEventIsDropped(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		sseHeaders(w)
		_, _ = fmt.Fprint(w, "data: {not json\n\n")
		bad := parsedJSON(btc, "1", 1)
		bad["price"] = map[string]any{"price": "1.5", "conf": "0", "expo": -8, "publish_time": 1}
		sseEvent(w, envelopeJSON(bad))
		sseEvent(w, envelopeJSON(parsedJSON(btc, "2", 2)))
		<-r.Context().Done()
	})
	c := newTestClient(t, f.URL, Options{})

	sub, err := c.Subscribe(t.Context(), []string{btc}, SubscribeOptions{Transport: TransportSSE, BenchmarksOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	if got := receive(t, sub, 1); got[0].Price.Price != "2" {
		t.Fatalf("got %+v", got)
	}

	if st := sub.Stats(); st.Invalid != 2 {
		t.Fatalf("stats %+v", st)
	}

	if f.recorded()[0].URL.Query().Get("benchmarks_only") != "true" {
		t.Fatal("benchmarks_only not sent")
	}
}

func TestSubscribeValidatesOptions(t *testing.T) {
	c := newTestClient(t, "http://127.0.0.1:1", Options{})

	bad := []SubscribeOptions{
		{BenchmarksOnly: true},
		{Transport: "carrier-pigeon"},
		{IdleTimeout: -1},
	}

	for _, opts := range bad {
		if _, err := c.Subscribe(t.Context(), []string{btc}, opts); err == nil {
			t.Errorf("Subscribe(%+v) accepted", opts)
		}
	}

	if _, err := c.Subscribe(t.Context(), []string{"nope"}, SubscribeOptions{}); err == nil {
		t.Error("bad id accepted")
	}
}

func TestWebSocketSubscribeStreamAndCleanClose(t *testing.T) {
	baseline := runtime.NumGoroutine()
	closedCleanly := make(chan websocket.StatusCode, 1)

	var subscribe map[string]any

	f := newWSFake(t, func(ctx context.Context, conn *websocket.Conn, _ int32) {
		subscribe = readSubscribe(ctx, t, conn)
		_ = conn.Write(ctx, websocket.MessageText, ackJSON)
		_ = conn.Write(ctx, websocket.MessageText, wsUpdateJSON(btc, "1", 1))
		_ = conn.Write(ctx, websocket.MessageText, wsUpdateJSON(eth, "2", 1))
		_, _, err := conn.Read(ctx)
		closedCleanly <- websocket.CloseStatus(err)
	})
	httpClient := &http.Client{Transport: &http.Transport{}}
	c := newTestClient(t, f.URL, Options{HTTPClient: httpClient})

	sub, err := c.Subscribe(t.Context(), []string{"0x" + btc, eth}, SubscribeOptions{Channel: ChannelFixed200ms})
	if err != nil {
		t.Fatal(err)
	}

	got := receive(t, sub, 2)
	if got[0].ID != btc || got[1].ID != eth || got[0].Metadata == nil || got[0].Metadata.ReceiveTime != 1 {
		t.Fatalf("got %+v", got)
	}

	if subscribe["type"] != "subscribe" || subscribe["verbose"] != true || fmt.Sprint(subscribe["ids"]) != fmt.Sprint([]any{btc, eth}) {
		t.Fatalf("subscribe %v", subscribe)
	}

	if f.recorded()[0].URL.Query().Get("channel") != "fixed_rate@200ms" {
		t.Fatalf("url %s", f.recorded()[0].URL)
	}

	if err = sub.Close(); err != nil {
		t.Fatal(err)
	}

	if code := <-closedCleanly; code != websocket.StatusNormalClosure {
		t.Fatalf("close code %d", code)
	}

	if sub.State() != StateClosed || sub.Err() != nil {
		t.Fatalf("state %s err %v", sub.State(), sub.Err())
	}

	httpClient.CloseIdleConnections()
	f.Close()
	settleGoroutines(t, baseline)
}

func TestWebSocketRefusedSubscriptionIsFatal(t *testing.T) {
	f := newWSFake(t, func(ctx context.Context, conn *websocket.Conn, _ int32) {
		readSubscribe(ctx, t, conn)
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response","status":"error","error":"Price feed(s) with id(s) `+btc+` not found"}`))
		_, _, _ = conn.Read(ctx)
	})
	c := newTestClient(t, f.URL, Options{})

	sub, err := c.Subscribe(t.Context(), []string{btc}, fastReconnect)
	if err != nil {
		t.Fatal(err)
	}

	<-sub.Done()

	var se *SubscriptionError
	if !errors.As(sub.Err(), &se) || f.wsConns.Load() != 1 {
		t.Fatalf("err = %v, connections %d", sub.Err(), f.wsConns.Load())
	}
}

func TestWebSocketResubscribesAfterServerClose(t *testing.T) {
	f := newWSFake(t, func(ctx context.Context, conn *websocket.Conn, n int32) {
		readSubscribe(ctx, t, conn)
		_ = conn.Write(ctx, websocket.MessageText, ackJSON)
		_ = conn.Write(ctx, websocket.MessageText, wsUpdateJSON(btc, "1", 1))

		if n == 1 {
			_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response","status":"error","error":"Connection timeout reached, reconnect"}`))
			_ = conn.Close(websocket.StatusNormalClosure, "")

			return
		}

		_ = conn.Write(ctx, websocket.MessageText, wsUpdateJSON(btc, "2", 2))
		_, _, _ = conn.Read(ctx)
	})
	c := newTestClient(t, f.URL, Options{})
	rec := &recorder{}

	sub, err := c.Subscribe(t.Context(), []string{btc}, rec.options(fastReconnect))
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	got := receive(t, sub, 2)
	if got[0].Price.Price != "1" || got[1].Price.Price != "2" || f.wsConns.Load() != 2 {
		t.Fatalf("got %+v, connections %d", got, f.wsConns.Load())
	}

	var serverErr *ServerError
	if errs := rec.errors(); len(errs) == 0 || !errors.As(errs[0], &serverErr) {
		t.Fatalf("errors %v", errs)
	}
}

func TestWebSocketAbnormalCloseReconnects(t *testing.T) {
	f := newWSFake(t, func(ctx context.Context, conn *websocket.Conn, n int32) {
		readSubscribe(ctx, t, conn)
		_ = conn.Write(ctx, websocket.MessageText, ackJSON)

		if n == 1 {
			_ = conn.Close(websocket.StatusPolicyViolation, "slow consumer")

			return
		}

		_ = conn.Write(ctx, websocket.MessageText, wsUpdateJSON(btc, "1", 1))
		_, _, _ = conn.Read(ctx)
	})
	c := newTestClient(t, f.URL, Options{})
	rec := &recorder{}

	sub, err := c.Subscribe(t.Context(), []string{btc}, rec.options(fastReconnect))
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	receive(t, sub, 1)

	if errs := rec.errors(); len(errs) == 0 || !strings.Contains(errs[0].Error(), "1008: slow consumer") {
		t.Fatalf("errors %v", errs)
	}
}

func TestWebSocketUnansweredPingReconnects(t *testing.T) {
	f := newWSFake(t, func(ctx context.Context, conn *websocket.Conn, n int32) {
		readSubscribe(ctx, t, conn)
		_ = conn.Write(ctx, websocket.MessageText, ackJSON)

		if n == 1 {
			<-ctx.Done()

			return
		}

		_ = conn.Write(ctx, websocket.MessageText, wsUpdateJSON(btc, "1", 1))
		_, _, _ = conn.Read(ctx)
	})
	c := newTestClient(t, f.URL, Options{})
	rec := &recorder{}

	opts := fastReconnect
	opts.IdleTimeout = 200 * time.Millisecond

	sub, err := c.Subscribe(t.Context(), []string{btc}, rec.options(opts))
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	receive(t, sub, 1)

	if errs := rec.errors(); len(errs) == 0 || !errors.Is(errs[0], ErrTimeout) {
		t.Fatalf("errors %v", errs)
	}
}

func TestWebSocketDropsMalformedAndUnrequestedMessages(t *testing.T) {
	f := newWSFake(t, func(ctx context.Context, conn *websocket.Conn, _ int32) {
		readSubscribe(ctx, t, conn)

		for _, m := range []string{
			string(ackJSON),
			"{broken",
			`{"type":"price_update","price_feed":{"id":"` + btc + `","price":{"price":"abc"}}}`,
			`{"type":"future_event","payload":1}`,
			string(wsUpdateJSON(eth, "9", 1)),
			string(wsUpdateJSON(btc, "1", 1)),
		} {
			_ = conn.Write(ctx, websocket.MessageText, []byte(m))
		}

		_, _, _ = conn.Read(ctx)
	})
	c := newTestClient(t, f.URL, Options{})

	sub, err := c.Subscribe(t.Context(), []string{btc}, SubscribeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	if got := receive(t, sub, 1); got[0].ID != btc {
		t.Fatalf("got %+v", got)
	}

	if st := sub.Stats(); st.Invalid != 2 {
		t.Fatalf("stats %+v", st)
	}
}

func TestWebSocketOversizedMessageEndsTheConnection(t *testing.T) {
	f := newWSFake(t, func(ctx context.Context, conn *websocket.Conn, n int32) {
		readSubscribe(ctx, t, conn)
		_ = conn.Write(ctx, websocket.MessageText, ackJSON)

		if n == 1 {
			_ = conn.Write(ctx, websocket.MessageText, []byte(strings.Repeat("x", 4096)))
		} else {
			_ = conn.Write(ctx, websocket.MessageText, wsUpdateJSON(btc, "1", 1))
		}

		_, _, _ = conn.Read(ctx)
	})
	c := newTestClient(t, f.URL, Options{})

	opts := fastReconnect
	opts.MaxMessageBytes = 1024

	sub, err := c.Subscribe(t.Context(), []string{btc}, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	receive(t, sub, 1)

	if f.wsConns.Load() != 2 {
		t.Fatalf("connections %d", f.wsConns.Load())
	}
}

func TestSlowConsumerGetsTheLatestPricePerFeed(t *testing.T) {
	f := newWSFake(t, func(ctx context.Context, conn *websocket.Conn, _ int32) {
		readSubscribe(ctx, t, conn)
		_ = conn.Write(ctx, websocket.MessageText, ackJSON)

		for i := 1; i <= 50; i++ {
			_ = conn.Write(ctx, websocket.MessageText, wsUpdateJSON(btc, fmt.Sprint(i), int64(i)))
		}

		_ = conn.Write(ctx, websocket.MessageText, wsUpdateJSON(eth, "7", 1))
		_, _, _ = conn.Read(ctx)
	})
	c := newTestClient(t, f.URL, Options{})

	sub, err := c.Subscribe(t.Context(), []string{btc, eth}, SubscribeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	eventually(t, func() bool { return sub.Stats().Coalesced >= 48 })
	time.Sleep(50 * time.Millisecond)

	var last PriceUpdate

	for u := range sub.Updates() {
		last = u
		if u.ID == eth {
			break
		}
	}

	if last.ID != eth {
		t.Fatalf("last %+v", last)
	}

	if st := sub.Stats(); st.Coalesced < 48 {
		t.Fatalf("stats %+v", st)
	}
}

func TestContextCancelClosesTheSubscription(t *testing.T) {
	baseline := runtime.NumGoroutine()

	f := newWSFake(t, func(ctx context.Context, conn *websocket.Conn, _ int32) {
		readSubscribe(ctx, t, conn)
		_ = conn.Write(ctx, websocket.MessageText, ackJSON)
		_, _, _ = conn.Read(ctx)
	})
	httpClient := &http.Client{Transport: &http.Transport{}}
	c := newTestClient(t, f.URL, Options{HTTPClient: httpClient})

	ctx, cancel := context.WithCancel(t.Context())

	sub, err := c.Subscribe(ctx, []string{btc}, SubscribeOptions{})
	if err != nil {
		t.Fatal(err)
	}

	eventually(t, func() bool { return sub.State() == StateOpen })
	cancel()
	<-sub.Done()

	if _, open := <-sub.Updates(); open {
		t.Fatal("updates channel still open")
	}

	httpClient.CloseIdleConnections()
	f.Close()
	settleGoroutines(t, baseline)
}

func TestManySubscriptionsLeaveNoGoroutines(t *testing.T) {
	baseline := runtime.NumGoroutine()

	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		sseHeaders(w)
		sseEvent(w, envelopeJSON(parsedJSON(btc, "1", 1)))
		<-r.Context().Done()
	})
	httpClient := &http.Client{Transport: &http.Transport{}}
	c := newTestClient(t, f.URL, Options{HTTPClient: httpClient})

	for range 20 {
		sub, err := c.Subscribe(t.Context(), []string{btc}, SubscribeOptions{Transport: TransportSSE})
		if err != nil {
			t.Fatal(err)
		}

		receive(t, sub, 1)

		if err = sub.Close(); err != nil {
			t.Fatal(err)
		}
	}

	httpClient.CloseIdleConnections()
	f.Close()
	settleGoroutines(t, baseline)
}

func TestCoalescingQueueIsBoundedByTheSubscribedIDs(t *testing.T) {
	ids := []string{btc, eth, btcAstra}
	s := &Subscription{
		wanted:  map[string]struct{}{btc: {}, eth: {}, btcAstra: {}},
		pending: map[string]PriceUpdate{},
		last:    map[string]lastSeen{},
		notify:  make(chan struct{}, 1),
		queue:   make([]string, len(ids)),
	}

	for i := 1; i <= 10_000; i++ {
		for _, id := range ids {
			s.accept(PriceUpdate{ID: id, Price: Price{Price: fmt.Sprint(i), PublishTime: int64(i)}})
		}

		u, ok, _ := s.pop()
		if !ok || u.ID != ids[(i-1)%len(ids)] || u.Price.PublishTime != int64(i) {
			t.Fatalf("pop %d: %+v, %v", i, u, ok)
		}
	}

	if len(s.queue) != len(ids) || cap(s.queue) != len(ids) || s.queued != len(ids)-1 || len(s.pending) != len(ids)-1 {
		t.Fatalf("queue %d/%d, queued %d, pending %d", len(s.queue), cap(s.queue), s.queued, len(s.pending))
	}

	if st := s.Stats(); st.Coalesced == 0 {
		t.Fatalf("stats %+v", st)
	}
}

func TestWebSocketUnacknowledgedSubscriptionReconnects(t *testing.T) {
	f := newWSFake(t, func(ctx context.Context, conn *websocket.Conn, n int32) {
		readSubscribe(ctx, t, conn)

		if n == 1 {
			_, _, _ = conn.Read(ctx)

			return
		}

		_ = conn.Write(ctx, websocket.MessageText, ackJSON)
		_ = conn.Write(ctx, websocket.MessageText, wsUpdateJSON(btc, "1", 1))
		_, _, _ = conn.Read(ctx)
	})
	c := newTestClient(t, f.URL, Options{})
	rec := &recorder{}

	opts := fastReconnect
	opts.IdleTimeout = 300 * time.Millisecond

	sub, err := c.Subscribe(t.Context(), []string{btc}, rec.options(opts))
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	receive(t, sub, 1)

	errs := rec.errors()
	if len(errs) == 0 || !errors.Is(errs[0], ErrTimeout) || !strings.Contains(errs[0].Error(), "subscription response") {
		t.Fatalf("errors %v", errs)
	}

	if f.wsConns.Load() != 2 || sub.Stats().Connects != 1 {
		t.Fatalf("connections %d, stats %+v", f.wsConns.Load(), sub.Stats())
	}
}

func TestSSEIgnoresTheHTTPClientTimeout(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		sseHeaders(w)
		sseEvent(w, envelopeJSON(parsedJSON(btc, "1", 1)))

		select {
		case <-time.After(300 * time.Millisecond):
		case <-r.Context().Done():
			return
		}

		sseEvent(w, envelopeJSON(parsedJSON(btc, "2", 2)))
		<-r.Context().Done()
	})
	c := newTestClient(t, f.URL, Options{HTTPClient: &http.Client{Transport: &http.Transport{}, Timeout: 100 * time.Millisecond}})
	rec := &recorder{}

	sub, err := c.Subscribe(t.Context(), []string{btc}, rec.options(SubscribeOptions{Transport: TransportSSE}))
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	if got := receive(t, sub, 2); got[1].Price.Price != "2" {
		t.Fatalf("got %+v", got)
	}

	if st := sub.Stats(); st.Connects != 1 || len(rec.errors()) != 0 {
		t.Fatalf("stats %+v, errors %v", st, rec.errors())
	}
}

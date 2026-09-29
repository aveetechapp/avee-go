package astra

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
)

func feedIDs(n int) []string {
	ids := make([]string, 0, n)
	for i := range n {
		ids = append(ids, fmt.Sprintf("%064x", i+1))
	}

	return ids
}

func echoLatest(w http.ResponseWriter, r *http.Request) {
	asked := r.URL.Query()["ids[]"]
	items := make([]map[string]any, 0, len(asked))

	for _, id := range asked {
		items = append(items, parsedJSON(id, "1", 5))
	}

	writeJSON(w, 200, envelopeJSON(items...))
}

func TestLatestPricesSplitsIDsIntoURLSizedChunks(t *testing.T) {
	cases := []struct {
		chunks []int
		n      int
	}{
		{n: 199, chunks: []int{199}},
		{n: 200, chunks: []int{200}},
		{n: 201, chunks: []int{200, 1}},
		{n: 450, chunks: []int{200, 200, 50}},
	}

	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.n), func(t *testing.T) {
			f := newFake(t, echoLatest)
			c := newTestClient(t, f.URL, Options{})
			ids := feedIDs(tc.n)

			got, err := c.LatestPrices(t.Context(), ids, PriceQuery{IgnoreInvalid: true})
			if err != nil {
				t.Fatal(err)
			}

			reqs := f.recorded()
			if len(reqs) != len(tc.chunks) {
				t.Fatalf("%d requests, want %d", len(reqs), len(tc.chunks))
			}

			for i, r := range reqs {
				q := r.URL.Query()
				if len(q["ids[]"]) != tc.chunks[i] || q.Get("ignore_invalid_price_ids") != "true" {
					t.Fatalf("request %d carries %d ids, flags %v", i, len(q["ids[]"]), q)
				}
			}

			if len(got) != tc.n {
				t.Fatalf("%d updates, want %d", len(got), tc.n)
			}

			for i := range got {
				if got[i].ID != ids[i] {
					t.Fatalf("update %d is %s, want %s", i, got[i].ID, ids[i])
				}
			}
		})
	}
}

func TestLatestPricesDeduplicatesAcrossTheChunkBoundary(t *testing.T) {
	f := newFake(t, echoLatest)
	c := newTestClient(t, f.URL, Options{})

	ids := feedIDs(201)
	asked := append(append([]string{}, ids...), "0x"+ids[0], ids[200])

	got, err := c.LatestPrices(t.Context(), asked, PriceQuery{})
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 201 || got[200].ID != ids[200] || len(f.recorded()) != 2 {
		t.Fatalf("%d updates over %d requests", len(got), len(f.recorded()))
	}
}

func TestLatestPricesFailsWholeOnAFailedChunk(t *testing.T) {
	var calls atomic.Int32

	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 2 {
			writeText(w, 404, "Price ids not found: "+r.URL.Query()["ids[]"][0])

			return
		}

		echoLatest(w, r)
	})
	c := newTestClient(t, f.URL, Options{})

	got, err := c.LatestPrices(t.Context(), feedIDs(450), PriceQuery{})

	var he *HTTPError
	if !errors.As(err, &he) || he.Status != 404 || got != nil {
		t.Fatalf("got %d updates, err %v", len(got), err)
	}

	if n := len(f.recorded()); n != 2 {
		t.Fatalf("%d requests after the failed chunk", n)
	}
}

func TestLatestPricesKeepsTheServerLimit(t *testing.T) {
	f := newFake(t, echoLatest)
	c := newTestClient(t, f.URL, Options{})

	var ve *ValidationError
	if _, err := c.LatestPrices(t.Context(), feedIDs(MaxIDsPerRequest+1), PriceQuery{}); !errors.As(err, &ve) {
		t.Fatalf("err = %v", err)
	}

	if n := len(f.recorded()); n != 0 {
		t.Fatalf("%d requests", n)
	}
}

func TestSSESubscribeRefusesMoreIDsThanAURLCarries(t *testing.T) {
	c := newTestClient(t, "http://127.0.0.1:1", Options{})

	sub, err := c.Subscribe(t.Context(), feedIDs(MaxIDsPerURL), SubscribeOptions{Transport: TransportSSE})
	if err != nil {
		t.Fatal(err)
	}

	_ = sub.Close()

	_, err = c.Subscribe(t.Context(), feedIDs(MaxIDsPerURL+1), SubscribeOptions{Transport: TransportSSE})

	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Reason != `SSE carries feed ids in the URL: at most 200, got 201; use transport "ws" for more` {
		t.Fatalf("err = %v", err)
	}
}

func TestWebSocketSubscribeTakesMoreIDsThanAURLCarries(t *testing.T) {
	asked := make(chan int, 1)

	f := newWSFake(t, func(ctx context.Context, conn *websocket.Conn, _ int32) {
		m := readSubscribe(ctx, t, conn)
		ids, _ := m["ids"].([]any)
		asked <- len(ids)

		_ = conn.Write(ctx, websocket.MessageText, ackJSON)
		_, _, _ = conn.Read(ctx)
	})
	c := newTestClient(t, f.URL, Options{})

	sub, err := c.Subscribe(t.Context(), feedIDs(MaxIDsPerURL+1), SubscribeOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if n := <-asked; n != MaxIDsPerURL+1 {
		t.Fatalf("subscribed %d ids", n)
	}

	if err = sub.Close(); err != nil {
		t.Fatal(err)
	}
}

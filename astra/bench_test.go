package astra

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func BenchmarkDecodeWebSocketUpdate(b *testing.B) {
	data := wsUpdateJSON(btc, "8309544250000", 1790574780)
	sub := &Subscription{wanted: map[string]struct{}{}}

	var (
		msg  wireStreamMessage
		feed wireUpdate[streamMetadata]
		ack  = false
	)

	b.ReportAllocs()
	b.SetBytes(int64(len(data)))

	for b.Loop() {
		if err := handleWSMessage(data, &msg, &feed, &ack, sub); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeLatest20(b *testing.B) {
	items := make([]map[string]any, 20)
	for i := range items {
		items[i] = parsedJSON(btc, "8309544250000", 1790574780)
	}

	data, err := json.Marshal(envelopeJSON(items...))
	if err != nil {
		b.Fatal(err)
	}

	out := make([]PriceUpdate, 0, 20)

	b.ReportAllocs()
	b.SetBytes(int64(len(data)))

	for b.Loop() {
		var w wireEnvelope
		if err = json.Unmarshal(data, &w); err != nil {
			b.Fatal(err)
		}

		if out, err = w.decode(out[:0]); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLatestPrices20(b *testing.B) {
	ids := make([]string, 20)
	items := make([]map[string]any, 20)

	for i := range items {
		ids[i] = fmt.Sprintf("%062x%02x", 0, i)
		items[i] = parsedJSON(ids[i], "8309544250000", 1790574780)
	}

	body, err := json.Marshal(envelopeJSON(items...))
	if err != nil {
		b.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	httpClient := &http.Client{Transport: &http.Transport{}}
	defer httpClient.CloseIdleConnections()

	c, err := New(Options{BaseURL: srv.URL, HTTPClient: httpClient})
	if err != nil {
		b.Fatal(err)
	}

	ctx := context.Background()

	b.ReportAllocs()
	b.SetBytes(int64(len(body)))

	for b.Loop() {
		if _, err = c.LatestPrices(ctx, ids, PriceQuery{}); err != nil {
			b.Fatal(err)
		}
	}
}

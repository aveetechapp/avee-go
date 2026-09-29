package astra

import (
	"os"
	"testing"
	"time"
)

func liveClient(t *testing.T) *Client {
	t.Helper()

	if os.Getenv("ASTRA_LIVE") != "1" {
		t.Skip("set ASTRA_LIVE=1 to run against a real Astra")
	}

	base := os.Getenv("ASTRA_BASE_URL")
	if base == "" {
		base = "https://astra.preview.avee.tech"
	}

	c, err := New(Options{BaseURL: base})
	if err != nil {
		t.Fatal(err)
	}

	return c
}

func TestLiveREST(t *testing.T) {
	c := liveClient(t)
	ctx := t.Context()

	feeds, err := c.PriceFeeds(ctx, PriceFeedsQuery{Query: "btc", AssetType: "crypto"})
	if err != nil || len(feeds) == 0 {
		t.Fatalf("feeds: %v", err)
	}

	latest, err := c.LatestPrices(ctx, []string{btc}, PriceQuery{})
	if err != nil || latest[0].Price.Float64() <= 0 {
		t.Fatalf("latest: %+v, %v", latest, err)
	}

	if _, err = c.Status(ctx); err != nil {
		t.Fatal(err)
	}

	now := time.Now().Unix()

	candles, err := c.Candles(ctx, CandlesQuery{Feed: "Crypto.BTC/USD", Resolution: "60", From: now - 6*3600, To: now})
	if err != nil || len(candles) == 0 {
		t.Fatalf("candles: %v", err)
	}

	if _, err = c.PricesAt(ctx, now-600, []string{btc}, PriceQuery{}); err != nil {
		t.Fatal(err)
	}
}

func TestLiveStreams(t *testing.T) {
	c := liveClient(t)

	for _, transport := range []Transport{TransportWebSocket, TransportSSE} {
		t.Run(string(transport), func(t *testing.T) {
			sub, err := c.Subscribe(t.Context(), []string{btc}, SubscribeOptions{Transport: transport})
			if err != nil {
				t.Fatal(err)
			}
			defer sub.Close()

			if u := receive(t, sub, 1); u[0].ID != btc {
				t.Fatalf("got %+v", u)
			}
		})
	}
}

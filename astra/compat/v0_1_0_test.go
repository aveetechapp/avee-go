package compat_test

import (
	"context"
	"errors"
	"math/big"
	"net/http"
	"testing"
	"time"

	astra "github.com/aveetechapp/avee-go/astra"
)

var (
	_ = astra.DefaultBaseURL
	_ = astra.MaxIntervalSeconds
	_ = astra.FeedIDsPerRequest
	_ = astra.MaxIDsPerRequest
	_ = astra.MaxIDsPerURL
	_ = astra.MaxHistoricalFeeds
	_ = astra.MinExpo
	_ = astra.MaxExpo
	_ = astra.MaxScaleDecimals
	_ = astra.ErrTimeout

	_ error = (*astra.HTTPError)(nil)
	_ error = (*astra.ValidationError)(nil)
	_ error = (*astra.SubscriptionError)(nil)
	_ error = (*astra.ServerError)(nil)

	_ func(astra.Options) (*astra.Client, error)                                                           = astra.New
	_ func(string) (string, error)                                                                         = astra.NormalizeFeedID
	_ func(*astra.Client, context.Context, astra.PriceFeedsQuery) ([]astra.FeedMetadata, error)            = (*astra.Client).PriceFeeds
	_ func(*astra.Client, context.Context, string) (astra.FeedMetadata, error)                             = (*astra.Client).PriceFeed
	_ func(*astra.Client, context.Context, []string, astra.PriceQuery) ([]astra.PriceUpdate, error)        = (*astra.Client).LatestPrices
	_ func(*astra.Client, context.Context, int64, []string, astra.PriceQuery) ([]astra.PriceUpdate, error) = (*astra.Client).PricesAt
	_ func(*astra.Client, context.Context, string) ([]astra.Feed, error)                                   = (*astra.Client).Feeds
	_ func(*astra.Client, context.Context, astra.FeedIDsOptions) (astra.FeedIDMap, error)                  = (*astra.Client).FeedIDs
	_ func(*astra.Client, context.Context, ...astra.StatusOptions) (astra.StatusReport, error)             = (*astra.Client).Status
	_ func(*astra.Client, context.Context, astra.CandlesQuery) ([]astra.Candle, error)                     = (*astra.Client).Candles
	_ func(*astra.Client, context.Context, []string, astra.SubscribeOptions) (*astra.Subscription, error)  = (*astra.Client).Subscribe
	_ func(astra.Price) float64                                                                            = astra.Price.Float64
	_ func(astra.Price) string                                                                             = astra.Price.Decimal
	_ func(astra.Price, int) (*big.Int, error)                                                             = astra.Price.Scaled

	_ = []astra.FeedStatus{astra.StatusTrading, astra.StatusDegraded, astra.StatusMarketClosed, astra.StatusReference, astra.StatusNoData}
	_ = []astra.Channel{astra.ChannelRealTime, astra.ChannelFixed200ms, astra.ChannelFixed1000ms}
	_ = []astra.ConnectionState{astra.StateConnecting, astra.StateOpen, astra.StateReconnecting, astra.StateClosed}
)

func newV010Client(t *testing.T, baseURL string) *astra.Client {
	t.Helper()

	c, err := astra.New(astra.Options{
		BaseURL:          baseURL,
		HTTPClient:       &http.Client{Transport: &http.Transport{}},
		Header:           http.Header{"X-Consumer": []string{"compat-v0.1.0"}},
		Timeout:          5 * time.Second,
		MaxRetries:       1,
		MaxRetryDelay:    50 * time.Millisecond,
		MaxResponseBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}

	return c
}

func TestV010REST(t *testing.T) {
	c := newV010Client(t, startFake(t))
	ctx := t.Context()

	metas, err := c.PriceFeeds(ctx, astra.PriceFeedsQuery{Query: "btc", AssetType: "crypto"})
	if err != nil || len(metas) != 1 || metas[0].Symbol != "Crypto.BTC/USD" || !metas[0].MarketOpen || metas[0].AstraID != btcAstra {
		t.Fatalf("PriceFeeds: %+v, %v", metas, err)
	}

	meta, err := c.PriceFeed(ctx, "0x"+btc)
	if err != nil || meta.ID != btc || meta.AssetType != "Crypto" || meta.DisplaySymbol != "BTC/USD" {
		t.Fatalf("PriceFeed: %+v, %v", meta, err)
	}

	updates, err := c.LatestPrices(ctx, []string{"0x" + btc, eth}, astra.PriceQuery{IgnoreInvalid: true})
	if err != nil || len(updates) != 2 || updates[0].ID != btc || updates[0].Price.Expo != -8 || updates[0].Price.Decimal() != "65000" {
		t.Fatalf("LatestPrices: %+v, %v", updates, err)
	}

	if updates[0].Metadata == nil || updates[0].Metadata.PrevPublishTime != 1699999999 || updates[0].EMAPrice.PublishTime != 1700000000 {
		t.Fatalf("LatestPrices metadata: %+v", updates[0])
	}

	if _, err = c.PricesAt(ctx, 1700000000, []string{btc}, astra.PriceQuery{}); err != nil {
		t.Fatalf("PricesAt: %v", err)
	}

	if _, err = c.PricesInInterval(ctx, 1700000000, astra.MaxIntervalSeconds, []string{btc}, astra.IntervalQuery{KeepAllUpdatesPerSecond: true}); err != nil {
		t.Fatalf("PricesInInterval: %v", err)
	}

	feeds, err := c.Feeds(ctx, "crypto")
	if err != nil || len(feeds) != 1 || feeds[0].PythID != btc || feeds[0].Live.Price == nil || feeds[0].Live.Status != astra.StatusTrading {
		t.Fatalf("Feeds: %+v, %v", feeds, err)
	}

	candles, err := c.Candles(ctx, astra.CandlesQuery{Feed: "Crypto.BTC/USD", Resolution: "60", From: 0, To: 60})
	if err != nil || len(candles) != 1 || candles[0].High != 2 {
		t.Fatalf("Candles: %+v, %v", candles, err)
	}
}

func TestV010FeedIDsAndStatus(t *testing.T) {
	c := newV010Client(t, startFake(t))
	ctx := t.Context()

	ids, err := c.FeedIDs(ctx, astra.FeedIDsOptions{PythIDs: []string{btc, unknown}})
	if err != nil || len(ids.Items) != 1 || ids.Items[0].AstraID != btcAstra || ids.Items[0].PythID != btc || len(ids.Missing) != 1 {
		t.Fatalf("FeedIDs: %+v, %v", ids, err)
	}

	report, err := c.Status(ctx)
	if err != nil || !report.Ready || len(report.Feeds) != 1 || report.Feeds[0].Stale {
		t.Fatalf("Status: %+v, %v", report, err)
	}

	stale, err := c.Status(ctx, astra.StatusOptions{Feed: btcAstra})
	if err != nil || len(stale.Feeds) != 1 || !stale.Feeds[0].Stale {
		t.Fatalf("Status(feed) on 503: %+v, %v", stale, err)
	}
}

func TestV010Errors(t *testing.T) {
	c := newV010Client(t, startFake(t))
	ctx := t.Context()

	var httpErr *astra.HTTPError
	if _, err := c.PriceFeed(ctx, unknown); !errors.As(err, &httpErr) || httpErr.Status != http.StatusNotFound || httpErr.Retryable() {
		t.Fatalf("unknown feed: %v", err)
	}

	var validation *astra.ValidationError
	if _, err := c.LatestPrices(ctx, []string{"not-a-feed-id"}, astra.PriceQuery{}); !errors.As(err, &validation) {
		t.Fatalf("malformed id: %v", err)
	}

	if _, err := astra.NormalizeFeedID("0x" + btc); err != nil {
		t.Fatalf("NormalizeFeedID: %v", err)
	}

	if _, err := astra.New(astra.Options{BaseURL: "::"}); err == nil {
		t.Fatal("bad base URL accepted")
	}
}

func TestV010PriceHelpers(t *testing.T) {
	p := astra.Price{Price: "6500012345678", Conf: "1000", Expo: -8, PublishTime: 1}

	if p.Decimal() != "65000.12345678" || p.Float64() != 65000.12345678 || p.ConfFloat64() != 0.00001 {
		t.Fatalf("price %s %v %v", p.Decimal(), p.Float64(), p.ConfFloat64())
	}

	scaled, err := p.Scaled(2)
	if err != nil || scaled.String() != "6500012" {
		t.Fatalf("Scaled: %v, %v", scaled, err)
	}

	if _, err = p.ConfScaled(18); err != nil {
		t.Fatalf("ConfScaled: %v", err)
	}
}

func TestV010Subscribe(t *testing.T) {
	for _, transport := range []astra.Transport{astra.TransportWebSocket, astra.TransportSSE} {
		t.Run(string(transport), func(t *testing.T) {
			c := newV010Client(t, startFake(t))

			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()

			sub, err := c.Subscribe(ctx, []string{"0x" + btc, eth}, astra.SubscribeOptions{
				Transport:          transport,
				Channel:            astra.ChannelRealTime,
				ReconnectBaseDelay: 10 * time.Millisecond,
				ReconnectMaxDelay:  50 * time.Millisecond,
				OnError:            func(error) {},
				OnStateChange:      func(astra.ConnectionState) {},
			})
			if err != nil {
				t.Fatal(err)
			}

			seen := map[string]astra.PriceUpdate{}
			for len(seen) < 2 {
				select {
				case u := <-sub.Updates():
					seen[u.ID] = u
				case <-ctx.Done():
					t.Fatalf("got %d updates before the deadline", len(seen))
				}
			}

			if seen[btc].Price.Decimal() != "65000" || len(sub.IDs()) != 2 || sub.Stats().Connects < 1 {
				t.Fatalf("updates %+v, ids %v, stats %+v", seen, sub.IDs(), sub.Stats())
			}

			if err = sub.Close(); err != nil {
				t.Fatal(err)
			}

			<-sub.Done()

			if sub.State() != astra.StateClosed || sub.Err() != nil {
				t.Fatalf("state %s, err %v", sub.State(), sub.Err())
			}
		})
	}
}

# Astra oracle client for Go

```go
c, _ := astra.New(astra.Options{})
prices, _ := c.LatestPrices(ctx, []string{"0xe62df6c8b4a85fe1a67db44dc12de5db330f7ac66b72dc658afedf0f4a415b43"}, astra.PriceQuery{})
fmt.Println(prices[0].Price.Decimal()) // 83095.4425
```

Astra is avee's composite exchange index served over the routes and JSON shapes of Pyth's Hermes. No
key is needed. This package wraps its REST, SSE and WebSocket surfaces: context-first, standard
`net/http`, one dependency (`github.com/coder/websocket`).

```sh
go get github.com/aveetechapp/avee-go
```

Import `github.com/aveetechapp/avee-go/astra`; the package is `astra`. The default host is the preview host
`https://astra.preview.avee.tech`; pass a base URL to use another (`Options.BaseURL`). A base URL with a path
prefix works. Go 1.24+.

## REST

| Method | Route |
|---|---|
| `PriceFeeds(ctx, PriceFeedsQuery{Query, AssetType})` | `GET /v2/price_feeds` |
| `PriceFeed(ctx, id)` | `GET /v2/price_feeds/{id}` |
| `LatestPrices(ctx, ids, PriceQuery{IgnoreInvalid})` | `GET /v2/updates/price/latest`, every id in one request |
| `PricesAt(ctx, publishTime, ids, PriceQuery{})` | `GET /v2/updates/price/{publish_time}` |
| `PricesInInterval(ctx, publishTime, seconds, ids, IntervalQuery{})` | `GET /v2/updates/price/{publish_time}/{interval}`, flattened oldest first |
| `Feeds(ctx, category)` | `GET /v1/feeds`: both ids, category, live value and status |
| `FeedIDs(ctx, FeedIDsOptions{PythIDs, AstraIDs, Category})` | `GET /v1/feed-ids`: which Pyth ids Astra serves, and their Astra ids |
| `Status(ctx, StatusOptions{Feed})` | `GET /v1/status`; with `Feed`, that feed's entry alone, and a `503` (not `trading`, or `stale`) is returned as the report, not as an error |
| `Candles(ctx, CandlesQuery{Feed, Resolution, From, To})` | `GET /v1/candles` as `[]Candle` |

Ids are accepted with or without `0x`, in any case, are de-duplicated, and come back lower-case
without `0x` (`NormalizeFeedID`). Times are Unix seconds.

**Prices are exact.** `Price` keeps `Price` and `Conf` as integer strings with `Expo`, as sent:

```go
p.Decimal()     // "83095.4425", exact
p.Float64()     // correctly rounded
p.Scaled(18)    // *big.Int 83095442500000000000000, truncated toward zero
p.ConfFloat64(); p.ConfScaled(8)
```

## Streaming

```go
sub, err := c.Subscribe(ctx, ids, astra.SubscribeOptions{Channel: astra.ChannelFixed1000ms})
defer sub.Close()
for u := range sub.Updates() { … }
if err := sub.Err(); err != nil { … } // set when the channel closed on a fatal error
```

- `Transport`: `TransportWebSocket` (default, the Hermes WebSocket protocol) or `TransportSSE`
  (`/v2/updates/price/stream`, which also takes `BenchmarksOnly`).
- **Reconnect** on any drop, 429 or 5xx: full-jitter backoff from 500 ms, capped at 30 s, reset after
  a connection has been up for 60 s; `Retry-After` is a floor. The same ids are resubscribed.
- **Keepalive**: WebSocket pings every `IdleTimeout/2` (45 s / 2) and reconnects when a pong is late
  or the subscription is not acknowledged within `IdleTimeout`; SSE reconnects after `IdleTimeout`
  without a byte, whatever `HTTPClient.Timeout` says.
- **Exactly the new values**: an update older than the last one delivered for its feed, or an exact
  repeat of it (the replay sent on reconnect), is dropped.
- **Bounded memory**: a consumer that falls behind gets the newest update per feed, never a queue;
  `Stats().Coalesced` counts what it skipped. Read buffers are reused per connection.
- **No leaks**: `Close` (or cancelling `ctx`) closes the socket with 1000, waits for every goroutine,
  and closes `Updates()`.
- **Fatal** (no retry): 400, 404, 422, and a refused WebSocket subscription. Everything else goes to
  `OnError` and is retried. Callbacks run on the connection goroutine; keep them short, and never call
  `Close` from one (it waits for that goroutine).

## Errors

| Error | When |
|---|---|
| `*HTTPError` | non-2xx: `Status`, `Body`, `Problem` (`application/problem+json`), `RetryAfter`, `Retryable()` |
| `ErrTimeout` (`errors.Is`) | a request exceeded `Timeout` (10 s), or a stream went idle |
| `*ValidationError` | bad input, or a response that breaks the contract |
| `*SubscriptionError` | the WebSocket subscribe was refused |
| `*ServerError` | a non-fatal error message on an open WebSocket |

Cancelling `ctx` returns `ctx.Err()` wrapped, never retried. REST calls are GETs and are retried
`MaxRetries` times (2; `DisableRetries` for none) on 408, 429, 502, 503, 504, timeouts and network
errors. `Retry-After` is honoured up to `MaxRetryDelay` (30 s); a longer one is returned as the error.

## Find your feeds

Paste the Pyth ids your code already uses and see which ones Astra serves:

```go
m, err := c.FeedIDs(ctx, astra.FeedIDsOptions{PythIDs: []string{"0xe62df6c8…", "0xff61491a…"}})
// m.Items:   {Symbol: "Crypto.BTC/USD", PythID: "e62d…", AstraID: "1de7…", …}
// m.Missing: Pyth ids Astra does not serve
```

Every id in `Items` works as it is on the Hermes routes: nothing in your code changes for those
feeds. `FeedIDs` with empty options lists every feed Astra serves, and `Missing` is then nil. Up to
`MaxIDsPerURL` (200) ids go in one request; a longer list is split, so the URL fits the request line
the edge accepts, and merged, sorted by symbol. `FeedIDsPerRequest` is the same number under its old
name.

## Moving from Pyth Hermes

There is no official Hermes client for Go. Code calling Hermes over HTTP only changes its host to
`https://astra.preview.avee.tech` and drops the `Authorization` header (it is accepted and ignored).

| Hermes | this module |
|---|---|
| `GET /v2/updates/price/latest?ids[]=…` | `LatestPrices(ctx, ids, PriceQuery{})` |
| `GET /v2/updates/price/stream` | `Subscribe(ctx, ids, SubscribeOptions{Transport: TransportSSE})` |
| `wss://…/ws` subscribe | `Subscribe(ctx, ids, SubscribeOptions{})` |
| `price * 10^expo` by hand | `Price.Float64()`, `Decimal()`, `Scaled(n)` |
| `binary.data` for `updatePriceFeeds` | always empty: Astra is unsigned and cannot be verified on-chain |

A reference-status feed is served over Hermes like a trading one; read `Status` or `Feeds` before
liquidating on it.

## Limits

| Limit | Value |
|---|---|
| Ids per call / subscription | 500 (historical routes: 100 feeds) |
| Ids per URL | `MaxIDsPerURL`, 200 |
| Interval window | 60 s |
| Candles per request | 5000 |
| Response body | `MaxResponseBytes`, 8 MiB |
| Stream message | `MaxMessageBytes`, 1 MiB; a larger WebSocket message ends the connection, which reconnects |

Astra's edge rejects a request line over about 16 KB before it reaches the server, which is about 200
ids. `LatestPrices` sends up to 200 ids per request and splits a longer list into several, one after
another, merged in the order you asked; if one of them fails, the call returns that error and no
prices. An SSE subscription carries its ids in the URL, so `Subscribe` with `TransportSSE` refuses
more than 200 with a `*ValidationError`: use the WebSocket transport (the default), where the ids
travel in the subscribe message, for up to 500.
| Stream lifetime | 24 h on the server; the client reconnects |

## Development

`go test -race ./...` runs against an `httptest` fake Astra, including goroutine-leak checks;
`ASTRA_LIVE=1 go test -run Live ./...` checks the preview host; `go test -bench . ./...` measures
decoding. The contract test reads `testdata/astra.yml`, the synced copy of the Astra OpenAPI file, and
checks every wire struct's JSON field against it.

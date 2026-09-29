# Changelog

## 0.1.0 — unreleased

- First release: every operation of `/api/v1` as a typed method, models generated from the OpenAPI
  document, tolerant of unknown fields and enum values.
- Lazy cursor iterators for every paged operation.
- Typed problem+json errors, GET retries honouring `Retry-After`, rate-limit info of the last response,
  input validated against the specification's bounds, bounded response size.
- Opt-in x402: with a payer, a spent keyless budget is paid for once and the receipt exposed.
- Redirects are not followed; an unreadable x402 challenge or a malformed offer (amount not a positive
  integer or above `maxAmountRequired`, no asset or payee) is refused before the payer is asked; a
  repeated cursor is detected in constant memory; public-API compatibility gates (`COMPATIBILITY.md`).
- Unions decode without building a map of the whole object (7x fewer allocations on a trade page).
- A constant for every known value of every value set, open sets included (`PairStatusScam`, `FactoryTypeUniswapV3`).

### Astra price oracle (`astra` package)

- `MaxIDsPerURL` (200): the most ids one URL carries through Astra's edge. `LatestPrices` splits a
  longer list into requests of 200, sent in turn and merged in request order; a failed request fails
  the call. `Subscribe` over SSE refuses more than 200 ids and points to the WebSocket transport.
  `FeedIDsPerRequest` stays, equal to `MaxIDsPerURL`.
- `FeedIDs` for `/v1/feed-ids`: which Pyth ids Astra serves and their Astra ids, with the ones it
  does not serve in `missing`.
- `Status(ctx, StatusOptions{Feed})`: one feed's health; the server's `503` for a feed that is not `trading`, or is
  `stale`, is a normal answer here and comes back as the report. Only an `application/json` body counts: a proxy page or a problem on `503` stays an error and is retried. An empty feed reads the whole report.
- First release: Hermes v2 REST (`price_feeds`, latest, at a time, over an interval), Astra's native
  `/v1/feeds`, `/v1/status` and `/v1/candles`.
- Streaming over WebSocket (Hermes protocol) and SSE with reconnect, resubscribe, keepalive, per-feed
  de-duplication and a bounded, coalescing buffer.
- Exact prices (integer string + `expo`) with decimal, float and fixed-point helpers.
- Typed errors, retries for GETs honouring `Retry-After`, and validation of every response.

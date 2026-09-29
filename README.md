# avee DEX data API client for Go

```go
c, _ := avee.New(avee.Options{})
page, err := c.Pairs(ctx, avee.PairsParams{Chains: []string{"base"}, Limit: avee.Ptr(int32(10))})
fmt.Println(page.Items[0].PairAddress, err)
```

Typed access to every operation of the avee client API (`/api/v1`): pairs, tokens, trades, candles,
wallets, leaderboards, farms, perps and oracle prices across every chain avee indexes. No key is
needed. Standard library only; Go 1.24.

```sh
go get github.com/aveetechapp/avee-go
```

The package name is `avee`. The default host is the preview host:
`avee.DefaultBaseURL` equals `avee.PreviewBaseURL`.

## Options

| Field | Default | |
|---|---|---|
| `BaseURL` | `DefaultBaseURL` | a path prefix works |
| `APIKey` | none | sent as `X-API-Key`; only raises the limits |
| `Timeout` | 30 s | per attempt, inside the caller's `ctx` |
| `MaxRetries` / `DisableRetries` | 2 | GETs only |
| `MaxRetryDelay` | 30 s | a longer `Retry-After` is returned as the error |
| `MaxResponseBytes` | 16 MiB | a larger body is refused |
| `HTTPClient`, `Header`, `UserAgent` | | |
| `Payer` | nil | opts into x402, see below |

A `Client` is safe for concurrent use.

## Operations

Each method takes `ctx`, the path parameters as strings (a chain slug or its numeric id), and a
`…Params` struct when the operation has query parameters: required ones are values, optional ones are
pointers (`avee.Ptr(v)`), lists are slices. Input is checked against the specification's bounds before
anything is sent (`*ValidationError`); a batch takes up to the `maxItems` of its request schema (pairs
50, tokens and wallet labels 200).

Every value set in the specification is a named string type with a constant per known value:
`avee.SortByVolume`, `avee.TimeFrame24h`, `avee.PairStatusScam`, `avee.FactoryTypeUniswapV3`. The
sets are open: an unknown value from the server decodes as it is, so compare against the constants and
keep a default branch. An omitted parameter takes the server's default, listed per operation in the
specification.

<!-- operations:start -->
| Method | Route | What it answers |
|---|---|---|
| **meta** | | |
| `X402Discovery(ctx)` | `GET /.well-known/x402` | Operations payable with x402 and their prices |
| `Status(ctx)` | `GET /status` | Liveness and upstream health |
| `Key(ctx)` | `GET /key` | Plan and limits of the calling key |
| `Config(ctx)` | `GET /config` | Enumerations and defaults |
| `Chains(ctx)` | `GET /chains` | Chains served right now |
| **dex** | | |
| `Search(ctx, params)` | `GET /search` | Typeahead across tokens and pairs |
| `Pairs(ctx, params), IterPairs` | `GET /pairs` | Liquidity pair screener |
| `Pair(ctx, chain, address)` | `GET /chains/{chain}/pairs/{address}` | One liquidity pair |
| `PairTrades(ctx, chain, address, params), IterPairTrades` | `GET /chains/{chain}/pairs/{address}/trades` | Trade tape of a pair |
| `PairCandles(ctx, chain, address, params)` | `GET /chains/{chain}/pairs/{address}/candles` | OHLCV candles |
| `Trending(ctx, params), IterTrending` | `GET /trending` | Trending pairs right now |
| `PairsNew(ctx, params), IterPairsNew` | `GET /pairs/new` | Newest pairs on one chain |
| `LaunchpadTokens(ctx, params), IterLaunchpadTokens` | `GET /launchpads/tokens` | Launchpad launches by stage (new, bonding or graduated) |
| `PairBatch(ctx, body)` | `POST /pairs/batch` | Many pairs in one call |
| `Perps(ctx, params), IterPerps` | `GET /perps` | Perpetual markets |
| `PerpHistory(ctx, market, params)` | `GET /perps/{market}/history` | Open interest, funding and mark history of a perpetual |
| `PerpLiquidations(ctx, params)` | `GET /perps/liquidations` | Daily liquidations of a perpetual or a whole venue |
| `DeployerTokens(ctx, address, params), IterDeployerTokens` | `GET /deployers/{address}/tokens` | Launches of one deployer, with its reputation card |
| **token** | | |
| `TokenByAddress(ctx, chain, address, params)` | `GET /chains/{chain}/tokens/{address}` | Canonical token by chain and address |
| `TokenPairs(ctx, chain, address, params)` | `GET /chains/{chain}/tokens/{address}/pairs` | Top pairs of a token by 24h volume |
| `TokenVerdict(ctx, chain, address)` | `GET /chains/{chain}/tokens/{address}/verdict` | Signed token verdict, submittable to the trust oracle |
| `TokenVerdictProof(ctx, chain, address)` | `GET /chains/{chain}/tokens/{address}/proof` | Merkle proof of a verdict at the last published epoch |
| `TokenBrief(ctx, chain, address)` | `GET /chains/{chain}/tokens/{address}/brief` | Everything needed to decide about a token, in one call |
| `TokenByID(ctx, id, params)` | `GET /tokens/{id}` | Canonical token by id |
| `TokenBySlug(ctx, slug, params)` | `GET /tokens/by-slug/{slug}` | Canonical token by slug |
| `Tokens(ctx, params), IterTokens` | `GET /tokens` | Token market list, ranked by market cap |
| `TokenBatch(ctx, body)` | `POST /tokens/batch` | Resolve many tokens at once |
| `TokenHolders(ctx, chain, address, params), IterTokenHolders` | `GET /chains/{chain}/tokens/{address}/holders` | Top holders of a token |
| `TokenTraders(ctx, chain, address, params), IterTokenTraders` | `GET /chains/{chain}/tokens/{address}/traders` | Wallets that traded a token, with their PNL on it |
| **farm** | | |
| `Farms(ctx, params), IterFarms` | `GET /farms` | Yield farm screener |
| `Farm(ctx, chain, address)` | `GET /chains/{chain}/farms/{address}` | One yield farm |
| **wallet** | | |
| `Wallets(ctx, params), IterWallets` | `GET /wallets` | Rank traders on one chain |
| `WalletStats(ctx, params)` | `GET /wallets/stats` | Trader population per chain |
| `WalletLabelsBatch(ctx, body)` | `POST /wallets/labels/batch` | Behaviour labels for many wallets |
| `WalletOverview(ctx, address, params)` | `GET /wallets/{address}/overview` | One wallet across every chain it traded |
| `Leaderboard(ctx, params)` | `GET /leaderboard` | Chain and DEX protocol boards |
| `WalletProfile(ctx, chain, address)` | `GET /chains/{chain}/wallets/{address}` | Trader profile with metrics for every window |
| `WalletPositions(ctx, chain, address, params), IterWalletPositions` | `GET /chains/{chain}/wallets/{address}/positions` | Open and closed positions of a wallet |
| `WalletTrades(ctx, chain, address, params), IterWalletTrades` | `GET /chains/{chain}/wallets/{address}/trades` | Raw trade history of a wallet |
| `WalletChart(ctx, chain, address, params)` | `GET /chains/{chain}/wallets/{address}/chart` | Wallet performance series — PNL and ROI |
| `WalletBestTrades(ctx, chain, address, params)` | `GET /chains/{chain}/wallets/{address}/best-trades` | Best and worst closed trades of a wallet |
| `WalletRounds(ctx, chain, address, params), IterWalletRounds` | `GET /chains/{chain}/wallets/{address}/rounds` | Position rounds — one entry and exit cycle per row |
| `WalletFunding(ctx, chain, address)` | `GET /chains/{chain}/wallets/{address}/funding` | Who funded a wallet |
| **oracle** | | |
| `OraclePrices(ctx, params)` | `GET /prices` | Latest oracle prices by feed id |
| `OraclePricesAt(ctx, params)` | `GET /prices/at` | Oracle prices at a past moment |
<!-- operations:end -->

## Pagination

Each paged operation has an `Iter…` twin returning `iter.Seq2[Item, error]`; the next page is fetched
only when the loop reaches it, and `break` stops it:

```go
for trade, err := range c.IterPairTrades(ctx, "base", pair, avee.PairTradesParams{}) {
	if err != nil {
		return err
	}
	_ = trade
}
```

A repeated cursor ends the walk with a `*ValidationError` rather than looping. Prefer `PairBatch`,
`TokenBatch` and `WalletLabelsBatch` to one call per address.

## Errors

| Error | When |
|---|---|
| `*APIError` | non-2xx: `Status`, `Code` (branch on it), `Detail`, `Param`, `RequestID`, `RateLimit`, `RetryAfter`, `Retryable()`, `Paid`, `PaymentRequired`, `Payment` |
| `ErrTimeout` (`errors.Is`) | an attempt exceeded `Timeout` |
| `*ValidationError` | bad input, or a response that does not decode or is too large |
| `*PaymentError` | the x402 flow stopped before paying: an unreadable challenge or offer; wraps `ErrNoPayableNetwork`, `ErrPaymentDeclined` or the payer's error |

GETs are retried on 408, 429, 5xx, timeouts and network errors with full-jitter backoff; a
`Retry-After` (or, on a 429 without one, the `RateLimit` reset) is waited out when it fits
`MaxRetryDelay`. POSTs are never retried. Redirects are not followed, whatever the `HTTPClient`'s own
policy: a 3xx is an `*APIError`, so the key and a payment signature never leave the host.

## Rate limits

`c.LastResponse()` returns the latest `Status`, `RequestID`, `RateLimit` (`Limit`, `Remaining`,
`Reset`, `RetryAfter`, `Policy`, `Known`) and the x402 `Payment` receipt. Under concurrency it is the
last response to arrive; an `*APIError` carries its own `RateLimit`.

## Paying past the keyless limit (x402)

Off by default: without a `Payer` the client never pays and a spent budget is an ordinary 429. With
one it sends `Accept-Payment: x402`; a spent budget then answers 402, the client picks the first
`exact` offer on a network from `Networks()` (in that order), calls `Sign` once, and repeats the
request once with `PAYMENT-SIGNATURE`. A paid request is never retried; the receipt is
`LastResponse().Payment`. `Sign` sees the amount, asset, network and `PayTo` in `p.Requirement` and
declines by returning an error. An offer whose amount is not a positive integer (or exceeds
`MaxAmountRequired`), or that lacks an asset or `PayTo`, is refused before `Sign` is called.

The SDK holds no key and imports no wallet library. With go-ethereum:

```go
payer := avee.NewPayer([]string{"eip155:84532"}, func(ctx context.Context, p avee.PaymentContext) (avee.PaymentSignature, error) {
	r := p.Requirement
	now := time.Now().Unix()
	nonce := make([]byte, 32)
	_, _ = rand.Read(nonce)
	auth := map[string]any{
		"from": from.Hex(), "to": r.PayTo, "value": r.Amount,
		"validAfter": strconv.FormatInt(now-5, 10), "validBefore": strconv.FormatInt(now+int64(r.MaxTimeoutSeconds), 10),
		"nonce": hexutil.Encode(nonce),
	}
	typed := apitypes.TypedData{
		Types: apitypes.Types{
			"EIP712Domain": {{Name: "name", Type: "string"}, {Name: "version", Type: "string"}, {Name: "chainId", Type: "uint256"}, {Name: "verifyingContract", Type: "address"}},
			"TransferWithAuthorization": {{Name: "from", Type: "address"}, {Name: "to", Type: "address"}, {Name: "value", Type: "uint256"},
				{Name: "validAfter", Type: "uint256"}, {Name: "validBefore", Type: "uint256"}, {Name: "nonce", Type: "bytes32"}},
		},
		PrimaryType: "TransferWithAuthorization",
		Domain: apitypes.TypedDataDomain{Name: fmt.Sprint(r.Extra["name"]), Version: fmt.Sprint(r.Extra["version"]),
			ChainId: math.NewHexOrDecimal256(84532), VerifyingContract: r.Asset},
		Message: auth,
	}
	hash, _, err := apitypes.TypedDataAndHash(typed)
	if err != nil {
		return avee.PaymentSignature{}, err
	}
	sig, err := crypto.Sign(hash, key)
	if err != nil {
		return avee.PaymentSignature{}, err
	}
	sig[64] += 27
	return avee.PaymentSignature{Payload: map[string]any{"signature": hexutil.Encode(sig), "authorization": auth}}, nil
})
c, _ := avee.New(avee.Options{Payer: payer})
```

`Sign` may instead return `PaymentSignature{Header: …}`, a finished `PAYMENT-SIGNATURE` from any x402
client library.

## Astra price oracle

The module also carries the client for Astra, avee's Hermes-compatible price oracle, as the package
`github.com/aveetechapp/avee-go/astra`. It is the one package with a dependency
(`github.com/coder/websocket`), compiled in only when imported.

```go
c, _ := astra.New(astra.Options{})
prices, _ := c.LatestPrices(ctx, []string{"0xe62df6c8b4a85fe1a67db44dc12de5db330f7ac66b72dc658afedf0f4a415b43"}, astra.PriceQuery{})
```

REST, SSE and WebSocket, exact prices, reconnecting streams: [astra/README.md](astra/README.md).

## Compatibility

Models are generated from the OpenAPI document: unknown fields are ignored, enums are string types
that keep unknown values, and the one polymorphic field (`Transaction.BlockInfo`, `EventData`) keeps
an unrecognised branch in `Raw`. Inside a major version the module is additive only.

## Development

`go test -race ./...` runs against a local fake server; `AVEE_LIVE=1 go test -run Live ./...` checks
the preview host; `go test -bench DecodePairPage` measures decoding a 100-pair page.

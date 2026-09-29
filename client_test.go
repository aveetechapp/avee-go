package avee

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestEveryOperationSendsTheSpecifiedRequest(t *testing.T) {
	checkGoroutines(t)

	spec := loadSpec(t)
	calls := loadCalls(t)

	specOps := 0
	for _, item := range spec.Paths {
		for m := range item {
			if m == "get" || m == "post" {
				specOps++
			}
		}
	}

	if len(generatedCalls) != specOps || len(calls) != specOps {
		t.Fatalf("spec has %d operations, the SDK %d, the fixture %d", specOps, len(generatedCalls), len(calls))
	}

	for _, gc := range generatedCalls {
		for _, v := range []variant{requiredOnly, everyField, unknownValues} {
			want := calls[gc.operation]
			body := spec.schemaInstance(t, gc.response, v)
			f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(body)
			})

			got, err := gc.invoke(context.Background(), f.client(t, Options{}))
			if err != nil {
				t.Fatalf("%s (variant %d): %v", gc.operation, v, err)
			}

			if reflect.ValueOf(got).IsNil() {
				t.Fatalf("%s: nil result", gc.operation)
			}

			reqs := f.calls()
			if len(reqs) != 1 {
				t.Fatalf("%s: %d requests", gc.operation, len(reqs))
			}

			r := reqs[0]
			if r.method != want.Method || r.path != "/api/v1"+want.Path {
				t.Fatalf("%s: sent %s %s, want %s %s", gc.operation, r.method, r.path, want.Method, want.Path)
			}

			q := url.Values{}
			for _, kv := range want.Query {
				q.Set(kv[0], kv[1])
			}

			if sortedQuery(r.query) != sortedQuery(q.Encode()) {
				t.Fatalf("%s: query %s, want %s", gc.operation, r.query, q.Encode())
			}

			if want.Body != nil {
				var sent any
				if err = json.Unmarshal([]byte(r.body), &sent); err != nil {
					t.Fatal(err)
				}

				if !reflect.DeepEqual(sent, want.Body) {
					t.Fatalf("%s: body %s, want %v", gc.operation, r.body, want.Body)
				}

				if r.header.Get("Content-Type") != "application/json" {
					t.Fatalf("%s: content type %q", gc.operation, r.header.Get("Content-Type"))
				}
			}

			if r.header.Get("Accept-Payment") != "" || r.header.Get("X-API-Key") != "" {
				t.Fatalf("%s: a keyless client without a payer sent %v", gc.operation, r.header)
			}
		}
	}
}

func TestDecodingKeepsKnownFieldsAndIgnoresUnknownOnes(t *testing.T) {
	raw := `{"items":[{"hash":"0x1","timestamp":"t","chain_id":8453,"tx_type":"x_new_kind","maker_address":"a","from_address":"b","to_address":"c",
		"block_info":{"seq_no":7,"shard_id":1,"work_chain":0,"x_extra":true},"event_data":{"x_unknown_branch":1},"x_future":{"a":[1]}}],"next_cursor":"n"}`

	var page TradePage
	if err := json.Unmarshal([]byte(raw), &page); err != nil {
		t.Fatal(err)
	}

	tx := page.Items[0]
	if tx.TxType != "x_new_kind" || tx.ChainID != 8453 || tx.BlockInfo.TonTxData == nil || tx.BlockInfo.TonTxData.SeqNo != 7 {
		t.Fatalf("decoded %+v", tx)
	}

	if tx.EventData.SwapEventData != nil || tx.EventData.LiquidityEventData != nil || string(tx.EventData.Raw) != `{"x_unknown_branch":1}` {
		t.Fatalf("unknown union branch: %+v", tx.EventData)
	}

	out, err := json.Marshal(tx.EventData)
	if err != nil || string(out) != `{"x_unknown_branch":1}` {
		t.Fatalf("re-encoded %s %v", out, err)
	}

	if page.NextCursor == nil || *page.NextCursor != "n" {
		t.Fatalf("cursor %v", page.NextCursor)
	}
}

func TestValidationHappensBeforeAnyRequest(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ int) { writeJSON(w, 200, map[string]any{}) })
	c := f.client(t, Options{})
	ctx := context.Background()

	cases := map[string]func() error{
		"empty chain":     func() error { _, err := c.Pair(ctx, "", "0xabc"); return err },
		"dot address":     func() error { _, err := c.Pair(ctx, "base", ".."); return err },
		"limit too big":   func() error { _, err := c.Pairs(ctx, PairsParams{Limit: Ptr(int32(101))}); return err },
		"too many chains": func() error { _, err := c.Pairs(ctx, PairsParams{Chains: make([]string, 33)}); return err },
		"comma in chain":  func() error { _, err := c.Pairs(ctx, PairsParams{Chains: []string{"a,b"}}); return err },
		"missing q":       func() error { _, err := c.Search(ctx, SearchParams{}); return err },
		"bad uuid":        func() error { _, err := c.TokenByID(ctx, "nope", TokenByIDParams{}); return err },
		"empty batch":     func() error { _, err := c.PairBatch(ctx, PairBatchRequest{}); return err },
		"batch over 50": func() error {
			_, err := c.PairBatch(ctx, PairBatchRequest{Items: make([]PairBatchRef, 51)})
			return err
		},
		"win rate over 1": func() error {
			_, err := c.Wallets(ctx, WalletsParams{Chain: "base", MinWinRate: Ptr(1.5)})
			return err
		},
	}

	for name, run := range cases {
		var v *ValidationError
		if err := run(); !errors.As(err, &v) {
			t.Errorf("%s: got %v, want a ValidationError", name, err)
		}
	}

	if n := len(f.calls()); n != 0 {
		t.Fatalf("%d requests were sent for invalid input", n)
	}
}

func TestPathParametersAreEscapedAndChainIDsAccepted(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		writeJSON(w, 200, map[string]any{"chain": map[string]any{"slug": "ton", "chain_id": 950000}})
	})

	if _, err := f.client(t, Options{}).WalletProfile(context.Background(), "950000", "EQ/A B?"); err != nil {
		t.Fatal(err)
	}

	if got := f.calls()[0].path; got != "/api/v1/chains/950000/wallets/EQ%2FA%20B%3F" {
		t.Fatalf("path %s", got)
	}
}

func TestIteratorsFetchPagesLazily(t *testing.T) {
	checkGoroutines(t)

	f := newFake(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		switch r.URL.Query().Get("cursor") {
		case "":
			writeJSON(w, 200, map[string]any{"items": []any{map[string]any{"pair_address": "a1"}, map[string]any{"pair_address": "a2"}}, "next_cursor": "p2"})
		case "p2":
			writeJSON(w, 200, map[string]any{"items": []any{map[string]any{"pair_address": "a3"}}, "next_cursor": "p3"})
		default:
			writeJSON(w, 200, map[string]any{"items": []any{map[string]any{"pair_address": "a4"}}})
		}
	})
	c := f.client(t, Options{})

	var got []string
	for pair, err := range c.IterPairs(context.Background(), PairsParams{Chains: []string{"base"}, Limit: Ptr(int32(2))}) {
		if err != nil {
			t.Fatal(err)
		}

		got = append(got, pair.PairAddress)
	}

	if strings.Join(got, ",") != "a1,a2,a3,a4" || len(f.calls()) != 3 {
		t.Fatalf("items %v after %d requests", got, len(f.calls()))
	}

	if q := f.calls()[1].query; !strings.Contains(q, "cursor=p2") || !strings.Contains(q, "chains=base") || !strings.Contains(q, "limit=2") {
		t.Fatalf("second page query %s", q)
	}

	before := len(f.calls())
	for range c.IterPairs(context.Background(), PairsParams{}) {
		break
	}

	if len(f.calls())-before != 1 {
		t.Fatalf("breaking after the first item fetched %d pages", len(f.calls())-before)
	}
}

func TestIteratorStopsOnARepeatedCursor(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		writeJSON(w, 200, map[string]any{"items": []any{map[string]any{"pair_address": "a"}}, "next_cursor": "same"})
	})

	var last error
	n := 0
	for _, err := range f.client(t, Options{}).IterTrending(context.Background(), TrendingParams{}) {
		n++
		last = err
	}

	var v *ValidationError
	if !errors.As(last, &v) || n != 3 {
		t.Fatalf("after %d items: %v", n, last)
	}
}

func TestProblemDetailsBecomeTypedErrors(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"type":"https://docs.avee.tech/errors/chain_not_found","title":"chain not found","status":404,"code":"chain_not_found","detail":"no chain nope","param":"chain","request_id":"r-9"}`))
	})

	_, err := f.client(t, Options{}).Pair(context.Background(), "nope", "0xabc")

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("got %v", err)
	}

	if apiErr.Status != 404 || apiErr.Code != "chain_not_found" || apiErr.Detail != "no chain nope" || apiErr.Param != "chain" || apiErr.RequestID != "r-9" || apiErr.Retryable() {
		t.Fatalf("error %+v", apiErr)
	}

	if len(f.calls()) != 1 {
		t.Fatal("a 404 was retried")
	}
}

func TestRetriesHonourRetryAfterAndStopForPost(t *testing.T) {
	checkGoroutines(t)

	f := newFake(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			writeProblem(w, 429, "rate_limited", "slow down")

			return
		}

		if n == 2 {
			writeProblem(w, 503, "upstream_unavailable", "later")

			return
		}

		writeJSON(w, 200, map[string]any{"status": "ok"})
	})
	c := f.client(t, Options{})

	start := time.Now()
	if _, err := c.Status(context.Background()); err != nil {
		t.Fatal(err)
	}

	if elapsed := time.Since(start); elapsed < time.Second {
		t.Fatalf("Retry-After ignored: retried after %s", elapsed)
	}

	if len(f.calls()) != 3 {
		t.Fatalf("%d requests", len(f.calls()))
	}

	post := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		writeProblem(w, 503, "upstream_unavailable", "later")
	})
	_, err := post.client(t, Options{}).PairBatch(context.Background(), PairBatchRequest{Items: []PairBatchRef{{Chain: "base", Address: "0xa"}}})

	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 503 || len(post.calls()) != 1 {
		t.Fatalf("POST: %v after %d requests", err, len(post.calls()))
	}
}

func TestRetryAfterBeyondTheCapIsReturned(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.Header().Set("Retry-After", "120")
		writeProblem(w, 429, "rate_limited", "slow down")
	})

	_, err := f.client(t, Options{MaxRetryDelay: time.Second}).Chains(context.Background())

	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.RetryAfter != 120*time.Second || len(f.calls()) != 1 {
		t.Fatalf("%v after %d requests", err, len(f.calls()))
	}
}

func TestTimeoutsAreRetriedThenReported(t *testing.T) {
	checkGoroutines(t)

	release := make(chan struct{})
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	defer close(release)

	_, err := f.client(t, Options{Timeout: 50 * time.Millisecond, MaxRetries: 1, MaxRetryDelay: 10 * time.Millisecond}).Status(context.Background())
	if !errors.Is(err, ErrTimeout) || len(f.calls()) != 2 {
		t.Fatalf("%v after %d requests", err, len(f.calls()))
	}
}

func TestRateLimitAndRequestIDOfTheLastResponse(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.Header().Set("RateLimit-Policy", `"plan";q=5;w=1;burst=20`)
		w.Header().Set("RateLimit", `"plan";r=19;t=1`)
		w.Header().Set("X-RateLimit-Limit", "5")
		w.Header().Set("X-Request-Id", "abc")
		writeJSON(w, 200, map[string]any{"status": "ok"})
	})
	c := f.client(t, Options{APIKey: "k1"})

	if _, err := c.Status(context.Background()); err != nil {
		t.Fatal(err)
	}

	info := c.LastResponse()
	if info.Status != 200 || info.RequestID != "abc" || info.Operation != "status" || !info.RateLimit.Known ||
		info.RateLimit.Limit != 5 || info.RateLimit.Remaining != 19 || info.RateLimit.Reset != time.Second || !strings.Contains(info.RateLimit.Policy, "q=5") {
		t.Fatalf("info %+v", info)
	}

	if f.calls()[0].header.Get("X-API-Key") != "k1" {
		t.Fatal("the API key was not sent")
	}
}

func TestOversizedResponsesAreRefused(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		writeJSON(w, 200, map[string]any{"status": strings.Repeat("x", 4096)})
	})

	_, err := f.client(t, Options{MaxResponseBytes: 1024}).Status(context.Background())

	var v *ValidationError
	if !errors.As(err, &v) {
		t.Fatalf("got %v", err)
	}
}

func TestNewRejectsBadOptions(t *testing.T) {
	for _, opts := range []Options{{BaseURL: "ftp://x"}, {BaseURL: "https://x/?a=1"}, {Timeout: -1}, {APIKey: "a\nb"}} {
		if _, err := New(opts); err == nil {
			t.Errorf("New(%+v) accepted", opts)
		}
	}

	c, err := New(Options{})
	if err != nil || c.base.String() != DefaultBaseURL {
		t.Fatalf("default client: %v", err)
	}
}

func challenge(accepts ...map[string]any) map[string]any {
	return map[string]any{
		"x402Version": 2,
		"error":       "keyless budget spent",
		"resource":    map[string]any{"url": "https://api.preview.avee.tech/api/v1/chains", "mimeType": "application/json"},
		"accepts":     accepts,
		"extensions":  map[string]any{"payment-identifier": map[string]any{"info": map[string]any{"required": false}}},
	}
}

func offer(network string) map[string]any {
	return map[string]any{
		"scheme": "exact", "network": network, "amount": "2000", "maxAmountRequired": "2000",
		"asset": "0x036CbD53842c5426634e7929541eC2318f3dCF7e", "payTo": "0x00000000000000000000000000000000000000b2",
		"maxTimeoutSeconds": 60, "extra": map[string]any{"name": "USDC", "version": "2", "x_future": 1},
	}
}

func writeChallenge(w http.ResponseWriter, doc map[string]any, header bool) {
	raw, _ := json.Marshal(doc)
	if header {
		w.Header().Set("PAYMENT-REQUIRED", base64.StdEncoding.EncodeToString(raw))
	}

	w.Header().Set("Retry-After", "30")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusPaymentRequired)
	_, _ = w.Write(raw)
}

func TestX402PaysOnceWithThePreferredNetwork(t *testing.T) {
	checkGoroutines(t)

	doc := challenge(offer("eip155:8453"), offer("eip155:84532"))
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if r.Header.Get("PAYMENT-SIGNATURE") == "" {
			writeChallenge(w, doc, true)

			return
		}

		receipt, _ := json.Marshal(map[string]any{"success": true, "transaction": "0xtx", "network": "eip155:84532", "payer": "0xp"})
		w.Header().Set("PAYMENT-RESPONSE", base64.StdEncoding.EncodeToString(receipt))
		writeJSON(w, 200, map[string]any{"items": []any{}})
	})

	var seen PaymentContext
	var signs atomic.Int32
	payer := NewPayer([]string{"eip155:84532", "eip155:8453"}, func(_ context.Context, pc PaymentContext) (PaymentSignature, error) {
		signs.Add(1)
		seen = pc

		return PaymentSignature{Payload: map[string]any{"signature": "0xsig", "authorization": map[string]any{"from": "0xp"}}}, nil
	})
	c := f.client(t, Options{Payer: payer})

	if _, err := c.Chains(context.Background()); err != nil {
		t.Fatal(err)
	}

	reqs := f.calls()
	if len(reqs) != 2 || signs.Load() != 1 {
		t.Fatalf("%d requests, %d signatures", len(reqs), signs.Load())
	}

	if reqs[0].header.Get("Accept-Payment") != "x402" || reqs[0].header.Get("PAYMENT-SIGNATURE") != "" {
		t.Fatalf("first request headers %v", reqs[0].header)
	}

	if seen.Requirement.Network != "eip155:84532" || seen.Requirement.Amount != "2000" || seen.Operation != "chains" || seen.PaymentID == "" {
		t.Fatalf("payer saw %+v", seen)
	}

	raw, err := base64.StdEncoding.DecodeString(reqs[1].header.Get("PAYMENT-SIGNATURE"))
	if err != nil {
		t.Fatal(err)
	}

	var sent map[string]any
	if err = json.Unmarshal(raw, &sent); err != nil {
		t.Fatal(err)
	}

	accepted, _ := sent["accepted"].(map[string]any)
	extra, _ := accepted["extra"].(map[string]any)
	ext, _ := sent["extensions"].(map[string]any)
	pid, _ := ext["payment-identifier"].(map[string]any)
	info, _ := pid["info"].(map[string]any)

	if sent["x402Version"] != float64(2) || accepted["network"] != "eip155:84532" || extra["x_future"] != float64(1) || info["id"] != seen.PaymentID {
		t.Fatalf("payment payload %s", raw)
	}

	if p := c.LastResponse().Payment; p == nil || !p.Success || p.Transaction != "0xtx" {
		t.Fatalf("receipt %+v", p)
	}
}

func TestX402ReadsTheChallengeFromTheBodyAndAcceptsAFinishedHeader(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		if r.Header.Get("PAYMENT-SIGNATURE") != "ready-made" {
			writeChallenge(w, challenge(offer("solana:mainnet")), false)

			return
		}

		writeJSON(w, 200, map[string]any{"chains": []any{}})
	})

	payer := NewPayer(nil, func(_ context.Context, pc PaymentContext) (PaymentSignature, error) {
		if pc.Requirement.Network != "solana:mainnet" {
			return PaymentSignature{}, errors.New("wrong network")
		}

		return PaymentSignature{Header: "ready-made"}, nil
	})

	if _, err := f.client(t, Options{Payer: payer}).Config(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestX402NeverPaysTwiceNorRetriesAPaidRequest(t *testing.T) {
	for _, status := range []int{402, 503} {
		f := newFake(t, func(w http.ResponseWriter, r *http.Request, _ int) {
			if r.Header.Get("PAYMENT-SIGNATURE") == "" || status == 402 {
				writeChallenge(w, challenge(offer("eip155:84532")), true)

				return
			}

			writeProblem(w, status, "upstream_unavailable", "later")
		})

		payer := NewPayer([]string{"eip155:84532"}, func(context.Context, PaymentContext) (PaymentSignature, error) {
			return PaymentSignature{Payload: map[string]any{"signature": "0x"}}, nil
		})

		_, err := f.client(t, Options{Payer: payer}).Chains(context.Background())

		var apiErr *APIError
		if !errors.As(err, &apiErr) || !apiErr.Paid || apiErr.Status != status || len(f.calls()) != 2 {
			t.Fatalf("status %d: %v after %d requests", status, err, len(f.calls()))
		}

		if status == 402 && (apiErr.PaymentRequired == nil || len(apiErr.PaymentRequired.Accepts) != 1) {
			t.Fatalf("the refused payment lost its requirements: %+v", apiErr)
		}
	}
}

func TestX402DeclinesAndMismatchesSendNothing(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		writeChallenge(w, challenge(offer("eip155:8453")), true)
	})

	declining := NewPayer(nil, func(context.Context, PaymentContext) (PaymentSignature, error) {
		return PaymentSignature{}, ErrPaymentDeclined
	})

	_, err := f.client(t, Options{Payer: declining}).Chains(context.Background())

	var payErr *PaymentError
	if !errors.As(err, &payErr) || !errors.Is(err, ErrPaymentDeclined) || payErr.Required == nil {
		t.Fatalf("declined: %v", err)
	}

	elsewhere := NewPayer([]string{"eip155:1"}, func(context.Context, PaymentContext) (PaymentSignature, error) {
		t.Fatal("asked to sign for a network it does not support")

		return PaymentSignature{}, nil
	})

	if _, err = f.client(t, Options{Payer: elsewhere}).Chains(context.Background()); !errors.Is(err, ErrNoPayableNetwork) {
		t.Fatalf("mismatch: %v", err)
	}

	if len(f.calls()) != 2 {
		t.Fatalf("%d requests", len(f.calls()))
	}
}

func TestWithoutAPayerA429IsJustA429(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		raw, _ := json.Marshal(challenge(offer("eip155:84532")))
		w.Header().Set("PAYMENT-REQUIRED", base64.StdEncoding.EncodeToString(raw))
		writeProblem(w, 429, "rate_limited", "spent")
	})

	_, err := f.client(t, Options{DisableRetries: true}).Chains(context.Background())

	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 429 || apiErr.PaymentRequired != nil || apiErr.Paid {
		t.Fatalf("got %+v", err)
	}

	if f.calls()[0].header.Get("Accept-Payment") != "" {
		t.Fatal("opted into x402 without a payer")
	}
}

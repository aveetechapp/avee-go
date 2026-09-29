package compat_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	avee "github.com/aveetechapp/avee-go"
)

var (
	_ func(*avee.Client, context.Context) (*avee.ChainList, error)                                            = (*avee.Client).Chains
	_ func(*avee.Client, context.Context, avee.PairsParams) (*avee.PairPage, error)                           = (*avee.Client).Pairs
	_ func(*avee.Client, context.Context, avee.PairsParams) iter.Seq2[avee.PairInfo, error]                   = (*avee.Client).IterPairs
	_ func(*avee.Client, context.Context, string, string) (*avee.PairInfo, error)                             = (*avee.Client).Pair
	_ func(*avee.Client, context.Context, avee.PairBatchRequest) (*avee.PairBatchResponse, error)             = (*avee.Client).PairBatch
	_ func(*avee.Client, context.Context, string, string, avee.PairCandlesParams) (*avee.ChartCandles, error) = (*avee.Client).PairCandles
	_ func(*avee.Client) avee.ResponseInfo                                                                    = (*avee.Client).LastResponse
	_ func(avee.Options) (*avee.Client, error)                                                                = avee.New
	_ avee.Payer                                                                                              = walletPayer{}
	_ error                                                                                                   = (*avee.APIError)(nil)
	_ error                                                                                                   = (*avee.ValidationError)(nil)
	_ error                                                                                                   = (*avee.PaymentError)(nil)
)

type walletPayer struct{}

func (walletPayer) Networks() []string {
	return []string{"eip155:84532"}
}

func (walletPayer) Sign(_ context.Context, p avee.PaymentContext) (avee.PaymentSignature, error) {
	if p.Requirement.Network != "eip155:84532" || p.Requirement.Amount == "" || p.Requirement.PayTo == "" || p.PaymentID == "" {
		return avee.PaymentSignature{}, avee.ErrPaymentDeclined
	}

	return avee.PaymentSignature{Payload: map[string]any{"signature": "0xsig"}}, nil
}

func server(t *testing.T) *httptest.Server {
	t.Helper()

	challenge, _ := json.Marshal(map[string]any{
		"x402Version": 2,
		"accepts": []any{map[string]any{
			"scheme": "exact", "network": "eip155:84532", "amount": "2000", "maxAmountRequired": "2000",
			"asset": "0xasset", "payTo": "0xpayee", "maxTimeoutSeconds": 60,
		}},
	})

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "req-1")

		switch r.URL.Path {
		case "/api/v1/chains":
			_, _ = w.Write([]byte(`{"items":[{"slug":"base","chain_id":8453,"latest_block":1,"block_lag_seconds":0,"protocols":[]}]}`))
		case "/api/v1/pairs":
			if r.URL.Query().Get("cursor") == "" {
				_, _ = w.Write([]byte(`{"items":[{"pair_address":"a1"},{"pair_address":"a2"}],"next_cursor":"p2"}`))

				return
			}

			_, _ = w.Write([]byte(`{"items":[{"pair_address":"a3"}]}`))
		case "/api/v1/status":
			if r.Header.Get("PAYMENT-SIGNATURE") == "" {
				w.Header().Set("PAYMENT-REQUIRED", base64.StdEncoding.EncodeToString(challenge))
				w.WriteHeader(http.StatusPaymentRequired)
				_, _ = w.Write(challenge)

				return
			}

			receipt, _ := json.Marshal(map[string]any{"success": true, "transaction": "0xtx", "network": "eip155:84532"})
			w.Header().Set("PAYMENT-RESPONSE", base64.StdEncoding.EncodeToString(receipt))
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"type":"t","title":"chain not found","status":404,"code":"chain_not_found","detail":"no chain nope","param":"chain","request_id":"req-1"}`))
		}
	}))
	t.Cleanup(s.Close)

	return s
}

func TestV010ClientOperationsIteratorsErrorsAndPayer(t *testing.T) {
	s := server(t)
	ctx := context.Background()

	if avee.DefaultBaseURL == "" || avee.PreviewBaseURL == "" || avee.Version == "" {
		t.Fatal("the base URLs and version are exported")
	}

	c, err := avee.New(avee.Options{
		BaseURL:          s.URL + "/api/v1",
		HTTPClient:       s.Client(),
		Header:           http.Header{"X-Trace": {"compat"}},
		APIKey:           "k1",
		UserAgent:        "compat/1",
		Timeout:          5 * time.Second,
		MaxRetryDelay:    time.Second,
		MaxRetries:       1,
		MaxResponseBytes: 1 << 20,
		Payer:            walletPayer{},
	})
	if err != nil {
		t.Fatal(err)
	}

	chains, err := c.Chains(ctx)
	if err != nil || len(chains.Items) != 1 || chains.Items[0].Slug != "base" {
		t.Fatalf("chains %+v %v", chains, err)
	}

	info := c.LastResponse()
	if info.Operation != "chains" || info.Status != 200 || info.RequestID != "req-1" {
		t.Fatalf("last response %+v", info)
	}

	page, err := c.Pairs(ctx, avee.PairsParams{Chains: []avee.ChainSlug{"base"}, Limit: avee.Ptr(int32(2)), MinLiquidityUSD: avee.Ptr(1000.0)})
	if err != nil || len(page.Items) != 2 || page.NextCursor == nil {
		t.Fatalf("pairs %+v %v", page, err)
	}

	var all []string
	for pair, err := range c.IterPairs(ctx, avee.PairsParams{}) {
		if err != nil {
			t.Fatal(err)
		}

		all = append(all, pair.PairAddress)
	}

	if len(all) != 3 {
		t.Fatalf("iterated %v", all)
	}

	_, err = c.Pair(ctx, "nope", "0xabc")

	var apiErr *avee.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 404 || apiErr.Code != "chain_not_found" || apiErr.Detail == "" || apiErr.Param != "chain" || apiErr.RequestID != "req-1" || apiErr.Retryable() || apiErr.Paid {
		t.Fatalf("problem %v", err)
	}

	_, err = c.Pairs(ctx, avee.PairsParams{Limit: avee.Ptr(int32(1000))})

	var invalid *avee.ValidationError
	if !errors.As(err, &invalid) || invalid.Reason == "" {
		t.Fatalf("validation %v", err)
	}

	status, err := c.Status(ctx)
	if err != nil || status.Status != "ok" {
		t.Fatalf("paid status %+v %v", status, err)
	}

	if receipt := c.LastResponse().Payment; receipt == nil || !receipt.Success || receipt.Transaction != "0xtx" {
		t.Fatalf("receipt %+v", receipt)
	}

	declining := avee.NewPayer([]string{"eip155:84532"}, func(context.Context, avee.PaymentContext) (avee.PaymentSignature, error) {
		return avee.PaymentSignature{}, avee.ErrPaymentDeclined
	})

	d, err := avee.New(avee.Options{BaseURL: s.URL + "/api/v1", HTTPClient: s.Client(), Payer: declining, DisableRetries: true})
	if err != nil {
		t.Fatal(err)
	}

	_, err = d.Status(ctx)

	var payErr *avee.PaymentError
	if !errors.As(err, &payErr) || !errors.Is(err, avee.ErrPaymentDeclined) || payErr.Required == nil || payErr.Operation != "status" {
		t.Fatalf("declined %v", err)
	}

	if errors.Is(avee.ErrTimeout, avee.ErrNoPayableNetwork) {
		t.Fatal("the sentinel errors are distinct")
	}
}

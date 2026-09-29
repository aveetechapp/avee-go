package avee

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func refusingPayer(t *testing.T) Payer {
	t.Helper()

	return NewPayer(nil, func(context.Context, PaymentContext) (PaymentSignature, error) {
		t.Error("the payer was asked to sign a challenge the SDK should have refused")

		return PaymentSignature{}, ErrPaymentDeclined
	})
}

func TestRedirectsAreNeverFollowedSoKeysAndSignaturesStayOnTheHost(t *testing.T) {
	checkGoroutines(t)

	var elsewhere atomic.Int32
	other := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		elsewhere.Add(1)
		writeJSON(w, 200, map[string]any{"status": "ok"})
	})

	f := newFake(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		if r.URL.Path == "/api/v1/chains" && r.Header.Get("PAYMENT-SIGNATURE") == "" {
			writeChallenge(w, challenge(offer("eip155:84532")), true)

			return
		}

		http.Redirect(w, r, other.URL+"/steal", http.StatusTemporaryRedirect)
	})

	payer := NewPayer(nil, func(context.Context, PaymentContext) (PaymentSignature, error) {
		return PaymentSignature{Payload: map[string]any{"signature": "0x"}}, nil
	})
	c := f.client(t, Options{APIKey: "k1", Payer: payer})

	_, err := c.Status(context.Background())

	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTemporaryRedirect || apiErr.Retryable() {
		t.Fatalf("status: %v", err)
	}

	_, err = c.Chains(context.Background())
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTemporaryRedirect || !apiErr.Paid {
		t.Fatalf("paid request: %v", err)
	}

	if elsewhere.Load() != 0 || len(f.calls()) != 3 {
		t.Fatalf("%d requests reached the redirect target, %d the API", elsewhere.Load(), len(f.calls()))
	}
}

func TestUnreadableChallengesAreRefusedBeforeThePayer(t *testing.T) {
	good, _ := json.Marshal(challenge(offer("eip155:84532")))

	cases := map[string]func(w http.ResponseWriter){
		"header not base64": func(w http.ResponseWriter) {
			w.Header().Set("PAYMENT-REQUIRED", "%%%not-base64%%%")
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write(good)
		},
		"header not JSON": func(w http.ResponseWriter) {
			w.Header().Set("PAYMENT-REQUIRED", base64.StdEncoding.EncodeToString([]byte("{not json")))
			w.WriteHeader(http.StatusPaymentRequired)
		},
		"no accepts": func(w http.ResponseWriter) {
			writeChallenge(w, challenge(), true)
		},
		"accepts of the wrong type": func(w http.ResponseWriter) {
			writeChallenge(w, map[string]any{"x402Version": 2, "accepts": []any{"exact"}}, true)
		},
		"body not JSON": func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write([]byte("pay up"))
		},
	}

	for name, write := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ int) { write(w) })

			_, err := f.client(t, Options{Payer: refusingPayer(t)}).Chains(context.Background())

			var payErr *PaymentError
			if !errors.As(err, &payErr) || !strings.Contains(payErr.Reason, "unreadable") || len(f.calls()) != 1 {
				t.Fatalf("%v after %d requests", err, len(f.calls()))
			}
		})
	}
}

func TestMalformedOffersAreRefusedBeforeThePayer(t *testing.T) {
	cases := map[string]func(o map[string]any){
		"zero amount":           func(o map[string]any) { o["amount"] = "0" },
		"negative amount":       func(o map[string]any) { o["amount"] = "-5" },
		"fractional amount":     func(o map[string]any) { o["amount"] = "1.5" },
		"empty amount":          func(o map[string]any) { o["amount"] = "" },
		"amount beyond uint256": func(o map[string]any) { o["amount"] = strings.Repeat("9", 79) },
		"amount above the max":  func(o map[string]any) { o["amount"], o["maxAmountRequired"] = "2001", "2000" },
		"no payee":              func(o map[string]any) { o["payTo"] = "" },
		"no asset":              func(o map[string]any) { o["asset"] = "" },
		"amount not a string":   func(o map[string]any) { o["amount"] = 2000 },
	}

	for name, spoil := range cases {
		t.Run(name, func(t *testing.T) {
			o := offer("eip155:84532")
			spoil(o)

			f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
				writeChallenge(w, challenge(o), true)
			})

			_, err := f.client(t, Options{Payer: refusingPayer(t)}).Chains(context.Background())

			var payErr *PaymentError
			if !errors.As(err, &payErr) || len(f.calls()) != 1 {
				t.Fatalf("%v after %d requests", err, len(f.calls()))
			}
		})
	}
}

func TestOfferAmountRules(t *testing.T) {
	ok := offer("eip155:84532")

	var base PaymentRequirements

	raw, _ := json.Marshal(ok)
	if err := json.Unmarshal(raw, &base); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		amount, max string
		valid       bool
	}{
		{"2000", "2000", true},
		{"1", "", true},
		{"0002000", "2000", true},
		{"1999", "2000", true},
		{strings.Repeat("9", 78), strings.Repeat("9", 78), true},
		{"2001", "2000", false},
		{"10000", "9999", false},
		{"0", "", false},
		{"000", "", false},
		{"+1", "", false},
		{" 1", "", false},
		{"1_000", "", false},
		{"1", "0", false},
		{"1", "x", false},
	} {
		o := base
		o.Amount, o.MaxAmountRequired = tc.amount, tc.max
		if err := checkOffer(o); (err == nil) != tc.valid {
			t.Errorf("amount %q max %q: %v", tc.amount, tc.max, err)
		}
	}
}

func TestAPaidRequestThatTimesOutIsNeverRetried(t *testing.T) {
	checkGoroutines(t)

	release := make(chan struct{})
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		if r.Header.Get("PAYMENT-SIGNATURE") == "" {
			writeChallenge(w, challenge(offer("eip155:84532")), true)

			return
		}

		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	defer close(release)

	var signs atomic.Int32
	payer := NewPayer(nil, func(context.Context, PaymentContext) (PaymentSignature, error) {
		signs.Add(1)

		return PaymentSignature{Payload: map[string]any{"signature": "0x"}}, nil
	})

	_, err := f.client(t, Options{Payer: payer, Timeout: 50 * time.Millisecond, MaxRetryDelay: time.Millisecond}).Chains(context.Background())
	if !errors.Is(err, ErrTimeout) || signs.Load() != 1 || len(f.calls()) != 2 {
		t.Fatalf("%v after %d signatures and %d requests", err, signs.Load(), len(f.calls()))
	}
}

func TestAPayerWithoutASignFunctionDeclines(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		writeChallenge(w, challenge(offer("eip155:84532")), true)
	})

	_, err := f.client(t, Options{Payer: NewPayer(nil, nil)}).Chains(context.Background())
	if !errors.Is(err, ErrPaymentDeclined) || len(f.calls()) != 1 {
		t.Fatalf("%v after %d requests", err, len(f.calls()))
	}
}

func TestMalformedProblemDetailsFallBackToTheRawBody(t *testing.T) {
	for _, body := range []string{`{"code":5,"title":["x"]}`, `{"code":`, `[]`, `null`, strings.Repeat("é", 300)} {
		f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(body))
		})

		_, err := f.client(t, Options{}).Chains(context.Background())

		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Problem != nil || apiErr.Code != "" || apiErr.Status != 404 {
			t.Fatalf("body %.20q: %v", body, err)
		}

		if !utf8.ValidString(apiErr.Detail) || !utf8.ValidString(apiErr.Body) || len(apiErr.Detail) > clipLimit+len("…") {
			t.Fatalf("body %.20q: detail %q", body, apiErr.Detail)
		}
	}
}

func TestContractBreakingResponsesAreErrorsNotData(t *testing.T) {
	for _, body := range []string{strings.Repeat("[", 20000), `{"status": 5}`, `not json`, `{"status": "ok"`} {
		f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		})

		out, err := f.client(t, Options{}).Status(context.Background())

		var v *ValidationError
		if !errors.As(err, &v) || out != nil {
			t.Fatalf("body %.20q: %v %v", body, out, err)
		}
	}
}

func TestOversizedErrorsAreRefusedToo(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		writeProblem(w, 500, "internal", strings.Repeat("x", 4096))
	})

	_, err := f.client(t, Options{MaxResponseBytes: 1024, DisableRetries: true}).Status(context.Background())

	var v *ValidationError
	if !errors.As(err, &v) || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("got %v", err)
	}
}

func TestUnionsDecodeKnownUnknownAndNull(t *testing.T) {
	var info TransactionBlockInfo

	if err := json.Unmarshal([]byte(`{"seq_no":1,"shard_id":2,"work_chain":0,"x":{}}`), &info); err != nil || info.TonTxData == nil || info.TonTxData.SeqNo != 1 {
		t.Fatalf("ton: %+v %v", info, err)
	}

	if err := json.Unmarshal([]byte(`{"slot":7}`), &info); err != nil || info.TonTxData != nil || string(info.Raw) != `{"slot":7}` {
		t.Fatalf("unknown: %+v %v", info, err)
	}

	if err := json.Unmarshal([]byte(`null`), &info); err != nil || info.TonTxData != nil || info.EthereumTxData != nil || info.Raw != nil {
		t.Fatalf("null: %+v %v", info, err)
	}

	if err := json.Unmarshal([]byte(`[1]`), &info); err == nil {
		t.Fatal("an array decoded as a union")
	}

	out, err := json.Marshal(TransactionBlockInfo{Raw: json.RawMessage(`{"slot":7}`)})
	if err != nil || string(out) != `{"slot":7}` {
		t.Fatalf("marshal: %s %v", out, err)
	}
}

func TestIteratorStopsOnALongerCursorCycle(t *testing.T) {
	cycle := []string{"c1", "c2", "c3", "c4", "c5"}
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		cur := r.URL.Query().Get("cursor")
		next := cycle[0]

		for i, c := range cycle {
			if c == cur {
				next = cycle[(i+1)%len(cycle)]
			}
		}

		writeJSON(w, 200, map[string]any{"items": []any{map[string]any{"pair_address": cur}}, "next_cursor": next})
	})

	var last error
	n := 0

	for _, err := range f.client(t, Options{}).IterTrending(context.Background(), TrendingParams{}) {
		n++
		last = err
	}

	var v *ValidationError
	if !errors.As(last, &v) || n > 3*len(cycle)+2 {
		t.Fatalf("after %d items: %v", n, last)
	}
}

func TestCursorLoopNeverFlagsDistinctCursorsAndHoldsNoHistory(t *testing.T) {
	var loops cursorLoop

	prev := ""
	for i := range 100_000 {
		next := fmt.Sprintf("c%d", i)
		if loops.repeats(&prev, next) {
			t.Fatalf("cursor %s flagged as repeated", next)
		}

		prev = next
	}

	if !loops.repeats(&prev, prev) {
		t.Fatal("a cursor that points at itself was not flagged")
	}
}

func TestIteratorHonoursCancellationBetweenPages(t *testing.T) {
	checkGoroutines(t)

	f := newFake(t, func(w http.ResponseWriter, r *http.Request, n int) {
		writeJSON(w, 200, map[string]any{"items": []any{map[string]any{"pair_address": "a"}}, "next_cursor": fmt.Sprintf("c%d", n)})
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var last error
	for _, err := range f.client(t, Options{}).IterTrending(ctx, TrendingParams{}) {
		cancel()

		last = err
	}

	if !errors.Is(last, context.Canceled) || len(f.calls()) != 1 {
		t.Fatalf("%v after %d requests", last, len(f.calls()))
	}
}

func TestNewRejectsReservedAndMultiLineHeaders(t *testing.T) {
	for _, h := range []http.Header{
		{"Payment-Signature": {"x"}},
		{"payment-signature": {"x"}},
		{"X-Trace": {"a\r\nX-Injected: 1"}},
	} {
		if _, err := New(Options{Header: h}); err == nil {
			t.Errorf("accepted %v", h)
		}
	}

	if _, err := New(Options{Header: http.Header{"X-Trace": {"abc"}}}); err != nil {
		t.Fatal(err)
	}
}

func TestClipKeepsRunesWhole(t *testing.T) {
	s := clip(strings.Repeat("é", 150))
	if !utf8.ValidString(s) || !strings.HasSuffix(s, "…") || len(s) > clipLimit+len("…") {
		t.Fatalf("clip gave %q", s)
	}
}

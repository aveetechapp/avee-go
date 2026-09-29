package astra

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

const tslaAstra = "2222222222222222222222222222222222222222222222222222222222222222"

var (
	btcEntryJSON  = map[string]any{"symbol": "Crypto.BTC/USD", "asset_type": "Crypto", "category": "crypto", "astra_id": btcAstra, "pyth_id": btc}
	tslaEntryJSON = map[string]any{"symbol": "Equity.RH.TSLA/USD", "asset_type": "Equity", "category": "equity", "astra_id": tslaAstra}
)

func TestFeedIDsReadsTheWholeMap(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"items": []any{btcEntryJSON, tslaEntryJSON}})
	})
	c := newTestClient(t, f.URL, Options{})

	got, err := c.FeedIDs(t.Context(), FeedIDsOptions{})
	if err != nil {
		t.Fatal(err)
	}

	want := FeedIDMap{Items: []FeedIDEntry{
		{Symbol: "Crypto.BTC/USD", AssetType: "Crypto", Category: "crypto", AstraID: btcAstra, PythID: btc},
		{Symbol: "Equity.RH.TSLA/USD", AssetType: "Equity", Category: "equity", AstraID: tslaAstra},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}

	if r := f.recorded()[0]; r.URL.Path != "/v1/feed-ids" || r.URL.RawQuery != "" {
		t.Fatalf("request %s", r.URL)
	}
}

func TestFeedIDsSendsNormalisedIDsInOneRequest(t *testing.T) {
	unknown := strings.Repeat("11", 32)
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"items": []any{btcEntryJSON}, "missing": []string{unknown}})
	})
	c := newTestClient(t, f.URL, Options{})

	got, err := c.FeedIDs(t.Context(), FeedIDsOptions{PythIDs: []string{"0x" + strings.ToUpper(btc), btc, unknown}, AstraIDs: []string{eth}, Category: "crypto"})
	if err != nil {
		t.Fatal(err)
	}

	if len(got.Items) != 1 || got.Items[0].PythID != btc || !reflect.DeepEqual(got.Missing, []string{unknown}) {
		t.Fatalf("got %+v", got)
	}

	reqs := f.recorded()
	if len(reqs) != 1 {
		t.Fatalf("%d requests", len(reqs))
	}

	q := reqs[0].URL.Query()
	if q.Get("pyth_ids") != btc+","+unknown || q.Get("astra_ids") != eth || q.Get("category") != "crypto" {
		t.Fatalf("query %v", q)
	}
}

func TestFeedIDsSplitsALongListAndMergesSorted(t *testing.T) {
	ids := make([]string, 0, MaxIDsPerURL+1)
	for i := range MaxIDsPerURL + 1 {
		ids = append(ids, fmt.Sprintf("%064x", i))
	}

	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		asked := strings.Split(r.URL.Query().Get("pyth_ids"), ",")
		if len(asked) > 1 {
			writeJSON(w, 200, map[string]any{"items": []any{tslaEntryJSON, btcEntryJSON}, "missing": asked[1:]})

			return
		}

		writeJSON(w, 200, map[string]any{"items": []any{btcEntryJSON}, "missing": asked})
	})
	c := newTestClient(t, f.URL, Options{})

	got, err := c.FeedIDs(t.Context(), FeedIDsOptions{PythIDs: ids})
	if err != nil {
		t.Fatal(err)
	}

	reqs := f.recorded()
	if len(reqs) != 2 || len(strings.Split(reqs[0].URL.Query().Get("pyth_ids"), ",")) != MaxIDsPerURL {
		t.Fatalf("%d requests", len(reqs))
	}

	if len(got.Items) != 2 || got.Items[0].Symbol != "Crypto.BTC/USD" || got.Items[1].Symbol != "Equity.RH.TSLA/USD" {
		t.Fatalf("items %+v", got.Items)
	}

	if !reflect.DeepEqual(got.Missing, ids[1:]) {
		t.Fatalf("missing %d ids", len(got.Missing))
	}
}

func TestFeedIDsValidatesBeforeAnyRequest(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]any{"items": []any{}}) })
	c := newTestClient(t, f.URL, Options{})

	got, err := c.FeedIDs(t.Context(), FeedIDsOptions{PythIDs: []string{}})
	if err != nil || len(got.Items) != 0 || got.Missing == nil {
		t.Fatalf("empty filter: %+v, %v", got, err)
	}

	var ve *ValidationError
	if _, err = c.FeedIDs(t.Context(), FeedIDsOptions{AstraIDs: []string{"nope"}}); !errors.As(err, &ve) {
		t.Fatalf("err = %v", err)
	}

	if n := len(f.recorded()); n != 0 {
		t.Fatalf("%d requests", n)
	}
}

func TestFeedIDsProblemIsNotRetried(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"type":"about:blank","title":"Bad Request","status":400,"detail":"Too many feed ids, the limit is 500"}`))
	})
	c := newTestClient(t, f.URL, Options{})

	_, err := c.FeedIDs(t.Context(), FeedIDsOptions{PythIDs: []string{btc}})

	var he *HTTPError
	if !errors.As(err, &he) || he.Status != 400 || he.Problem == nil || len(f.recorded()) != 1 {
		t.Fatalf("err = %v", err)
	}
}

func TestFeedIDsRejectsAMalformedMap(t *testing.T) {
	bodies := map[string]any{
		"no items":         map[string]any{},
		"entry without id": map[string]any{"items": []any{map[string]any{"symbol": "x", "asset_type": "Crypto", "category": "crypto"}}},
		"bad missing id":   map[string]any{"items": []any{}, "missing": []string{"zz"}},
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, body) })
			c := newTestClient(t, f.URL, Options{})

			var ve *ValidationError
			if _, err := c.FeedIDs(t.Context(), FeedIDsOptions{}); !errors.As(err, &ve) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestFeedIDsPacksBothListsIntoOneURLBudgetAndMergesOnce(t *testing.T) {
	gone := strings.Repeat("33", 32)
	pyth := make([]string, 0, 150)
	astra := make([]string, 0, 100)

	for i := range 150 {
		pyth = append(pyth, fmt.Sprintf("%064x", i+1000))
	}

	for i := range 100 {
		astra = append(astra, fmt.Sprintf("%064x", i+2000))
	}

	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("pyth_ids") {
			writeJSON(w, 200, map[string]any{"items": []any{tslaEntryJSON, btcEntryJSON}, "missing": []string{gone}})

			return
		}

		writeJSON(w, 200, map[string]any{"items": []any{btcEntryJSON}, "missing": []string{gone}})
	})
	c := newTestClient(t, f.URL, Options{})

	got, err := c.FeedIDs(t.Context(), FeedIDsOptions{PythIDs: pyth, AstraIDs: astra})
	if err != nil {
		t.Fatal(err)
	}

	reqs := f.recorded()
	if len(reqs) != 2 {
		t.Fatalf("%d requests", len(reqs))
	}

	first, second := reqs[0].URL.Query(), reqs[1].URL.Query()
	if len(strings.Split(first.Get("pyth_ids"), ",")) != 150 || len(strings.Split(first.Get("astra_ids"), ",")) != 50 {
		t.Fatalf("first chunk %v", first)
	}

	if second.Has("pyth_ids") || second.Get("astra_ids") != strings.Join(astra[50:], ",") {
		t.Fatalf("second chunk %v", second)
	}

	if len(got.Items) != 2 || got.Items[0].Symbol != "Crypto.BTC/USD" || !reflect.DeepEqual(got.Missing, []string{gone}) {
		t.Fatalf("got %+v", got)
	}
}

func TestFeedIDsFilteredAnswerWithoutMissingIsEmptyNotNil(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"items": []any{btcEntryJSON}})
	})
	c := newTestClient(t, f.URL, Options{})

	got, err := c.FeedIDs(t.Context(), FeedIDsOptions{PythIDs: []string{btc}})
	if err != nil || got.Missing == nil || len(got.Missing) != 0 || len(got.Items) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestFeedIDsFailsWholeOnAFailedChunk(t *testing.T) {
	ids := make([]string, 0, MaxIDsPerURL+1)
	for i := range MaxIDsPerURL + 1 {
		ids = append(ids, fmt.Sprintf("%064x", i))
	}

	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Count(r.URL.Query().Get("pyth_ids"), ",") == 0 {
			writeText(w, 404, "gone")

			return
		}

		writeJSON(w, 200, map[string]any{"items": []any{btcEntryJSON}, "missing": []string{}})
	})
	c := newTestClient(t, f.URL, Options{})

	got, err := c.FeedIDs(t.Context(), FeedIDsOptions{PythIDs: ids})

	var he *HTTPError
	if !errors.As(err, &he) || got.Items != nil || got.Missing != nil {
		t.Fatalf("got %+v, %v", got, err)
	}
}

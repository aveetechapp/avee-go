package avee

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

type fixedResponse struct {
	body []byte
}

func (f fixedResponse) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode:    http.StatusOK,
		Header:        http.Header{"Content-Type": {"application/json"}},
		Body:          io.NopCloser(bytes.NewReader(f.body)),
		ContentLength: int64(len(f.body)),
		Request:       r,
	}, nil
}

func pageOf(b *testing.B, schema string, n int) []byte {
	b.Helper()

	spec := loadSpec(b)

	var one map[string]any
	if err := json.Unmarshal(spec.schemaInstance(b, schema, everyField), &one); err != nil {
		b.Fatal(err)
	}

	items := make([]any, n)
	for i := range items {
		items[i] = one
	}

	data, err := json.Marshal(map[string]any{"items": items, "next_cursor": "c2"})
	if err != nil {
		b.Fatal(err)
	}

	return data
}

func BenchmarkDecodePairPage(b *testing.B) {
	data := pageOf(b, "PairInfo", 100)

	b.ReportAllocs()
	b.SetBytes(int64(len(data)))

	for b.Loop() {
		var page PairPage
		if err := json.Unmarshal(data, &page); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeTradePage(b *testing.B) {
	data := pageOf(b, "Transaction", 100)

	b.ReportAllocs()
	b.SetBytes(int64(len(data)))

	for b.Loop() {
		var page TradePage
		if err := json.Unmarshal(data, &page); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPairsCall(b *testing.B) {
	data := pageOf(b, "PairInfo", 100)

	c, err := New(Options{BaseURL: "https://api.example/api/v1", HTTPClient: &http.Client{Transport: fixedResponse{body: data}}})
	if err != nil {
		b.Fatal(err)
	}

	ctx := context.Background()

	b.ReportAllocs()
	b.SetBytes(int64(len(data)))

	for b.Loop() {
		if _, err := c.Pairs(ctx, PairsParams{}); err != nil {
			b.Fatal(err)
		}
	}
}

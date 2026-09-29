package avee

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestLiveSmoke(t *testing.T) {
	if os.Getenv("AVEE_LIVE") != "1" {
		t.Skip("set AVEE_LIVE=1 to run against a real API")
	}

	base := os.Getenv("AVEE_BASE_URL")
	if base == "" {
		base = PreviewBaseURL
	}

	c, err := New(Options{BaseURL: base})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	status, err := c.Status(ctx)
	if err != nil || status.Status == "" {
		t.Fatalf("status: %v", err)
	}

	chains, err := c.Chains(ctx)
	if err != nil || len(chains.Items) == 0 {
		t.Fatalf("chains: %v", err)
	}

	page, err := c.Pairs(ctx, PairsParams{Chains: []string{chains.Items[0].Slug}, Limit: Ptr(int32(5))})
	if err != nil {
		t.Fatalf("pairs: %v", err)
	}

	if len(page.Items) > 0 {
		p := page.Items[0]
		if _, err = c.Pair(ctx, p.Network.Slug, p.PairAddress); err != nil {
			t.Fatalf("pair: %v", err)
		}
	}

	if info := c.LastResponse(); !info.RateLimit.Known || info.RequestID == "" {
		t.Fatalf("rate limit info missing: %+v", info)
	}
}

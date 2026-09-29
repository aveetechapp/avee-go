package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	avee "github.com/aveetechapp/avee-go"
)

func main() {
	base := os.Getenv("AVEE_BASE_URL")
	if base == "" {
		base = avee.PreviewBaseURL
	}

	c, err := avee.New(avee.Options{BaseURL: base})
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()

	chains, err := c.Chains(ctx)
	if err != nil {
		var apiErr *avee.APIError
		if errors.As(err, &apiErr) {
			log.Fatalf("%d %s %s", apiErr.Status, apiErr.Code, apiErr.RequestID)
		}

		log.Fatal(err)
	}

	n := 0
	for pair, err := range c.IterPairs(ctx, avee.PairsParams{Chains: []string{chains.Items[0].Slug}, Limit: avee.Ptr(int32(20))}) {
		if err != nil {
			log.Fatal(err)
		}

		fmt.Println(pair.Network.Slug, pair.PairAddress)

		if n++; n == 40 {
			break
		}
	}

	fmt.Printf("%d pairs; budget left: %d\n", n, c.LastResponse().RateLimit.Remaining)
}

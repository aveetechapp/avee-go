package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	astra "github.com/aveetechapp/avee-go/astra"
)

const (
	btc = "0xe62df6c8b4a85fe1a67db44dc12de5db330f7ac66b72dc658afedf0f4a415b43"
	eth = "0xff61491a931112ddf1bd8147cd1b641375f79f5825126d665480874634fd0ace"
)

func main() {
	c, err := astra.New(astra.Options{BaseURL: os.Getenv("ASTRA_BASE_URL")})
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	prices, err := c.LatestPrices(ctx, []string{btc, eth}, astra.PriceQuery{})
	if err != nil {
		log.Fatal(err)
	}

	for _, u := range prices {
		wei, err := u.Price.Scaled(18)
		if err != nil {
			log.Fatal(err)
		}

		fmt.Println(u.ID, u.Price.Decimal(), "±", u.Price.ConfFloat64(), "as 1e18:", wei)
	}

	status, err := c.Status(ctx)
	if err != nil {
		log.Fatal(err)
	}

	for _, f := range status.Feeds {
		if f.Stale {
			fmt.Println("stale:", f.Symbol)
		}
	}
}

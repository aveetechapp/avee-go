package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	astra "github.com/aveetechapp/avee-go/astra"
)

const btc = "0xe62df6c8b4a85fe1a67db44dc12de5db330f7ac66b72dc658afedf0f4a415b43"

func main() {
	c, err := astra.New(astra.Options{BaseURL: os.Getenv("ASTRA_BASE_URL")})
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	sub, err := c.Subscribe(ctx, []string{btc}, astra.SubscribeOptions{
		Channel:       astra.ChannelFixed1000ms,
		OnError:       func(err error) { log.Println("astra:", err) },
		OnStateChange: func(s astra.ConnectionState) { log.Println("connection", s) },
	})
	if err != nil {
		log.Fatal(err)
	}
	defer sub.Close()

	for u := range sub.Updates() {
		fmt.Println(time.Unix(u.Price.PublishTime, 0).UTC().Format(time.RFC3339), u.Price.Decimal())
	}

	if err := sub.Err(); err != nil {
		log.Fatal(err)
	}
}

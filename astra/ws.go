package astra

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

type wsTransport struct {
	client    *Client
	url       string
	subscribe []byte
	idle      time.Duration
	maxBytes  int64
}

type wsSubscribe struct {
	Type                  string   `json:"type"`
	IDs                   []string `json:"ids"`
	Verbose               bool     `json:"verbose"`
	Binary                bool     `json:"binary"`
	IgnoreInvalidPriceIDs bool     `json:"ignore_invalid_price_ids"`
}

func newWSTransport(c *Client, ids []string, opts SubscribeOptions) (*wsTransport, error) {
	q := url.Values{}
	if opts.Channel != "" {
		q.Set("channel", string(opts.Channel))
	}

	u := c.resolve("ws", q)
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}

	msg, err := json.Marshal(wsSubscribe{Type: "subscribe", IDs: ids, Verbose: true, IgnoreInvalidPriceIDs: opts.IgnoreInvalid})
	if err != nil {
		return nil, fmt.Errorf("astra: encode subscribe: %w", err)
	}

	return &wsTransport{client: c, url: u.String(), subscribe: msg, idle: opts.IdleTimeout, maxBytes: opts.MaxMessageBytes}, nil
}

func (t *wsTransport) run(ctx context.Context, sink *Subscription) error {
	dialCtx, cancelDial := context.WithTimeout(ctx, t.idle)
	conn, resp, err := websocket.Dial(dialCtx, t.url, &websocket.DialOptions{HTTPClient: t.client.http, HTTPHeader: t.client.header.Clone()})
	timedOut := errors.Is(dialCtx.Err(), context.DeadlineExceeded)
	cancelDial()

	if err != nil {
		return t.dialFailure(ctx, resp, timedOut, err)
	}

	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(closed)
		_ = conn.Close(websocket.StatusNormalClosure, "")
	})

	var (
		wg         sync.WaitGroup
		pingFailed atomic.Bool
	)

	pingCtx, cancelPing := context.WithCancel(context.Background())

	defer func() {
		cancelPing()
		wg.Wait()

		if !stop() {
			<-closed
		}

		_ = conn.CloseNow()
	}()

	conn.SetReadLimit(t.maxBytes)

	if err = conn.Write(ctx, websocket.MessageText, t.subscribe); err != nil {
		return t.readFailure(ctx, &pingFailed, err)
	}

	wg.Add(1)

	go func() {
		defer wg.Done()
		t.keepalive(pingCtx, conn, &pingFailed)
	}()

	var (
		buf         bytes.Buffer
		msg         wireStreamMessage
		feed        wireUpdate[streamMetadata]
		awaitingAck = true
	)

	for {
		typ, err := t.read(conn, &buf, awaitingAck)
		if err != nil {
			return t.readFailure(ctx, &pingFailed, err)
		}

		if typ != websocket.MessageText {
			continue
		}

		if err = handleWSMessage(buf.Bytes(), &msg, &feed, &awaitingAck, sink); err != nil {
			return err
		}
	}
}

func (t *wsTransport) read(conn *websocket.Conn, buf *bytes.Buffer, awaitingAck bool) (websocket.MessageType, error) {
	ctx, cancel := context.Background(), context.CancelFunc(func() {})
	if awaitingAck {
		ctx, cancel = context.WithTimeout(ctx, t.idle)
	}
	defer cancel()

	buf.Reset()

	typ, r, err := conn.Reader(ctx)
	if err == nil {
		if typ == websocket.MessageText {
			_, err = buf.ReadFrom(r)
		} else {
			_, err = io.Copy(io.Discard, r)
		}
	}

	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return typ, fmt.Errorf("%w: no WebSocket subscription response within %s", ErrTimeout, t.idle)
	}

	return typ, err
}

func (t *wsTransport) keepalive(ctx context.Context, conn *websocket.Conn, failed *atomic.Bool) {
	interval := t.idle / 2
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pctx, cancel := context.WithTimeout(ctx, interval)
			err := conn.Ping(pctx)
			cancel()

			if err != nil && ctx.Err() == nil {
				failed.Store(true)
				_ = conn.CloseNow()

				return
			}
		}
	}
}

func (t *wsTransport) dialFailure(ctx context.Context, resp *http.Response, timedOut bool, err error) error {
	if ctx.Err() != nil {
		return nil
	}

	if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols {
		if resp.Body == nil {
			return &HTTPError{Status: resp.StatusCode, URL: t.url}
		}

		defer resp.Body.Close()

		return readHTTPError(resp, t.url)
	}

	if timedOut {
		return fmt.Errorf("%w: WebSocket dial %s", ErrTimeout, t.url)
	}

	return fmt.Errorf("astra: WebSocket dial %s: %w", t.url, err)
}

func (t *wsTransport) readFailure(ctx context.Context, pingFailed *atomic.Bool, err error) error {
	if ctx.Err() != nil {
		return nil
	}

	if errors.Is(err, ErrTimeout) {
		return err
	}

	if pingFailed.Load() {
		return fmt.Errorf("%w: WebSocket ping unanswered within %s", ErrTimeout, t.idle/2)
	}

	switch status := websocket.CloseStatus(err); status {
	case websocket.StatusNormalClosure:
		return nil
	case -1:
		return fmt.Errorf("astra: WebSocket %s: %w", t.url, err)
	default:
		var ce websocket.CloseError
		errors.As(err, &ce)

		return fmt.Errorf("astra: WebSocket closed with %d: %s", int(status), clip(ce.Reason))
	}
}

func handleWSMessage(data []byte, msg *wireStreamMessage, feed *wireUpdate[streamMetadata], awaitingAck *bool, sink *Subscription) error {
	*feed = wireUpdate[streamMetadata]{}
	*msg = wireStreamMessage{PriceFeed: feed}

	if err := json.Unmarshal(data, msg); err != nil {
		sink.warn(invalidf("WebSocket message does not match the contract: %s", clip(err.Error())))

		return nil
	}

	switch msg.Type {
	case "response":
		failed := msg.Status == "error"

		if *awaitingAck {
			*awaitingAck = false

			if failed {
				return &SubscriptionError{Reason: clip(msg.Error)}
			}

			sink.opened()

			return nil
		}

		if failed {
			sink.report(&ServerError{Reason: clip(msg.Error)})
		}
	case "price_update":
		u, err := feed.decode()
		if err != nil {
			sink.warn(err)

			return nil
		}

		sink.accept(u)
	}

	return nil
}

package astra

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"
)

const sseReadBuffer = 32 << 10

var errMessageTooLarge = errors.New("astra: stream message exceeds the size limit")

type sseTransport struct {
	client   *Client
	http     *http.Client
	url      string
	idle     time.Duration
	maxBytes int
}

func newSSETransport(c *Client, ids []string, opts SubscribeOptions) *sseTransport {
	q := idValues(ids)
	if opts.Channel != "" {
		q.Set("channel", string(opts.Channel))
	}

	setFlag(q, "ignore_invalid_price_ids", opts.IgnoreInvalid)
	setFlag(q, "benchmarks_only", opts.BenchmarksOnly)

	stream := c.http
	if stream.Timeout > 0 {
		unbounded := *stream
		unbounded.Timeout = 0
		stream = &unbounded
	}

	return &sseTransport{
		client:   c,
		http:     stream,
		url:      c.resolve("v2/updates/price/stream", q).String(),
		idle:     opts.IdleTimeout,
		maxBytes: int(opts.MaxMessageBytes),
	}
}

func (t *sseTransport) run(ctx context.Context, sink *Subscription) error {
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var idle atomic.Bool

	timer := time.AfterFunc(t.idle, func() {
		idle.Store(true)
		cancel()
	})
	defer timer.Stop()

	req, err := http.NewRequestWithContext(cctx, http.MethodGet, t.url, nil)
	if err != nil {
		return fmt.Errorf("astra: build request: %w", err)
	}

	t.client.decorate(req.Header)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	resp, err := t.http.Do(req)
	if err != nil {
		return t.failure(ctx, &idle, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return readHTTPError(resp, t.url)
	}

	sink.opened()

	r := sseReader{
		br:    bufio.NewReaderSize(resp.Body, sseReadBuffer),
		max:   t.maxBytes,
		touch: func() { timer.Reset(t.idle) },
	}

	var env wireEnvelope

	for {
		data, err := r.next()
		if err != nil {
			if errors.Is(err, io.EOF) && !idle.Load() && ctx.Err() == nil {
				return nil
			}

			return t.failure(ctx, &idle, err)
		}

		dispatchEnvelope(data, &env, sink)
	}
}

func (t *sseTransport) failure(ctx context.Context, idle *atomic.Bool, err error) error {
	switch {
	case ctx.Err() != nil:
		return nil
	case idle.Load():
		return fmt.Errorf("%w: SSE stream idle for %s", ErrTimeout, t.idle)
	case errors.Is(err, errMessageTooLarge):
		return err
	default:
		var ue *url.Error
		if errors.As(err, &ue) {
			return fmt.Errorf("astra: SSE %s: %w", t.url, ue.Err)
		}

		return fmt.Errorf("astra: SSE %s: %w", t.url, err)
	}
}

func dispatchEnvelope(data []byte, env *wireEnvelope, sink *Subscription) {
	clear(env.Parsed)
	env.Parsed = env.Parsed[:0]

	if err := json.Unmarshal(data, env); err != nil {
		sink.warn(invalidf("SSE event does not match the contract: %s", clip(err.Error())))

		return
	}

	for i := range env.Parsed {
		u, err := env.Parsed[i].decode()
		if err != nil {
			sink.warn(err)

			continue
		}

		sink.accept(u)
	}
}

type sseReader struct {
	br    *bufio.Reader
	touch func()
	line  bytes.Buffer
	data  bytes.Buffer
	max   int
}

func (r *sseReader) readLine() ([]byte, error) {
	r.line.Reset()

	for {
		chunk, err := r.br.ReadSlice('\n')
		if r.line.Len()+len(chunk) > r.max {
			return nil, errMessageTooLarge
		}

		if errors.Is(err, bufio.ErrBufferFull) {
			r.line.Write(chunk)

			continue
		}

		if err != nil {
			return nil, err
		}

		line := chunk
		if r.line.Len() > 0 {
			r.line.Write(chunk)
			line = r.line.Bytes()
		}

		line = bytes.TrimSuffix(line, []byte{'\n'})

		return bytes.TrimSuffix(line, []byte{'\r'}), nil
	}
}

func (r *sseReader) next() ([]byte, error) {
	r.data.Reset()

	for {
		line, err := r.readLine()
		if err != nil {
			return nil, err
		}

		r.touch()

		if len(line) == 0 {
			if r.data.Len() > 0 {
				return r.data.Bytes(), nil
			}

			continue
		}

		if line[0] == ':' {
			continue
		}

		field, value, _ := bytes.Cut(line, []byte{':'})
		if !bytes.Equal(field, []byte("data")) {
			continue
		}

		value = bytes.TrimPrefix(value, []byte{' '})
		if r.data.Len() > 0 {
			r.data.WriteByte('\n')
		}

		if r.data.Len()+len(value) > r.max {
			return nil, errMessageTooLarge
		}

		r.data.Write(value)
	}
}

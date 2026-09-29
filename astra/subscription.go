package astra

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

type Transport string

const (
	TransportWebSocket Transport = "ws"
	TransportSSE       Transport = "sse"
)

type ConnectionState string

const (
	StateConnecting   ConnectionState = "connecting"
	StateOpen         ConnectionState = "open"
	StateReconnecting ConnectionState = "reconnecting"
	StateClosed       ConnectionState = "closed"
)

const (
	defaultIdleTimeout        = 45 * time.Second
	defaultMaxMessageBytes    = 1 << 20
	defaultReconnectBaseDelay = 500 * time.Millisecond
	defaultReconnectMaxDelay  = 30 * time.Second
	defaultStableAfter        = time.Minute
)

type SubscribeOptions struct {
	OnError            func(error)
	OnStateChange      func(ConnectionState)
	Transport          Transport
	Channel            Channel
	IdleTimeout        time.Duration
	ReconnectBaseDelay time.Duration
	ReconnectMaxDelay  time.Duration
	StableAfter        time.Duration
	MaxMessageBytes    int64
	IgnoreInvalid      bool
	BenchmarksOnly     bool
}

type Stats struct {
	Connects   uint64
	Reconnects uint64
	Coalesced  uint64
	Duplicates uint64
	Invalid    uint64
}

type streamTransport interface {
	run(ctx context.Context, sink *Subscription) error
}

type lastSeen struct {
	price       string
	conf        string
	emaPrice    string
	publishTime int64
}

type Subscription struct {
	transport  streamTransport
	wanted     map[string]struct{}
	pending    map[string]PriceUpdate
	last       map[string]lastSeen
	out        chan PriceUpdate
	notify     chan struct{}
	done       chan struct{}
	cancel     context.CancelFunc
	err        error
	openedAt   time.Time
	state      ConnectionState
	opts       SubscribeOptions
	ids        []string
	queue      []string
	head       int
	queued     int
	mu         sync.Mutex
	finished   bool
	connects   atomic.Uint64
	reconnects atomic.Uint64
	coalesced  atomic.Uint64
	duplicates atomic.Uint64
	invalid    atomic.Uint64
}

func (c *Client) Subscribe(ctx context.Context, ids []string, opts SubscribeOptions) (*Subscription, error) {
	norm, err := normalizeFeedIDs(ids, MaxIDsPerRequest)
	if err != nil {
		return nil, err
	}

	if opts.IdleTimeout < 0 || opts.ReconnectBaseDelay < 0 || opts.ReconnectMaxDelay < 0 || opts.StableAfter < 0 || opts.MaxMessageBytes < 0 {
		return nil, invalidf("subscribe options must not be negative")
	}

	opts.IdleTimeout = orDefault(opts.IdleTimeout, defaultIdleTimeout)
	opts.ReconnectBaseDelay = orDefault(opts.ReconnectBaseDelay, defaultReconnectBaseDelay)
	opts.ReconnectMaxDelay = orDefault(opts.ReconnectMaxDelay, defaultReconnectMaxDelay)
	opts.StableAfter = orDefault(opts.StableAfter, defaultStableAfter)
	opts.MaxMessageBytes = orDefault(opts.MaxMessageBytes, defaultMaxMessageBytes)
	opts.Transport = orDefault(opts.Transport, TransportWebSocket)

	var transport streamTransport

	switch opts.Transport {
	case TransportWebSocket:
		if opts.BenchmarksOnly {
			return nil, invalidf("BenchmarksOnly is supported on the SSE transport only")
		}

		transport, err = newWSTransport(c, norm, opts)
	case TransportSSE:
		if len(norm) > MaxIDsPerURL {
			return nil, invalidf("SSE carries feed ids in the URL: at most %d, got %d; use transport %q for more", MaxIDsPerURL, len(norm), TransportWebSocket)
		}

		transport = newSSETransport(c, norm, opts)
	default:
		return nil, invalidf("unknown transport %q", clip(string(opts.Transport)))
	}

	if err != nil {
		return nil, err
	}

	return startSubscription(ctx, norm, transport, opts), nil
}

func startSubscription(parent context.Context, ids []string, transport streamTransport, opts SubscribeOptions) *Subscription {
	ctx, cancel := context.WithCancel(parent)
	s := &Subscription{
		transport: transport,
		wanted:    make(map[string]struct{}, len(ids)),
		pending:   make(map[string]PriceUpdate, len(ids)),
		last:      make(map[string]lastSeen, len(ids)),
		out:       make(chan PriceUpdate),
		notify:    make(chan struct{}, 1),
		done:      make(chan struct{}),
		cancel:    cancel,
		state:     StateConnecting,
		opts:      opts,
		ids:       ids,
		queue:     make([]string, len(ids)),
	}

	for _, id := range ids {
		s.wanted[id] = struct{}{}
	}

	var wg sync.WaitGroup

	wg.Add(2)

	go func() {
		defer wg.Done()
		s.run(ctx)
	}()

	go func() {
		defer wg.Done()
		s.deliver(ctx)
	}()

	go func() {
		wg.Wait()
		cancel()
		close(s.out)
		s.setState(StateClosed)
		close(s.done)
	}()

	return s
}

func (s *Subscription) Updates() <-chan PriceUpdate {
	return s.out
}

func (s *Subscription) Done() <-chan struct{} {
	return s.done
}

func (s *Subscription) IDs() []string {
	return append([]string(nil), s.ids...)
}

func (s *Subscription) Close() error {
	s.cancel()
	<-s.done

	return nil
}

func (s *Subscription) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.err
}

func (s *Subscription) State() ConnectionState {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.state
}

func (s *Subscription) Stats() Stats {
	return Stats{
		Connects:   s.connects.Load(),
		Reconnects: s.reconnects.Load(),
		Coalesced:  s.coalesced.Load(),
		Duplicates: s.duplicates.Load(),
		Invalid:    s.invalid.Load(),
	}
}

func (s *Subscription) setState(state ConnectionState) {
	s.mu.Lock()
	changed := s.state != state
	s.state = state
	s.mu.Unlock()

	if changed && s.opts.OnStateChange != nil {
		s.opts.OnStateChange(state)
	}
}

func (s *Subscription) opened() {
	s.openedAt = time.Now()
	s.connects.Add(1)
	s.setState(StateOpen)
}

func (s *Subscription) warn(err error) {
	s.invalid.Add(1)
	s.report(err)
}

func (s *Subscription) report(err error) {
	if s.opts.OnError != nil {
		s.opts.OnError(err)
	}
}

func (s *Subscription) accept(u PriceUpdate) {
	if _, ok := s.wanted[u.ID]; !ok {
		return
	}

	s.mu.Lock()

	prev, seen := s.last[u.ID]
	if seen && (u.Price.PublishTime < prev.publishTime || (u.Price.PublishTime == prev.publishTime &&
		u.Price.Price == prev.price && u.Price.Conf == prev.conf && u.EMAPrice.Price == prev.emaPrice)) {
		s.mu.Unlock()
		s.duplicates.Add(1)

		return
	}

	s.last[u.ID] = lastSeen{price: u.Price.Price, conf: u.Price.Conf, emaPrice: u.EMAPrice.Price, publishTime: u.Price.PublishTime}

	if _, waiting := s.pending[u.ID]; waiting {
		s.coalesced.Add(1)
	} else {
		s.queue[(s.head+s.queued)%len(s.queue)] = u.ID
		s.queued++
	}

	s.pending[u.ID] = u
	s.mu.Unlock()

	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *Subscription) pop() (PriceUpdate, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.queued == 0 {
		return PriceUpdate{}, false, s.finished
	}

	id := s.queue[s.head]
	s.queue[s.head] = ""
	s.head = (s.head + 1) % len(s.queue)
	s.queued--

	u := s.pending[id]
	delete(s.pending, id)

	return u, true, false
}

func (s *Subscription) deliver(ctx context.Context) {
	for {
		u, ok, finished := s.pop()
		if !ok {
			if finished {
				return
			}

			select {
			case <-s.notify:
			case <-ctx.Done():
				return
			}

			continue
		}

		select {
		case s.out <- u:
		case <-ctx.Done():
			return
		}
	}
}

func fatal(err error) bool {
	var sub *SubscriptionError
	if errors.As(err, &sub) {
		return true
	}

	var httpErr *HTTPError

	return errors.As(err, &httpErr) && !httpErr.Retryable()
}

func (s *Subscription) run(ctx context.Context) {
	defer func() {
		s.mu.Lock()
		s.finished = true
		s.mu.Unlock()

		select {
		case s.notify <- struct{}{}:
		default:
		}
	}()

	attempt := 0

	for ctx.Err() == nil {
		s.openedAt = time.Time{}

		err := s.transport.run(ctx, s)
		if ctx.Err() != nil {
			return
		}

		var retryAfter time.Duration

		if err != nil {
			if fatal(err) {
				s.mu.Lock()
				s.err = err
				s.mu.Unlock()
				s.report(err)

				return
			}

			s.report(err)

			var httpErr *HTTPError
			if errors.As(err, &httpErr) {
				retryAfter = httpErr.RetryAfter
			}
		}

		if !s.openedAt.IsZero() && time.Since(s.openedAt) >= s.opts.StableAfter {
			attempt = 0
		}

		delay := max(retryAfter, fullJitter(attempt, s.opts.ReconnectBaseDelay, s.opts.ReconnectMaxDelay))
		attempt++

		s.reconnects.Add(1)
		s.setState(StateReconnecting)

		if sleepContext(ctx, delay) != nil {
			return
		}
	}
}

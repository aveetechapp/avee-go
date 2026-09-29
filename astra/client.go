package astra

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultBaseURL     = "https://astra.preview.avee.tech"
	MaxIntervalSeconds = 60
	FeedIDsPerRequest  = MaxIDsPerURL

	defaultTimeout          = 10 * time.Second
	defaultMaxRetries       = 2
	defaultMaxRetryDelay    = 30 * time.Second
	defaultMaxResponseBytes = 8 << 20
)

type Options struct {
	HTTPClient       *http.Client
	Header           http.Header
	BaseURL          string
	Timeout          time.Duration
	MaxRetryDelay    time.Duration
	MaxRetries       int
	MaxResponseBytes int64
	DisableRetries   bool
}

type Client struct {
	base             *url.URL
	http             *http.Client
	header           http.Header
	timeout          time.Duration
	maxRetryDelay    time.Duration
	maxRetries       int
	maxResponseBytes int64
}

type PriceFeedsQuery struct {
	Query     string
	AssetType string
}

type PriceQuery struct {
	IgnoreInvalid bool
}

type IntervalQuery struct {
	IgnoreInvalid           bool
	KeepAllUpdatesPerSecond bool
}

type StatusOptions struct {
	Feed string
}

type FeedIDsOptions struct {
	PythIDs  []string
	AstraIDs []string
	Category string
}

type CandlesQuery struct {
	Feed       string
	Resolution string
	From       int64
	To         int64
}

func New(opts Options) (*Client, error) {
	raw := opts.BaseURL
	if raw == "" {
		raw = DefaultBaseURL
	}

	base, err := url.Parse(raw)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, invalidf("base URL %q must be an absolute http(s) URL", clip(raw))
	}

	if opts.Timeout < 0 || opts.MaxRetryDelay < 0 || opts.MaxRetries < 0 || opts.MaxResponseBytes < 0 {
		return nil, invalidf("options must not be negative")
	}

	c := &Client{
		base:             base,
		http:             opts.HTTPClient,
		header:           opts.Header.Clone(),
		timeout:          orDefault(opts.Timeout, defaultTimeout),
		maxRetryDelay:    orDefault(opts.MaxRetryDelay, defaultMaxRetryDelay),
		maxRetries:       orDefault(opts.MaxRetries, defaultMaxRetries),
		maxResponseBytes: orDefault(opts.MaxResponseBytes, defaultMaxResponseBytes),
	}

	if opts.DisableRetries {
		c.maxRetries = 0
	}

	if c.http == nil {
		c.http = &http.Client{}
	}

	return c, nil
}

func orDefault[T comparable](v, fallback T) T {
	var zero T
	if v == zero {
		return fallback
	}

	return v
}

func idValues(ids []string) url.Values {
	q := make(url.Values, 4)
	q["ids[]"] = ids

	return q
}

func setFlag(q url.Values, name string, on bool) {
	if on {
		q.Set(name, "true")
	}
}

func checkUnix(name string, v int64) error {
	if v < 0 || v > maxUnixSeconds {
		return invalidf("%s must be unix seconds, got %d", name, v)
	}

	return nil
}

func validResolution(r string) bool {
	if r == "" || len(r) > 8 {
		return false
	}

	for i := range len(r) {
		c := r[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return false
		}
	}

	return true
}

func (c *Client) PriceFeeds(ctx context.Context, q PriceFeedsQuery) ([]FeedMetadata, error) {
	values := url.Values{}
	if q.Query != "" {
		values.Set("query", q.Query)
	}

	if q.AssetType != "" {
		values.Set("asset_type", q.AssetType)
	}

	var wire []wireFeedMetadata
	if err := c.getJSON(ctx, "v2/price_feeds", values, &wire); err != nil {
		return nil, err
	}

	out := make([]FeedMetadata, 0, len(wire))

	for i := range wire {
		m, err := wire[i].decode()
		if err != nil {
			return nil, invalidf("price_feeds[%d]: %s", i, reason(err))
		}

		out = append(out, m)
	}

	return out, nil
}

func (c *Client) PriceFeed(ctx context.Context, id string) (FeedMetadata, error) {
	n, err := NormalizeFeedID(id)
	if err != nil {
		return FeedMetadata{}, err
	}

	var wire wireFeedMetadata
	if err = c.getJSON(ctx, "v2/price_feeds/"+n, nil, &wire); err != nil {
		return FeedMetadata{}, err
	}

	return wire.decode()
}

func (c *Client) LatestPrices(ctx context.Context, ids []string, q PriceQuery) ([]PriceUpdate, error) {
	norm, err := normalizeFeedIDs(ids, MaxIDsPerRequest)
	if err != nil {
		return nil, err
	}

	out := make([]PriceUpdate, 0, len(norm))

	for chunk := range slices.Chunk(norm, MaxIDsPerURL) {
		values := idValues(chunk)
		setFlag(values, "ignore_invalid_price_ids", q.IgnoreInvalid)

		var wire wireEnvelope
		if err = c.getJSON(ctx, "v2/updates/price/latest", values, &wire); err != nil {
			return nil, err
		}

		if out, err = wire.decode(out); err != nil {
			return nil, err
		}
	}

	return out, nil
}

func (c *Client) PricesAt(ctx context.Context, publishTime int64, ids []string, q PriceQuery) ([]PriceUpdate, error) {
	if err := checkUnix("publishTime", publishTime); err != nil {
		return nil, err
	}

	norm, err := normalizeFeedIDs(ids, MaxHistoricalFeeds)
	if err != nil {
		return nil, err
	}

	values := idValues(norm)
	setFlag(values, "ignore_invalid_price_ids", q.IgnoreInvalid)

	return c.updates(ctx, "v2/updates/price/"+strconv.FormatInt(publishTime, 10), values)
}

func (c *Client) PricesInInterval(ctx context.Context, publishTime int64, intervalSeconds int, ids []string, q IntervalQuery) ([]PriceUpdate, error) {
	if err := checkUnix("publishTime", publishTime); err != nil {
		return nil, err
	}

	if intervalSeconds < 0 || intervalSeconds > MaxIntervalSeconds {
		return nil, invalidf("intervalSeconds must be in [0, %d], got %d", MaxIntervalSeconds, intervalSeconds)
	}

	norm, err := normalizeFeedIDs(ids, MaxHistoricalFeeds)
	if err != nil {
		return nil, err
	}

	values := idValues(norm)
	setFlag(values, "ignore_invalid_price_ids", q.IgnoreInvalid)

	if q.KeepAllUpdatesPerSecond {
		values.Set("unique", "false")
	}

	var wire []wireEnvelope

	path := "v2/updates/price/" + strconv.FormatInt(publishTime, 10) + "/" + strconv.Itoa(intervalSeconds)
	if err = c.getJSON(ctx, path, values, &wire); err != nil {
		return nil, err
	}

	var out []PriceUpdate

	for i := range wire {
		if out, err = wire[i].decode(out); err != nil {
			return nil, invalidf("updates[%d]: %s", i, reason(err))
		}
	}

	return out, nil
}

func (c *Client) updates(ctx context.Context, path string, values url.Values) ([]PriceUpdate, error) {
	var wire wireEnvelope
	if err := c.getJSON(ctx, path, values, &wire); err != nil {
		return nil, err
	}

	return wire.decode(make([]PriceUpdate, 0, len(wire.Parsed)))
}

func (c *Client) Feeds(ctx context.Context, category string) ([]Feed, error) {
	values := url.Values{}
	if category != "" {
		values.Set("category", category)
	}

	var wire []wireFeed
	if err := c.getJSON(ctx, "v1/feeds", values, &wire); err != nil {
		return nil, err
	}

	out := make([]Feed, 0, len(wire))

	for i := range wire {
		f, err := wire[i].decode()
		if err != nil {
			return nil, invalidf("feeds[%d]: %s", i, reason(err))
		}

		out = append(out, f)
	}

	return out, nil
}

func (c *Client) FeedIDs(ctx context.Context, opts FeedIDsOptions) (FeedIDMap, error) {
	values := url.Values{}
	if opts.Category != "" {
		values.Set("category", opts.Category)
	}

	if opts.PythIDs == nil && opts.AstraIDs == nil {
		return c.feedIDs(ctx, values)
	}

	pyth, err := uniqueFeedIDs(opts.PythIDs)
	if err != nil {
		return FeedIDMap{}, err
	}

	astra, err := uniqueFeedIDs(opts.AstraIDs)
	if err != nil {
		return FeedIDMap{}, err
	}

	return c.feedIDChunks(ctx, values, pyth, astra)
}

func (c *Client) feedIDChunks(ctx context.Context, values url.Values, pyth, astra []string) (FeedIDMap, error) {
	out := FeedIDMap{Items: []FeedIDEntry{}, Missing: []string{}}
	seenItem := make(map[string]struct{})
	seenMissing := make(map[string]struct{})

	for len(pyth)+len(astra) > 0 {
		np := min(len(pyth), MaxIDsPerURL)
		na := min(len(astra), MaxIDsPerURL-np)

		setJoined(values, "pyth_ids", pyth[:np])
		setJoined(values, "astra_ids", astra[:na])
		pyth, astra = pyth[np:], astra[na:]

		page, err := c.feedIDs(ctx, values)
		if err != nil {
			return FeedIDMap{}, err
		}

		for _, e := range page.Items {
			if _, dup := seenItem[e.AstraID]; !dup {
				seenItem[e.AstraID] = struct{}{}
				out.Items = append(out.Items, e)
			}
		}

		for _, m := range page.Missing {
			if _, dup := seenMissing[m]; !dup {
				seenMissing[m] = struct{}{}
				out.Missing = append(out.Missing, m)
			}
		}
	}

	slices.SortStableFunc(out.Items, func(a, b FeedIDEntry) int { return strings.Compare(a.Symbol, b.Symbol) })

	return out, nil
}

func setJoined(values url.Values, name string, ids []string) {
	if len(ids) == 0 {
		values.Del(name)

		return
	}

	values.Set(name, strings.Join(ids, ","))
}

func (c *Client) feedIDs(ctx context.Context, values url.Values) (FeedIDMap, error) {
	var wire wireFeedIDList
	if err := c.getJSON(ctx, "v1/feed-ids", values, &wire); err != nil {
		return FeedIDMap{}, err
	}

	return wire.decode()
}

func (c *Client) Status(ctx context.Context, opts ...StatusOptions) (StatusReport, error) {
	var (
		wire   wireStatusReport
		values url.Values
		accept []int
	)

	if len(opts) > 0 && opts[0].Feed != "" {
		id, err := NormalizeFeedID(opts[0].Feed)
		if err != nil {
			return StatusReport{}, err
		}

		values, accept = url.Values{"feed": {id}}, []int{http.StatusServiceUnavailable}
	}

	if err := c.getJSON(ctx, "v1/status", values, &wire, accept...); err != nil {
		return StatusReport{}, err
	}

	return wire.decode()
}

func (c *Client) Candles(ctx context.Context, q CandlesQuery) ([]Candle, error) {
	if q.Feed == "" || len(q.Feed) > 200 {
		return nil, invalidf("feed must be a feed id or symbol")
	}

	if !validResolution(q.Resolution) {
		return nil, invalidf("invalid resolution %q", clip(q.Resolution))
	}

	if err := checkUnix("from", q.From); err != nil {
		return nil, err
	}

	if err := checkUnix("to", q.To); err != nil {
		return nil, err
	}

	if q.To < q.From {
		return nil, invalidf("to must not be before from")
	}

	values := url.Values{
		"feed":       {q.Feed},
		"resolution": {q.Resolution},
		"from":       {strconv.FormatInt(q.From, 10)},
		"to":         {strconv.FormatInt(q.To, 10)},
	}

	var wire wireBars
	if err := c.getJSON(ctx, "v1/candles", values, &wire); err != nil {
		return nil, err
	}

	return wire.decode()
}

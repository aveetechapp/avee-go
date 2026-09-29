package avee

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	DefaultBaseURL = "https://api.preview.avee.tech/api/v1"
	PreviewBaseURL = "https://api.preview.avee.tech/api/v1"
	Version        = "0.1.0"

	defaultTimeout          = 30 * time.Second
	defaultMaxRetries       = 2
	defaultMaxRetryDelay    = 30 * time.Second
	defaultMaxResponseBytes = 16 << 20
)

type Options struct {
	HTTPClient       *http.Client
	Header           http.Header
	Payer            Payer
	BaseURL          string
	APIKey           string
	UserAgent        string
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
	payer            Payer
	apiKey           string
	userAgent        string
	last             ResponseInfo
	timeout          time.Duration
	maxRetryDelay    time.Duration
	maxRetries       int
	maxResponseBytes int64
	mu               sync.Mutex
}

type ResponseInfo struct {
	Payment   *PaymentReceipt
	Operation string
	RequestID string
	RateLimit RateLimit
	Status    int
}

type RateLimit struct {
	Policy     string
	Limit      int
	Remaining  int
	Reset      time.Duration
	RetryAfter time.Duration
	Known      bool
}

func New(opts Options) (*Client, error) {
	raw := opts.BaseURL
	if raw == "" {
		raw = DefaultBaseURL
	}

	base, err := url.Parse(raw)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.RawQuery != "" {
		return nil, invalidf("base URL %q must be an absolute http(s) URL without a query", clip(raw))
	}

	if opts.Timeout < 0 || opts.MaxRetryDelay < 0 || opts.MaxRetries < 0 || opts.MaxResponseBytes < 0 {
		return nil, invalidf("options must not be negative")
	}

	if strings.ContainsAny(opts.APIKey, "\r\n\x00") || strings.ContainsAny(opts.UserAgent, "\r\n\x00") {
		return nil, invalidf("the API key and user agent must be a single line")
	}

	for name, values := range opts.Header {
		if strings.EqualFold(name, "Payment-Signature") {
			return nil, invalidf("the PAYMENT-SIGNATURE header is sent only by the payer")
		}

		for _, v := range values {
			if strings.ContainsAny(v, "\r\n\x00") {
				return nil, invalidf("header %s must be a single line", clip(name))
			}
		}
	}

	c := &Client{
		base:             base,
		http:             withoutRedirects(opts.HTTPClient),
		header:           opts.Header.Clone(),
		payer:            opts.Payer,
		apiKey:           opts.APIKey,
		userAgent:        opts.UserAgent,
		timeout:          opts.Timeout,
		maxRetryDelay:    opts.MaxRetryDelay,
		maxRetries:       opts.MaxRetries,
		maxResponseBytes: opts.MaxResponseBytes,
	}

	if c.timeout == 0 {
		c.timeout = defaultTimeout
	}

	if c.maxRetryDelay == 0 {
		c.maxRetryDelay = defaultMaxRetryDelay
	}

	if c.maxRetries == 0 {
		c.maxRetries = defaultMaxRetries
	}

	if opts.DisableRetries {
		c.maxRetries = 0
	}

	if c.maxResponseBytes == 0 {
		c.maxResponseBytes = defaultMaxResponseBytes
	}

	if c.userAgent == "" {
		c.userAgent = "avee-go/" + Version
	}

	return c, nil
}

func (c *Client) LastResponse() ResponseInfo {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.last
}

func (c *Client) record(info ResponseInfo) {
	c.mu.Lock()
	c.last = info
	c.mu.Unlock()
}

func withoutRedirects(hc *http.Client) *http.Client {
	if hc == nil {
		hc = http.DefaultClient
	}

	cp := *hc
	cp.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	return &cp
}

func Ptr[T any](v T) *T {
	return &v
}

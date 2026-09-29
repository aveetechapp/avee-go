package astra

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func statusJSON(stale bool) map[string]any {
	return map[string]any{"ready": true, "feeds": []any{map[string]any{
		"id": btcAstra, "symbol": "Crypto.BTC/USD", "status": "trading", "age_seconds": 1.5,
		"publish_time": 1, "served_publish_time": 1, "sources": 3, "stale": stale,
	}}}
}

func TestStatusOfOneFeedSendsTheNormalisedID(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, statusJSON(false)) })
	c := newTestClient(t, f.URL, Options{})

	got, err := c.Status(t.Context(), StatusOptions{Feed: "0x" + strings.ToUpper(btc)})
	if err != nil || len(got.Feeds) != 1 || got.Feeds[0].Stale {
		t.Fatalf("got %+v, %v", got, err)
	}

	if q := f.recorded()[0].URL.Query().Get("feed"); q != btc {
		t.Fatalf("feed %q", q)
	}
}

func TestStatusOfOneFeedReturnsTheReportOn503(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 503, statusJSON(true)) })
	c := newTestClient(t, f.URL, Options{MaxRetries: 2, MaxRetryDelay: 20 * time.Millisecond})

	got, err := c.Status(t.Context(), StatusOptions{Feed: btc})
	if err != nil || !got.Feeds[0].Stale {
		t.Fatalf("got %+v, %v", got, err)
	}

	if n := len(f.recorded()); n != 1 {
		t.Fatalf("%d requests", n)
	}
}

func TestStatusOfOneFeedStillFailsOnOtherAnswers(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("feed") == btc {
			writeText(w, 503, "upstream down")

			return
		}

		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"type":"about:blank","title":"Not Found","status":404,"detail":"unknown feed"}`))
	})
	c := newTestClient(t, f.URL, Options{MaxRetries: 1, MaxRetryDelay: 20 * time.Millisecond})

	var he *HTTPError
	if _, err := c.Status(t.Context(), StatusOptions{Feed: btc}); !errors.As(err, &he) || he.Status != 503 || len(f.recorded()) != 2 {
		t.Fatalf("proxy 503: %v, %d requests", err, len(f.recorded()))
	}

	if _, err := c.Status(t.Context(), StatusOptions{Feed: eth}); !errors.As(err, &he) || he.Status != 404 {
		t.Fatalf("unknown feed: %v", err)
	}

	var ve *ValidationError
	if _, err := c.Status(t.Context(), StatusOptions{Feed: "nope"}); !errors.As(err, &ve) {
		t.Fatalf("malformed feed: %v", err)
	}
}

func TestStatusWithoutFeedKeeps503AnError(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 503, statusJSON(true)) })
	c := newTestClient(t, f.URL, Options{DisableRetries: true})

	var he *HTTPError
	if _, err := c.Status(t.Context()); !errors.As(err, &he) {
		t.Fatalf("err = %v", err)
	}
}

func TestStatusOfOneFeedTreatsAProblem503AsAnError(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"type":"about:blank","title":"Service Unavailable","status":503,"detail":"draining"}`))
	})
	c := newTestClient(t, f.URL, Options{MaxRetries: 1, MaxRetryDelay: 20 * time.Millisecond})

	var he *HTTPError
	if _, err := c.Status(t.Context(), StatusOptions{Feed: btc}); !errors.As(err, &he) || he.Status != 503 || he.Problem == nil {
		t.Fatalf("err = %v", err)
	}

	if n := len(f.recorded()); n != 2 {
		t.Fatalf("%d requests", n)
	}
}

func TestStatusOfOneFeedSurfacesTheServer400WithoutRetrying(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"type":"about:blank","title":"Bad Request","status":400,"detail":"malformed feed"}`))
	})
	c := newTestClient(t, f.URL, Options{MaxRetries: 2, MaxRetryDelay: 20 * time.Millisecond})

	var he *HTTPError
	if _, err := c.Status(t.Context(), StatusOptions{Feed: btc}); !errors.As(err, &he) || he.Status != 400 || len(f.recorded()) != 1 {
		t.Fatalf("err = %v, %d requests", err, len(f.recorded()))
	}
}

func TestStatusWithAnEmptyFeedReadsTheWholeReport(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, statusJSON(false)) })
	c := newTestClient(t, f.URL, Options{})

	if _, err := c.Status(t.Context(), StatusOptions{}); err != nil {
		t.Fatal(err)
	}

	if q := f.recorded()[0].URL.RawQuery; q != "" {
		t.Fatalf("query %q", q)
	}
}

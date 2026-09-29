package compat_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

const (
	btc      = "e62df6c8b4a85fe1a67db44dc12de5db330f7ac66b72dc658afedf0f4a415b43"
	eth      = "ff61491a931112ddf1bd8147cd1b641375f79f5825126d665480874634fd0ace"
	btcAstra = "1de769477ecf66f69ca287676658956a211c207a9ca608ad5235bd095b02f4b2"
	unknown  = "1111111111111111111111111111111111111111111111111111111111111111"
)

func priceJSON(value string, publishTime int64) map[string]any {
	return map[string]any{"price": value, "conf": "1000", "expo": -8, "publish_time": publishTime}
}

func parsedJSON(id string, publishTime int64) map[string]any {
	return map[string]any{
		"id":        id,
		"price":     priceJSON("6500000000000", publishTime),
		"ema_price": priceJSON("6490000000000", publishTime),
		"metadata":  map[string]any{"slot": 0, "proof_available_time": publishTime, "prev_publish_time": publishTime - 1, "future": true},
	}
}

func envelopeJSON(ids []string, publishTime int64) map[string]any {
	parsed := make([]any, 0, len(ids))
	for _, id := range ids {
		parsed = append(parsed, parsedJSON(strings.TrimPrefix(id, "0x"), publishTime))
	}

	return map[string]any{"binary": map[string]any{"encoding": "hex", "data": []string{}}, "parsed": parsed, "future": 1}
}

func feedMetadataJSON() map[string]any {
	return map[string]any{
		"id": btc,
		"attributes": map[string]any{
			"asset_type": "Crypto", "base": "BTC", "description": "BITCOIN / US DOLLAR", "display_symbol": "BTC/USD",
			"quote_currency": "USD", "symbol": "Crypto.BTC/USD", "min_channel": "real_time", "astra_id": btcAstra, "future": "x",
		},
		"market_hours": map[string]any{"is_open": true},
	}
}

func statusJSON(stale bool) map[string]any {
	return map[string]any{"ready": true, "feeds": []any{map[string]any{
		"id": btcAstra, "symbol": "Crypto.BTC/USD", "status": "trading", "age_seconds": 1.5,
		"publish_time": 1, "served_publish_time": 1, "sources": 3, "stale": stale,
	}}}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func serveREST(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	switch {
	case r.URL.Path == "/v2/price_feeds":
		writeJSON(w, http.StatusOK, []any{feedMetadataJSON()})
	case r.URL.Path == "/v2/price_feeds/"+btc:
		writeJSON(w, http.StatusOK, feedMetadataJSON())
	case strings.HasPrefix(r.URL.Path, "/v2/price_feeds/"):
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, "Price ids not found: "+unknown)
	case r.URL.Path == "/v2/updates/price/stream":
		serveSSE(w, r)
	case strings.Count(r.URL.Path, "/") == 5:
		writeJSON(w, http.StatusOK, []any{envelopeJSON(q["ids[]"], 1700000000)})
	case strings.HasPrefix(r.URL.Path, "/v2/updates/price/"):
		writeJSON(w, http.StatusOK, envelopeJSON(q["ids[]"], 1700000000))
	case r.URL.Path == "/v1/feeds":
		writeJSON(w, http.StatusOK, []any{map[string]any{
			"id": btcAstra, "pyth_id": btc, "symbol": "Crypto.BTC/USD", "category": "crypto", "attributes": map[string]any{},
			"live": map[string]any{"status": "trading", "price": "6500000", "conf": "10", "expo": -2, "publish_time": 1, "timestamp_ms": 1000, "served_publish_time": 1, "sources": 3},
		}})
	case r.URL.Path == "/v1/feed-ids":
		writeJSON(w, http.StatusOK, map[string]any{
			"items":   []any{map[string]any{"symbol": "Crypto.BTC/USD", "asset_type": "Crypto", "category": "crypto", "astra_id": btcAstra, "pyth_id": btc}},
			"missing": []string{unknown},
		})
	case r.URL.Path == "/v1/status" && q.Get("feed") != "":
		writeJSON(w, http.StatusServiceUnavailable, statusJSON(true))
	case r.URL.Path == "/v1/status":
		writeJSON(w, http.StatusOK, statusJSON(false))
	case r.URL.Path == "/v1/candles":
		writeJSON(w, http.StatusOK, map[string]any{"s": "ok", "t": []int{0}, "o": []float64{1}, "h": []float64{2}, "l": []float64{0.5}, "c": []float64{1.5}, "v": []int{0}})
	default:
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"type":"about:blank","title":"Not Found","status":404,"detail":"no route"}`)
	}
}

func serveSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	b, _ := json.Marshal(envelopeJSON(r.URL.Query()["ids[]"], 1700000000))
	_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
	w.(http.Flusher).Flush()
	<-r.Context().Done()
}

func serveWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()

	ctx := r.Context()

	_, data, err := conn.Read(ctx)
	if err != nil {
		return
	}

	var sub map[string]any
	if json.Unmarshal(data, &sub) != nil {
		return
	}

	_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response","status":"success"}`))

	ids, _ := sub["ids"].([]any)
	for _, id := range ids {
		s, _ := id.(string)
		msg, _ := json.Marshal(map[string]any{"type": "price_update", "price_feed": map[string]any{
			"id": s, "price": priceJSON("6500000000000", 1700000000), "ema_price": priceJSON("6490000000000", 1700000000),
			"metadata": map[string]any{"emitter_chain": 0, "price_service_receive_time": 1700000000, "prev_publish_time": 1699999999},
		}})
		_ = conn.Write(ctx, websocket.MessageText, msg)
	}

	_, _, _ = conn.Read(ctx)
}

func startFake(t *testing.T) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws" {
			serveWS(w, r)

			return
		}

		serveREST(w, r)
	}))
	t.Cleanup(srv.Close)

	return srv.URL
}

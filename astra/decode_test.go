package astra

import (
	"encoding/json"
	"errors"
	"testing"
)

func decodeEnvelope(t *testing.T, body any) ([]PriceUpdate, error) {
	t.Helper()

	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	var w wireEnvelope
	if err = json.Unmarshal(raw, &w); err != nil {
		return nil, &ValidationError{Reason: err.Error()}
	}

	return w.decode(nil)
}

func TestDecodeEnvelope(t *testing.T) {
	got, err := decodeEnvelope(t, envelopeJSON(parsedJSON("0x"+btc, "6512345000000", 1790540000)))
	if err != nil {
		t.Fatal(err)
	}

	u := got[0]
	if u.ID != btc || u.Price.Decimal() != "65123.45" || u.Metadata == nil || u.Metadata.PrevPublishTime != 1790539999 {
		t.Fatalf("got %+v", u)
	}
}

func TestDecodeEnvelopeRejectsMalformedPrices(t *testing.T) {
	cases := map[string]map[string]any{
		"non-integer price":   {"price": "12.5"},
		"numeric price":       {"price": 125},
		"empty price":         {"price": ""},
		"negative conf":       {"conf": "-1"},
		"absurd expo":         {"expo": -400},
		"fractional expo":     {"expo": -8.5},
		"millisecond time":    {"publish_time": int64(1790540000000)},
		"negative time":       {"publish_time": -1},
		"missing publish":     {"publish_time": nil},
		"string publish time": {"publish_time": "1"},
	}

	for name, patch := range cases {
		t.Run(name, func(t *testing.T) {
			p := priceJSON("1", 1)
			for k, v := range patch {
				if v == nil {
					delete(p, k)
				} else {
					p[k] = v
				}
			}

			item := parsedJSON(btc, "1", 1)
			item["price"] = p

			var ve *ValidationError
			if _, err := decodeEnvelope(t, envelopeJSON(item)); !errors.As(err, &ve) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestDecodeEnvelopeRejectsBadIDAndMissingEMA(t *testing.T) {
	bad := parsedJSON("xyz", "1", 1)
	if _, err := decodeEnvelope(t, envelopeJSON(bad)); err == nil {
		t.Error("bad id accepted")
	}

	noEMA := parsedJSON(btc, "1", 1)
	delete(noEMA, "ema_price")

	if _, err := decodeEnvelope(t, envelopeJSON(noEMA)); err == nil {
		t.Error("missing ema_price accepted")
	}
}

func TestDecodeToleratesUnknownFieldsAndIncompleteMetadata(t *testing.T) {
	item := parsedJSON(btc, "1", 1)
	item["future"] = map[string]any{"nested": []int{1}}
	item["metadata"] = map[string]any{"slot": 0}

	body := envelopeJSON(item)
	body["version"] = 2

	got, err := decodeEnvelope(t, body)
	if err != nil || len(got) != 1 || got[0].Metadata != nil {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestDecodeFeedMetadata(t *testing.T) {
	raw := `{"id":"` + btc + `","attributes":{"asset_type":"Crypto","description":"d","display_symbol":"BTC/USD",
	"quote_currency":"USD","symbol":"Crypto.BTC/USD","min_channel":"real_time","astra_id":"` + btcAstra + `","weight":3,"future":"x"},
	"market_hours":{"is_open":true,"next_open":null,"next_close":null}}`

	var w wireFeedMetadata
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		t.Fatal(err)
	}

	m, err := w.decode()
	if err != nil {
		t.Fatal(err)
	}

	if m.AstraID != btcAstra || m.Base != "" || !m.MarketOpen || m.Attributes["future"] != "x" {
		t.Fatalf("got %+v", m)
	}

	if _, ok := m.Attributes["weight"]; ok {
		t.Error("non-string attribute kept")
	}

	delete(w.Attributes, "symbol")

	if _, err = w.decode(); err == nil {
		t.Error("missing symbol accepted")
	}
}

func TestDecodeFeedWithAndWithoutLivePrice(t *testing.T) {
	raw := `{"id":"` + btcAstra + `","pyth_id":"` + btc + `","symbol":"Crypto.BTC/USD","category":"crypto","attributes":{},
	"live":{"status":"halted_by_regulator","price":"149606601500","conf":"28398500","expo":-9,"publish_time":10,
	"timestamp_ms":10500,"served_publish_time":10,"sources":6}}`

	var w wireFeed
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		t.Fatal(err)
	}

	f, err := w.decode()
	if err != nil {
		t.Fatal(err)
	}

	if f.Live.Price.Decimal() != "149.6066015" || f.PythID != btc || f.Live.Status != "halted_by_regulator" {
		t.Fatalf("got %+v", f)
	}

	w.Live.Conf = nil

	if _, err = w.decode(); err == nil {
		t.Error("price without conf accepted")
	}

	w.Live.Price = nil

	if f, err = w.decode(); err != nil || f.Live.Price != nil {
		t.Fatalf("no_data feed: %+v, %v", f, err)
	}
}

func TestDecodeStatusReport(t *testing.T) {
	var w wireStatusReport

	raw := `{"ready":true,"feeds":[{"id":"` + btcAstra + `","symbol":"X","status":"trading","age_seconds":0.3,"publish_time":1,"served_publish_time":1,"sources":5,"stale":false}]}`
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		t.Fatal(err)
	}

	r, err := w.decode()
	if err != nil || !r.Ready || r.Feeds[0].AgeSeconds != 0.3 {
		t.Fatalf("got %+v, %v", r, err)
	}

	w.Feeds[0].Stale = nil

	if _, err = w.decode(); err == nil {
		t.Error("missing stale accepted")
	}
}

func decodeBars(t *testing.T, body string) ([]Candle, error) {
	t.Helper()

	var w wireBars
	if err := json.Unmarshal([]byte(body), &w); err != nil {
		t.Fatalf("%s: %v", body, err)
	}

	return w.decode()
}

func TestDecodeCandles(t *testing.T) {
	got, err := decodeBars(t, `{"s":"ok","t":[60,120],"o":[1,2],"h":[3,4],"l":[0.5,1],"c":[2,3],"v":[9,9]}`)
	if err != nil || len(got) != 2 || got[1] != (Candle{Time: 120, Open: 2, High: 4, Low: 1, Close: 3}) {
		t.Fatalf("got %+v, %v", got, err)
	}

	if got, err = decodeBars(t, `{"s":"no_data"}`); err != nil || len(got) != 0 {
		t.Fatalf("no_data: %v, %v", got, err)
	}

	for _, bad := range []string{
		`{"s":"ok","t":[60,120],"o":[1],"h":[3,4],"l":[0.5,1],"c":[2,3]}`,
		`{"s":"error","errmsg":"x"}`,
		`{"s":"ok","t":[60],"o":[null],"h":[3],"l":[0.5],"c":[2]}`,
		`{"s":"ok","t":[60],"o":[1],"h":[3],"l":[0.5],"c":[null]}`,
		`{"s":"ok","t":[null],"o":[1],"h":[3],"l":[0.5],"c":[2]}`,
		`{"s":"ok","t":[-1],"o":[1],"h":[3],"l":[0.5],"c":[2]}`,
	} {
		if got, err := decodeBars(t, bad); err == nil {
			t.Errorf("%s accepted as %+v", bad, got)
		}
	}
}

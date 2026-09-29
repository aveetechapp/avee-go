package astra

import (
	"math"
	"regexp"
)

const (
	maxUnixSeconds = 100_000_000_000
	maxUnixMillis  = 100_000_000_000_000
	maxCount       = 1_000_000
)

var (
	signedInt   = regexp.MustCompile(`^-?[0-9]{1,80}$`)
	unsignedInt = regexp.MustCompile(`^[0-9]{1,80}$`)
)

type wirePrice struct {
	Expo        *int32 `json:"expo"`
	PublishTime *int64 `json:"publish_time"`
	Price       string `json:"price"`
	Conf        string `json:"conf"`
}

type proofMetadata struct {
	ProofAvailableTime *int64 `json:"proof_available_time"`
	PrevPublishTime    *int64 `json:"prev_publish_time"`
}

type streamMetadata struct {
	PriceServiceReceiveTime *int64 `json:"price_service_receive_time"`
	PrevPublishTime         *int64 `json:"prev_publish_time"`
}

type metadataWire interface {
	proofMetadata | streamMetadata
	times() (*int64, *int64)
}

func (m proofMetadata) times() (*int64, *int64) { return m.ProofAvailableTime, m.PrevPublishTime }

func (m streamMetadata) times() (*int64, *int64) { return m.PriceServiceReceiveTime, m.PrevPublishTime }

type wireUpdate[M metadataWire] struct {
	Metadata M         `json:"metadata"`
	ID       string    `json:"id"`
	Price    wirePrice `json:"price"`
	EMAPrice wirePrice `json:"ema_price"`
}

type wireEnvelope struct {
	Parsed []wireUpdate[proofMetadata] `json:"parsed"`
}

type wireStreamMessage struct {
	PriceFeed *wireUpdate[streamMetadata] `json:"price_feed"`
	Type      string                      `json:"type"`
	Status    string                      `json:"status"`
	Error     string                      `json:"error"`
}

type wireMarketHours struct {
	IsOpen *bool `json:"is_open"`
}

type wireFeedMetadata struct {
	Attributes  map[string]any  `json:"attributes"`
	MarketHours wireMarketHours `json:"market_hours"`
	ID          string          `json:"id"`
}

type wireLive struct {
	Price             *string `json:"price"`
	Conf              *string `json:"conf"`
	Expo              *int32  `json:"expo"`
	PublishTime       *int64  `json:"publish_time"`
	TimestampMs       *int64  `json:"timestamp_ms"`
	ServedPublishTime *int64  `json:"served_publish_time"`
	Sources           *int    `json:"sources"`
	Status            string  `json:"status"`
}

type wireFeed struct {
	Attributes map[string]any `json:"attributes"`
	ID         string         `json:"id"`
	PythID     string         `json:"pyth_id"`
	Symbol     string         `json:"symbol"`
	Category   string         `json:"category"`
	Live       wireLive       `json:"live"`
}

type wireFeedIDEntry struct {
	Symbol    string `json:"symbol"`
	AssetType string `json:"asset_type"`
	Category  string `json:"category"`
	AstraID   string `json:"astra_id"`
	PythID    string `json:"pyth_id"`
}

type wireFeedIDList struct {
	Items   []wireFeedIDEntry `json:"items"`
	Missing []string          `json:"missing"`
}

type wireFeedHealth struct {
	AgeSeconds        *float64 `json:"age_seconds"`
	PublishTime       *int64   `json:"publish_time"`
	ServedPublishTime *int64   `json:"served_publish_time"`
	Sources           *int     `json:"sources"`
	Stale             *bool    `json:"stale"`
	ID                string   `json:"id"`
	Symbol            string   `json:"symbol"`
	Status            string   `json:"status"`
}

type wireStatusReport struct {
	Ready *bool            `json:"ready"`
	Feeds []wireFeedHealth `json:"feeds"`
}

type wireBars struct {
	S string     `json:"s"`
	T []*int64   `json:"t"`
	O []*float64 `json:"o"`
	H []*float64 `json:"h"`
	L []*float64 `json:"l"`
	C []*float64 `json:"c"`
}

func requireInt64(v *int64, field string, lo, hi int64) (int64, error) {
	if v == nil {
		return 0, invalidf("%s is missing", field)
	}

	if *v < lo || *v > hi {
		return 0, invalidf("%s = %d is outside [%d, %d]", field, *v, lo, hi)
	}

	return *v, nil
}

func requireCount(v *int, field string) (int, error) {
	if v == nil {
		return 0, invalidf("%s is missing", field)
	}

	if *v < 0 || *v > maxCount {
		return 0, invalidf("%s = %d is outside [0, %d]", field, *v, maxCount)
	}

	return *v, nil
}

func requireExpo(v *int32, field string) (int32, error) {
	if v == nil {
		return 0, invalidf("%s is missing", field)
	}

	if *v < MinExpo || *v > MaxExpo {
		return 0, invalidf("%s = %d is outside [%d, %d]", field, *v, MinExpo, MaxExpo)
	}

	return *v, nil
}

func requireBool(v *bool, field string) (bool, error) {
	if v == nil {
		return false, invalidf("%s is missing", field)
	}

	return *v, nil
}

func requireString(v, field string) (string, error) {
	if v == "" {
		return "", invalidf("%s is missing", field)
	}

	return v, nil
}

func requireMantissa(v, field string, signed bool) (string, error) {
	re := unsignedInt
	if signed {
		re = signedInt
	}

	if !re.MatchString(v) {
		return "", invalidf("%s = %q is not an integer string", field, clip(v))
	}

	return v, nil
}

func requireFeedID(v, field string) (string, error) {
	id, err := NormalizeFeedID(v)
	if err != nil {
		return "", invalidf("%s = %q is not a feed id", field, clip(v))
	}

	return id, nil
}

func (w *wirePrice) decode(field string) (Price, error) {
	var (
		p   Price
		err error
	)

	if p.Price, err = requireMantissa(w.Price, field+".price", true); err != nil {
		return Price{}, err
	}

	if p.Conf, err = requireMantissa(w.Conf, field+".conf", false); err != nil {
		return Price{}, err
	}

	if p.Expo, err = requireExpo(w.Expo, field+".expo"); err != nil {
		return Price{}, err
	}

	if p.PublishTime, err = requireInt64(w.PublishTime, field+".publish_time", 0, maxUnixSeconds); err != nil {
		return Price{}, err
	}

	return p, nil
}

func (w *wireUpdate[M]) decode() (PriceUpdate, error) {
	var (
		u   PriceUpdate
		err error
	)

	if u.ID, err = requireFeedID(w.ID, "id"); err != nil {
		return PriceUpdate{}, err
	}

	if u.Price, err = w.Price.decode("price"); err != nil {
		return PriceUpdate{}, err
	}

	if u.EMAPrice, err = w.EMAPrice.decode("ema_price"); err != nil {
		return PriceUpdate{}, err
	}

	receive, prev := w.Metadata.times()
	if receive == nil || prev == nil {
		return u, nil
	}

	r, err := requireInt64(receive, "metadata receive time", 0, maxUnixSeconds)
	if err != nil {
		return PriceUpdate{}, err
	}

	pp, err := requireInt64(prev, "metadata.prev_publish_time", 0, maxUnixSeconds)
	if err != nil {
		return PriceUpdate{}, err
	}

	u.Metadata = &UpdateMetadata{ReceiveTime: r, PrevPublishTime: pp}

	return u, nil
}

func (w *wireEnvelope) decode(dst []PriceUpdate) ([]PriceUpdate, error) {
	for i := range w.Parsed {
		u, err := w.Parsed[i].decode()
		if err != nil {
			return nil, invalidf("parsed[%d]: %s", i, reason(err))
		}

		dst = append(dst, u)
	}

	return dst, nil
}

func stringAttributes(in map[string]any) map[string]string {
	out := make(map[string]string, len(in))

	for k, v := range in {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}

	return out
}

func (w *wireFeedMetadata) decode() (FeedMetadata, error) {
	if w.Attributes == nil {
		return FeedMetadata{}, invalidf("attributes is missing")
	}

	attrs := stringAttributes(w.Attributes)
	m := FeedMetadata{Attributes: attrs, Base: attrs["base"], Schedule: attrs["schedule"]}

	var err error

	if m.ID, err = requireFeedID(w.ID, "id"); err != nil {
		return FeedMetadata{}, err
	}

	if m.AstraID, err = requireFeedID(attrs["astra_id"], "attributes.astra_id"); err != nil {
		return FeedMetadata{}, err
	}

	required := []struct {
		dst *string
		key string
	}{
		{&m.Symbol, "symbol"},
		{&m.AssetType, "asset_type"},
		{&m.DisplaySymbol, "display_symbol"},
		{&m.QuoteCurrency, "quote_currency"},
		{&m.Description, "description"},
		{&m.MinChannel, "min_channel"},
	}

	for _, r := range required {
		v, ok := attrs[r.key]
		if !ok {
			return FeedMetadata{}, invalidf("attributes.%s is missing", r.key)
		}

		*r.dst = v
	}

	if m.MarketOpen, err = requireBool(w.MarketHours.IsOpen, "market_hours.is_open"); err != nil {
		return FeedMetadata{}, err
	}

	return m, nil
}

func (w *wireLive) decode() (LiveValue, error) {
	var (
		v   LiveValue
		err error
	)

	if w.Status == "" {
		return LiveValue{}, invalidf("live.status is missing")
	}

	v.Status = FeedStatus(w.Status)

	if v.Expo, err = requireExpo(w.Expo, "live.expo"); err != nil {
		return LiveValue{}, err
	}

	if v.PublishTime, err = requireInt64(w.PublishTime, "live.publish_time", 0, maxUnixSeconds); err != nil {
		return LiveValue{}, err
	}

	if v.TimestampMs, err = requireInt64(w.TimestampMs, "live.timestamp_ms", 0, maxUnixMillis); err != nil {
		return LiveValue{}, err
	}

	if v.ServedPublishTime, err = requireInt64(w.ServedPublishTime, "live.served_publish_time", 0, maxUnixSeconds); err != nil {
		return LiveValue{}, err
	}

	if v.Sources, err = requireCount(w.Sources, "live.sources"); err != nil {
		return LiveValue{}, err
	}

	if w.Price == nil {
		return v, nil
	}

	if w.Conf == nil {
		return LiveValue{}, invalidf("live.conf is missing")
	}

	p := Price{Expo: v.Expo, PublishTime: v.PublishTime}

	if p.Price, err = requireMantissa(*w.Price, "live.price", true); err != nil {
		return LiveValue{}, err
	}

	if p.Conf, err = requireMantissa(*w.Conf, "live.conf", false); err != nil {
		return LiveValue{}, err
	}

	v.Price = &p

	return v, nil
}

func (w *wireFeed) decode() (Feed, error) {
	var (
		f   Feed
		err error
	)

	if f.ID, err = requireFeedID(w.ID, "id"); err != nil {
		return Feed{}, err
	}

	if w.PythID != "" {
		if f.PythID, err = requireFeedID(w.PythID, "pyth_id"); err != nil {
			return Feed{}, err
		}
	}

	if f.Symbol, err = requireString(w.Symbol, "symbol"); err != nil {
		return Feed{}, err
	}

	if f.Category, err = requireString(w.Category, "category"); err != nil {
		return Feed{}, err
	}

	f.Attributes = stringAttributes(w.Attributes)

	if f.Live, err = w.Live.decode(); err != nil {
		return Feed{}, err
	}

	return f, nil
}

func (w *wireFeedIDEntry) decode() (FeedIDEntry, error) {
	var (
		e   FeedIDEntry
		err error
	)

	if e.Symbol, err = requireString(w.Symbol, "symbol"); err != nil {
		return FeedIDEntry{}, err
	}

	if e.AssetType, err = requireString(w.AssetType, "asset_type"); err != nil {
		return FeedIDEntry{}, err
	}

	if e.Category, err = requireString(w.Category, "category"); err != nil {
		return FeedIDEntry{}, err
	}

	if e.AstraID, err = requireFeedID(w.AstraID, "astra_id"); err != nil {
		return FeedIDEntry{}, err
	}

	if w.PythID != "" {
		if e.PythID, err = requireFeedID(w.PythID, "pyth_id"); err != nil {
			return FeedIDEntry{}, err
		}
	}

	return e, nil
}

func (w *wireFeedIDList) decode() (FeedIDMap, error) {
	if w.Items == nil {
		return FeedIDMap{}, invalidf("items is missing")
	}

	out := FeedIDMap{Items: make([]FeedIDEntry, 0, len(w.Items))}

	for i := range w.Items {
		e, err := w.Items[i].decode()
		if err != nil {
			return FeedIDMap{}, invalidf("items[%d]: %s", i, reason(err))
		}

		out.Items = append(out.Items, e)
	}

	if w.Missing == nil {
		return out, nil
	}

	out.Missing = make([]string, 0, len(w.Missing))

	for i, m := range w.Missing {
		id, err := requireFeedID(m, "missing")
		if err != nil {
			return FeedIDMap{}, invalidf("missing[%d]: %s", i, reason(err))
		}

		out.Missing = append(out.Missing, id)
	}

	return out, nil
}

func (w *wireFeedHealth) decode() (FeedHealth, error) {
	var (
		h   FeedHealth
		err error
	)

	if h.ID, err = requireFeedID(w.ID, "id"); err != nil {
		return FeedHealth{}, err
	}

	if h.Symbol, err = requireString(w.Symbol, "symbol"); err != nil {
		return FeedHealth{}, err
	}

	status, err := requireString(w.Status, "status")
	if err != nil {
		return FeedHealth{}, err
	}

	h.Status = FeedStatus(status)

	if w.AgeSeconds == nil || math.IsNaN(*w.AgeSeconds) || math.IsInf(*w.AgeSeconds, 0) {
		return FeedHealth{}, invalidf("age_seconds is missing")
	}

	h.AgeSeconds = *w.AgeSeconds

	if h.PublishTime, err = requireInt64(w.PublishTime, "publish_time", 0, maxUnixSeconds); err != nil {
		return FeedHealth{}, err
	}

	if h.ServedPublishTime, err = requireInt64(w.ServedPublishTime, "served_publish_time", 0, maxUnixSeconds); err != nil {
		return FeedHealth{}, err
	}

	if h.Sources, err = requireCount(w.Sources, "sources"); err != nil {
		return FeedHealth{}, err
	}

	if h.Stale, err = requireBool(w.Stale, "stale"); err != nil {
		return FeedHealth{}, err
	}

	return h, nil
}

func (w *wireStatusReport) decode() (StatusReport, error) {
	ready, err := requireBool(w.Ready, "ready")
	if err != nil {
		return StatusReport{}, err
	}

	if w.Feeds == nil {
		return StatusReport{}, invalidf("feeds is missing")
	}

	r := StatusReport{Ready: ready, Feeds: make([]FeedHealth, 0, len(w.Feeds))}

	for i := range w.Feeds {
		h, err := w.Feeds[i].decode()
		if err != nil {
			return StatusReport{}, invalidf("feeds[%d]: %s", i, reason(err))
		}

		r.Feeds = append(r.Feeds, h)
	}

	return r, nil
}

func (w *wireBars) decode() ([]Candle, error) {
	switch w.S {
	case "no_data":
		return []Candle{}, nil
	case "ok":
	default:
		return nil, invalidf("candles status %q", clip(w.S))
	}

	n := len(w.T)
	if len(w.O) != n || len(w.H) != n || len(w.L) != n || len(w.C) != n {
		return nil, invalidf("candle arrays differ in length")
	}

	out := make([]Candle, n)

	for i := range n {
		if w.T[i] == nil || *w.T[i] < 0 || *w.T[i] > maxUnixSeconds {
			return nil, invalidf("t[%d] is not unix seconds", i)
		}

		if w.O[i] == nil || w.H[i] == nil || w.L[i] == nil || w.C[i] == nil {
			return nil, invalidf("bar %d has a null price", i)
		}

		out[i] = Candle{Time: *w.T[i], Open: *w.O[i], High: *w.H[i], Low: *w.L[i], Close: *w.C[i]}
	}

	return out, nil
}

func reason(err error) string {
	if v, ok := err.(*ValidationError); ok {
		return v.Reason
	}

	return err.Error()
}

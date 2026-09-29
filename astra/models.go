package astra

type Channel string

const (
	ChannelRealTime    Channel = "real_time"
	ChannelFixed200ms  Channel = "fixed_rate@200ms"
	ChannelFixed1000ms Channel = "fixed_rate@1000ms"
)

type FeedStatus string

const (
	StatusTrading      FeedStatus = "trading"
	StatusDegraded     FeedStatus = "degraded"
	StatusMarketClosed FeedStatus = "market_closed"
	StatusReference    FeedStatus = "reference"
	StatusNoData       FeedStatus = "no_data"
)

type UpdateMetadata struct {
	ReceiveTime     int64
	PrevPublishTime int64
}

type PriceUpdate struct {
	Metadata *UpdateMetadata
	ID       string
	Price    Price
	EMAPrice Price
}

type FeedMetadata struct {
	Attributes    map[string]string
	ID            string
	AstraID       string
	Symbol        string
	AssetType     string
	DisplaySymbol string
	QuoteCurrency string
	Description   string
	MinChannel    string
	Base          string
	Schedule      string
	MarketOpen    bool
}

type LiveValue struct {
	Price             *Price
	Status            FeedStatus
	PublishTime       int64
	TimestampMs       int64
	ServedPublishTime int64
	Sources           int
	Expo              int32
}

type Feed struct {
	Attributes map[string]string
	ID         string
	PythID     string
	Symbol     string
	Category   string
	Live       LiveValue
}

type FeedIDEntry struct {
	Symbol    string
	AssetType string
	Category  string
	AstraID   string
	PythID    string
}

type FeedIDMap struct {
	Items   []FeedIDEntry
	Missing []string
}

type FeedHealth struct {
	ID                string
	Symbol            string
	Status            FeedStatus
	AgeSeconds        float64
	PublishTime       int64
	ServedPublishTime int64
	Sources           int
	Stale             bool
}

type StatusReport struct {
	Feeds []FeedHealth
	Ready bool
}

type Candle struct {
	Time  int64
	Open  float64
	High  float64
	Low   float64
	Close float64
}

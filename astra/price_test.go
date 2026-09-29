package astra

import (
	"errors"
	"math/big"
	"strings"
	"testing"
)

func TestPriceDecimal(t *testing.T) {
	cases := []struct {
		price string
		want  string
		expo  int32
	}{
		{"83095442500000", "83095.4425", -9},
		{"-12345", "-123.45", -2},
		{"5", "0.005", -3},
		{"-5", "-0.005", -3},
		{"12", "12000", 3},
		{"0", "0", -8},
		{"-0", "0", 2},
		{"000123", "12.3", -1},
		{"1000", "1", -3},
	}

	for _, c := range cases {
		if got := (Price{Price: c.price, Expo: c.expo}).Decimal(); got != c.want {
			t.Errorf("Decimal(%s e%d) = %s, want %s", c.price, c.expo, got, c.want)
		}
	}
}

func TestPriceFloat64(t *testing.T) {
	p := Price{Price: "83095442500000", Conf: "4947500000", Expo: -9}
	if p.Float64() != 83095.4425 || p.ConfFloat64() != 4.9475 {
		t.Fatalf("got %v %v", p.Float64(), p.ConfFloat64())
	}
}

func TestPriceScaledTruncatesTowardZero(t *testing.T) {
	cases := []struct {
		price    string
		want     string
		expo     int32
		decimals int
	}{
		{"83095442500000", "83095442500000000000000", -9, 18},
		{"83095442500000", "8309544", -9, 2},
		{"83095442500000", "83095", -9, 0},
		{"-12345", "-1234", -2, 1},
		{"7", "700000", 3, 2},
		{"42", "42", -6, 6},
		{strings.Repeat("9", 40), strings.Repeat("9", 40) + strings.Repeat("0", 69), -8, 77},
	}

	for _, c := range cases {
		got, err := Price{Price: c.price, Expo: c.expo}.Scaled(c.decimals)
		if err != nil {
			t.Fatal(err)
		}

		want, _ := new(big.Int).SetString(c.want, 10)
		if got.Cmp(want) != 0 {
			t.Errorf("Scaled(%s e%d, %d) = %s, want %s", c.price, c.expo, c.decimals, got, c.want)
		}
	}

	conf, err := Price{Conf: "4947500000", Expo: -9}.ConfScaled(6)
	if err != nil || conf.Int64() != 4947500 {
		t.Fatalf("ConfScaled = %v, %v", conf, err)
	}
}

func TestPriceScaledRefusesBadInput(t *testing.T) {
	var ve *ValidationError

	for _, d := range []int{-1, 78} {
		if _, err := (Price{Price: "1"}).Scaled(d); !errors.As(err, &ve) {
			t.Errorf("decimals %d: err = %v", d, err)
		}
	}

	if _, err := (Price{Price: "1.5"}).Scaled(2); !errors.As(err, &ve) {
		t.Errorf("non-integer mantissa: err = %v", err)
	}
}

func TestNormalizeFeedID(t *testing.T) {
	for _, in := range []string{btc, "0x" + btc, "0X" + strings.ToUpper(btc)} {
		got, err := NormalizeFeedID(in)
		if err != nil || got != btc {
			t.Errorf("NormalizeFeedID(%q) = %q, %v", in, got, err)
		}
	}

	for _, bad := range []string{"", "0x", btc[1:], btc + "0", btc[1:] + "g", " " + btc[1:]} {
		if _, err := NormalizeFeedID(bad); err == nil {
			t.Errorf("NormalizeFeedID(%q) accepted", bad)
		}
	}
}

func TestNormalizeFeedIDsDedupesAndBounds(t *testing.T) {
	got, err := normalizeFeedIDs([]string{btc, "0x" + btc, eth}, 500)
	if err != nil || len(got) != 2 || got[0] != btc || got[1] != eth {
		t.Fatalf("got %v, %v", got, err)
	}

	if _, err = normalizeFeedIDs(nil, 500); err == nil {
		t.Error("empty batch accepted")
	}

	if _, err = normalizeFeedIDs([]string{btc, eth}, 1); err == nil {
		t.Error("oversized batch accepted")
	}
}

package astra

import (
	"math/big"
	"strconv"
	"strings"
)

const (
	MinExpo          = -32
	MaxExpo          = 32
	MaxScaleDecimals = 77
)

type Price struct {
	Price       string
	Conf        string
	Expo        int32
	PublishTime int64
}

func (p Price) Float64() float64 {
	return mantissaFloat(p.Price, p.Expo)
}

func (p Price) ConfFloat64() float64 {
	return mantissaFloat(p.Conf, p.Expo)
}

func (p Price) Decimal() string {
	return mantissaDecimal(p.Price, p.Expo)
}

func (p Price) Scaled(decimals int) (*big.Int, error) {
	return mantissaScaled(p.Price, p.Expo, decimals)
}

func (p Price) ConfScaled(decimals int) (*big.Int, error) {
	return mantissaScaled(p.Conf, p.Expo, decimals)
}

func mantissaFloat(m string, expo int32) float64 {
	f, err := strconv.ParseFloat(m+"e"+strconv.Itoa(int(expo)), 64)
	if err != nil {
		return 0
	}

	return f
}

func mantissaDecimal(m string, expo int32) string {
	negative := strings.HasPrefix(m, "-")
	digits := strings.TrimLeft(strings.TrimPrefix(m, "-"), "0")

	if digits == "" {
		return "0"
	}

	var out string

	if expo >= 0 {
		out = digits + strings.Repeat("0", int(expo))
	} else {
		places := int(-expo)
		if len(digits) <= places {
			digits = strings.Repeat("0", places-len(digits)+1) + digits
		}

		whole, frac := digits[:len(digits)-places], strings.TrimRight(digits[len(digits)-places:], "0")
		out = whole

		if frac != "" {
			out += "." + frac
		}
	}

	if negative {
		return "-" + out
	}

	return out
}

func mantissaScaled(m string, expo int32, decimals int) (*big.Int, error) {
	if decimals < 0 || decimals > MaxScaleDecimals {
		return nil, invalidf("decimals must be in [0, %d], got %d", MaxScaleDecimals, decimals)
	}

	v, ok := new(big.Int).SetString(m, 10)
	if !ok {
		return nil, invalidf("price %q is not an integer", clip(m))
	}

	shift := int64(expo) + int64(decimals)
	if shift == 0 {
		return v, nil
	}

	abs := shift
	if abs < 0 {
		abs = -abs
	}

	factor := new(big.Int).Exp(big.NewInt(10), big.NewInt(abs), nil)
	if shift > 0 {
		return v.Mul(v, factor), nil
	}

	return v.Quo(v, factor), nil
}

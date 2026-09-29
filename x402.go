package avee

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
)

const (
	x402Version        = 2
	maxSignatureHeader = 16 << 10
	schemeExact        = "exact"
	maxAmountDigits    = 78
)

type Payer interface {
	Networks() []string
	Sign(ctx context.Context, p PaymentContext) (PaymentSignature, error)
}

type PaymentContext struct {
	Required    PaymentRequired
	Requirement PaymentRequirements
	Operation   string
	Method      string
	URL         string
	PaymentID   string
}

type PaymentSignature struct {
	Payload map[string]any
	Header  string
}

type PaymentReceipt struct {
	Transaction string `json:"transaction"`
	Network     string `json:"network"`
	Payer       string `json:"payer,omitempty"`
	ErrorReason string `json:"errorReason,omitempty"`
	Success     bool   `json:"success"`
}

type payerFunc struct {
	sign     func(context.Context, PaymentContext) (PaymentSignature, error)
	networks []string
}

func (p payerFunc) Networks() []string {
	return p.networks
}

func (p payerFunc) Sign(ctx context.Context, pc PaymentContext) (PaymentSignature, error) {
	if p.sign == nil {
		return PaymentSignature{}, ErrPaymentDeclined
	}

	return p.sign(ctx, pc)
}

func NewPayer(networks []string, sign func(context.Context, PaymentContext) (PaymentSignature, error)) Payer {
	return payerFunc{networks: append([]string(nil), networks...), sign: sign}
}

type requiredWire struct {
	Resource    json.RawMessage   `json:"resource"`
	Accepts     []json.RawMessage `json:"accepts"`
	X402Version int               `json:"x402Version"`
}

func decodeRequired(rep *reply) (*PaymentRequired, *requiredWire, error) {
	raw := rep.body.Bytes()

	if h := strings.TrimSpace(rep.header.Get("PAYMENT-REQUIRED")); h != "" {
		decoded, err := decodeBase64(h)
		if err != nil {
			return nil, nil, errors.New("the PAYMENT-REQUIRED header is not base64")
		}

		raw = decoded
	}

	var (
		typed PaymentRequired
		wire  requiredWire
	)

	if err := json.Unmarshal(raw, &typed); err != nil {
		return nil, nil, err
	}

	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, nil, err
	}

	if len(typed.Accepts) == 0 || len(typed.Accepts) != len(wire.Accepts) {
		return nil, nil, errors.New("the challenge offers no way to pay")
	}

	return &typed, &wire, nil
}

func (c *Client) pay(ctx context.Context, r *call, target string, rep *reply) (string, error) {
	required, wire, err := decodeRequired(rep)
	if err != nil {
		return "", &PaymentError{Operation: r.operation, Reason: "the 402 challenge is unreadable", Err: err}
	}

	choice := pickRequirement(required.Accepts, c.payer.Networks())
	if choice < 0 {
		return "", &PaymentError{Operation: r.operation, Reason: "cannot pay", Err: ErrNoPayableNetwork, Required: required}
	}

	if err = checkOffer(required.Accepts[choice]); err != nil {
		return "", &PaymentError{Operation: r.operation, Reason: "the offer is malformed", Err: err, Required: required}
	}

	id, err := newPaymentID()
	if err != nil {
		return "", &PaymentError{Operation: r.operation, Reason: "cannot create a payment identifier", Err: err}
	}

	sig, err := c.payer.Sign(ctx, PaymentContext{
		Required:    *required,
		Requirement: required.Accepts[choice],
		Operation:   r.operation,
		Method:      r.method,
		URL:         target,
		PaymentID:   id,
	})
	if err != nil {
		return "", &PaymentError{Operation: r.operation, Reason: "the payer did not sign", Err: err, Required: required}
	}

	if sig.Header != "" {
		if len(sig.Header) > maxSignatureHeader || strings.ContainsAny(sig.Header, "\r\n") {
			return "", &PaymentError{Operation: r.operation, Reason: "the payer returned an unusable PAYMENT-SIGNATURE", Required: required}
		}

		return sig.Header, nil
	}

	if sig.Payload == nil {
		return "", &PaymentError{Operation: r.operation, Reason: "the payer returned nothing", Err: ErrPaymentDeclined, Required: required}
	}

	version := wire.X402Version
	if version == 0 {
		version = x402Version
	}

	payload := map[string]any{
		"x402Version": version,
		"accepted":    wire.Accepts[choice],
		"payload":     sig.Payload,
		"extensions":  map[string]any{"payment-identifier": map[string]any{"info": map[string]string{"id": id}}},
	}

	if len(wire.Resource) > 0 {
		payload["resource"] = wire.Resource
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", &PaymentError{Operation: r.operation, Reason: "the signed payload cannot be encoded", Err: err, Required: required}
	}

	header := base64.StdEncoding.EncodeToString(encoded)
	if len(header) > maxSignatureHeader {
		return "", &PaymentError{Operation: r.operation, Reason: "the signed payload is too large", Required: required}
	}

	return header, nil
}

func pickRequirement(accepts []PaymentRequirements, networks []string) int {
	if len(networks) == 0 {
		for i, a := range accepts {
			if a.Scheme == schemeExact {
				return i
			}
		}

		return -1
	}

	for _, n := range networks {
		for i, a := range accepts {
			if a.Scheme == schemeExact && strings.EqualFold(a.Network, n) {
				return i
			}
		}
	}

	return -1
}

func checkOffer(a PaymentRequirements) error {
	switch {
	case a.Network == "" || a.Asset == "" || a.PayTo == "":
		return errors.New("it names no network, asset or payee")
	case !positiveAmount(a.Amount):
		return fmt.Errorf("amount %q is not a positive integer", clip(a.Amount))
	case a.MaxAmountRequired == "":
		return nil
	case !positiveAmount(a.MaxAmountRequired):
		return fmt.Errorf("maxAmountRequired %q is not a positive integer", clip(a.MaxAmountRequired))
	case amount(a.Amount).Cmp(amount(a.MaxAmountRequired)) > 0:
		return fmt.Errorf("amount %s exceeds maxAmountRequired %s", a.Amount, a.MaxAmountRequired)
	default:
		return nil
	}
}

func positiveAmount(s string) bool {
	if s == "" || len(s) > maxAmountDigits {
		return false
	}

	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}

	return strings.TrimLeft(s, "0") != ""
}

func amount(s string) *big.Int {
	n, _ := new(big.Int).SetString(s, 10)

	return n
}

func parseReceipt(h http.Header) *PaymentReceipt {
	v := h.Get("PAYMENT-RESPONSE")
	if v == "" {
		v = h.Get("X-PAYMENT-RESPONSE")
	}

	if v == "" {
		return nil
	}

	raw, err := decodeBase64(strings.TrimSpace(v))
	if err != nil {
		return nil
	}

	var receipt PaymentReceipt
	if json.Unmarshal(raw, &receipt) != nil {
		return nil
	}

	return &receipt
}

func decodeBase64(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}

	return nil, errors.New("not base64")
}

func newPaymentID() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	return "avee_" + base64.RawURLEncoding.EncodeToString(b), nil
}

package astra

import "strings"

const (
	MaxIDsPerRequest   = 500
	MaxIDsPerURL       = 200
	MaxHistoricalFeeds = 100
	feedIDHexLen       = 64
)

func NormalizeFeedID(id string) (string, error) {
	hex := id
	if len(hex) >= 2 && hex[0] == '0' && (hex[1] == 'x' || hex[1] == 'X') {
		hex = hex[2:]
	}

	if len(hex) != feedIDHexLen {
		return "", invalidf("invalid feed id %q", clip(id))
	}

	lower := true

	for i := range len(hex) {
		c := hex[i]

		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
			lower = false
		default:
			return "", invalidf("invalid feed id %q", clip(id))
		}
	}

	if lower {
		return hex, nil
	}

	return strings.ToLower(hex), nil
}

func uniqueFeedIDs(ids []string) ([]string, error) {
	out := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))

	for _, id := range ids {
		n, err := NormalizeFeedID(id)
		if err != nil {
			return nil, err
		}

		if _, dup := seen[n]; !dup {
			seen[n] = struct{}{}
			out = append(out, n)
		}
	}

	return out, nil
}

func normalizeFeedIDs(ids []string, limit int) ([]string, error) {
	out, err := uniqueFeedIDs(ids)
	if err != nil {
		return nil, err
	}

	if len(out) == 0 {
		return nil, invalidf("at least one feed id is required")
	}

	if len(out) > limit {
		return nil, invalidf("at most %d distinct feed ids per request, got %d", limit, len(out))
	}

	return out, nil
}

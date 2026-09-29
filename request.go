package avee

import (
	"context"
	"iter"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	noMin = math.MinInt64
	noMax = math.MaxInt64
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type integer interface {
	~int | ~int32 | ~int64 | ~uint32 | ~uint64
}

type call struct {
	body      any
	err       error
	query     url.Values
	operation string
	method    string
	segments  []string
}

func newCall(operation, method string) *call {
	return &call{operation: operation, method: method, query: url.Values{}}
}

func (r *call) fail(format string, args ...any) {
	if r.err == nil {
		r.err = invalidf(r.operation+": "+format, args...)
	}
}

func (r *call) pathLiteral(s string) {
	r.segments = append(r.segments, s)
}

func (r *call) pathParam(name, v string, minLen, maxLen int, uuid bool) {
	v = strings.TrimSpace(v)
	switch {
	case v == "" || v == "." || v == "..":
		r.fail("%s must not be empty", name)
	case !lengthOK(v, minLen, maxLen):
		r.fail("%s must be %s characters", name, lengthRange(minLen, maxLen))
	case uuid && !uuidPattern.MatchString(v):
		r.fail("%s must be a UUID", name)
	}

	r.segments = append(r.segments, v)
}

func (r *call) checkItems(name string, n, minItems, maxItems int) {
	if n < minItems || (maxItems >= 0 && n > maxItems) {
		r.fail("%s must hold %s items, got %d", name, lengthRange(minItems, maxItems), n)
	}
}

func lengthOK(v string, minLen, maxLen int) bool {
	n := utf8.RuneCountInString(v)

	return n >= minLen && (maxLen < 0 || n <= maxLen)
}

func lengthRange(lo, hi int) string {
	if hi < 0 {
		return "at least " + strconv.Itoa(lo)
	}

	return strconv.Itoa(lo) + " to " + strconv.Itoa(hi)
}

func strQ[T ~string](r *call, name string, v *T, minLen, maxLen int, required, uuid bool) {
	if v == nil || (required && *v == "") {
		if required {
			r.fail("%s is required", name)
		}

		return
	}

	s := string(*v)
	switch {
	case !lengthOK(s, minLen, maxLen):
		r.fail("%s must be %s characters", name, lengthRange(minLen, maxLen))
	case uuid && !uuidPattern.MatchString(s):
		r.fail("%s must be a UUID", name)
	}

	r.query.Set(name, s)
}

func intQ[T integer](r *call, name string, v *T, lo, hi int64) {
	if v == nil {
		return
	}

	n := int64(*v)
	if n < lo || n > hi {
		r.fail("%s is out of range: %d", name, n)
	}

	r.query.Set(name, strconv.FormatInt(n, 10))
}

func floatQ(r *call, name string, v *float64, lo, hi float64) {
	if v == nil {
		return
	}

	if math.IsNaN(*v) || math.IsInf(*v, 0) || *v < lo || *v > hi {
		r.fail("%s is out of range: %v", name, *v)
	}

	r.query.Set(name, strconv.FormatFloat(*v, 'f', -1, 64))
}

func boolQ(r *call, name string, v *bool) {
	if v != nil {
		r.query.Set(name, strconv.FormatBool(*v))
	}
}

func listQ[T ~string](r *call, name string, vs []T, minItems, maxItems, itemMin, itemMax int, required bool) {
	if len(vs) == 0 {
		if required || minItems > 0 && vs != nil {
			r.fail("%s needs at least one value", name)
		}

		return
	}

	r.checkItems(name, len(vs), minItems, maxItems)

	parts := make([]string, len(vs))
	for i, v := range vs {
		s := strings.TrimSpace(string(v))
		if s == "" || strings.Contains(s, ",") || !lengthOK(s, max(itemMin, 1), itemMax) {
			r.fail("%s[%d] must be %s characters without commas", name, i, lengthRange(max(itemMin, 1), itemMax))
		}

		parts[i] = s
	}

	r.query.Set(name, strings.Join(parts, ","))
}

func paginate[T any](ctx context.Context, start *string, fetch func(cursor *string) ([]T, *string, error)) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var (
			zero  T
			loops cursorLoop
		)

		cursor := start

		for {
			if err := ctx.Err(); err != nil {
				yield(zero, err)

				return
			}

			items, next, err := fetch(cursor)
			if err != nil {
				yield(zero, err)

				return
			}

			for _, item := range items {
				if !yield(item, nil) {
					return
				}
			}

			if next == nil || *next == "" {
				return
			}

			if loops.repeats(cursor, *next) {
				yield(zero, invalidf("the server repeated cursor %q", clip(*next)))

				return
			}

			cursor = next
		}
	}
}

type cursorLoop struct {
	mark  string
	steps int
	span  int
}

func (l *cursorLoop) repeats(current *string, next string) bool {
	if (current != nil && *current == next) || next == l.mark {
		return true
	}

	l.steps++
	if l.steps >= l.span {
		l.mark, l.steps, l.span = next, 0, max(1, 2*l.span)
	}

	return false
}

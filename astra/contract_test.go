package astra

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type contract struct {
	Routes map[string][]string          `json:"routes"`
	Fields map[string]map[string]string `json:"fields"`
	Enums  map[string][]string          `json:"enums"`
}

type openAPI struct {
	Paths      map[string]map[string]node `yaml:"paths"`
	Components map[string]map[string]node `yaml:"components"`
}

type node = map[string]any

func loadContract(t *testing.T) (openAPI, contract) {
	t.Helper()

	raw, err := os.ReadFile("testdata/astra.yml")
	if err != nil {
		t.Fatal(err)
	}

	var spec openAPI
	if err = yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}

	raw, err = os.ReadFile("testdata/sdk-contract.json")
	if err != nil {
		t.Fatal(err)
	}

	var c contract
	if err = json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}

	return spec, c
}

var refPattern = regexp.MustCompile(`^#/components/(\w+)/(.+)$`)

func (s openAPI) resolve(t *testing.T, n node) node {
	t.Helper()

	for {
		ref, ok := n["$ref"].(string)
		if !ok {
			return n
		}

		m := refPattern.FindStringSubmatch(ref)
		if m == nil || s.Components[m[1]][m[2]] == nil {
			t.Fatalf("unresolved %s", ref)
		}

		n = s.Components[m[1]][m[2]]
	}
}

func (s openAPI) child(t *testing.T, n node, part string) (node, bool) {
	t.Helper()

	key, items := strings.CutSuffix(part, "[]")

	props, _ := n["properties"].(node)

	next, ok := props[key].(node)
	if !ok {
		return nil, false
	}

	next = s.resolve(t, next)
	if items {
		if next["type"] != "array" {
			return nil, false
		}

		next = s.resolve(t, next["items"].(node))
	}

	return next, true
}

func (s openAPI) walk(t *testing.T, schema, path string) node {
	t.Helper()

	n := s.resolve(t, s.Components["schemas"][schema])

	for _, part := range strings.Split(path, ".") {
		next, ok := s.child(t, n, part)
		if !ok {
			t.Fatalf("%s.%s: no property %s", schema, path, part)
		}

		n = next
	}

	return n
}

func TestContractRoutes(t *testing.T) {
	spec, c := loadContract(t)

	for route, params := range c.Routes {
		op, ok := spec.Paths[route]["get"]
		if !ok {
			t.Errorf("GET %s is gone", route)

			continue
		}

		names := map[string]bool{}

		list, _ := op["parameters"].([]any)
		for _, p := range list {
			names[spec.resolve(t, p.(node))["name"].(string)] = true
		}

		for _, p := range params {
			if !names[p] {
				t.Errorf("GET %s lost parameter %s", route, p)
			}
		}
	}
}

func TestContractFields(t *testing.T) {
	spec, c := loadContract(t)

	for schema, fields := range c.Fields {
		for path, want := range fields {
			if got := spec.walk(t, schema, path)["type"]; got != want {
				t.Errorf("%s.%s: type %v, want %s", schema, path, got, want)
			}
		}
	}
}

func TestContractEnums(t *testing.T) {
	spec, c := loadContract(t)

	for ref, values := range c.Enums {
		section, name, _ := strings.Cut(ref, ".")

		n := spec.resolve(t, spec.Components[section][name])
		if section == "parameters" {
			n = spec.resolve(t, n["schema"].(node))
		}

		have := map[string]bool{}
		for _, v := range n["enum"].([]any) {
			have[v.(string)] = true
		}

		for _, v := range values {
			if !have[v] {
				t.Errorf("%s lost %s", ref, v)
			}
		}
	}

	if ChannelRealTime != "real_time" || ChannelFixed200ms != "fixed_rate@200ms" || ChannelFixed1000ms != "fixed_rate@1000ms" {
		t.Error("Channel constants drifted from the contract")
	}
}

var goToSpec = map[reflect.Kind]string{
	reflect.String:  "string",
	reflect.Int:     "integer",
	reflect.Int32:   "integer",
	reflect.Int64:   "integer",
	reflect.Float64: "number",
	reflect.Bool:    "boolean",
	reflect.Slice:   "array",
	reflect.Struct:  "object",
	reflect.Map:     "object",
}

func (s openAPI) checkWire(t *testing.T, typ reflect.Type, n node, path string) {
	t.Helper()

	for i := range typ.NumField() {
		f := typ.Field(i)
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")

		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}

		part := tag
		if ft.Kind() == reflect.Slice && ft.Elem().Kind() == reflect.Struct {
			part = tag + "[]"
		}

		child, ok := s.child(t, n, strings.TrimSuffix(part, "[]"))
		if !ok {
			t.Errorf("%s.%s: the SDK reads a field the spec does not have", path, tag)

			continue
		}

		if want := goToSpec[ft.Kind()]; child["type"] != want {
			t.Errorf("%s.%s: spec type %v, SDK decodes %s", path, tag, child["type"], want)

			continue
		}

		switch {
		case ft.Kind() == reflect.Struct:
			s.checkWire(t, ft, child, path+"."+tag)
		case ft.Kind() == reflect.Slice && ft.Elem().Kind() == reflect.Struct:
			s.checkWire(t, ft.Elem(), s.resolve(t, child["items"].(node)), path+"."+tag+"[]")
		case ft.Kind() == reflect.Slice:
			elem := ft.Elem()
			for elem.Kind() == reflect.Pointer {
				elem = elem.Elem()
			}

			if want := goToSpec[elem.Kind()]; s.resolve(t, child["items"].(node))["type"] != want {
				t.Errorf("%s.%s[]: item type differs from %s", path, tag, want)
			}
		}
	}
}

func TestContractWireStructsMatchSpec(t *testing.T) {
	spec, _ := loadContract(t)

	wire := map[string]any{
		"PriceUpdate":       wireEnvelope{},
		"ParsedPriceUpdate": wireUpdate[proofMetadata]{},
		"PriceFeed":         wireUpdate[streamMetadata]{},
		"PriceFeedMetadata": wireFeedMetadata{},
		"Feed":              wireFeed{},
		"StatusReport":      wireStatusReport{},
		"FeedIDList":        wireFeedIDList{},
		"Bars":              wireBars{},
		"Problem":           Problem{},
	}

	for schema, v := range wire {
		spec.checkWire(t, reflect.TypeOf(v), spec.resolve(t, spec.Components["schemas"][schema]), schema)
	}
}

func (s openAPI) instance(t *testing.T, n node, full bool) any {
	t.Helper()

	n = s.resolve(t, n)

	if v, ok := n["const"]; ok {
		return v
	}

	if e, ok := n["enum"].([]any); ok {
		return e[0]
	}

	if one, ok := n["oneOf"].([]any); ok {
		return s.instance(t, one[0].(node), full)
	}

	switch n["type"] {
	case "object":
		out := map[string]any{}
		if full {
			out["x_future_field"] = map[string]any{"nested": []int{1}}
		}

		required := map[string]bool{}
		if r, ok := n["required"].([]any); ok {
			for _, k := range r {
				required[k.(string)] = true
			}
		}

		props, _ := n["properties"].(node)
		for k, v := range props {
			if full || required[k] {
				out[k] = s.instance(t, v.(node), full)
			}
		}

		return out
	case "array":
		if n["maxItems"] == 0 {
			return []any{}
		}

		return []any{s.instance(t, n["items"].(node), full)}
	case "integer":
		return 1
	case "number":
		return 1.5
	case "boolean":
		return true
	case "null":
		return nil
	default:
		if p, ok := n["pattern"].(string); ok && strings.Contains(p, "{64}") {
			return strings.Repeat("ab", 32)
		}

		return "1"
	}
}

func TestContractInstancesDecode(t *testing.T) {
	spec, _ := loadContract(t)

	decoders := map[string]func([]byte) error{
		"PriceUpdate": func(b []byte) error {
			var w wireEnvelope
			if err := json.Unmarshal(b, &w); err != nil {
				return err
			}

			_, err := w.decode(nil)

			return err
		},
		"PriceFeed":         decodeWith[wireUpdate[streamMetadata], PriceUpdate],
		"PriceFeedMetadata": decodeWith[wireFeedMetadata, FeedMetadata],
		"Feed":              decodeWith[wireFeed, Feed],
		"StatusReport":      decodeWith[wireStatusReport, StatusReport],
		"FeedIDList":        decodeWith[wireFeedIDList, FeedIDMap],
		"Bars":              decodeWith[wireBars, []Candle],
	}

	for schema, decode := range decoders {
		for _, full := range []bool{false, true} {
			raw, err := json.Marshal(spec.instance(t, spec.Components["schemas"][schema], full))
			if err != nil {
				t.Fatal(err)
			}

			if err = decode(raw); err != nil {
				t.Errorf("%s (all fields %v): %v\n%s", schema, full, err, raw)
			}
		}
	}

	example, _ := json.Marshal(spec.resolve(t, spec.Components["schemas"]["PriceUpdate"])["example"])

	got, err := decodeEnvelope(t, json.RawMessage(example))
	if err != nil || got[0].Price.Decimal() != "65123.45" {
		t.Fatalf("spec example: %+v, %v", got, err)
	}
}

type decodable[T any] interface {
	decode() (T, error)
}

func decodeWith[W any, T any](b []byte) error {
	var w W
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}

	d, ok := any(&w).(decodable[T])
	if !ok {
		return fmt.Errorf("%T has no decoder", w)
	}

	_, err := d.decode()

	return err
}

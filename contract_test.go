package avee

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type contract struct {
	Operations map[string]struct {
		Method   string   `json:"method"`
		Path     string   `json:"path"`
		Response string   `json:"response"`
		Params   []string `json:"params"`
	} `json:"operations"`
	Fields map[string]map[string]string `json:"fields"`
}

func (s openAPI) contractType(t *testing.T, n node) string {
	t.Helper()

	if ref, ok := n["$ref"].(string); ok {
		return "#" + ref[strings.LastIndex(ref, "/")+1:]
	}

	if _, ok := n["oneOf"]; ok {
		return "oneOf"
	}

	switch n["type"] {
	case "array":
		item, _ := n["items"].(node)

		return s.contractType(t, item) + "[]"
	case "object":
		if ap, ok := n["additionalProperties"].(node); ok {
			return "map<" + s.contractType(t, ap) + ">"
		}

		if _, ok := n["properties"]; ok {
			return "object"
		}

		return "map<any>"
	case nil:
		return "any"
	default:
		typ, _ := n["type"].(string)

		return typ
	}
}

func TestSpecStillServesEverythingTheSDKReads(t *testing.T) {
	spec := loadSpec(t)

	raw, err := os.ReadFile("testdata/sdk-contract.json")
	if err != nil {
		t.Fatal(err)
	}

	var c contract
	if err = json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}

	for id, op := range c.Operations {
		item, ok := spec.Paths[op.Path][strings.ToLower(op.Method)]
		if !ok {
			t.Errorf("%s: %s %s is gone", id, op.Method, op.Path)

			continue
		}

		if item["operationId"] != id {
			t.Errorf("%s: operationId is now %v", id, item["operationId"])
		}

		names := map[string]bool{}
		params, _ := item["parameters"].([]any)
		for _, p := range params {
			pn, _ := p.(node)
			pn = spec.resolve(t, pn)
			name, _ := pn["name"].(string)
			names[name] = true
		}

		for _, p := range op.Params {
			if !names[p] {
				t.Errorf("%s: parameter %s is gone", id, p)
			}
		}
	}

	for schema, fields := range c.Fields {
		sn, ok := spec.Components["schemas"][schema]
		if !ok {
			t.Errorf("schema %s is gone", schema)

			continue
		}

		for path, want := range fields {
			n := sn
			for _, part := range strings.Split(path, ".") {
				n = spec.resolve(t, n)
				props, _ := n["properties"].(node)
				n, _ = props[part].(node)
			}

			if n == nil {
				t.Errorf("%s.%s is gone", schema, path)

				continue
			}

			got := spec.contractType(t, n)
			if strings.HasPrefix(want, "oneOf<") {
				want = "oneOf"
			}

			if _, isEnum := spec.resolve(t, n)["enum"]; isEnum && !strings.HasPrefix(want, "#") {
				got = "string"
			}

			if got != want {
				t.Errorf("%s.%s is %s, the SDK reads %s", schema, path, got, want)
			}
		}
	}
}

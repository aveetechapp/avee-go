package avee

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

type generatedCall struct {
	invoke    func(ctx context.Context, c *Client) (any, error)
	operation string
	response  string
}

type node = map[string]any

type openAPI struct {
	Paths      map[string]map[string]node `yaml:"paths"`
	Components map[string]map[string]node `yaml:"components"`
}

type recorded struct {
	header http.Header
	method string
	path   string
	query  string
	body   string
}

type fakeServer struct {
	*httptest.Server
	handler  func(w http.ResponseWriter, r *http.Request, n int)
	requests []recorded
	mu       sync.Mutex
}

func newFake(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, n int)) *fakeServer {
	t.Helper()

	f := &fakeServer{handler: handler}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		f.mu.Lock()
		f.requests = append(f.requests, recorded{header: r.Header.Clone(), method: r.Method, path: r.URL.EscapedPath(), query: r.URL.RawQuery, body: string(body)})
		n := len(f.requests)
		f.mu.Unlock()

		f.handler(w, r, n)
	}))
	t.Cleanup(func() {
		f.Close()
		f.Client().CloseIdleConnections()
	})

	return f
}

func (f *fakeServer) calls() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]recorded(nil), f.requests...)
}

func (f *fakeServer) client(t *testing.T, opts Options) *Client {
	t.Helper()

	opts.BaseURL = f.URL + "/api/v1"
	if opts.HTTPClient == nil {
		opts.HTTPClient = f.Client()
	}

	c, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}

	return c
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeProblem(w http.ResponseWriter, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("X-Request-Id", "req-1")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type": "https://docs.avee.tech/errors/" + code, "title": code, "status": status, "code": code,
		"detail": detail, "request_id": "req-1",
	})
}

var loadedSpec struct {
	err  error
	spec openAPI
	once sync.Once
}

func loadSpec(t testing.TB) openAPI {
	t.Helper()

	loadedSpec.once.Do(func() {
		raw, err := os.ReadFile("testdata/openapi.yml")
		if err != nil {
			loadedSpec.err = err

			return
		}

		loadedSpec.err = yaml.Unmarshal(raw, &loadedSpec.spec)
	})

	if loadedSpec.err != nil {
		t.Fatal(loadedSpec.err)
	}

	return loadedSpec.spec
}

var refPattern = regexp.MustCompile(`^#/components/(\w+)/(.+)$`)

func (s openAPI) resolve(t testing.TB, n node) node {
	t.Helper()

	for {
		ref, ok := n["$ref"].(string)
		if !ok {
			return n
		}

		m := refPattern.FindStringSubmatch(ref)
		if m == nil {
			t.Fatalf("unsupported $ref %s", ref)
		}

		next, ok := s.Components[m[1]][m[2]]
		if !ok {
			t.Fatalf("unresolved $ref %s", ref)
		}

		n = next
	}
}

type variant int

const (
	requiredOnly variant = iota
	everyField
	unknownValues
)

func (s openAPI) instance(t testing.TB, n node, v variant) any {
	t.Helper()

	n = s.resolve(t, n)

	if values, ok := n["enum"].([]any); ok {
		if v == unknownValues {
			return "x_future_value"
		}

		return values[0]
	}

	if options, ok := n["oneOf"].([]any); ok {
		first, _ := options[0].(node)

		return s.instance(t, first, v)
	}

	switch n["type"] {
	case "object":
		out := map[string]any{}
		props, _ := n["properties"].(node)

		if v != requiredOnly && len(props) > 0 {
			out["x_future_field"] = map[string]any{"nested": []any{1}}
		}

		required := map[string]bool{}
		if list, ok := n["required"].([]any); ok {
			for _, r := range list {
				name, _ := r.(string)
				required[name] = true
			}
		}

		for k, p := range props {
			if v != requiredOnly || required[k] {
				child, _ := p.(node)
				out[k] = s.instance(t, child, v)
			}
		}

		if ap, ok := n["additionalProperties"].(node); ok && len(props) == 0 && v != requiredOnly {
			out["k1"] = s.instance(t, ap, v)
		}

		return out
	case "array":
		item, _ := n["items"].(node)

		return []any{s.instance(t, item, v)}
	case "string":
		if f, _ := n["format"].(string); f == "date-time" {
			return "2026-09-28T00:00:00Z"
		}

		return "1"
	case "integer":
		return 1
	case "number":
		return 1.5
	case "boolean":
		return true
	default:
		return "1"
	}
}

func (s openAPI) schemaInstance(t testing.TB, name string, v variant) []byte {
	t.Helper()

	schema, ok := s.Components["schemas"][name]
	if !ok {
		t.Fatalf("no schema %s", name)
	}

	raw, err := json.Marshal(s.instance(t, schema, v))
	if err != nil {
		t.Fatal(err)
	}

	return raw
}

type sdkCall struct {
	Body      any        `json:"body"`
	Operation string     `json:"operation"`
	Method    string     `json:"method"`
	Path      string     `json:"path"`
	Response  string     `json:"response"`
	Query     [][]string `json:"query"`
	Paged     bool       `json:"paged"`
}

func loadCalls(t *testing.T) map[string]sdkCall {
	t.Helper()

	raw, err := os.ReadFile("testdata/sdk-calls.json")
	if err != nil {
		t.Fatal(err)
	}

	var list []sdkCall
	if err = json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}

	out := make(map[string]sdkCall, len(list))
	for _, c := range list {
		out[c.Operation] = c
	}

	return out
}

func sortedQuery(raw string) string {
	parts := strings.Split(raw, "&")
	sort.Strings(parts)

	return strings.Join(parts, "&")
}

func checkGoroutines(t *testing.T) {
	t.Helper()

	before := runtime.NumGoroutine()

	t.Cleanup(func() {
		deadline := time.Now().Add(3 * time.Second)
		for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}

		if n := runtime.NumGoroutine(); n > before {
			buf := make([]byte, 1<<16)
			t.Errorf("goroutines leaked: %d before, %d after\n%s", before, n, buf[:runtime.Stack(buf, true)])
		}
	})
}

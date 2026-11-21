package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"

	apispec "github.com/raven-clown/rosetta/api/admin"
)

const specURL = "file:///openapi.json"

type spec struct {
	doc      map[string]any
	compiler *jsonschema.Compiler
	mu       sync.Mutex
	schemas  map[string]*jsonschema.Schema
}

var (
	specOnce sync.Once
	loaded   *spec
)

func loadSpec(t *testing.T) *spec {
	t.Helper()
	specOnce.Do(func() {
		var y any
		if err := yaml.Unmarshal(apispec.OpenAPI, &y); err != nil {
			t.Fatalf("parsing openapi.yaml: %v", err)
		}
		raw, err := json.Marshal(y)
		if err != nil {
			t.Fatalf("openapi.yaml as JSON: %v", err)
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		c := jsonschema.NewCompiler()
		c.DefaultDraft(jsonschema.Draft2020)
		c.AssertFormat()
		if err := c.AddResource(specURL, doc); err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		loaded = &spec{doc: m, compiler: c, schemas: map[string]*jsonschema.Schema{}}
	})
	if loaded == nil {
		t.Fatal("openapi.yaml did not load")
	}
	return loaded
}

func (s *spec) operations() []string {
	var out []string
	for path, item := range s.doc["paths"].(map[string]any) {
		for method := range item.(map[string]any) {
			if method != "parameters" {
				out = append(out, strings.ToUpper(method)+" "+path)
			}
		}
	}
	slices.Sort(out)
	return out
}

// resolve follows a local $ref.
func (s *spec) resolve(node map[string]any) map[string]any {
	for {
		ref, ok := node["$ref"].(string)
		if !ok {
			return node
		}
		var cur any = s.doc
		for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			cur = cur.(map[string]any)[part]
		}
		node = cur.(map[string]any)
	}
}

func (s *spec) schema(t *testing.T, ref string) *jsonschema.Schema {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if sch, ok := s.schemas[ref]; ok {
		return sch
	}
	sch, err := s.compiler.Compile(specURL + ref)
	if err != nil {
		t.Fatalf("compiling %s: %v", ref, err)
	}
	s.schemas[ref] = sch
	return sch
}

// check validates a response against the file: the operation must list
// the status (or a default), and a JSON body must match its schema.
func (s *spec) check(t *testing.T, op string, status int, header http.Header, body []byte) {
	t.Helper()
	method, path, _ := strings.Cut(op, " ")
	item, ok := s.doc["paths"].(map[string]any)[path].(map[string]any)
	if !ok {
		t.Fatalf("%s is not in openapi.yaml", path)
	}
	opNode, ok := item[strings.ToLower(method)].(map[string]any)
	if !ok {
		t.Fatalf("%s is not in openapi.yaml", op)
	}
	responses := opNode["responses"].(map[string]any)
	resp, ok := responses[strconv.Itoa(status)].(map[string]any)
	if !ok {
		if resp, ok = responses["default"].(map[string]any); !ok || status < 400 {
			t.Fatalf("%s answered %d, which openapi.yaml does not list", op, status)
		}
	}
	resp = s.resolve(resp)
	content, _ := resp["content"].(map[string]any)
	jsonContent, ok := content["application/json"].(map[string]any)
	if !ok {
		if len(content) == 0 && len(body) > 0 {
			t.Errorf("%s %d has a body but openapi.yaml lists none", op, status)
		}
		return
	}
	if ct := header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("%s %d content type %q", op, status, ct)
	}
	ref, ok := jsonContent["schema"].(map[string]any)["$ref"].(string)
	if !ok {
		t.Fatalf("%s %d: response schemas must be a $ref", op, status)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("%s %d: body is not JSON: %v", op, status, err)
	}
	if err := s.schema(t, ref).Validate(inst); err != nil {
		t.Errorf("%s %d: body does not match %s: %v\n%s", op, status, ref, err, body)
	}
	headers, _ := resp["headers"].(map[string]any)
	if _, ok := headers["ETag"]; ok && header.Get("ETag") == "" {
		t.Errorf("%s %d: no ETag", op, status)
	}
}

func TestRoutesMatchSpec(t *testing.T) {
	var served []string
	for _, r := range routes {
		served = append(served, r.method+" "+r.path)
	}
	slices.Sort(served)
	if got := loadSpec(t).operations(); !slices.Equal(got, served) {
		t.Errorf("openapi.yaml operations:\n%v\nserved routes:\n%v", got, served)
	}
}

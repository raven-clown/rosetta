package config

import (
	"reflect"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

const envPrefix = "ROSETTA_"

// expand replaces ${NAME} with its value from environ. Only that form is
// expanded, so a "$5" in a prompt stays as written.
func expand(s string, environ []string) string {
	vars := map[string]string{}
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok {
			vars[k] = v
		}
	}
	var b strings.Builder
	for {
		start := strings.Index(s, "${")
		if start < 0 {
			break
		}
		end := strings.Index(s[start:], "}")
		if end < 0 {
			break
		}
		b.WriteString(s[:start])
		b.WriteString(vars[s[start+2:start+end]])
		s = s[start+end+1:]
	}
	b.WriteString(s)
	return b.String()
}

// override applies ROSETTA_* variables to the decoded YAML tree. Variables
// that do not name a config key are ignored, so ROSETTA_ is free for
// secrets referenced with ${...}.
func override(tree map[string]any, environ []string) {
	root := reflect.TypeFor[Config]()
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(k, envPrefix) {
			continue
		}
		path := strings.Split(strings.ToLower(strings.TrimPrefix(k, envPrefix)), "__")
		leaf, ok := leafType(root, path)
		if !ok {
			continue
		}
		set(tree, path, value(leaf, v))
	}
}

func leafType(t reflect.Type, path []string) (reflect.Type, bool) {
	for _, p := range path {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		switch t.Kind() {
		case reflect.Struct:
			f, ok := fieldByTag(t, p)
			if !ok {
				return nil, false
			}
			t = f.Type
		case reflect.Slice:
			if _, err := strconv.Atoi(p); err != nil {
				return nil, false
			}
			t = t.Elem()
		default:
			return nil, false
		}
	}
	return t, true
}

func fieldByTag(t reflect.Type, name string) (reflect.StructField, bool) {
	for i := range t.NumField() {
		f := t.Field(i)
		if tag, _, _ := strings.Cut(f.Tag.Get("yaml"), ","); tag == name {
			return f, true
		}
	}
	return reflect.StructField{}, false
}

func value(t reflect.Type, raw string) any {
	if t.Kind() == reflect.String {
		return raw
	}
	var v any
	if err := yaml.Unmarshal([]byte(raw), &v); err != nil {
		return raw
	}
	return v
}

func set(node map[string]any, path []string, v any) {
	var cur any = node
	for i, p := range path {
		last := i == len(path)-1
		switch n := cur.(type) {
		case map[string]any:
			if last {
				n[p] = v
				return
			}
			next, ok := n[p]
			if !ok || next == nil {
				next = map[string]any{}
				n[p] = next
			}
			cur = next
		case []any:
			idx, _ := strconv.Atoi(p)
			if idx < 0 || idx >= len(n) {
				return
			}
			if last {
				n[idx] = v
				return
			}
			cur = n[idx]
		default:
			return
		}
	}
}

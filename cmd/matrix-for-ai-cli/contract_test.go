package main

// The appservice's contract, testdata/openapi.json (a copy of the appservice's own), checks what crosses a fake
// appservice: each request the client makes and each answer the fake gives. A fake that drifts from the
// appservice, or a client that asks for something the appservice does not take, fails the case that showed it.
// The validator knows only the keywords that file uses, and calls any other a violation.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// A fake answers with this header when the case is the appservice, or a proxy, answering off contract.
const offContract = "X-Fake-Off-Contract"

type contract struct {
	doc map[string]any
}

var (
	loadedContract     *contract
	loadedContractErr  error
	loadContractOnce   sync.Once
	knownSchemaKeyword = map[string]bool{
		"$ref": true, "type": true, "properties": true, "required": true, "additionalProperties": true,
		"items": true, "enum": true, "const": true, "oneOf": true, "pattern": true, "minimum": true,
		"maximum": true, "minItems": true, "maxProperties": true, "format": true, "default": true,
		"description": true, "allOf": true, "discriminator": true,
	}
)

func appserviceContract(t testing.TB) *contract {
	loadContractOnce.Do(func() {
		data, err := os.ReadFile("testdata/openapi.json")
		if err != nil {
			loadedContractErr = err
			return
		}
		var doc map[string]any
		loadedContractErr = json.Unmarshal(data, &doc)
		loadedContract = &contract{doc}
	})
	if loadedContractErr != nil {
		t.Fatal(loadedContractErr)
	}
	return loadedContract
}

func (c *contract) version() string {
	return c.doc["info"].(map[string]any)["version"].(string)
}

func (c *contract) checked(h http.Handler, report func(string)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readAllAndRestore(r)
		op, path, problems := c.checkRequest(r, body)
		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		h.ServeHTTP(rec, r)
		if rec.Header().Get(offContract) != "" {
			return
		}
		if op != nil {
			problems = append(problems, c.checkResponse(op, rec.status, rec.Header(), rec.body.Bytes())...)
		}
		for _, p := range problems {
			report(fmt.Sprintf("%s %s: %s", r.Method, path, p))
		}
	})
}

type recorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (r *recorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(b []byte) (int, error) {
	r.body.Write(b)
	return r.ResponseWriter.Write(b)
}

func readAllAndRestore(r *http.Request) ([]byte, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(r.Body)
	r.Body.Close()
	r.Body = readCloser{bytes.NewReader(buf.Bytes())}
	return buf.Bytes(), err
}

type readCloser struct{ *bytes.Reader }

func (readCloser) Close() error { return nil }

func (c *contract) route(method, escapedPath string) (string, map[string]any, map[string]string) {
	segs := strings.Split(strings.Trim(escapedPath, "/"), "/")
	paths := c.doc["paths"].(map[string]any)
	templates := make([]string, 0, len(paths))
	for t := range paths {
		templates = append(templates, t)
	}
	sort.Strings(templates)
	for _, t := range templates {
		tsegs := strings.Split(strings.Trim(t, "/"), "/")
		if len(tsegs) != len(segs) {
			continue
		}
		params, match := map[string]string{}, true
		for i, ts := range tsegs {
			if strings.HasPrefix(ts, "{") {
				v, err := url.PathUnescape(segs[i])
				if err != nil {
					match = false
					break
				}
				params[strings.Trim(ts, "{}")] = v
			} else if ts != segs[i] {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		if op, ok := paths[t].(map[string]any)[strings.ToLower(method)].(map[string]any); ok {
			return t, op, params
		}
	}
	return escapedPath, nil, nil
}

func (c *contract) checkRequest(r *http.Request, body []byte) (map[string]any, string, []string) {
	path, op, pathParams := c.route(r.Method, r.URL.EscapedPath())
	if op == nil {
		return nil, path, []string{"no such route in the contract"}
	}
	var problems []string
	query := r.URL.Query()
	for _, p := range c.parameters(op) {
		name, in := p["name"].(string), p["in"].(string)
		switch in {
		case "path":
			problems = append(problems, c.validate(p["schema"], pathParams[name], "path "+name)...)
		case "query":
			values, given := query[name]
			delete(query, name)
			if !given {
				if p["required"] == true {
					problems = append(problems, "query "+name+" is required")
				}
				continue
			}
			v, err := queryValue(p, values)
			if err != nil {
				problems = append(problems, "query "+name+": "+err.Error())
				continue
			}
			problems = append(problems, c.validate(p["schema"], v, "query "+name)...)
		}
	}
	for name := range query {
		problems = append(problems, "query "+name+" is not a parameter of this route")
	}
	rb, _ := op["requestBody"].(map[string]any)
	switch {
	case rb == nil && len(body) > 0:
		problems = append(problems, "a body, and this route takes none")
	case rb != nil && len(body) == 0:
		if rb["required"] == true {
			problems = append(problems, "no body, and this route needs one")
		}
	case rb != nil:
		problems = append(problems, c.checkContent(rb, r.Header.Get("Content-Type"), body, "body")...)
	}
	return op, path, problems
}

func (c *contract) checkResponse(op map[string]any, status int, header http.Header, body []byte) []string {
	responses := op["responses"].(map[string]any)
	resp, ok := responses[strconv.Itoa(status)].(map[string]any)
	if !ok {
		resp, ok = responses[strconv.Itoa(status/100)+"XX"].(map[string]any)
	}
	if !ok {
		return []string{fmt.Sprintf("answers %d, which the contract does not list here", status)}
	}
	problems := c.checkHeaders(resp, header, fmt.Sprintf("answer %d", status))
	if _, has := resp["content"]; !has {
		if len(body) > 0 {
			problems = append(problems, fmt.Sprintf("answers %d with a body, and the contract has none", status))
		}
		return problems
	}
	return append(problems, c.checkContent(resp, header.Get("Content-Type"), body, fmt.Sprintf("answer %d", status))...)
}

// Every header the contract lists for an answer must come with it.
func (c *contract) checkHeaders(resp map[string]any, header http.Header, at string) []string {
	headers, _ := resp["headers"].(map[string]any)
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	var problems []string
	for _, name := range names {
		values, given := header[http.CanonicalHeaderKey(name)]
		if !given {
			problems = append(problems, at+": header "+name+" is missing")
			continue
		}
		h := c.resolve(headers[name])
		v, err := queryValue(h, values)
		if err != nil {
			problems = append(problems, at+": header "+name+": "+err.Error())
			continue
		}
		problems = append(problems, c.validate(h["schema"], v, at+": header "+name)...)
	}
	return problems
}

func (c *contract) checkContent(holder map[string]any, ctype string, body []byte, at string) []string {
	content := holder["content"].(map[string]any)
	media, _, _ := mime.ParseMediaType(ctype)
	spec, ok := content[media].(map[string]any)
	if !ok {
		kind, _, _ := strings.Cut(media, "/")
		spec, ok = content[kind+"/*"].(map[string]any)
	}
	if ok && !strings.HasSuffix(media, "json") {
		if s := c.resolve(spec["schema"]); s["format"] != "binary" {
			return []string{fmt.Sprintf("%s is %s, and its schema is not binary", at, media)}
		}
		return nil
	}
	if !ok {
		if spec, ok = content["*/*"].(map[string]any); ok {
			return nil
		}
		types := make([]string, 0, len(content))
		for k := range content {
			types = append(types, k)
		}
		sort.Strings(types)
		return []string{fmt.Sprintf("%s is %q, not %s", at, ctype, strings.Join(types, " or "))}
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return []string{at + " is not JSON: " + err.Error()}
	}
	return c.validate(spec["schema"], v, at)
}

func (c *contract) parameters(op map[string]any) []map[string]any {
	list, _ := op["parameters"].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, p := range list {
		out = append(out, c.resolve(p))
	}
	return out
}

func queryValue(p map[string]any, values []string) (any, error) {
	schema := p["schema"].(map[string]any)
	if len(values) > 1 {
		return nil, fmt.Errorf("given %d times", len(values))
	}
	s := values[0]
	switch schema["type"] {
	case "boolean":
		b, err := strconv.ParseBool(s)
		if err != nil || (s != "true" && s != "false") {
			return nil, fmt.Errorf("%q is not true or false", s)
		}
		return b, nil
	case "integer":
		n, err := strconv.Atoi(s)
		if err != nil {
			return nil, fmt.Errorf("%q is not an integer", s)
		}
		return float64(n), nil
	case "array":
		if p["explode"] != false {
			return nil, fmt.Errorf("only explode: false arrays are known to this validator")
		}
		items := []any{}
		for _, part := range strings.Split(s, ",") {
			items = append(items, part)
		}
		return items, nil
	}
	return s, nil
}

func (c *contract) resolve(s any) map[string]any {
	m, _ := s.(map[string]any)
	ref, ok := m["$ref"].(string)
	if !ok {
		return m
	}
	var node any = c.doc
	for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		node = node.(map[string]any)[part]
	}
	base := c.resolve(node)
	if len(m) == 1 {
		return base
	}
	// Next to a $ref, properties narrow the referenced ones (a Problem whose code is one of a few).
	merged := map[string]any{}
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range m {
		switch k {
		case "$ref", "description":
		case "properties":
			props := map[string]any{}
			for name, p := range base["properties"].(map[string]any) {
				props[name] = p
			}
			for name, p := range v.(map[string]any) {
				if have, ok := props[name]; ok {
					props[name] = map[string]any{"allOf": []any{have, p}}
				} else {
					props[name] = p
				}
			}
			merged[k] = props
		default:
			merged["$ref with "+k] = v
		}
	}
	return merged
}

// A property the schema does not name is a violation unless additionalProperties allows it:
// The appservice rejects unknown fields in what it takes, and a fake must not invent any in what it gives.
func (c *contract) validate(s any, v any, at string) []string {
	if b, ok := s.(bool); ok {
		if !b {
			return []string{at + ": not allowed"}
		}
		return nil
	}
	schema := c.resolve(s)
	for k := range schema {
		if !knownSchemaKeyword[k] {
			return []string{fmt.Sprintf("%s: the schema uses %q, which this validator does not know", at, k)}
		}
	}
	if all, ok := schema["allOf"].([]any); ok {
		var problems []string
		for _, one := range all {
			problems = append(problems, c.validate(one, v, at)...)
		}
		if len(problems) > 0 {
			return problems
		}
	}
	if d, ok := schema["discriminator"].(map[string]any); ok {
		name := d["propertyName"].(string)
		mapping := d["mapping"].(map[string]any)
		obj, _ := v.(map[string]any)
		target, ok := mapping[fmt.Sprint(obj[name])]
		if !ok {
			keys := make([]string, 0, len(mapping))
			for k := range mapping {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			return []string{fmt.Sprintf("%s: %s %v is not one of %s", at, name, obj[name], strings.Join(keys, ", "))}
		}
		return c.validate(map[string]any{"$ref": target}, v, at)
	}
	if alts, ok := schema["oneOf"].([]any); ok {
		passing := 0
		for _, alt := range alts {
			if len(c.validate(alt, v, at)) == 0 {
				passing++
			}
		}
		if passing != 1 {
			return []string{fmt.Sprintf("%s: matches %d of the oneOf alternatives, not 1", at, passing)}
		}
	}
	if want, ok := schema["const"]; ok && !jsonEqual(want, v) {
		return []string{fmt.Sprintf("%s: %v, not the constant %v", at, v, want)}
	}
	if enum, ok := schema["enum"].([]any); ok {
		found := false
		for _, e := range enum {
			found = found || jsonEqual(e, v)
		}
		if !found {
			return []string{fmt.Sprintf("%s: %v is not one of %v", at, v, enum)}
		}
	}
	if t, ok := schema["type"]; ok && !hasType(t, v) {
		return []string{fmt.Sprintf("%s: %s, not %v", at, jsonType(v), t)}
	}
	var problems []string
	switch x := v.(type) {
	case string:
		if p, ok := schema["pattern"].(string); ok && !regexp.MustCompile(p).MatchString(x) {
			problems = append(problems, fmt.Sprintf("%s: %q does not match %s", at, x, p))
		}
	case float64:
		if m, ok := schema["minimum"].(float64); ok && x < m {
			problems = append(problems, fmt.Sprintf("%s: %v is below %v", at, x, m))
		}
		if m, ok := schema["maximum"].(float64); ok && x > m {
			problems = append(problems, fmt.Sprintf("%s: %v is above %v", at, x, m))
		}
	case []any:
		if m, ok := schema["minItems"].(float64); ok && float64(len(x)) < m {
			problems = append(problems, fmt.Sprintf("%s: %d items, fewer than %v", at, len(x), m))
		}
		if items, ok := schema["items"]; ok {
			for i, item := range x {
				problems = append(problems, c.validate(items, item, fmt.Sprintf("%s[%d]", at, i))...)
			}
		}
	case map[string]any:
		if m, ok := schema["maxProperties"].(float64); ok && float64(len(x)) > m {
			problems = append(problems, fmt.Sprintf("%s: %d properties, more than %v", at, len(x), m))
		}
		props, _ := schema["properties"].(map[string]any)
		required, _ := schema["required"].([]any)
		for _, r := range required {
			if _, ok := x[r.(string)]; !ok {
				problems = append(problems, fmt.Sprintf("%s: %s is missing", at, r))
			}
		}
		extra, hasExtra := schema["additionalProperties"]
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if ps, ok := props[k]; ok {
				problems = append(problems, c.validate(ps, x[k], at+"."+k)...)
			} else if hasExtra {
				problems = append(problems, c.validate(extra, x[k], at+"."+k)...)
			} else if props != nil {
				problems = append(problems, fmt.Sprintf("%s: %s is not in the contract", at, k))
			}
		}
	}
	return problems
}

func hasType(t any, v any) bool {
	if list, ok := t.([]any); ok {
		for _, one := range list {
			if hasType(one, v) {
				return true
			}
		}
		return false
	}
	got := jsonType(v)
	return got == t || (t == "number" && got == "integer")
}

func jsonType(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case float64:
		if x == float64(int64(x)) {
			return "integer"
		}
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return fmt.Sprintf("%T", v)
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func TestContractIsTheAppserviceAPIVersion(t *testing.T) {
	v := appserviceContract(t).version()
	if major, _, _ := strings.Cut(v, "."); major != strconv.Itoa(appserviceAPIVersion) {
		t.Fatalf("testdata/openapi.json is %s; the client speaks API v%d", v, appserviceAPIVersion)
	}
}

func TestContractValidator(t *testing.T) {
	c := appserviceContract(t)
	cases := []struct {
		name, method, target, body string
		want                       string
	}{
		{"unknown route", "GET", "/nothere", "", "no such route"},
		{"unknown query", "GET", "/channels?nope=1", "", "query nope is not a parameter"},
		{"bad boolean", "GET", "/channels?unread=yes", "", `"yes" is not true or false`},
		{"limit too big", "GET", "/channels/%21r%3Ax/events?limit=500", "", "500 is above 200"},
		{"room pattern", "GET", "/channels/r%3Ax/events", "", "does not match ^!"},
		{"unknown field", "POST", "/channels/%21r%3Ax/events", `{"type":"text","content":{"text":"hi","colour":"red"}}`, "content.colour: not allowed"},
		{"wrong enum", "POST", "/channels/%21r%3Ax/events", `{"type":"video"}`, "is not one of"},
		{"discriminator", "POST", "/channels/%21r%3Ax/events", `{"type":"text","content":{"transcript":"hi"}}`, "content: text is missing"},
		{"body where none", "GET", "/channels", `{}`, "takes none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := http.NewRequest(tc.method, "http://appservice"+tc.target, strings.NewReader(tc.body))
			if tc.body != "" {
				r.Header.Set("Content-Type", "application/json")
			}
			_, _, problems := c.checkRequest(r, []byte(tc.body))
			if !strings.Contains(strings.Join(problems, "\n"), tc.want) {
				t.Fatalf("want %q among %q", tc.want, problems)
			}
		})
	}
	_, op, _ := c.route("GET", "/channels")
	for _, tc := range []struct {
		status int
		ctype  string
		body   string
		want   string
	}{
		{200, "application/json", `{"channels":[],"next_cursor":null,"extra":1}`, "extra is not in the contract"},
		{200, "application/json", `{"next_cursor":null}`, "channels is missing"},
		{418, "application/json", `{}`, "does not list"},
		{401, "application/json", `{}`, `not application/problem+json`},
	} {
		h := http.Header{"Content-Type": {tc.ctype}, "X-Api-Version": {strconv.Itoa(appserviceAPIVersion)}}
		problems := c.checkResponse(op, tc.status, h, []byte(tc.body))
		if !strings.Contains(strings.Join(problems, "\n"), tc.want) {
			t.Errorf("%d %s: want %q among %q", tc.status, tc.body, tc.want, problems)
		}
	}
}

func contractAppservice(t *testing.T, h http.Handler) *httptest.Server {
	return httptest.NewServer(appserviceContract(t).checked(likeTheAppservice(h), func(v string) { t.Errorf("off the appservice's contract: %s", v) }))
}

// What the appservice puts on every answer, so each fake need not: its major, and a Location
// for what it creates.
func likeTheAppservice(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-API-Version", strconv.Itoa(appserviceAPIVersion))
		h.ServeHTTP(&locating{ResponseWriter: w, path: r.URL.EscapedPath()}, r)
	})
}

type locating struct {
	http.ResponseWriter
	path string
}

func (l *locating) WriteHeader(status int) {
	if status == http.StatusCreated && l.Header().Get("Location") == "" {
		l.Header().Set("Location", l.path+"/new")
	}
	l.ResponseWriter.WriteHeader(status)
}

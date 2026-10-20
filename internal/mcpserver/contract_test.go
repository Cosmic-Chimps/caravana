package mcpserver

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// loadContract reads api/openapi.yaml, the contract the API's clients are
// generated from, as internal/api's contract test does.
func loadContract(t *testing.T) *openapi3.T {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("load api/openapi.yaml: %v", err)
	}
	return doc
}

// TestOperationsAreInTheContract: each operation this package calls is in the
// contract, with that ID, method and path.
func TestOperationsAreInTheContract(t *testing.T) {
	doc := loadContract(t)
	for _, op := range allOperations {
		item := doc.Paths.Value(op.Path)
		if item == nil {
			t.Errorf("%s: path %s is not in api/openapi.yaml", op.ID, op.Path)
			continue
		}
		got := item.GetOperation(op.Method)
		if got == nil {
			t.Errorf("%s: %s %s is not in api/openapi.yaml", op.ID, op.Method, op.Path)
			continue
		}
		if got.OperationID != op.ID {
			t.Errorf("%s %s is operation %q in the contract, not %q", op.Method, op.Path, got.OperationID, op.ID)
		}
	}
}

// pathMatcher finds the contract's operation for a concrete request path by
// turning each template into a pattern: /v1/apps/{id} becomes /v1/apps/[^/]+.
type pathMatcher struct {
	doc       *openapi3.T
	templates map[string]*regexp.Regexp
}

func newPathMatcher(doc *openapi3.T) *pathMatcher {
	m := &pathMatcher{doc: doc, templates: map[string]*regexp.Regexp{}}
	placeholder := regexp.MustCompile(`\\\{[^}]+\\\}`)
	for template := range doc.Paths.Map() {
		pattern := placeholder.ReplaceAllString(regexp.QuoteMeta(template), `[^/]+`)
		m.templates[template] = regexp.MustCompile("^" + pattern + "$")
	}
	return m
}

// find returns the template and the operation for a method and a path, or "" and nil.
func (m *pathMatcher) find(method, path string) (string, *openapi3.Operation) {
	for template, re := range m.templates {
		if !re.MatchString(path) {
			continue
		}
		if op := m.doc.Paths.Value(template).GetOperation(method); op != nil {
			return template, op
		}
	}
	return "", nil
}

// declaredParameter says whether the operation (or its path) declares a
// parameter, by place and name.
func declaredParameter(doc *openapi3.T, template string, op *openapi3.Operation, in, name string) bool {
	params := slices.Concat(doc.Paths.Value(template).Parameters, op.Parameters)
	return slices.ContainsFunc(params, func(p *openapi3.ParameterRef) bool {
		return p.Value != nil && p.Value.In == in && strings.EqualFold(p.Value.Name, name)
	})
}

// TestToolRequestsAreInTheContract runs every tool and checks each request it
// makes against api/openapi.yaml: the operation exists, every query parameter
// and header it sends is declared, and a body is one the operation takes and
// matches its schema.
func TestToolRequestsAreInTheContract(t *testing.T) {
	doc := loadContract(t)
	matcher := newPathMatcher(doc)

	for _, c := range slices.Concat(readCases, actionCases) {
		t.Run(c.tool+"/"+firstArgs(c.args), func(t *testing.T) {
			f := newFakeAPI(t)
			c.serve(f)
			cs := f.session(t, true, nil, fastTuning, nil)
			mustCall(t, cs, c.tool, c.args)

			reqs := f.requests()
			if len(reqs) == 0 {
				t.Fatal("the tool made no request")
			}
			for _, r := range reqs {
				template, op := matcher.find(r.Method, r.Path)
				if op == nil {
					t.Errorf("%s %s is not an operation of api/openapi.yaml", r.Method, r.Path)
					continue
				}
				for _, pair := range strings.Split(r.Query, "&") {
					if name, _, _ := strings.Cut(pair, "="); name != "" && !declaredParameter(doc, template, op, "query", name) {
						t.Errorf("%s sends query parameter %q, which the operation doesn't declare", op.OperationID, name)
					}
				}
				if r.Header.Get("If-Match") != "" && !declaredParameter(doc, template, op, "header", "If-Match") {
					t.Errorf("%s sends If-Match, which the operation doesn't declare", op.OperationID)
				}
				if r.Body != "" {
					checkBodyAgainstContract(t, op, r.Body)
				}
			}
		})
	}
}

func checkBodyAgainstContract(t *testing.T, op *openapi3.Operation, body string) {
	t.Helper()
	if op.RequestBody == nil || op.RequestBody.Value == nil {
		t.Errorf("%s takes no body, and one was sent: %s", op.OperationID, body)
		return
	}
	media := op.RequestBody.Value.Content.Get("application/json")
	if media == nil || media.Schema == nil || media.Schema.Value == nil {
		t.Errorf("%s has no JSON body schema", op.OperationID)
		return
	}
	var v any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Errorf("%s: the body is not JSON: %v", op.OperationID, err)
		return
	}
	if err := media.Schema.Value.VisitJSON(v); err != nil {
		t.Errorf("%s: the body doesn't match the contract's schema: %v\n%s", op.OperationID, err, body)
	}
}

// TestContractCheckIsNotVacuous: the schema check above refuses a body that
// isn't an App, so passing it means something.
func TestContractCheckIsNotVacuous(t *testing.T) {
	doc := loadContract(t)
	op := doc.Paths.Value(opDeployApp.Path).GetOperation(opDeployApp.Method)
	schema := op.RequestBody.Value.Content.Get("application/json").Schema.Value
	if err := schema.VisitJSON(map[string]any{"id": "web", "revision": "0123456789abcdef"}); err == nil {
		t.Error("an app with the record's fields and no services was accepted")
	}
}

// TestDeployBodyMatchesTheContract: the app deploy_image sends (a whole stored
// app, with fields the case table doesn't have) is an App of the contract.
func TestDeployBodyMatchesTheContract(t *testing.T) {
	doc := loadContract(t)
	op := doc.Paths.Value(opDeployApp.Path).GetOperation(opDeployApp.Method)

	f := newFakeAPI(t)
	f.reply("GET /v1/apps/web", 200, `{"app":{"id":"web","source":{"kind":"image"},"services":[`+
		`{"name":"web","image":"ghcr.io/o/web:1","ports":[{"name":"http","containerPort":8080}],`+
		`"domains":[{"host":"web.example.com","port":"http"}],"health":{"path":"/up","interval":"10s","timeout":"2s"}}]},"revision":"0123456789abcdef"}`)
	f.reply("POST /v1/apps/web/deployments", 202, deploymentJSON)
	cs := f.session(t, true, nil, fastTuning, nil)
	mustCall(t, cs, "deploy_image", map[string]any{"app": "web", "image": "ghcr.io/o/web:2"})

	reqs := f.requests()
	checkBodyAgainstContract(t, op, reqs[len(reqs)-1].Body)
}

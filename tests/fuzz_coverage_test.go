package tests

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestFuzzCoverage ensures every mux.Handle route in handler packages has a
// corresponding Fuzz* function in a *_fuzz_test.go file. This test reads the
// source directly via go/ast — no extra slices or maps to keep in sync.
//
// When it fails, the fix is: write a Fuzz* test for the new endpoint.
func TestFuzzCoverage(t *testing.T) {
	cases := []struct {
		name       string
		handlerDir string
		routeFile  string
		fuzzGlob   string
	}{
		{
			name:       "wqapi",
			handlerDir: "../internal/wqapi",
			routeFile:  "handler.go",
			fuzzGlob:   "*_fuzz_test.go",
		},
		{
			name:       "admin",
			handlerDir: "../internal/admin",
			routeFile:  "handler.go",
			fuzzGlob:   "*_fuzz_test.go",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			routes := extractRoutes(t, filepath.Join(tc.handlerDir, tc.routeFile))
			fuzzTargets := extractFuzzTargets(t, tc.handlerDir, tc.fuzzGlob)

			if len(routes) == 0 {
				t.Fatal("found no mux.Handle routes — is the routeFile path correct?")
			}

			for _, route := range routes {
				found := false
				for _, target := range fuzzTargets {
					if routeMatchesFuzz(route, target) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("route %q has no matching Fuzz* target — add one to a *_fuzz_test.go file", route)
				}
			}
		})
	}
}

// extractRoutes parses a Go source file and returns all string literal first
// arguments to mux.Handle/mux.HandleFunc calls (e.g., "POST /wq/enqueue").
func extractRoutes(t *testing.T, filename string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}

	var routes []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc" {
			return true
		}
		if len(call.Args) < 1 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		// Strip quotes.
		route := strings.Trim(lit.Value, `"`)
		routes = append(routes, route)
		return true
	})
	return routes
}

// extractFuzzTargets finds all Fuzz* function names in files matching glob
// within the given directory.
func extractFuzzTargets(t *testing.T, dir, glob string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, glob))
	if err != nil {
		t.Fatalf("glob %s/%s: %v", dir, glob, err)
	}

	fset := token.NewFileSet()
	var targets []string
	for _, match := range matches {
		f, err := parser.ParseFile(fset, match, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", match, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if strings.HasPrefix(fn.Name.Name, "Fuzz") {
				targets = append(targets, fn.Name.Name)
			}
		}
	}
	return targets
}

// routeMatchesFuzz checks whether a fuzz target name plausibly covers a route.
// It extracts the path from the route pattern (strips "POST ", "GET ", etc.),
// converts it to a comparable form, and checks if the fuzz target name contains
// the key path segments.
//
// Examples:
//
//	"POST /wq/enqueue"                          → matches FuzzWqapiEnqueue
//	"GET /admin/queues/{name}/items"             → matches FuzzAdminListItems
//	"POST /admin/queues/{name}/items/{key}/retry" → matches FuzzAdminRetryItem
func routeMatchesFuzz(route, fuzzName string) bool {
	// Normalize: strip method, remove path params, split into segments.
	parts := strings.SplitN(route, " ", 2)
	path := route
	if len(parts) == 2 {
		path = parts[1]
	}

	// Remove path parameter placeholders.
	cleaned := strings.NewReplacer(
		"{name}", "", "{key}", "", "{id}", "",
	).Replace(path)

	// Build a set of meaningful path segments.
	var segments []string
	for _, seg := range strings.Split(cleaned, "/") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		segments = append(segments, strings.ToLower(seg))
	}

	lower := strings.ToLower(fuzzName)

	// The fuzz target must contain the last meaningful path segment.
	// e.g., "/wq/enqueue" → "enqueue", "/admin/queues/{name}/items" → "items"
	if len(segments) == 0 {
		return false
	}

	// Check last 1-2 segments to handle compound names like "dead-letters" → "deadletters".
	for i := max(0, len(segments)-2); i < len(segments); i++ {
		seg := strings.ReplaceAll(segments[i], "-", "")
		if strings.Contains(lower, seg) {
			return true
		}
	}

	return false
}

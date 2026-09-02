// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"
)

// Guards on the HTTP layer's shape.
//
// HTTPGateway is the gateway's CONFIGURATION surface: cmd/dzd builds one and
// assigns its fields, and mountRoutes then builds a per-domain handler from
// them. Route handlers belong on those domain types, each declaring the few
// dependencies it actually touches, rather than on the gateway itself, where
// every handler can reach all of its fields.
//
// Both tests below are structural. They read this package's own source, in the
// manner of route_sweep_test.go and gdpr_coverage_test.go, because what they
// check is a property of the code's shape that no runtime assertion can see.

// maxGatewayHandlers is a RATCHET, not a budget. It is the number of route
// handlers still hanging off HTTPGateway, and it may only ever go DOWN: a new
// handler belongs on a domain type (see billingAPI, supportAPI, runnerAPI, …),
// and moving an existing one off the gateway should lower this number.
//
// If this fails because you ADDED a handler, put it on a domain type instead of
// the gateway. If it fails because you MOVED one off, lower the constant.
const maxGatewayHandlers = 2

// TestLayering_GatewayHandlerRatchet keeps route handlers migrating off the
// gateway's god object rather than accumulating on it.
func TestLayering_GatewayHandlerRatchet(t *testing.T) {
	byRecv, _ := handlersByReceiver(t)
	got := byRecv["HTTPGateway"]
	if got > maxGatewayHandlers {
		t.Errorf("HTTPGateway declares %d route handlers, want at most %d.\n"+
			"A new handler belongs on a domain type that names its own dependencies, "+
			"not on the gateway (which exposes all of them).", got, maxGatewayHandlers)
	}
	if got < maxGatewayHandlers {
		t.Errorf("HTTPGateway declares %d route handlers, below the ratchet of %d — "+
			"lower maxGatewayHandlers to %d to lock the improvement in.",
			got, maxGatewayHandlers, got)
	}
}

// TestLayering_DomainHandlersStayNarrow rejects a domain handler type that
// embeds *HTTPGateway. Embedding it would promote all of the gateway's fields
// and methods back onto the domain type, which silently undoes the point of
// splitting handlers out: the struct would no longer say what the handler
// touches. A domain type takes the stores and helpers it needs (auditor,
// urlBuilder, adminCheck, a store, a config value) as its own fields.
func TestLayering_DomainHandlersStayNarrow(t *testing.T) {
	_, files := handlersByReceiver(t)
	fset := token.NewFileSet()
	var bad []string
	for _, name := range files {
		af, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok || ts.Name.Name == "HTTPGateway" {
				return true
			}
			for _, f := range st.Fields.List {
				if len(f.Names) > 0 {
					continue // named field, not an embed
				}
				if embedsGateway(f.Type) {
					bad = append(bad, ts.Name.Name+" (in "+name+")")
				}
			}
			return true
		})
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		t.Errorf("these types embed HTTPGateway, which re-exposes every gateway "+
			"field to their handlers: %s\nDeclare the few dependencies the handlers "+
			"actually use as fields instead.", strings.Join(bad, ", "))
	}
}

// TestLayering_CoreDoesNotDependOnTheGateway asserts the dependency direction
// that makes the HTTP layer separable at all: HTTP files reach into the service
// core, and the core never reaches back. Nothing in the language enforces it
// inside one package, so it is asserted here.
//
// A "core" file is one that declares no HTTP handler and no HTTPGateway method.
// Such a file must not mention HTTPGateway in CODE. Comments may (and do)
// reference it, which is why this walks the AST rather than the text.
func TestLayering_CoreDoesNotDependOnTheGateway(t *testing.T) {
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	var offenders []string
	for _, e := range ents {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		isHTTP := false
		for _, d := range af.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if takesResponseWriter(fd) || receiverName(fd) == "HTTPGateway" {
				isHTTP = true
				break
			}
		}
		if isHTTP {
			continue
		}
		ast.Inspect(af, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if ok && id.Name == "HTTPGateway" {
				offenders = append(offenders, fset.Position(id.Pos()).String())
			}
			return true
		})
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Errorf("service-core files reference HTTPGateway in code: %s\n"+
			"The core must not depend on the HTTP layer — pass the value the core "+
			"needs, or move the code into the HTTP layer.", strings.Join(offenders, ", "))
	}
}

// TestLayering_NoSelfRecursiveForwarder catches a forwarder that calls itself.
//
// Moving a method onto a narrow type leaves a one-line forwarder behind, and if
// the receiver is rewritten without the body, the forwarder becomes
// `func (h *T) f() { return h.f() }`. An embedded type's method of the same
// name is then shadowed by the outer one, so it compiles, vet says nothing, and
// the first request through it overflows the stack. That happened once during
// this refactor; this is the cheap check that it does not happen again.
func TestLayering_NoSelfRecursiveForwarder(t *testing.T) {
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	var bad []string
	for _, e := range ents {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, d := range af.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || fd.Body == nil || len(fd.Body.List) != 1 {
				continue
			}
			call := soleCall(fd.Body.List[0])
			if call == nil {
				continue
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != fd.Name.Name {
				continue
			}
			// Recursive only when the call is on the receiver itself; a call on
			// an embedded value (h.urls().foo) goes through a different type.
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == receiverIdent(fd) {
				bad = append(bad, receiverName(fd)+"."+fd.Name.Name+" ("+name+")")
			}
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		t.Errorf("these one-line methods call themselves and will overflow the "+
			"stack: %s\nA forwarder must call the moved method on the narrow type "+
			"(h.urls().x, h.auditor().x, …), or be deleted when the type is embedded.",
			strings.Join(bad, ", "))
	}
}

// soleCall returns the call in a body of exactly one return-or-expression
// statement, or nil.
func soleCall(st ast.Stmt) *ast.CallExpr {
	var e ast.Expr
	switch s := st.(type) {
	case *ast.ReturnStmt:
		if len(s.Results) != 1 {
			return nil
		}
		e = s.Results[0]
	case *ast.ExprStmt:
		e = s.X
	default:
		return nil
	}
	call, _ := e.(*ast.CallExpr)
	return call
}

func receiverIdent(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 || len(fd.Recv.List[0].Names) == 0 {
		return ""
	}
	return fd.Recv.List[0].Names[0].Name
}

func embedsGateway(e ast.Expr) bool {
	if s, ok := e.(*ast.StarExpr); ok {
		e = s.X
	}
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "HTTPGateway"
}

// handlersByReceiver counts, per receiver type, the funcs in this package that
// take an http.ResponseWriter — route handlers and the middleware around them.
// It also returns the names of the source files scanned. `go test` runs in the
// package directory, so those names need no prefix.
func handlersByReceiver(t *testing.T) (map[string]int, []string) {
	t.Helper()
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	counts := map[string]int{}
	var files []string
	for _, e := range ents {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, name)
		for _, d := range af.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || !takesResponseWriter(fd) {
				continue
			}
			counts[receiverName(fd)]++
		}
	}
	if counts["HTTPGateway"] == 0 {
		t.Fatal("found no HTTPGateway handlers — the scan is broken, not the code")
	}
	return counts, files
}

func takesResponseWriter(fd *ast.FuncDecl) bool {
	if fd.Type.Params == nil {
		return false
	}
	for _, p := range fd.Type.Params.List {
		sel, ok := p.Type.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		if x, ok := sel.X.(*ast.Ident); ok && x.Name == "http" && sel.Sel.Name == "ResponseWriter" {
			return true
		}
	}
	return false
}

func receiverName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return "" // plain function
	}
	e := fd.Recv.List[0].Type
	if s, ok := e.(*ast.StarExpr); ok {
		e = s.X
	}
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

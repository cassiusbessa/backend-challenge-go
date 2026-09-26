package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func main() {
	root := flag.String("root", ".", "repository root")
	cover := flag.String("cover", "", "cover profile from go test")
	flag.Parse()

	violations := scanTests(*root)
	if *cover != "" {
		violations = append(violations, scanCover(*root, *cover)...)
	}
	for _, v := range violations {
		fmt.Fprintln(os.Stderr, v)
	}
	if len(violations) > 0 {
		os.Exit(1)
	}
}

func scanTests(root string) []string {
	var out []string
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if d != nil && (d.Name() == ".git" || d.Name() == "vendor" || d.Name() == "testgates") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			out = append(out, fmt.Sprintf("%s: parse: %v", path, err))
			return nil
		}
		kind := testKind(file)
		pkgVars := packageVars(file)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if strings.HasPrefix(fn.Name.Name, "Test") && fn.Name.Name != "TestMain" {
				out = append(out, checkTestFunc(fset, path, kind, fn, pkgVars)...)
			}
		}
		if kind == unitKind {
			out = append(out, checkUnitFile(fset, path, file)...)
		}
		return nil
	})
	// A walk that stopped early has not seen every test file, and an incomplete
	// scan must not read as a clean one.
	if walkErr != nil {
		out = append(out, fmt.Sprintf("%s: walk: %v", root, walkErr))
	}
	return out
}

type kind int

const (
	unitKind kind = iota
	integrationKind
)

func testKind(file *ast.File) kind {
	for _, group := range file.Comments {
		for _, c := range group.List {
			text := c.Text
			if strings.Contains(text, "go:build integration") || strings.Contains(text, "go:build e2e") || strings.Contains(text, "+build integration") || strings.Contains(text, "+build e2e") {
				return integrationKind
			}
		}
	}
	return unitKind
}

func packageVars(file *ast.File) map[string]bool {
	names := map[string]bool{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			for _, name := range vs.Names {
				names[name.Name] = true
			}
		}
	}
	return names
}

func checkTestFunc(fset *token.FileSet, path string, k kind, fn *ast.FuncDecl, pkgVars map[string]bool) []string {
	var out []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.CallExpr:
			if k == integrationKind && isParallel(node) {
				out = append(out, fmt.Sprintf("%s:%d: integration test uses t.Parallel", path, fset.Position(node.Pos()).Line))
			}
		case *ast.AssignStmt:
			for _, lhs := range node.Lhs {
				id, ok := lhs.(*ast.Ident)
				if ok && pkgVars[id.Name] {
					out = append(out, fmt.Sprintf("%s:%d: test writes package variable %s", path, fset.Position(id.Pos()).Line, id.Name))
				}
			}
		}
		return true
	})
	if k == unitKind {
		count := 0
		for _, stmt := range fn.Body.List {
			if expr, ok := stmt.(*ast.ExprStmt); ok && isParallel(expr.X) {
				count++
			}
		}
		if count > 1 {
			out = append(out, fmt.Sprintf("%s:%d: t.Parallel more than once in %s", path, fset.Position(fn.Pos()).Line, fn.Name.Name))
		}
	}
	return out
}

func checkUnitFile(fset *token.FileSet, path string, file *ast.File) []string {
	var out []string
	seeded := false
	usesRand := false
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		name := sel.Sel.Name
		pkg, _ := sel.X.(*ast.Ident)
		if pkg == nil {
			return true
		}
		pos := fset.Position(sel.Pos())
		switch {
		case pkg.Name == "time" && (name == "Now" || name == "Since" || name == "Sleep"):
			out = append(out, fmt.Sprintf("%s:%d: unit test uses time.%s", path, pos.Line, name))
		case (pkg.Name == "net" && (name == "Dial" || name == "DialTimeout")) || (pkg.Name == "sql" && name == "Open"):
			out = append(out, fmt.Sprintf("%s:%d: unit test opens %s.%s", path, pos.Line, pkg.Name, name))
		case pkg.Name == "rand" && name == "NewSource":
			seeded = true
		case pkg.Name == "rand":
			usesRand = true
		}
		return true
	})
	if usesRand && !seeded {
		out = append(out, fmt.Sprintf("%s: unit test uses math/rand without a seed", path))
	}
	return out
}

func isParallel(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Parallel"
}

func scanCover(root, profile string) []string {
	data, err := os.ReadFile(profile) //nolint:gosec // the path comes from a flag the operator controls
	if err != nil {
		return []string{fmt.Sprintf("cover: %v", err)}
	}
	byDir := map[string]*acc{}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		stmts, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		count, err := strconv.Atoi(fields[2])
		if err != nil {
			continue
		}
		file := fields[0]
		if i := strings.LastIndex(file, "/"); i >= 0 {
			dir := file[:i]
			if j := strings.Index(dir, "internal/"); j >= 0 {
				dir = dir[j:]
			}
			bucket := byDir[dir]
			if bucket == nil {
				bucket = &acc{}
				byDir[dir] = bucket
			}
			bucket.total += stmts
			if count > 0 {
				bucket.covered += stmts
			}
		}
	}
	var out []string
	checks := []struct {
		dir  string
		need float64
	}{
		{"internal/domain/identity", 0.90},
		{"internal/domain/money", 0.90},
		{"internal/domain/wallet", 0.90},
		{"internal/domain/wager", 0.90},
		{"internal/domain/ledger", 0.90},
		{"internal/app", 0.80},
		{"internal/platform", 0.70},
	}
	for _, check := range checks {
		full := filepath.Join(root, check.dir)
		info, err := os.Stat(full)
		if err != nil || !info.IsDir() {
			continue
		}
		production, err := hasProductionGo(full)
		if err != nil {
			out = append(out, fmt.Sprintf("%s: walk: %v", check.dir, err))
			continue
		}
		if !production {
			continue
		}
		out = append(out, checkFloor(check.dir, check.need, subtreeOf(byDir, check.dir))...)
	}
	return out
}

type acc struct{ covered, total int }

func subtreeOf(byDir map[string]*acc, dir string) acc {
	var total acc
	for name, bucket := range byDir {
		if name != dir && !strings.HasPrefix(name, dir+"/") {
			continue
		}
		total.covered += bucket.covered
		total.total += bucket.total
	}
	return total
}

func checkFloor(dir string, need float64, measured acc) []string {
	if measured.total == 0 {
		return []string{fmt.Sprintf("%s: package has no measured coverage", dir)}
	}
	ratio := float64(measured.covered) / float64(measured.total)
	if ratio+1e-9 >= need {
		return nil
	}
	return []string{fmt.Sprintf("%s: coverage %.1f%%, minimum %.0f%%", dir, ratio*100, need*100)}
}

func hasProductionGo(dir string) (bool, error) {
	found := false
	err := filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found, err
}

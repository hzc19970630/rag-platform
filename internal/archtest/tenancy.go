// Package archtest also hosts the static tenancy gate. The gate scans raw SQL
// passed to Raw/Exec/Query/QueryRow and fails when a tenant-owned table is
// referenced without a tenant predicate. GORM chains are covered by code review
// and the tenantdb helpers; this check exists because raw SQL bypasses them.
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// TenantTables own a tenant_id column. Keep this list in sync with the models
// and the RLS migration; the count is asserted by tenancy_test.go.
var TenantTables = []string{
	"answer_jobs",
	"api_keys",
	"audit_logs",
	"auth_sessions",
	"authorization_revisions",
	"chunks",
	"connector_items",
	"connectors",
	"conversations",
	"document_versions",
	"documents",
	"evidence_packages",
	"graph_entities",
	"graph_entity_aliases",
	"graph_relation_sources",
	"graph_relations",
	"graph_snapshots",
	"knowledge_bases",
	"knowledge_index_records",
	"knowledge_projection_states",
	"memberships",
	"message_citations",
	"messages",
	"outbox_events",
	"pipeline_runs",
	"projection_generation_cache",
	"projection_runs",
	"stream_events",
	"sync_runs",
	"task_dead_letters",
	"wiki_page_sources",
	"wiki_page_versions",
	"wiki_pages",
}

// tenantChildTables have no tenant_id and are scoped through their parent.
// A raw statement touching one of them must reference the parent table in the
// same statement (an EXISTS or JOIN), unless the statement carries an explicit
// tenant_id (task_inbox gains one in the RLS migration).
var tenantChildTables = map[string][]string{
	"stage_runs":             {"pipeline_runs"},
	"projection_stage_runs":  {"projection_runs"},
	"knowledge_base_members": {"knowledge_bases"},
	"task_inbox":             {"pipeline_runs", "projection_runs"},
}

// TenancyFile is one Go source file handed to CheckTenancy. Path is
// repo-relative and uses forward slashes so allowlist entries are portable.
type TenancyFile struct {
	Path   string
	Source []byte
}

// TenancyViolation is one raw statement that can cross tenant boundaries.
type TenancyViolation struct {
	File   string
	Line   int
	Method string
	Tables []string
}

func (v TenancyViolation) String() string {
	return fmt.Sprintf("%s:%d: %s() on %s: add tenant_id or an allowlist entry with a reason",
		v.File, v.Line, v.Method, strings.Join(v.Tables, ", "))
}

// CheckTenancy scans the supplied files. Test files are ignored: synthetic
// fixtures and acceptance helpers are allowed to build deliberately unsafe SQL.
func CheckTenancy(files []TenancyFile) ([]TenancyViolation, error) {
	groups := map[string][]TenancyFile{}
	for _, file := range files {
		normalized := path.Clean(strings.ReplaceAll(file.Path, "\\", "/"))
		if strings.HasSuffix(normalized, "_test.go") {
			continue
		}
		groups[path.Dir(normalized)] = append(groups[path.Dir(normalized)], TenancyFile{Path: normalized, Source: file.Source})
	}
	directories := make([]string, 0, len(groups))
	for directory := range groups {
		directories = append(directories, directory)
	}
	sort.Strings(directories)

	var violations []TenancyViolation
	for _, directory := range directories {
		pkg, err := parsePackage(groups[directory])
		if err != nil {
			return nil, err
		}
		violations = append(violations, pkg.check()...)
	}
	return violations, nil
}

// LoadTenancyFiles reads every non-test Go file below the given directories.
// Paths in the result are relative to root and slash-separated, matching the
// tenancy allowlist keys.
func LoadTenancyFiles(root string, directories ...string) ([]TenancyFile, error) {
	var files []TenancyFile
	for _, directory := range directories {
		base := filepath.Join(root, filepath.FromSlash(directory))
		err := filepath.WalkDir(base, func(current string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				switch entry.Name() {
				case "vendor", "testdata", "node_modules":
					return filepath.SkipDir
				}
				if current != base && strings.HasPrefix(entry.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				return nil
			}
			relative, err := filepath.Rel(root, current)
			if err != nil {
				return err
			}
			source, err := os.ReadFile(current)
			if err != nil {
				return err
			}
			files = append(files, TenancyFile{Path: filepath.ToSlash(relative), Source: source})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk %s: %w", base, err)
		}
	}
	return files, nil
}

type fileSource struct {
	path string
	file *ast.File
}

type callSite struct {
	args []string
}

type rawStatement struct {
	file   string
	line   int
	method string
	sql    string
}

type packageInfo struct {
	fset       *token.FileSet
	files      []*fileSource
	consts     map[string]string
	funcs      map[string]*ast.FuncDecl
	callSites  map[string][]callSite
	statements []rawStatement
	collecting bool
}

func parsePackage(files []TenancyFile) (*packageInfo, error) {
	fset := token.NewFileSet()
	pkg := &packageInfo{
		fset:      fset,
		consts:    map[string]string{},
		funcs:     map[string]*ast.FuncDecl{},
		callSites: map[string][]callSite{},
	}
	for _, source := range files {
		file, err := parser.ParseFile(fset, source.Path, source.Source, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", source.Path, err)
		}
		pkg.files = append(pkg.files, &fileSource{path: source.Path, file: file})
	}
	pkg.consts = collectPackageStrings(pkg.files)
	for _, source := range pkg.files {
		for _, declaration := range source.file.Decls {
			if function, ok := declaration.(*ast.FuncDecl); ok && function.Body != nil {
				pkg.funcs[function.Name.Name] = function
			}
		}
	}

	pkg.collecting = true
	for _, source := range pkg.files {
		for _, declaration := range source.file.Decls {
			if function, ok := declaration.(*ast.FuncDecl); ok && function.Body != nil {
				newWalker(pkg, function.Name.Name).run(function)
			}
		}
	}
	pkg.collecting = false
	for _, source := range pkg.files {
		for _, declaration := range source.file.Decls {
			if function, ok := declaration.(*ast.FuncDecl); ok && function.Body != nil {
				newWalker(pkg, function.Name.Name).run(function)
			}
		}
	}
	return pkg, nil
}

// collectPackageStrings resolves package-level string constants and variables,
// including concatenations such as neighbourChunksQuery. Unresolvable parts
// contribute an empty string; the statement is still checked on what is known.
func collectPackageStrings(files []*fileSource) map[string]string {
	expressions := map[string]ast.Expr{}
	for _, source := range files {
		for _, declaration := range source.file.Decls {
			gen, ok := declaration.(*ast.GenDecl)
			if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
				continue
			}
			for _, spec := range gen.Specs {
				values, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for index, name := range values.Names {
					switch {
					case index < len(values.Values):
						expressions[name.Name] = values.Values[index]
					case len(values.Values) == 1:
						expressions[name.Name] = values.Values[0]
					}
				}
			}
		}
	}
	resolved := map[string]string{}
	for pass := 0; pass < 8; pass++ {
		changed := false
		for name, expression := range expressions {
			value := evalStaticString(expression, resolved)
			if resolved[name] != value {
				resolved[name] = value
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return resolved
}

func evalStaticString(expression ast.Expr, resolved map[string]string) string {
	switch value := expression.(type) {
	case *ast.BasicLit:
		if value.Kind == token.STRING {
			unquoted, err := strconv.Unquote(value.Value)
			if err == nil {
				return unquoted
			}
		}
	case *ast.Ident:
		return resolved[value.Name]
	case *ast.ParenExpr:
		return evalStaticString(value.X, resolved)
	case *ast.BinaryExpr:
		if value.Op == token.ADD {
			return evalStaticString(value.X, resolved) + evalStaticString(value.Y, resolved)
		}
	case *ast.CallExpr:
		if isFmtSprintf(value) && len(value.Args) > 0 {
			return evalStaticString(value.Args[0], resolved)
		}
		if isStringConversion(value) {
			return evalStaticString(value.Args[0], resolved)
		}
	}
	return ""
}

func isFmtSprintf(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Sprintf" {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	return ok && pkg.Name == "fmt"
}

func isStringConversion(call *ast.CallExpr) bool {
	function, ok := call.Fun.(*ast.Ident)
	return ok && function.Name == "string" && len(call.Args) == 1
}

type walker struct {
	pkg    *packageInfo
	fnName string
	params map[string]int
	locals map[string]string
}

func newWalker(pkg *packageInfo, fnName string) *walker {
	return &walker{pkg: pkg, fnName: fnName, params: map[string]int{}, locals: map[string]string{}}
}

func (w *walker) run(function *ast.FuncDecl) {
	index := 0
	if function.Type.Params != nil {
		for _, field := range function.Type.Params.List {
			if len(field.Names) == 0 {
				index++
				continue
			}
			for _, name := range field.Names {
				w.params[name.Name] = index
				index++
			}
		}
	}
	w.statement(function.Body)
}

func (w *walker) statement(statement ast.Stmt) {
	if statement == nil {
		return
	}
	switch node := statement.(type) {
	case *ast.AssignStmt:
		for _, expression := range node.Rhs {
			w.expression(expression)
		}
		w.assign(node)
	case *ast.DeclStmt:
		w.declStatement(node)
	case *ast.ExprStmt:
		w.expression(node.X)
	case *ast.ReturnStmt:
		for _, expression := range node.Results {
			w.expression(expression)
		}
	case *ast.BlockStmt:
		for _, child := range node.List {
			w.statement(child)
		}
	case *ast.IfStmt:
		w.statement(node.Init)
		w.expression(node.Cond)
		w.statement(node.Body)
		w.statement(node.Else)
	case *ast.ForStmt:
		w.statement(node.Init)
		w.expression(node.Cond)
		w.statement(node.Body)
		w.statement(node.Post)
	case *ast.RangeStmt:
		w.expression(node.X)
		w.statement(node.Body)
	case *ast.SwitchStmt:
		w.statement(node.Init)
		w.expression(node.Tag)
		w.clauses(node.Body)
	case *ast.TypeSwitchStmt:
		w.statement(node.Init)
		w.statement(node.Assign)
		w.clauses(node.Body)
	case *ast.SelectStmt:
		w.clauses(node.Body)
	case *ast.LabeledStmt:
		w.statement(node.Stmt)
	case *ast.GoStmt:
		w.expression(node.Call)
	case *ast.DeferStmt:
		w.expression(node.Call)
	case *ast.SendStmt:
		w.expression(node.Chan)
		w.expression(node.Value)
	case *ast.IncDecStmt:
		w.expression(node.X)
	}
}

func (w *walker) clauses(block *ast.BlockStmt) {
	if block == nil {
		return
	}
	for _, statement := range block.List {
		switch clause := statement.(type) {
		case *ast.CaseClause:
			for _, expression := range clause.List {
				w.expression(expression)
			}
			for _, child := range clause.Body {
				w.statement(child)
			}
		case *ast.CommClause:
			w.statement(clause.Comm)
			for _, child := range clause.Body {
				w.statement(child)
			}
		default:
			w.statement(statement)
		}
	}
}

func (w *walker) assign(assignment *ast.AssignStmt) {
	if assignment.Tok == token.ADD_ASSIGN && len(assignment.Lhs) == 1 && len(assignment.Rhs) == 1 {
		if target, ok := assignment.Lhs[0].(*ast.Ident); ok {
			w.locals[target.Name] += w.eval(assignment.Rhs[0])
		}
		return
	}
	if assignment.Tok != token.ASSIGN && assignment.Tok != token.DEFINE {
		return
	}
	if len(assignment.Lhs) != len(assignment.Rhs) {
		return
	}
	for index, target := range assignment.Lhs {
		identifier, ok := target.(*ast.Ident)
		if !ok {
			continue
		}
		w.locals[identifier.Name] = w.eval(assignment.Rhs[index])
	}
}

func (w *walker) declStatement(declaration *ast.DeclStmt) {
	gen, ok := declaration.Decl.(*ast.GenDecl)
	if !ok || gen.Tok != token.VAR {
		return
	}
	for _, spec := range gen.Specs {
		values, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for index, name := range values.Names {
			if index < len(values.Values) {
				w.locals[name.Name] = w.eval(values.Values[index])
			} else {
				w.locals[name.Name] = ""
			}
		}
	}
}

func (w *walker) expression(expression ast.Expr) {
	if expression == nil {
		return
	}
	ast.Inspect(expression, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.FuncLit:
			w.functionLiteral(value)
			return false
		case *ast.CallExpr:
			w.call(value)
		}
		return true
	})
}

func (w *walker) functionLiteral(literal *ast.FuncLit) {
	child := newWalker(w.pkg, w.fnName)
	for name, value := range w.locals {
		child.locals[name] = value
	}
	index := 0
	if literal.Type.Params != nil {
		for _, field := range literal.Type.Params.List {
			if len(field.Names) == 0 {
				index++
				continue
			}
			for _, name := range field.Names {
				child.params[name.Name] = index
				index++
			}
		}
	}
	child.statement(literal.Body)
}

func (w *walker) call(call *ast.CallExpr) {
	if name := localFunctionName(call); name != "" && w.pkg.collecting {
		if _, known := w.pkg.funcs[name]; known {
			args := make([]string, len(call.Args))
			for index, argument := range call.Args {
				args[index] = strings.TrimSpace(w.eval(argument))
			}
			w.pkg.callSites[name] = append(w.pkg.callSites[name], callSite{args: args})
		}
	}
	if w.pkg.collecting {
		return
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || len(call.Args) == 0 || !rawSQLMethods[selector.Sel.Name] {
		return
	}
	sql := strings.TrimSpace(w.eval(call.Args[0]))
	if sql == "" {
		// The statement was built from a value this gate cannot follow (for
		// example a function parameter with no local call sites). Keeping the
		// skip narrow is deliberate: a guessed violation would be noise.
		return
	}
	position := w.pkg.fset.Position(call.Pos())
	w.pkg.statements = append(w.pkg.statements, rawStatement{
		file: position.Filename, line: position.Line, method: selector.Sel.Name, sql: sql,
	})
}

// localFunctionName resolves both direct calls (load(...)) and method calls on
// a package type (e.load(...)), so a helper that receives SQL as a parameter
// can still be checked against its call sites.
func localFunctionName(call *ast.CallExpr) string {
	switch function := call.Fun.(type) {
	case *ast.Ident:
		return function.Name
	case *ast.SelectorExpr:
		return function.Sel.Name
	}
	return ""
}

var rawSQLMethods = map[string]bool{
	"Raw":             true,
	"Exec":            true,
	"ExecContext":     true,
	"Query":           true,
	"QueryContext":    true,
	"QueryRow":        true,
	"QueryRowContext": true,
}

func (w *walker) eval(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.BasicLit:
		if value.Kind == token.STRING {
			unquoted, err := strconv.Unquote(value.Value)
			if err == nil {
				return unquoted
			}
		}
	case *ast.Ident:
		if local, ok := w.locals[value.Name]; ok {
			return local
		}
		if index, ok := w.params[value.Name]; ok {
			if w.pkg.collecting {
				return ""
			}
			var parts []string
			for _, site := range w.pkg.callSites[w.fnName] {
				if index < len(site.args) && site.args[index] != "" {
					parts = append(parts, site.args[index])
				}
			}
			return strings.Join(parts, "\n")
		}
		return w.pkg.consts[value.Name]
	case *ast.ParenExpr:
		return w.eval(value.X)
	case *ast.BinaryExpr:
		if value.Op == token.ADD {
			return w.eval(value.X) + w.eval(value.Y)
		}
	case *ast.CallExpr:
		if isFmtSprintf(value) && len(value.Args) > 0 {
			return w.eval(value.Args[0])
		}
		if isStringConversion(value) {
			return w.eval(value.Args[0])
		}
	}
	return ""
}

func (p *packageInfo) check() []TenancyViolation {
	var violations []TenancyViolation
	for _, statement := range p.statements {
		// DDL names a table but does not read or write rows, so a tenant
		// predicate has no meaning (for example CREATE INDEX ... ON chunks).
		if isDDLStatement(statement.sql) {
			continue
		}
		tokens := sqlIdentifiers(statement.sql)
		hasTenantID := tokens["tenant_id"]
		// A parent table that scopes a referenced child table is exempt from
		// the standalone predicate: the child rule below requires the parent
		// reference, and the EXISTS/JOIN is where the tenant filter belongs.
		// A standalone parent scan is still reported.
		scopingParent := map[string]bool{}
		for child, parents := range tenantChildTables {
			if !tokens[child] {
				continue
			}
			for _, parent := range parents {
				if tokens[parent] {
					scopingParent[parent] = true
				}
			}
		}
		missing := map[string]bool{}
		for _, table := range TenantTables {
			if !tokens[table] || hasTenantID || scopingParent[table] {
				continue
			}
			if !tenancyAllowed(statement.file, table) {
				missing[table] = true
			}
		}
		for table, parents := range tenantChildTables {
			if !tokens[table] || hasTenantID {
				continue
			}
			parentReferenced := false
			for _, parent := range parents {
				if tokens[parent] {
					parentReferenced = true
					break
				}
			}
			if !parentReferenced && !tenancyAllowed(statement.file, table) {
				missing[table] = true
			}
		}
		if len(missing) == 0 {
			continue
		}
		tables := make([]string, 0, len(missing))
		for table := range missing {
			tables = append(tables, table)
		}
		sort.Strings(tables)
		violations = append(violations, TenancyViolation{
			File: statement.file, Line: statement.line, Method: statement.method, Tables: tables,
		})
	}
	return violations
}

var identifierPattern = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

var ddlPrefixes = []string{"create ", "alter ", "drop ", "comment ", "vacuum ", "analyze "}

func isDDLStatement(sql string) bool {
	normalized := strings.ToLower(strings.TrimSpace(stripSQLComments(sql)))
	for _, prefix := range ddlPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

func sqlIdentifiers(sql string) map[string]bool {
	tokens := map[string]bool{}
	for _, token := range identifierPattern.FindAllString(stripSQLComments(sql), -1) {
		tokens[strings.ToLower(token)] = true
	}
	return tokens
}

func stripSQLComments(sql string) string {
	var builder strings.Builder
	for index := 0; index < len(sql); index++ {
		if sql[index] == '-' && index+1 < len(sql) && sql[index+1] == '-' {
			for index < len(sql) && sql[index] != '\n' {
				index++
			}
			if index < len(sql) {
				builder.WriteByte('\n')
			}
			continue
		}
		if sql[index] == '/' && index+1 < len(sql) && sql[index+1] == '*' {
			index += 2
			for index+1 < len(sql) && !(sql[index] == '*' && sql[index+1] == '/') {
				index++
			}
			index++
			builder.WriteByte(' ')
			continue
		}
		builder.WriteByte(sql[index])
	}
	return builder.String()
}

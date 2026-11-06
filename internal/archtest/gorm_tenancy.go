package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// GormAllowlistEntry records a reviewed GORM chain that is deliberately not
// tenant-scoped. The file+table key mirrors the raw-SQL allowlist: a new query
// in the same file cannot inherit an old exemption.
type GormAllowlistEntry struct {
	File   string
	Table  string
	Reason string
}

// GormAllowlist is locked by gorm_tenancy_test.go. Adding an entry requires
// editing that test, which keeps every exception reviewable.
var GormAllowlist = []GormAllowlistEntry{
	{
		File:   "internal/identity/service.go",
		Table:  "auth_sessions",
		Reason: "logout revokes by globally unique token id before any tenant context exists",
	},
	{
		File:   "internal/ingestion/rebuild.go",
		Table:  "chunks",
		Reason: "maintenance rebuild audit scans the whole corpus across tenants by design",
	},
	{
		File:   "internal/ingestion/rebuild.go",
		Table:  "knowledge_bases",
		Reason: "maintenance rebuild discovery lists knowledge bases needing a model/dimension rebuild",
	},
	{
		File:   "internal/pipeline/deadletter.go",
		Table:  "task_inbox",
		Reason: "inbox failure addresses one globally unique idempotency key",
	},
	{
		File:   "internal/pipeline/runtime.go",
		Table:  "task_inbox",
		Reason: "inbox lease transitions address one globally unique idempotency key",
	},
	{
		File:   "internal/platform/evidenceindex/postgres.go",
		Table:  "chunks",
		Reason: "embedding writer counts one document_version's chunks by globally unique version id",
	},
	{
		File:   "internal/platform/persistence/inboxlease/lease.go",
		Table:  "task_inbox",
		Reason: "lease maintenance resolves the tenant from the inbox row and addresses one idempotency key",
	},
}

// GormTenancyViolation is one direct GORM chain that touches a tenant-owned
// table without a tenant predicate or an explicit bypass.
type GormTenancyViolation struct {
	File  string
	Line  int
	Table string
	Call  string
}

func (v GormTenancyViolation) String() string {
	return fmt.Sprintf("%s:%d: %s on %s: add tenantdb.Scoped/MustScoped, an explicit tenant_id predicate, or a GORM allowlist entry with a reason",
		v.File, v.Line, v.Call, v.Table)
}

type gormCall struct {
	name string
	call *ast.CallExpr
	pos  token.Pos
}

// CheckGormTenancy inspects direct GORM chains. It resolves `.Model(&models.X{})`
// and `.Table("x")` against the canonical tenant table catalog plus the model
// TableName methods found in the scanned files. Chains built through variables
// or helpers are out of scope; the runtime callback in tenantdb covers them.
func CheckGormTenancy(files []TenancyFile) ([]GormTenancyViolation, error) {
	modelTables, err := collectModelTables(files)
	if err != nil {
		return nil, err
	}
	tenantTables := map[string]struct{}{}
	for _, table := range TenantTables {
		tenantTables[table] = struct{}{}
	}
	var violations []GormTenancyViolation
	seen := map[string]struct{}{}
	for _, file := range files {
		normalized := path.Clean(strings.ReplaceAll(file.Path, "\\", "/"))
		if strings.HasSuffix(normalized, "_test.go") {
			continue
		}
		fileSet := token.NewFileSet()
		parsed, err := parser.ParseFile(fileSet, normalized, file.Source, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", normalized, err)
		}
		inner := map[*ast.CallExpr]struct{}{}
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
				if nested, ok := selector.X.(*ast.CallExpr); ok {
					inner[nested] = struct{}{}
				}
			}
			return true
		})
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || call == nil {
				return true
			}
			if _, nested := inner[call]; nested {
				return true
			}
			calls := flattenGormChain(call)
			if !gormTerminalOperation(calls) {
				return true
			}
			table, source, ok := gormChainTable(calls, modelTables)
			if !ok {
				return true
			}
			if _, owned := tenantTables[table]; !owned {
				return true
			}
			if gormChainScoped(calls, source) || gormAllowlisted(normalized, table) {
				return true
			}
			line := fileSet.Position(source.Pos()).Line
			key := fmt.Sprintf("%s:%d:%s", normalized, line, table)
			if _, duplicate := seen[key]; duplicate {
				return true
			}
			seen[key] = struct{}{}
			violations = append(violations, GormTenancyViolation{
				File: normalized, Line: line, Table: table, Call: chainName(calls),
			})
			return true
		})
	}
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].File != violations[j].File {
			return violations[i].File < violations[j].File
		}
		return violations[i].Line < violations[j].Line
	})
	return violations, nil
}

// collectModelTables maps Go model types to their GORM table names by reading
// the TableName methods in the scanned package.
func collectModelTables(files []TenancyFile) (map[string]string, error) {
	tables := map[string]string{}
	for _, file := range files {
		parsed, err := parser.ParseFile(token.NewFileSet(), file.Path, file.Source, 0)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", file.Path, err)
		}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Name.Name != "TableName" || function.Recv == nil || len(function.Recv.List) == 0 {
				continue
			}
			receiver := receiverType(function.Recv.List[0].Type)
			table := returnedString(function.Body)
			if receiver != "" && table != "" {
				tables[receiver] = table
			}
		}
	}
	return tables, nil
}

func receiverType(expression ast.Expr) string {
	switch typed := expression.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.StarExpr:
		return receiverType(typed.X)
	case *ast.IndexExpr:
		return receiverType(typed.X)
	case *ast.SelectorExpr:
		return typed.Sel.Name
	default:
		return ""
	}
}

func returnedString(body *ast.BlockStmt) string {
	if body == nil {
		return ""
	}
	for _, statement := range body.List {
		returnStatement, ok := statement.(*ast.ReturnStmt)
		if !ok || len(returnStatement.Results) != 1 {
			continue
		}
		return literalString(returnStatement.Results[0])
	}
	return ""
}

func literalString(expression ast.Expr) string {
	literal, ok := expression.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return ""
	}
	value, err := strconv.Unquote(literal.Value)
	if err != nil {
		return ""
	}
	return value
}

func flattenGormChain(call *ast.CallExpr) []gormCall {
	var calls []gormCall
	var walk func(expression ast.Expr)
	walk = func(expression ast.Expr) {
		current, ok := expression.(*ast.CallExpr)
		if !ok {
			return
		}
		if selector, ok := current.Fun.(*ast.SelectorExpr); ok {
			calls = append([]gormCall{{name: selector.Sel.Name, call: current, pos: current.Pos()}}, calls...)
			walk(selector.X)
		}
	}
	walk(call)
	return calls
}

// gormChainTable resolves the primary table of a chain and the call that named
// it. Create/Save with a model literal is also a valid source.
func gormChainTable(calls []gormCall, modelTables map[string]string) (string, *ast.CallExpr, bool) {
	for _, call := range calls {
		switch call.name {
		case "Model":
			if len(call.call.Args) == 0 {
				continue
			}
			if table := modelTableName(call.call.Args[0], modelTables); table != "" {
				return table, call.call, true
			}
		case "Table":
			if len(call.call.Args) == 0 {
				continue
			}
			if table := literalString(call.call.Args[0]); table != "" {
				return table, call.call, true
			}
		case "Create", "Save":
			if len(call.call.Args) == 0 {
				continue
			}
			if table := modelTableName(call.call.Args[0], modelTables); table != "" {
				return table, call.call, true
			}
		}
	}
	return "", nil, false
}

func modelTableName(expression ast.Expr, modelTables map[string]string) string {
	for {
		switch typed := expression.(type) {
		case *ast.UnaryExpr:
			expression = typed.X
			continue
		case *ast.CompositeLit:
			switch model := typed.Type.(type) {
			case *ast.SelectorExpr:
				return modelTables[model.Sel.Name]
			case *ast.Ident:
				return modelTables[model.Name]
			}
		}
		return ""
	}
}

// gormTerminalOperation drops builder chains that do not execute a statement.
// A chain assigned to a variable before its terminal call is out of scope; the
// runtime callback still scopes it when a tenant is in context.
func gormTerminalOperation(calls []gormCall) bool {
	if len(calls) == 0 {
		return false
	}
	switch calls[len(calls)-1].name {
	case "Find", "First", "Take", "Last", "Scan", "Pluck", "Count",
		"Update", "Updates", "Delete", "Create", "Save":
		return true
	default:
		return false
	}
}

func gormChainScoped(calls []gormCall, source *ast.CallExpr) bool {
	for _, call := range calls {
		switch call.name {
		case "Scoped", "MustScoped", "Unscoped":
			return true
		case "Where", "Joins", "Select":
			for _, argument := range call.call.Args {
				if strings.Contains(strings.ToLower(literalString(argument)), "tenant_id") {
					return true
				}
			}
			if (call.name == "Where" || call.name == "First" || call.name == "Take" || call.name == "Last") && pointKeyPredicate(call.call) {
				return true
			}
		}
	}
	return compositeHasTenantID(source) || compositeHasID(source)
}

// pointKeyPattern matches a bare primary-key predicate such as "id = ?",
// "chunks.id = ?" or "id IN ?". Foreign-key columns like conversation_id do not
// match because "_" is a word character before "id".
var pointKeyPattern = regexp.MustCompile(`(?i)(^|[^a-z0-9_])([a-z0-9_]+\.)?id\s*(=|in\b)`)

// pointKeyPredicate accepts a primary-key filter for point reads/writes. The id
// is globally unique, so this is safe scoping; collection queries still need a
// tenant predicate.
func pointKeyPredicate(call *ast.CallExpr) bool {
	for _, argument := range call.Args {
		if pointKeyPattern.MatchString(literalString(argument)) {
			return true
		}
	}
	return false
}

func compositeHasID(call *ast.CallExpr) bool {
	for _, argument := range call.Args {
		expression := argument
		if unary, ok := expression.(*ast.UnaryExpr); ok {
			expression = unary.X
		}
		literal, ok := expression.(*ast.CompositeLit)
		if !ok {
			continue
		}
		for _, element := range literal.Elts {
			keyValue, ok := element.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if key, ok := keyValue.Key.(*ast.Ident); ok && (key.Name == "ID" || key.Name == "Id") {
				return true
			}
		}
	}
	return false
}

func compositeHasTenantID(call *ast.CallExpr) bool {
	for _, argument := range call.Args {
		expression := argument
		if unary, ok := expression.(*ast.UnaryExpr); ok {
			expression = unary.X
		}
		literal, ok := expression.(*ast.CompositeLit)
		if !ok {
			continue
		}
		for _, element := range literal.Elts {
			keyValue, ok := element.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if key, ok := keyValue.Key.(*ast.Ident); ok && key.Name == "TenantID" {
				return true
			}
		}
	}
	return false
}

func chainName(calls []gormCall) string {
	if len(calls) == 0 {
		return ""
	}
	return calls[len(calls)-1].name
}

func gormAllowlisted(file, table string) bool {
	for _, entry := range GormAllowlist {
		if entry.File == file && entry.Table == table {
			return true
		}
	}
	return false
}

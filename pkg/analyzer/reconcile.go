package analyzer

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/packages"
)

// ReconcileFinder finds Reconcile methods in packages.
type ReconcileFinder struct {
	fset *token.FileSet
}

// NewReconcileFinder creates a new ReconcileFinder.
func NewReconcileFinder(fset *token.FileSet) *ReconcileFinder {
	return &ReconcileFinder{fset: fset}
}

// ReconcileFunc holds a discovered Reconcile function with context.
type ReconcileFunc struct {
	Pkg          *packages.Package
	File         *ast.File
	Func         *ast.FuncDecl
	ReceiverType string
	ReceiverPkg  string
}

// FindReconcileFunctions finds all Reconcile methods matching the controller-runtime signature.
func (rf *ReconcileFinder) FindReconcileFunctions(pkgs []*packages.Package) []ReconcileFunc {
	var results []ReconcileFunc

	for _, pkg := range pkgs {
		// Skip test packages.
		if strings.HasSuffix(pkg.PkgPath, "_test") {
			continue
		}

		for _, file := range pkg.Syntax {
			// Skip test files.
			fileName := rf.fset.Position(file.Pos()).Filename
			if strings.HasSuffix(fileName, "_test.go") {
				continue
			}

			ast.Inspect(file, func(n ast.Node) bool {
				fn, ok := n.(*ast.FuncDecl)
				if !ok {
					return true
				}

				// Check if this is a method (has receiver).
				if fn.Recv == nil || len(fn.Recv.List) == 0 {
					return true
				}

				// Check method name.
				if fn.Name.Name != "Reconcile" {
					return true
				}

				// Check signature matches.
				if !rf.matchesReconcileSignature(fn, pkg) {
					return true
				}

				recvType, recvPkg := rf.extractReceiverInfo(fn, pkg)
				results = append(results, ReconcileFunc{
					Pkg:          pkg,
					File:         file,
					Func:         fn,
					ReceiverType: recvType,
					ReceiverPkg:  recvPkg,
				})

				return true
			})
		}
	}

	return results
}

// matchesReconcileSignature checks if function matches controller-runtime Reconcile signature.
// Expected: func (r *T) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error)
func (rf *ReconcileFinder) matchesReconcileSignature(fn *ast.FuncDecl, pkg *packages.Package) bool {
	// Check parameters: (ctx context.Context, req ctrl.Request).
	if fn.Type.Params == nil || len(fn.Type.Params.List) < 2 {
		return false
	}

	// Check returns: (ctrl.Result, error).
	if fn.Type.Results == nil || len(fn.Type.Results.List) < 2 {
		return false
	}

	// Check first parameter is context.Context.
	firstParam := fn.Type.Params.List[0]
	if !rf.isContextType(firstParam.Type, pkg) {
		return false
	}

	// Check second parameter type name contains "Request".
	secondParam := fn.Type.Params.List[1]
	if !rf.isRequestType(secondParam.Type, pkg) {
		return false
	}

	// Check first return type contains "Result".
	firstResult := fn.Type.Results.List[0]
	if !rf.isResultType(firstResult.Type, pkg) {
		return false
	}

	// Check second return is error.
	if len(fn.Type.Results.List) < 2 {
		return false
	}
	secondResult := fn.Type.Results.List[1]
	if !rf.isErrorType(secondResult.Type, pkg) {
		return false
	}

	return true
}

// extractReceiverInfo extracts receiver type name and package.
func (rf *ReconcileFinder) extractReceiverInfo(fn *ast.FuncDecl, pkg *packages.Package) (string, string) {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return "", ""
	}

	recvField := fn.Recv.List[0]
	recvType := recvField.Type

	// Handle pointer receiver: *T -> T.
	if star, ok := recvType.(*ast.StarExpr); ok {
		recvType = star.X
	}

	// Get type name.
	var typeName string
	switch t := recvType.(type) {
	case *ast.Ident:
		typeName = t.Name
	case *ast.SelectorExpr:
		typeName = t.Sel.Name
	default:
		typeName = "unknown"
	}

	return typeName, pkg.PkgPath
}

// isContextType checks if a type is context.Context.
func (rf *ReconcileFinder) isContextType(expr ast.Expr, pkg *packages.Package) bool {
	return rf.typeNameContains(expr, pkg, "Context")
}

// isRequestType checks if a type name contains "Request".
func (rf *ReconcileFinder) isRequestType(expr ast.Expr, pkg *packages.Package) bool {
	return rf.typeNameContains(expr, pkg, "Request")
}

// isResultType checks if a type name contains "Result".
func (rf *ReconcileFinder) isResultType(expr ast.Expr, pkg *packages.Package) bool {
	return rf.typeNameContains(expr, pkg, "Result")
}

// isErrorType checks if a type is error.
func (rf *ReconcileFinder) isErrorType(expr ast.Expr, pkg *packages.Package) bool {
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name == "error"
	}
	return false
}

// typeNameContains checks if a type's name contains the given string.
func (rf *ReconcileFinder) typeNameContains(expr ast.Expr, pkg *packages.Package, substr string) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		return strings.Contains(t.Name, substr)
	case *ast.SelectorExpr:
		return strings.Contains(t.Sel.Name, substr)
	}

	// Try using type info if available.
	if pkg.TypesInfo != nil {
		if typeInfo := pkg.TypesInfo.TypeOf(expr); typeInfo != nil {
			typeName := typeInfo.String()
			return strings.Contains(typeName, substr)
		}
	}

	return false
}

// ExtractReqParamName extracts the request parameter name from the function signature.
func ExtractReqParamName(fn *ast.FuncDecl) string {
	if fn.Type.Params == nil || len(fn.Type.Params.List) < 2 {
		return "req" // default fallback
	}

	secondParam := fn.Type.Params.List[1]
	if len(secondParam.Names) > 0 {
		return secondParam.Names[0].Name
	}

	return "req" // default fallback
}

// ExtractClientFieldName tries to find the client field name in the receiver type.
// Common patterns: r.Client, c.client, reconciler.Client, etc.
func ExtractClientFieldName(recvType *types.Struct) []string {
	var candidates []string

	if recvType == nil {
		return []string{"Client", "client"}
	}

	for i := 0; i < recvType.NumFields(); i++ {
		field := recvType.Field(i)
		fieldName := field.Name()

		// Look for fields named "Client" or "client".
		if strings.Contains(strings.ToLower(fieldName), "client") {
			candidates = append(candidates, fieldName)
		}
	}

	if len(candidates) == 0 {
		// Return common defaults.
		return []string{"Client", "client"}
	}

	return candidates
}

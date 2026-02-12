package analyzer

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/printer"
	"go/token"
	"io"
	"strings"

	"github.com/rg0now/k8s-controller-survey/pkg/models"
	"golang.org/x/tools/go/packages"
)

// PatternDetector detects reconciliation patterns in a Reconcile function.
type PatternDetector struct {
	fset     *token.FileSet
	pkg      *packages.Package
	fileData []byte

	// Track the request parameter name (usually "req" or "request").
	reqParamName string

	// Track possible client field names.
	clientFieldNames []string
}

// NewPatternDetector creates a new PatternDetector.
func NewPatternDetector(fset *token.FileSet, pkg *packages.Package, fileData []byte, reqParamName string) *PatternDetector {
	return &PatternDetector{
		fset:             fset,
		pkg:              pkg,
		fileData:         fileData,
		reqParamName:     reqParamName,
		clientFieldNames: []string{"Client", "client", "c"},
	}
}

// DetectPatterns analyzes a Reconcile function and returns detected signals.
func (pd *PatternDetector) DetectPatterns(fn *ast.FuncDecl) []models.Signal {
	var signals []models.Signal

	if fn.Body == nil {
		return signals
	}

	// Walk the function body.
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			sigs := pd.detectCallPatterns(node)
			signals = append(signals, sigs...)
		case *ast.IfStmt:
			sigs := pd.detectControlFlowPatterns(node)
			signals = append(signals, sigs...)
		case *ast.ForStmt:
			sigs := pd.detectLoopPatterns(node)
			signals = append(signals, sigs...)
		case *ast.RangeStmt:
			sigs := pd.detectRangeLoopPatterns(node)
			signals = append(signals, sigs...)
		}
		return true
	})

	return signals
}

// detectCallPatterns detects client.List, client.Get, etc.
func (pd *PatternDetector) detectCallPatterns(call *ast.CallExpr) []models.Signal {
	var signals []models.Signal

	// Get the method being called.
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return signals
	}

	methodName := sel.Sel.Name

	// Check if this is a client method call.
	if !pd.isClientCall(sel) {
		return signals
	}

	switch methodName {
	case "List":
		sig := pd.analyzeListCall(call)
		if sig.Type != "" {
			signals = append(signals, sig)
		}
	case "Get":
		sig := pd.analyzeGetCall(call)
		if sig.Type != "" {
			signals = append(signals, sig)
		}
	case "Create", "Update", "Delete", "Patch":
		sig := pd.analyzeWriteCall(call, methodName)
		if sig.Type != "" {
			signals = append(signals, sig)
		}
	}

	return signals
}

// analyzeListCall determines if List is scoped or unscoped.
func (pd *PatternDetector) analyzeListCall(call *ast.CallExpr) models.Signal {
	line := pd.fset.Position(call.Pos()).Line
	snippet := pd.extractSnippet(call)

	// List signature: List(ctx, list, opts...).
	// Check opts for request-scoped options.
	if len(call.Args) < 2 {
		return models.Signal{} // malformed
	}

	// Check if any option references the request parameter.
	hasReqScopedOpts := false
	hasNamespaceOpt := false
	hasLabelOpt := false

	for _, arg := range call.Args[2:] { // skip ctx and list
		if pd.referencesReqParam(arg) {
			hasReqScopedOpts = true
		}
		// Check for specific option types.
		if pd.isNamespaceOption(arg) {
			hasNamespaceOpt = true
		}
		if pd.isLabelMatchOption(arg) {
			hasLabelOpt = true
		}
	}

	if !hasReqScopedOpts {
		return models.Signal{
			Type:        models.SignalListUnscoped,
			Line:        line,
			Score:       3,
			Snippet:     snippet,
			Description: "client.List without request-scoped selectors",
		}
	}

	if hasNamespaceOpt && !hasLabelOpt {
		return models.Signal{
			Type:        models.SignalListNamespaceScoped,
			Line:        line,
			Score:       1,
			Snippet:     snippet,
			Description: "client.List scoped to request namespace only",
		}
	}

	return models.Signal{
		Type:        models.SignalListLabelScoped,
		Line:        line,
		Score:       0,
		Snippet:     snippet,
		Description: "client.List scoped by labels/fields derived from request",
	}
}

// analyzeGetCall determines if Get is req-scoped or not.
func (pd *PatternDetector) analyzeGetCall(call *ast.CallExpr) models.Signal {
	line := pd.fset.Position(call.Pos()).Line
	snippet := pd.extractSnippet(call)

	// Get signature: Get(ctx, key, obj, opts...).
	if len(call.Args) < 3 {
		return models.Signal{} // malformed
	}

	keyArg := call.Args[1]

	// Check if key is exactly req.NamespacedName.
	if pd.isReqNamespacedName(keyArg) {
		return models.Signal{
			Type:        models.SignalGetReqScoped,
			Line:        line,
			Score:       -1,
			Snippet:     snippet,
			Description: "client.Get with req.NamespacedName (primary resource fetch)",
		}
	}

	// Check if key is derived from req.
	if pd.referencesReqParam(keyArg) {
		return models.Signal{
			Type:        models.SignalGetDerived,
			Line:        line,
			Score:       -1,
			Snippet:     snippet,
			Description: "client.Get with key derived from request",
		}
	}

	// Key not related to request.
	return models.Signal{
		Type:        models.SignalGetUnrelated,
		Line:        line,
		Score:       1,
		Snippet:     snippet,
		Description: "client.Get with key not derived from request",
	}
}

// analyzeWriteCall analyzes Create/Update/Delete/Patch calls.
func (pd *PatternDetector) analyzeWriteCall(call *ast.CallExpr, method string) models.Signal {
	line := pd.fset.Position(call.Pos()).Line
	snippet := pd.extractSnippet(call)

	return models.Signal{
		Type:        models.SignalSingleWrite,
		Line:        line,
		Score:       -1,
		Snippet:     snippet,
		Description: fmt.Sprintf("client.%s call", method),
	}
}

// detectControlFlowPatterns detects early return on NotFound, etc.
func (pd *PatternDetector) detectControlFlowPatterns(ifStmt *ast.IfStmt) []models.Signal {
	var signals []models.Signal

	// Check for: if apierrors.IsNotFound(err) { ... }.
	if pd.isNotFoundCheck(ifStmt.Cond) {
		// Check what happens in the body.
		if pd.isEarlyReturn(ifStmt.Body) {
			// Check if it just returns nil or handles delete.
			if pd.isNilReturn(ifStmt.Body) {
				signals = append(signals, models.Signal{
					Type:        models.SignalNotFoundIgnore,
					Line:        pd.fset.Position(ifStmt.Pos()).Line,
					Score:       -1,
					Snippet:     pd.extractSnippet(ifStmt),
					Description: "Early return on NotFound (ignores deletes)",
				})
			} else {
				signals = append(signals, models.Signal{
					Type:        models.SignalNotFoundEarlyReturn,
					Line:        pd.fset.Position(ifStmt.Pos()).Line,
					Score:       -2,
					Snippet:     pd.extractSnippet(ifStmt),
					Description: "NotFound handling with delete logic (classic edge-triggered pattern)",
				})
			}
		}
	}

	return signals
}

// detectLoopPatterns detects for loops containing write operations.
func (pd *PatternDetector) detectLoopPatterns(forStmt *ast.ForStmt) []models.Signal {
	var signals []models.Signal

	if forStmt.Body == nil {
		return signals
	}

	if pd.hasWriteOperation(forStmt.Body) {
		signals = append(signals, models.Signal{
			Type:        models.SignalLoopWrite,
			Line:        pd.fset.Position(forStmt.Pos()).Line,
			Score:       3,
			Snippet:     pd.extractSnippet(forStmt),
			Description: "Loop containing write operations (SoTW pattern)",
		})
	}

	return signals
}

// detectRangeLoopPatterns detects range loops containing write operations.
func (pd *PatternDetector) detectRangeLoopPatterns(rangeStmt *ast.RangeStmt) []models.Signal {
	var signals []models.Signal

	if rangeStmt.Body == nil {
		return signals
	}

	if pd.hasWriteOperation(rangeStmt.Body) {
		signals = append(signals, models.Signal{
			Type:        models.SignalLoopWrite,
			Line:        pd.fset.Position(rangeStmt.Pos()).Line,
			Score:       3,
			Snippet:     pd.extractSnippet(rangeStmt),
			Description: "Loop containing write operations (SoTW pattern)",
		})
	}

	return signals
}

// hasWriteOperation checks if a block contains client write operations.
func (pd *PatternDetector) hasWriteOperation(body *ast.BlockStmt) bool {
	hasWrite := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if pd.isClientCall(sel) {
			switch sel.Sel.Name {
			case "Create", "Update", "Delete", "Patch":
				hasWrite = true
				return false
			}
		}
		return true
	})
	return hasWrite
}

// Helper methods.

// isClientCall checks if a selector expression is a client method call.
func (pd *PatternDetector) isClientCall(sel *ast.SelectorExpr) bool {
	// First check if the method name is a known client method.
	// This handles cases like r.Get(), r.List(), etc. where r embeds the client.
	methodName := sel.Sel.Name
	isClientMethod := methodName == "Get" || methodName == "List" ||
		methodName == "Create" || methodName == "Update" ||
		methodName == "Delete" || methodName == "Patch"

	// Check common patterns: r.Client, c.client, Client, client, etc.
	switch x := sel.X.(type) {
	case *ast.Ident:
		// Direct call on a variable: client.Get(), Client.Get().
		if pd.isClientIdentifier(x.Name) {
			return true
		}
		// If it's a known client method, assume it's a client call.
		// This handles cases where the receiver embeds the client.
		return isClientMethod
	case *ast.SelectorExpr:
		// Nested selector: r.Client.Get() or r.client.Status().Patch().
		if pd.isClientIdentifier(x.Sel.Name) {
			return true
		}
		// Recursively check if deeper in the chain there's a client reference.
		return pd.isClientCall(x)
	case *ast.CallExpr:
		// Handle chained calls like r.client.Status().Patch().
		if sel2, ok := x.Fun.(*ast.SelectorExpr); ok {
			return pd.isClientCall(sel2)
		}
	}
	return false
}

// isClientIdentifier checks if a name looks like a client identifier.
func (pd *PatternDetector) isClientIdentifier(name string) bool {
	for _, candidate := range pd.clientFieldNames {
		if name == candidate {
			return true
		}
	}
	// Also check common variations.
	lowerName := strings.ToLower(name)
	return strings.Contains(lowerName, "client")
}

// referencesReqParam checks if expression references the request parameter.
func (pd *PatternDetector) referencesReqParam(expr ast.Expr) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if ident, ok := n.(*ast.Ident); ok {
			if ident.Name == pd.reqParamName {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// isReqNamespacedName checks for patterns like req.NamespacedName.
func (pd *PatternDetector) isReqNamespacedName(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if sel.Sel.Name != "NamespacedName" {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return ident.Name == pd.reqParamName
}

// isNamespaceOption checks if an expression is a namespace option.
func (pd *PatternDetector) isNamespaceOption(expr ast.Expr) bool {
	// Look for InNamespace(...).
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	if ident, ok := call.Fun.(*ast.Ident); ok {
		return strings.Contains(ident.Name, "InNamespace")
	}
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		return strings.Contains(sel.Sel.Name, "InNamespace")
	}
	return false
}

// isLabelMatchOption checks if an expression is a label matching option.
func (pd *PatternDetector) isLabelMatchOption(expr ast.Expr) bool {
	// Look for MatchingLabels(...) or MatchingFields(...).
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	if ident, ok := call.Fun.(*ast.Ident); ok {
		name := ident.Name
		return strings.Contains(name, "MatchingLabels") || strings.Contains(name, "MatchingFields")
	}
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		name := sel.Sel.Name
		return strings.Contains(name, "MatchingLabels") || strings.Contains(name, "MatchingFields")
	}
	return false
}

// isNotFoundCheck checks for apierrors.IsNotFound(err).
func (pd *PatternDetector) isNotFoundCheck(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	return sel.Sel.Name == "IsNotFound"
}

// isEarlyReturn checks if a block statement has an early return.
func (pd *PatternDetector) isEarlyReturn(body *ast.BlockStmt) bool {
	for _, stmt := range body.List {
		if _, ok := stmt.(*ast.ReturnStmt); ok {
			return true
		}
	}
	return false
}

// isNilReturn checks if a block has a return nil or return nil, nil.
func (pd *PatternDetector) isNilReturn(body *ast.BlockStmt) bool {
	for _, stmt := range body.List {
		retStmt, ok := stmt.(*ast.ReturnStmt)
		if !ok {
			continue
		}
		// Check if it's returning nil or (something, nil).
		if len(retStmt.Results) == 0 {
			continue
		}
		// Look for nil in the return values.
		for _, result := range retStmt.Results {
			if ident, ok := result.(*ast.Ident); ok {
				if ident.Name == "nil" {
					return true
				}
			}
		}
	}
	return false
}

// extractSnippet extracts source code snippet for an AST node.
func (pd *PatternDetector) extractSnippet(node ast.Node) string {
	var buf bytes.Buffer

	// Try to use file data if available.
	if pd.fileData != nil && len(pd.fileData) > 0 {
		start := pd.fset.Position(node.Pos()).Offset
		end := pd.fset.Position(node.End()).Offset
		if start >= 0 && end <= len(pd.fileData) && start < end {
			snippet := string(pd.fileData[start:end])
			return pd.truncateSnippet(snippet)
		}
	}

	// Fallback: use printer.
	cfg := printer.Config{Mode: printer.RawFormat, Tabwidth: 8}
	if err := cfg.Fprint(&buf, pd.fset, node); err != nil {
		// If printer fails, try format.Node as last resort.
		buf.Reset()
		if err := format.Node(&buf, pd.fset, node); err != nil {
			return "<unprintable>"
		}
	}

	return pd.truncateSnippet(buf.String())
}

// truncateSnippet truncates a snippet to a reasonable length.
func (pd *PatternDetector) truncateSnippet(s string) string {
	// Remove leading/trailing whitespace.
	s = strings.TrimSpace(s)

	// Replace multiple spaces with single space.
	s = strings.Join(strings.Fields(s), " ")

	// Truncate if too long.
	maxLen := 200
	if len(s) > maxLen {
		s = s[:maxLen] + "..."
	}

	return s
}

// ReadFileData reads the source file data for snippet extraction.
func ReadFileData(fset *token.FileSet, file *ast.File) []byte {
	fileName := fset.Position(file.Pos()).Filename
	if fileName == "" {
		return nil
	}

	// The file data might be available in the token.File.
	tf := fset.File(file.Pos())
	if tf == nil {
		return nil
	}

	// Try to read the source - this is a bit tricky with go/packages.
	// For now, return nil and rely on printer fallback.
	return nil
}

// ReadFileDataFromPath reads file data from a file path.
func ReadFileDataFromPath(path string) ([]byte, error) {
	// This will be used when we have the file path available.
	// For now, defer to the packages loading mechanism.
	return nil, nil
}

// ExtractSnippetFromSource extracts a snippet from source using positions.
func ExtractSnippetFromSource(fset *token.FileSet, node ast.Node, src []byte) string {
	if src == nil || len(src) == 0 {
		return ""
	}

	start := fset.Position(node.Pos()).Offset
	end := fset.Position(node.End()).Offset

	if start < 0 || end > len(src) || start >= end {
		return ""
	}

	snippet := string(src[start:end])
	snippet = strings.TrimSpace(snippet)
	snippet = strings.Join(strings.Fields(snippet), " ")

	maxLen := 200
	if len(snippet) > maxLen {
		snippet = snippet[:maxLen] + "..."
	}

	return snippet
}

// DummyReader implements io.Reader for testing.
type DummyReader struct{}

func (d DummyReader) Read(p []byte) (n int, err error) {
	return 0, io.EOF
}

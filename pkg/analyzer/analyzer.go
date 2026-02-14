package analyzer

import (
	"fmt"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/rg0now/k8s-controller-survey/pkg/models"
	"golang.org/x/tools/go/packages"
)

// Analyzer orchestrates the analysis of a repository.
type Analyzer struct {
	workDir string
	verbose bool
}

// NewAnalyzer creates a new Analyzer.
func NewAnalyzer(workDir string, verbose bool) *Analyzer {
	return &Analyzer{
		workDir: workDir,
		verbose: verbose,
	}
}

// AnalyzeRepo analyzes a single repository and returns all found reconcilers.
func (a *Analyzer) AnalyzeRepo(repo models.Repository) ([]models.Reconciler, error) {
	if a.verbose {
		log.Printf("Analyzing repository: %s", repo.URL)
	}

	// Load packages.
	pkgs, err := a.loadPackages(repo.LocalPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load packages: %w", err)
	}

	if len(pkgs) == 0 {
		return nil, fmt.Errorf("no packages found in repository")
	}

	// Use the FileSet from the first package (they all share the same one).
	var fset *token.FileSet
	if len(pkgs) > 0 && pkgs[0].Fset != nil {
		fset = pkgs[0].Fset
	} else {
		fset = token.NewFileSet()
	}

	// Find Reconcile functions.
	finder := NewReconcileFinder(fset)
	reconcileFuncs := finder.FindReconcileFunctions(pkgs)

	if a.verbose {
		log.Printf("Found %d Reconcile functions in %s", len(reconcileFuncs), repo.URL)
	}

	// Analyze each Reconcile function.
	var results []models.Reconciler
	for _, recFunc := range reconcileFuncs {
		reconciler, err := a.analyzeReconcileFunc(recFunc, repo, fset)
		if err != nil {
			if a.verbose {
				log.Printf("Error analyzing Reconcile function: %v", err)
			}
			continue
		}
		results = append(results, reconciler)
	}

	return results, nil
}

// loadPackages loads all Go packages from a repository.
func (a *Analyzer) loadPackages(repoPath string) ([]*packages.Package, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports,
		Dir: repoPath,
		Env: append(os.Environ(), "GOFLAGS=-tags="),
	}

	// Load all packages.
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, err
	}

	// Filter out packages with errors (but still return what we can).
	var validPkgs []*packages.Package
	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 && len(pkg.Syntax) == 0 {
			if a.verbose {
				for _, e := range pkg.Errors {
					log.Printf("Package error in %s: %v", pkg.PkgPath, e)
				}
			}
		} else {
			validPkgs = append(validPkgs, pkg)
		}
	}

	return validPkgs, nil
}

// analyzeReconcileFunc analyzes a single Reconcile function.
func (a *Analyzer) analyzeReconcileFunc(
	recFunc ReconcileFunc,
	repo models.Repository,
	fset *token.FileSet,
) (models.Reconciler, error) {
	// Get file path and position.
	filePath := fset.Position(recFunc.Func.Pos()).Filename
	line := fset.Position(recFunc.Func.Pos()).Line
	endLine := fset.Position(recFunc.Func.End()).Line

	// Make file path relative to repo.
	relPath, err := filepath.Rel(repo.LocalPath, filePath)
	if err != nil {
		relPath = filePath
	}

	// Read file data for snippet extraction.
	fileData, err := os.ReadFile(filePath)
	if err != nil {
		if a.verbose {
			log.Printf("Warning: could not read file %s: %v", filePath, err)
		}
		fileData = nil
	}

	// Extract request parameter name.
	reqParamName := ExtractReqParamName(recFunc.Func)

	// Create pattern detector.
	detector := NewPatternDetector(fset, recFunc.Pkg, fileData, reqParamName)

	// Detect patterns.
	signals := detector.DetectPatterns(recFunc.Func)

	// Classify.
	score, classification := Classify(signals)

	// Build reconciler ID.
	repoName := strings.TrimPrefix(repo.URL, "https://github.com/")
	repoName = strings.TrimPrefix(repoName, "http://github.com/")
	id := fmt.Sprintf("%s#%s#%d", repoName, relPath, line)

	return models.Reconciler{
		ID:             id,
		Repo:           repoName,
		File:           relPath,
		Line:           line,
		EndLine:        endLine,
		ReceiverType:   recFunc.ReceiverType,
		ReceiverPkg:    recFunc.ReceiverPkg,
		Score:          score,
		Classification: classification,
		Signals:        signals,
	}, nil
}

// CloneRepo clones a repository to the work directory.
func (a *Analyzer) CloneRepo(repoURL string) (string, error) {
	// Extract repo name from URL.
	parts := strings.Split(strings.TrimSuffix(repoURL, ".git"), "/")
	repoName := parts[len(parts)-1]
	ownerName := parts[len(parts)-2]

	localPath := filepath.Join(a.workDir, ownerName, repoName)

	// Check if already exists.
	if _, err := os.Stat(localPath); err == nil {
		if a.verbose {
			log.Printf("Repository already exists at %s, skipping clone", localPath)
		}
		return localPath, nil
	}

	if a.verbose {
		log.Printf("Cloning %s to %s", repoURL, localPath)
	}

	// Create parent directory.
	if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return "", fmt.Errorf("failed to create directory: %w", err)
	}

	// Clone with depth 1 for speed.
	// Note: This would normally use exec.Command, but we'll do that in the CLI.
	return localPath, nil
}

// ParseRepoURL extracts owner and name from a GitHub URL.
func ParseRepoURL(url string) (owner, name string) {
	url = strings.TrimPrefix(url, "https://github.com/")
	url = strings.TrimPrefix(url, "http://github.com/")
	url = strings.TrimSuffix(url, ".git")
	url = strings.TrimSuffix(url, "/")

	parts := strings.Split(url, "/")
	if len(parts) >= 2 {
		return parts[0], parts[1]
	}
	return "", ""
}

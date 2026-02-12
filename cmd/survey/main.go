package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rg0now/k8s-controller-survey/pkg/analyzer"
	"github.com/rg0now/k8s-controller-survey/pkg/models"
	"github.com/rg0now/k8s-controller-survey/pkg/output"
	"github.com/spf13/cobra"
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "k8s-controller-survey",
		Short: "Analyze Kubernetes controllers for SoTW vs edge-triggered patterns",
		Long: `A static analysis tool to classify Kubernetes controllers as
State-of-the-World (SoTW) vs Edge-Triggered based on their
reconciliation patterns.`,
	}

	rootCmd.AddCommand(analyzeCmd())
	rootCmd.AddCommand(reportCmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// analyzeCmd analyzes repositories.
func analyzeCmd() *cobra.Command {
	var (
		reposFile  string
		repoURLs   []string
		outputFile string
		workDir    string
		keepClones bool
		verbose    bool
	)

	cmd := &cobra.Command{
		Use:   "analyze",
		Short: "Analyze repositories for reconciliation patterns",
		Long: `Analyze Go repositories for Kubernetes controller patterns.

Examples:
  # Analyze repos from a file
  k8s-controller-survey analyze --repos=repos.txt --output=results.jsonl

  # Analyze a specific repo
  k8s-controller-survey analyze --repo=https://github.com/cert-manager/cert-manager

  # Analyze with verbose output
  k8s-controller-survey analyze --repo=https://github.com/cert-manager/cert-manager --verbose`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Collect repos to analyze.
			var repos []models.Repository

			// Load from file if specified.
			if reposFile != "" {
				fileRepos, err := loadReposFromFile(reposFile)
				if err != nil {
					return fmt.Errorf("failed to load repos from file: %w", err)
				}
				repos = append(repos, fileRepos...)
			}

			// Add individual repos from flags.
			for _, url := range repoURLs {
				owner, name := analyzer.ParseRepoURL(url)
				repos = append(repos, models.Repository{
					URL:    url,
					Owner:  owner,
					Name:   name,
					Source: "cli",
				})
			}

			if len(repos) == 0 {
				return fmt.Errorf("no repositories specified")
			}

			// Create work directory.
			if err := os.MkdirAll(workDir, 0755); err != nil {
				return fmt.Errorf("failed to create work directory: %w", err)
			}

			// Create analyzer.
			a := analyzer.NewAnalyzer(workDir, verbose)

			// Create output writer.
			w, err := output.NewWriter(outputFile)
			if err != nil {
				return fmt.Errorf("failed to create output writer: %w", err)
			}
			defer w.Close()

			// Analyze each repo.
			var allReconcilers []models.Reconciler
			for _, repo := range repos {
				log.Printf("Processing repository: %s", repo.URL)

				// Clone repository.
				localPath, err := cloneRepo(repo.URL, workDir, verbose)
				if err != nil {
					log.Printf("Error cloning %s: %v", repo.URL, err)
					continue
				}
				repo.LocalPath = localPath

				// Analyze.
				reconcilers, err := a.AnalyzeRepo(repo)
				if err != nil {
					log.Printf("Error analyzing %s: %v", repo.URL, err)
					continue
				}

				log.Printf("Found %d reconcilers in %s", len(reconcilers), repo.URL)

				// Write results.
				if err := w.WriteReconcilers(reconcilers); err != nil {
					log.Printf("Error writing results: %v", err)
				}

				allReconcilers = append(allReconcilers, reconcilers...)

				// Clean up clone if not keeping.
				if !keepClones {
					if err := os.RemoveAll(localPath); err != nil {
						log.Printf("Warning: failed to remove %s: %v", localPath, err)
					}
				}
			}

			// Print summary.
			summary := output.GenerateSummary(allReconcilers, 10)
			output.PrintSummary(os.Stderr, summary)

			return nil
		},
	}

	cmd.Flags().StringVarP(&reposFile, "repos", "r", "", "File with repo URLs (one per line)")
	cmd.Flags().StringSliceVar(&repoURLs, "repo", nil, "Individual repo URL(s) to analyze")
	cmd.Flags().StringVarP(&outputFile, "output", "o", "", "Output file (JSONL format, default: stdout)")
	cmd.Flags().StringVar(&workDir, "work-dir", "./repos", "Directory for cloning repos")
	cmd.Flags().BoolVar(&keepClones, "keep-clones", false, "Keep cloned repos after analysis")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Verbose output")

	return cmd
}

// reportCmd generates reports from analysis results.
func reportCmd() *cobra.Command {
	var (
		inputFile string
		topN      int
	)

	cmd := &cobra.Command{
		Use:   "report",
		Short: "Generate reports from analysis results",
		Long: `Generate summary reports from JSONL analysis results.

Examples:
  # Generate report from results file
  k8s-controller-survey report --input=results.jsonl`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load reconcilers from file.
			reconcilers, err := loadReconcilersFromFile(inputFile)
			if err != nil {
				return fmt.Errorf("failed to load results: %w", err)
			}

			// Generate summary.
			summary := output.GenerateSummary(reconcilers, topN)

			// Print summary.
			output.PrintSummary(os.Stdout, summary)

			return nil
		},
	}

	cmd.Flags().StringVarP(&inputFile, "input", "i", "", "Input JSONL file with analysis results")
	cmd.Flags().IntVar(&topN, "top", 10, "Number of top reconcilers to show")
	cmd.MarkFlagRequired("input")

	return cmd
}

// loadReposFromFile loads repository URLs from a file.
func loadReposFromFile(path string) ([]models.Repository, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var repos []models.Repository
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// Skip empty lines and comments.
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		owner, name := analyzer.ParseRepoURL(line)
		repos = append(repos, models.Repository{
			URL:    line,
			Owner:  owner,
			Name:   name,
			Source: "file",
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return repos, nil
}

// loadReconcilersFromFile loads reconcilers from a JSONL file.
func loadReconcilersFromFile(path string) ([]models.Reconciler, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var reconcilers []models.Reconciler
	scanner := bufio.NewScanner(file)

	// Increase buffer size for large lines.
	const maxCapacity = 1024 * 1024 // 1MB
	buf := make([]byte, maxCapacity)
	scanner.Buffer(buf, maxCapacity)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var r models.Reconciler
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			log.Printf("Warning: failed to parse line: %v", err)
			continue
		}

		reconcilers = append(reconcilers, r)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return reconcilers, nil
}

// cloneRepo clones a repository to the work directory.
func cloneRepo(repoURL, workDir string, verbose bool) (string, error) {
	// Parse repo URL to get owner and name.
	owner, name := analyzer.ParseRepoURL(repoURL)
	if owner == "" || name == "" {
		return "", fmt.Errorf("invalid repo URL: %s", repoURL)
	}

	localPath := filepath.Join(workDir, owner, name)

	// Check if already exists.
	if _, err := os.Stat(localPath); err == nil {
		if verbose {
			log.Printf("Repository already exists at %s, using existing clone", localPath)
		}
		return localPath, nil
	}

	// Create parent directory.
	if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return "", fmt.Errorf("failed to create directory: %w", err)
	}

	if verbose {
		log.Printf("Cloning %s to %s", repoURL, localPath)
	}

	// Clone with depth 1 for speed.
	cmd := exec.Command("git", "clone", "--depth=1", repoURL, localPath)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if verbose {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git clone failed: %w", err)
	}

	return localPath, nil
}

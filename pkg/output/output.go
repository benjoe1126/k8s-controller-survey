package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/rg0now/k8s-controller-survey/pkg/models"
)

// Writer handles output of analysis results.
type Writer struct {
	file   *os.File
	writer io.Writer
}

// NewWriter creates a new output writer.
func NewWriter(path string) (*Writer, error) {
	if path == "" || path == "-" {
		return &Writer{
			file:   nil,
			writer: os.Stdout,
		}, nil
	}

	file, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("failed to create output file: %w", err)
	}

	return &Writer{
		file:   file,
		writer: file,
	}, nil
}

// WriteReconciler writes a single reconciler as a JSON line.
func (w *Writer) WriteReconciler(r models.Reconciler) error {
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("failed to marshal reconciler: %w", err)
	}

	_, err = fmt.Fprintf(w.writer, "%s\n", data)
	if err != nil {
		return fmt.Errorf("failed to write output: %w", err)
	}

	return nil
}

// WriteReconcilers writes multiple reconcilers as JSON lines.
func (w *Writer) WriteReconcilers(reconcilers []models.Reconciler) error {
	for _, r := range reconcilers {
		if err := w.WriteReconciler(r); err != nil {
			return err
		}
	}
	return nil
}

// Close closes the output file if it was opened.
func (w *Writer) Close() error {
	if w.file != nil {
		return w.file.Close()
	}
	return nil
}

// Summary represents analysis summary statistics.
type Summary struct {
	TotalReconcilers int                 `json:"total_reconcilers"`
	ByClassification map[string]int      `json:"by_classification"`
	ByRepo           map[string]int      `json:"by_repo"`
	SignalFrequency  map[string]int      `json:"signal_frequency"`
	AverageScore     float64             `json:"average_score"`
	TopSoTW          []models.Reconciler `json:"top_sotw,omitempty"`
	TopEdge          []models.Reconciler `json:"top_edge,omitempty"`
}

// GenerateSummary generates a summary from a list of reconcilers.
func GenerateSummary(reconcilers []models.Reconciler, topN int) Summary {
	summary := Summary{
		TotalReconcilers: len(reconcilers),
		ByClassification: make(map[string]int),
		ByRepo:           make(map[string]int),
		SignalFrequency:  make(map[string]int),
	}

	totalScore := 0
	for _, r := range reconcilers {
		summary.ByClassification[r.Classification]++
		summary.ByRepo[r.Repo]++
		totalScore += r.Score

		for _, sig := range r.Signals {
			summary.SignalFrequency[sig.Type]++
		}
	}

	if len(reconcilers) > 0 {
		summary.AverageScore = float64(totalScore) / float64(len(reconcilers))
	}

	// Get top SoTW and edge-triggered reconcilers.
	if topN > 0 {
		summary.TopSoTW = getTopByScore(reconcilers, topN, true)
		summary.TopEdge = getTopByScore(reconcilers, topN, false)
	}

	return summary
}

// getTopByScore gets the top N reconcilers by score (highest or lowest).
func getTopByScore(reconcilers []models.Reconciler, n int, highest bool) []models.Reconciler {
	// Simple selection - could be optimized with heap.
	sorted := make([]models.Reconciler, len(reconcilers))
	copy(sorted, reconcilers)

	// Bubble sort for simplicity (good enough for small N).
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if highest {
				if sorted[j].Score > sorted[i].Score {
					sorted[i], sorted[j] = sorted[j], sorted[i]
				}
			} else {
				if sorted[j].Score < sorted[i].Score {
					sorted[i], sorted[j] = sorted[j], sorted[i]
				}
			}
		}
	}

	if n > len(sorted) {
		n = len(sorted)
	}

	return sorted[:n]
}

// PrintSummary prints a summary to the given writer.
func PrintSummary(w io.Writer, summary Summary) {
	fmt.Fprintf(w, "=== Analysis Summary ===\n\n")
	fmt.Fprintf(w, "Total Reconcilers: %d\n", summary.TotalReconcilers)
	fmt.Fprintf(w, "Average Score: %.2f\n\n", summary.AverageScore)

	fmt.Fprintf(w, "Classification Distribution:\n")
	for class, count := range summary.ByClassification {
		pct := 100.0 * float64(count) / float64(summary.TotalReconcilers)
		fmt.Fprintf(w, "  %s: %d (%.1f%%)\n", class, count, pct)
	}
	fmt.Fprintf(w, "\n")

	fmt.Fprintf(w, "Top Signal Types:\n")
	// Sort by frequency.
	type sigFreq struct {
		Type  string
		Count int
	}
	var freqs []sigFreq
	for sigType, count := range summary.SignalFrequency {
		freqs = append(freqs, sigFreq{sigType, count})
	}
	// Simple bubble sort.
	for i := 0; i < len(freqs); i++ {
		for j := i + 1; j < len(freqs); j++ {
			if freqs[j].Count > freqs[i].Count {
				freqs[i], freqs[j] = freqs[j], freqs[i]
			}
		}
	}
	for i := 0; i < len(freqs) && i < 10; i++ {
		fmt.Fprintf(w, "  %s: %d\n", freqs[i].Type, freqs[i].Count)
	}
	fmt.Fprintf(w, "\n")

	if len(summary.TopSoTW) > 0 {
		fmt.Fprintf(w, "Top SoTW Reconcilers:\n")
		for i, r := range summary.TopSoTW {
			fmt.Fprintf(w, "  %d. %s (score: %d)\n", i+1, r.ID, r.Score)
		}
		fmt.Fprintf(w, "\n")
	}

	if len(summary.TopEdge) > 0 {
		fmt.Fprintf(w, "Top Edge-Triggered Reconcilers:\n")
		for i, r := range summary.TopEdge {
			fmt.Fprintf(w, "  %d. %s (score: %d)\n", i+1, r.ID, r.Score)
		}
		fmt.Fprintf(w, "\n")
	}
}

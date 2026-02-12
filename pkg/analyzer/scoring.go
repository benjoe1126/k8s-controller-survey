package analyzer

import "github.com/rg0now/k8s-controller-survey/pkg/models"

// Classify computes score and classification from signals.
func Classify(signals []models.Signal) (int, string) {
	score := 0
	for _, sig := range signals {
		score += sig.Score
	}

	var classification string
	switch {
	case score <= models.ThresholdEdgeTriggered:
		classification = "edge_triggered"
	case score <= models.ThresholdMostlyEdge:
		classification = "mostly_edge"
	case score <= models.ThresholdMostlySoTW:
		classification = "mostly_sotw"
	default:
		classification = "sotw"
	}

	return score, classification
}

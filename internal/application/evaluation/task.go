package evaluation

import (
	"context"
	"fmt"
)

// Predictor produces system outputs for one offline sample. Implementations
// wrap the real retrieval or failure-analysis pipeline.
type Predictor interface {
	PredictRetrieval(ctx context.Context, sample RetrievalSample) (RetrievalPrediction, error)
	PredictFailure(ctx context.Context, sample FailureSample) (FailurePrediction, error)
}

// Run executes a versioned dataset as an offline evaluation task and returns a
// single report snapshot. A dataset of a given kind only exercises the matching
// predictor path.
func Run(ctx context.Context, dataset Dataset, predictor Predictor) (EvaluationReport, error) {
	if predictor == nil {
		return EvaluationReport{}, fmt.Errorf("%w: evaluation requires a predictor", ErrInvalidDataset)
	}
	switch dataset.Kind {
	case DatasetRetrieval:
		predictions := make([]RetrievalPrediction, 0, len(dataset.Retrieval))
		for _, sample := range dataset.Retrieval {
			prediction, err := predictor.PredictRetrieval(ctx, sample)
			if err != nil {
				return EvaluationReport{}, fmt.Errorf("retrieval sample %q: %w", sample.ID, err)
			}
			prediction.SampleID = sample.ID
			predictions = append(predictions, prediction)
		}
		return EvaluateRetrieval(dataset, predictions)
	case DatasetFailure:
		predictions := make([]FailurePrediction, 0, len(dataset.Failure))
		for _, sample := range dataset.Failure {
			prediction, err := predictor.PredictFailure(ctx, sample)
			if err != nil {
				return EvaluationReport{}, fmt.Errorf("failure sample %q: %w", sample.ID, err)
			}
			prediction.SampleID = sample.ID
			predictions = append(predictions, prediction)
		}
		return EvaluateFailure(dataset, predictions)
	default:
		return EvaluationReport{}, fmt.Errorf("%w: dataset kind %q has no evaluation task", ErrInvalidDataset, dataset.Kind)
	}
}

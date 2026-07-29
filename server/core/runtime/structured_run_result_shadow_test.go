// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package runtime_test

import (
	"errors"
	"testing"

	"github.com/runatlantis/atlantis/server/core/runtime"
	. "github.com/runatlantis/atlantis/testing"
)

func TestCompareStructuredRunResult(t *testing.T) {
	noChanges := runtime.StepChangeSummary{}
	oneAdd := runtime.StepChangeSummary{HasChanges: true, Add: 1}
	twoAdds := runtime.StepChangeSummary{HasChanges: true, Add: 2}
	importAndForget := runtime.StepChangeSummary{
		HasChanges: true,
		Import:     1,
		Forget:     2,
	}
	inlineReview := &runtime.StepReview{
		DetailMode:       runtime.StepReviewDetailModeInline,
		InlineDetailPath: "review.txt",
	}
	urlReview := &runtime.StepReview{
		DetailMode: runtime.StepReviewDetailModeURL,
		DetailsURL: "https://example.com/plan",
	}

	tests := []struct {
		name       string
		completion runtime.CompletedRun
		expected   runtime.StructuredRunResultComparison
	}{
		{
			name: "matching no-op",
			completion: runtime.CompletedRun{
				Execution: runtime.RunExecution{
					ConsoleOutput: "No changes. Your infrastructure matches the configuration.\n",
				},
				Result: runtime.StepResultV1{
					Outcome: runtime.StepResultOutcomeSuccess,
					Changes: &noChanges,
					Review:  inlineReview,
				},
			},
			expected: runtime.StructuredRunResultComparisonMatch,
		},
		{
			name: "matching counts and URL detail",
			completion: runtime.CompletedRun{
				Execution: runtime.RunExecution{
					ConsoleOutput: "Plan: 1 to add, 0 to change, 0 to destroy.\n" +
						"==ATLANTIS_PLAN_DIFF_V1==\n" +
						"mode:url_if_oversize\n" +
						"inline_bytes:60000\n" +
						"max_bytes:55000\n",
				},
				Result: runtime.StepResultV1{
					Outcome: runtime.StepResultOutcomeSuccess,
					Changes: &oneAdd,
					Review:  urlReview,
				},
			},
			expected: runtime.StructuredRunResultComparisonMatch,
		},
		{
			name: "matching import and forget counts",
			completion: runtime.CompletedRun{
				Execution: runtime.RunExecution{
					ConsoleOutput: "Plan: 1 to import, 0 to add, 0 to change, 0 to destroy, 2 to forget.\n",
				},
				Result: runtime.StepResultV1{
					Outcome: runtime.StepResultOutcomeSuccess,
					Changes: &importAndForget,
					Review:  inlineReview,
				},
			},
			expected: runtime.StructuredRunResultComparisonMatch,
		},
		{
			name: "matching error outcome",
			completion: runtime.CompletedRun{
				Execution: runtime.RunExecution{Err: errors.New("exit status 1")},
				Result: runtime.StepResultV1{
					Outcome: runtime.StepResultOutcomeError,
				},
			},
			expected: runtime.StructuredRunResultComparisonMatch,
		},
		{
			name: "legacy facts unavailable",
			completion: runtime.CompletedRun{
				Execution: runtime.RunExecution{ConsoleOutput: "plan completed\n"},
				Result: runtime.StepResultV1{
					Outcome: runtime.StepResultOutcomeSuccess,
					Changes: &oneAdd,
				},
			},
			expected: runtime.StructuredRunResultComparisonLegacyUnavailable,
		},
		{
			name: "outcome mismatch",
			completion: runtime.CompletedRun{
				Execution: runtime.RunExecution{},
				Result: runtime.StepResultV1{
					Outcome: runtime.StepResultOutcomeError,
				},
			},
			expected: runtime.StructuredRunResultComparisonOutcomeMismatch,
		},
		{
			name: "change presence mismatch",
			completion: runtime.CompletedRun{
				Execution: runtime.RunExecution{
					ConsoleOutput: "Plan: 1 to add, 0 to change, 0 to destroy.\n",
				},
				Result: runtime.StepResultV1{
					Outcome: runtime.StepResultOutcomeSuccess,
					Changes: &noChanges,
				},
			},
			expected: runtime.StructuredRunResultComparisonChangePresenceMismatch,
		},
		{
			name: "count mismatch",
			completion: runtime.CompletedRun{
				Execution: runtime.RunExecution{
					ConsoleOutput: "Plan: 1 to add, 0 to change, 0 to destroy.\n",
				},
				Result: runtime.StepResultV1{
					Outcome: runtime.StepResultOutcomeSuccess,
					Changes: &twoAdds,
				},
			},
			expected: runtime.StructuredRunResultComparisonCountMismatch,
		},
		{
			name: "review detail mismatch",
			completion: runtime.CompletedRun{
				Execution: runtime.RunExecution{
					ConsoleOutput: "Plan: 1 to add, 0 to change, 0 to destroy.\n",
				},
				Result: runtime.StepResultV1{
					Outcome: runtime.StepResultOutcomeSuccess,
					Changes: &oneAdd,
					Review:  urlReview,
				},
			},
			expected: runtime.StructuredRunResultComparisonReviewDetailMismatch,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			Equals(t, test.expected, runtime.CompareStructuredRunResult(test.completion))
		})
	}
}

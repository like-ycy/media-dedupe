package score

import (
	"testing"

	"media-dedupe/internal/model"
)

func TestChooseActions(t *testing.T) {
	q := map[int64]float64{1: 0.9, 2: 0.5, 3: 0.4}
	actions := ChooseActions(q, 0.02)
	if actions[1] != model.ActionKeep {
		t.Fatalf("expected keep for 1, got %v", actions[1])
	}
	if actions[2] != model.ActionCleanup || actions[3] != model.ActionCleanup {
		t.Fatalf("expected cleanup, got %v %v", actions[2], actions[3])
	}

	q2 := map[int64]float64{1: 0.80, 2: 0.80}
	actions2 := ChooseActions(q2, 0.02)
	if actions2[1] != model.ActionReview || actions2[2] != model.ActionReview {
		t.Fatalf("expected review, got %v", actions2)
	}
}

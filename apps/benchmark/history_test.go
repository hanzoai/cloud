package benchmark

import (
	"testing"
	"time"
)

// The case that motivated runs: a model measured badly, then measured again
// later and better. The board must show the NEW number, and the history must
// show both with the improvement between them.
func TestARemeasuredModelImprovesRatherThanAveraging(t *testing.T) {
	mar := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	aug := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	var as []attempt
	// March: 4 of 10 correct.
	for i := 0; i < 10; i++ {
		as = append(as, attempt{
			Benchmark: "gpqa_diamond", ID: string(rune('a' + i)), Model: "m",
			Correct: i < 4, Answer: "x", Run: "mar", At: mar,
		})
	}
	// August, same items, 9 of 10.
	for i := 0; i < 10; i++ {
		as = append(as, attempt{
			Benchmark: "gpqa_diamond", ID: string(rune('a' + i)), Model: "m",
			Correct: i < 9, Answer: "x", Run: "aug", At: aug,
		})
	}

	rows := computeLeaderboard(as, "gpqa_diamond", nil)
	var got *LeaderRow
	for i := range rows {
		if rows[i].Model == "m" {
			got = &rows[i]
		}
	}
	if got == nil || got.Measured == nil {
		t.Fatal("model m missing from the board")
	}
	// 90, not 65. Blending the runs would report the average of a model it no
	// longer is.
	if *got.Measured < 89.9 || *got.Measured > 90.1 {
		t.Fatalf("measured = %.1f, want 90 (the latest run, not the blend)", *got.Measured)
	}
	if got.N != 10 {
		t.Fatalf("n = %d, want 10 — coverage is the run's, not the sum of runs", got.N)
	}
	if got.Run != "aug" {
		t.Fatalf("run = %q, want aug", got.Run)
	}
	if got.MeasuredAt == nil || !got.MeasuredAt.Equal(aug) {
		t.Fatalf("measuredAt = %v, want %v", got.MeasuredAt, aug)
	}
}

func TestHistoryShowsBothRunsAndTheDelta(t *testing.T) {
	mar := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	aug := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	var as []attempt
	for i := 0; i < 10; i++ {
		as = append(as, attempt{Benchmark: "gpqa_diamond", ID: string(rune('a' + i)), Model: "m", Correct: i < 4, Answer: "x", Run: "mar", At: mar})
		as = append(as, attempt{Benchmark: "gpqa_diamond", ID: string(rune('a' + i)), Model: "m", Correct: i < 9, Answer: "x", Run: "aug", At: aug})
	}

	data := computeHistory(as, "gpqa_diamond", "m")
	if len(data) != 1 || len(data[0].Points) != 2 {
		t.Fatalf("history = %+v, want one model with two runs", data)
	}
	p := data[0].Points
	if p[0].Run != "mar" || p[1].Run != "aug" {
		t.Fatalf("points are not oldest-first: %v then %v", p[0].Run, p[1].Run)
	}
	if p[1].Delta == nil || *p[1].Delta < 49.9 || *p[1].Delta > 50.1 {
		t.Fatalf("delta = %v, want +50", p[1].Delta)
	}
	if data[0].Trend == nil || *data[0].Trend < 49.9 {
		t.Fatalf("trend = %v, want +50", data[0].Trend)
	}
}

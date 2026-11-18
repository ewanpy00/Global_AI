package eval

import (
	"context"
	"testing"

	"github.com/ewanpy00/Global_AI/backend/internal/demo"
	"github.com/ewanpy00/Global_AI/backend/internal/verify"
)

func TestDatasetsLoad(t *testing.T) {
	for _, d := range []bool{false, true} {
		ds, err := Load("", d)
		if err != nil {
			t.Fatal(err)
		}
		if len(ds.Claims) == 0 || len(ds.Answers) == 0 {
			t.Fatalf("empty dataset (demo=%v)", d)
		}
		ids := map[string]bool{}
		for _, c := range ds.Claims {
			if ids[c.ID] {
				t.Fatalf("duplicate id %s", c.ID)
			}
			ids[c.ID] = true
		}
	}
}

func TestBenchmarkOnDemo(t *testing.T) {
	ds, _ := Load("", true)
	sr := &demo.Search{}
	e := &verify.Engine{LLM: &demo.LLM{}, Search: sr, Fetch: sr, MaxClaims: 12, Concurrency: 4}
	base := Run(context.Background(), e, ds, Options{Label: "baseline", Extraction: true})
	e.Features = verify.AllFeatures()
	full := Run(context.Background(), e, ds, Options{Label: "full", Extraction: true})
	if full.Metrics.Accuracy != 1 || base.Metrics.Accuracy >= full.Metrics.Accuracy {
		t.Fatalf("accuracy baseline %.3f full %.3f", base.Metrics.Accuracy, full.Metrics.Accuracy)
	}
	if full.Extraction == nil || full.Extraction.Recall != 1 || full.Extraction.SkipViolations != 0 {
		t.Fatalf("extraction: %+v", full.Extraction)
	}
	if md := Markdown([]Result{*base, *full}); len(md) < 100 {
		t.Fatal("markdown table missing")
	}
}

func TestComputeMetrics(t *testing.T) {
	ok := true
	m := Compute([]ClaimOutcome{
		{Gold: Stale, Predicted: Stale, Score: 10, ValueOK: &ok},
		{Gold: Stale, Predicted: Unverifiable, Score: 55},
		{Gold: Current, Predicted: Current, Score: 90},
		{Gold: Unverifiable, Predicted: Unverifiable, Score: 50},
	})
	if m.Accuracy != 0.75 || m.Coverage != 0.667 || m.PerClass[Stale].Recall != 0.5 || m.PerClass[Stale].Precision != 1 {
		t.Fatalf("metrics: %+v", m)
	}
	if m.ValueAccuracy == nil || *m.ValueAccuracy != 1 {
		t.Fatalf("value accuracy: %v", m.ValueAccuracy)
	}
}

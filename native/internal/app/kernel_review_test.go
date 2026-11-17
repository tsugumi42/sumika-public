package app

import (
	"strings"
	"testing"

	"sumika-native/internal/config"
	"sumika-native/internal/llm"
	"sumika-native/internal/turn"
)

func TestKernelReviewDefaultsAndPrerequisites(t *testing.T) {
	t.Parallel()
	c := config.Defaults()
	tc := turn.Config{Models: map[string]string{"r": "role/model"}}
	if err := ConfigureKernelReviews(c.Agent.ReviewConfig, nil, &tc, nil, ReviewHost{}); err != nil {
		t.Fatal(err)
	}
	if tc.Advisor != nil || tc.Completion != nil || tc.Models["r"] != "role/model" {
		t.Fatalf("disabled review changed role: %+v", tc)
	}
	c.Agent.Advisor.Enabled = true
	if err := ConfigureKernelReviews(c.Agent.ReviewConfig, nil, &tc, nil, ReviewHost{}); err == nil {
		t.Fatal("enabled without model")
	}
	c.Agent.Advisor.Model = "review/model"
	if err := ConfigureKernelReviews(c.Agent.ReviewConfig, []llm.Model{{ID: "role/model"}}, &tc, nil, ReviewHost{}); err == nil {
		t.Fatal("unknown review route accepted")
	}
	if err := ConfigureKernelReviews(c.Agent.ReviewConfig, []llm.Model{{ID: "review/model"}}, &tc, nil, ReviewHost{}); err == nil || !strings.Contains(err.Error(), "端口") {
		t.Fatalf("missing K1/K2/K3 silently bypassed: %v", err)
	}
}
func TestKernelReviewConfigBoundary(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"agent:\n  advisor:\n    enabled: true\n",
		"agent:\n  advisor:\n    maxFindings: 5\n",
		"agent:\n  advisor:\n    mode: continuous\n",
		"agent:\n  advisor:\n    maxReviewsPerRoot: 4\n",
		"agent:\n  verify:\n    maxContinuations: 2\n",
		"agent:\n  verify:\n    enabled: true\n",
	} {
		if _, err := config.Parse([]byte(raw), "fixture.yml"); err == nil {
			t.Fatalf("invalid config accepted: %s", raw)
		}
	}
	c, err := config.Parse([]byte("agent:\n  advisor:\n    enabled: true\n    model: review/a\n  verify:\n    enabled: true\n    model: review/v\n"), "fixture.yml")
	if err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: c}
	c.Source = ""
	a.cfg = c
	if err := a.initModelRuntime([]llm.Model{{ID: "review/a"}, {ID: "review/v"}}); err != nil {
		t.Fatal(err)
	}
	s := a.currentModels()
	if s.ReviewModels["advisor"] != "review/a" || s.ReviewModels["verify"] != "review/v" {
		t.Fatalf("review purposes missing: %+v", s.ReviewModels)
	}
}

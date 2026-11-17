package config

import "fmt"

// ReviewConfig 嵌进 agent；关闭时不创建审阅运行或文件。
type ReviewConfig struct {
	Advisor AdvisorConfig `yaml:"advisor"`
	Verify  VerifyConfig  `yaml:"verify"`
}
type AdvisorConfig struct {
	Enabled           bool   `yaml:"enabled"`
	Model             string `yaml:"model"`
	Mode              string `yaml:"mode"`
	MaxActive         int    `yaml:"maxActive"`
	TimeoutMs         int    `yaml:"timeoutMs"`
	MaxInputTokens    int    `yaml:"maxInputTokens"`
	MaxOutputTokens   int    `yaml:"maxOutputTokens"`
	MaxFindings       int    `yaml:"maxFindings"`
	MaxReviewsPerRoot int    `yaml:"maxReviewsPerRoot"`
}
type VerifyConfig struct {
	Enabled          bool   `yaml:"enabled"`
	Model            string `yaml:"model"`
	TimeoutMs        int    `yaml:"timeoutMs"`
	MaxOutputTokens  int    `yaml:"maxOutputTokens"`
	MaxContinuations int    `yaml:"maxContinuations"`
}

func DefaultReviews() ReviewConfig {
	return ReviewConfig{
		Advisor: AdvisorConfig{Mode: "checkpoint", MaxActive: 2, TimeoutMs: 30000, MaxInputTokens: 12000, MaxOutputTokens: 1024, MaxFindings: 4, MaxReviewsPerRoot: 3},
		Verify:  VerifyConfig{TimeoutMs: 30000, MaxOutputTokens: 2048, MaxContinuations: 1},
	}
}
func (c ReviewConfig) Check() error {
	a, v := c.Advisor, c.Verify
	if a.Enabled && a.Model == "" {
		return fmt.Errorf("agent.advisor.model：开启必须选模型")
	}
	if v.Enabled && v.Model == "" {
		return fmt.Errorf("agent.verify.model：开启必须选模型")
	}
	if a.Mode != "checkpoint" || a.MaxActive < 1 || a.MaxActive > 2 || a.TimeoutMs < 1 || a.TimeoutMs > 30000 || a.MaxInputTokens < 1 || a.MaxInputTokens > 12000 || a.MaxOutputTokens < 1 || a.MaxOutputTokens > 1024 || a.MaxFindings < 1 || a.MaxFindings > 4 || a.MaxReviewsPerRoot < 1 || a.MaxReviewsPerRoot > 3 {
		return fmt.Errorf("agent.advisor：参数超出 checkpoint 审阅边界")
	}
	if v.TimeoutMs < 1 || v.TimeoutMs > 30000 || v.MaxOutputTokens < 1 || v.MaxOutputTokens > 2048 || v.MaxContinuations < 0 || v.MaxContinuations > 1 {
		return fmt.Errorf("agent.verify：参数超出有界核验边界")
	}
	return nil
}

package config

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
)

// WorkInt 拒绝 YAML 隐式截断小数；只在配置输入边界校验。
type WorkInt int64

func (n *WorkInt) UnmarshalYAML(b []byte) error {
	v, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return fmt.Errorf("agent.work：必须是正整数")
	}
	*n = WorkInt(v)
	return nil
}

type Work struct {
	InlineBytes      WorkInt `yaml:"inlineBytes"`
	ResultMaxBytes   WorkInt `yaml:"resultMaxBytes"`
	RootMaxBytes     WorkInt `yaml:"rootMaxBytes"`
	ReadPageBytes    WorkInt `yaml:"readPageBytes"`
	KeepRecentTokens WorkInt `yaml:"keepRecentTokens"`
	SummaryMaxTokens WorkInt `yaml:"summaryMaxTokens"`
	CompactTimeoutMs WorkInt `yaml:"compactTimeoutMs"`
	UnreferencedDays WorkInt `yaml:"unreferencedDays"`
	RecoveryPageSize WorkInt `yaml:"recoveryPageSize"`
}

func DefaultWork() Work {
	return Work{16384, 16 << 20, 64 << 20, 20480, 20000, 4096, 180000, 30, 200}
}
func (w Work) Validate() error {
	for _, b := range []struct {
		name        string
		n, min, max WorkInt
	}{
		{"inlineBytes", w.InlineBytes, 1024, 65536}, {"resultMaxBytes", w.ResultMaxBytes, 1, 64 << 20},
		{"rootMaxBytes", w.RootMaxBytes, 1, 256 << 20}, {"readPageBytes", w.ReadPageBytes, 1024, 65536},
		{"keepRecentTokens", w.KeepRecentTokens, 1, 200000}, {"summaryMaxTokens", w.SummaryMaxTokens, 1, 32768},
		{"compactTimeoutMs", w.CompactTimeoutMs, 1, 180000}, {"unreferencedDays", w.UnreferencedDays, 1, 3650},
		{"recoveryPageSize", w.RecoveryPageSize, 1, 1000},
	} {
		if b.n < b.min || b.n > b.max {
			return fmt.Errorf("agent.work.%s：必须在 %d～%d", b.name, b.min, b.max)
		}
	}
	if w.RootMaxBytes < w.ResultMaxBytes {
		return fmt.Errorf("agent.work.rootMaxBytes：不能小于 resultMaxBytes")
	}
	return nil
}
func (w *Work) UnmarshalYAML(b []byte) error {
	type plain Work
	v := plain(DefaultWork())
	if err := yaml.UnmarshalWithOptions(b, &v, yaml.Strict()); err != nil {
		return err
	}
	if err := Work(v).Validate(); err != nil {
		return err
	}
	*w = Work(v)
	return nil
}

func validateWorkRaw(raw map[string]any) error {
	agent, _ := raw["agent"].(map[string]any)
	value, present := agent["work"]
	if !present {
		return nil
	}
	work, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("agent.work：必须是配置对象")
	}
	for key, value := range work {
		if value == nil {
			return fmt.Errorf("agent.work.%s：不能为 null", key)
		}
	}
	return nil
}

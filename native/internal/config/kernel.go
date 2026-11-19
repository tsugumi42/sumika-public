package config

import "fmt"

type Lifecycle struct {
	MaxPendingPerConversation int    `yaml:"maxPendingPerConversation"`
	MaxPendingGlobal          int    `yaml:"maxPendingGlobal"`
	DrainTimeoutMS            int    `yaml:"drainTimeoutMs"`
	StopGraceMS               int    `yaml:"stopGraceMs"`
	SnapshotIntervalMS        int    `yaml:"snapshotIntervalMs"`
	RecoverInputs             string `yaml:"recoverInputs"`
}

func DefaultLifecycle() Lifecycle {
	return Lifecycle{MaxPendingPerConversation: 32, MaxPendingGlobal: 256, DrainTimeoutMS: 10000, StopGraceMS: 5000, SnapshotIntervalMS: 50, RecoverInputs: "hold"}
}
func (l Lifecycle) check() []string {
	var bad []string
	for _, r := range []struct {
		key             string
		value, min, max int
	}{
		{"maxPendingPerConversation", l.MaxPendingPerConversation, 1, 256},
		{"maxPendingGlobal", l.MaxPendingGlobal, 1, 2048},
		{"drainTimeoutMs", l.DrainTimeoutMS, 1, 30000},
		{"snapshotIntervalMs", l.SnapshotIntervalMS, 10, 200},
	} {
		if r.value < r.min || r.value > r.max {
			bad = append(bad, fmt.Sprintf("agent.lifecycle.%s：必须在 %d～%d", r.key, r.min, r.max))
		}
	}
	if l.StopGraceMS < 0 || l.StopGraceMS >= l.DrainTimeoutMS {
		bad = append(bad, "agent.lifecycle.stopGraceMs：必须不小于 0 且小于 drainTimeoutMs")
	}
	if l.RecoverInputs != "hold" {
		bad = append(bad, "agent.lifecycle.recoverInputs：只允许 hold")
	}
	return bad
}

// fitStopGrace 没写 stopGraceMs 时，默认宽限放不进 drainTimeoutMs 就取它的一半，旧配置升级后照样能启动。写了的照常校验。
func fitStopGrace(raw map[string]any, l *Lifecycle) {
	agent, _ := raw["agent"].(map[string]any)
	life, _ := agent["lifecycle"].(map[string]any)
	if _, written := life["stopGraceMs"]; !written && l.StopGraceMS >= l.DrainTimeoutMS {
		l.StopGraceMS = l.DrainTimeoutMS / 2
	}
}

func validateLifecycleRaw(raw map[string]any) error {
	agent, _ := raw["agent"].(map[string]any)
	value, present := agent["lifecycle"]
	if !present {
		return nil
	}
	life, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("agent.lifecycle：必须是配置对象")
	}
	for _, key := range []string{"maxPendingPerConversation", "maxPendingGlobal", "drainTimeoutMs", "stopGraceMs", "snapshotIntervalMs"} {
		n, ok := life[key]
		if !ok {
			continue
		}
		switch v := n.(type) {
		case int, int64, uint64:
		case float64:
			if v != float64(int64(v)) {
				return fmt.Errorf("agent.lifecycle.%s：必须是整数", key)
			}
		default:
			return fmt.Errorf("agent.lifecycle.%s：必须是整数", key)
		}
	}
	return nil
}

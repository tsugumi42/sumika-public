package memory

import (
	"context"
	"testing"
)

// 本包测试里的假嵌入器，模型名都是编的，不在守卫标定过的模型表里。这里把那些要报 full 的名字登记进去（只在测试二进制里，
// 生产的表 guardCalibratedModels 只有 gte-multilingual-base）；要考"没标定过"的测试，用没登记的名字（下面那条）
func init() {
	for _, name := range []string{"编的嵌入", "编的新模型", "预热测试嵌入", "编的新嵌入"} {
		guardCalibratedModels[name] = true
	}
}

// 规格-Go本地模型 2.7"门槛绑模型"（GM7 验收 3a）：嵌入就绪、句向量预热做完，模型不在"门槛标定过的"表里也报 literal-pending；
// 现在表里只有 gte-multilingual-base（ADR-202 的 0.70 是在它上面量的），回滚到 Qwen 不会悄悄放开发送。
// 改坏检查：去掉 GuardLevel 里的 !GuardCalibrated(...)，除 gte 以外的几行都红
func TestGuardLevelNeedsCalibratedModel(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		model, want string
	}{
		{"gte-multilingual-base", "full"},
		{"qwen3-embedding-0.6b-local", "literal-pending"}, // 现在生产跑的、回滚会回到的模型
		{"qwen3-embedding-0.6b", "literal-pending"},
		{"没量过门槛的模型", "literal-pending"},
		{"GTE-multilingual-base", "literal-pending"}, // 只认这一个串，大小写不同的不算
		{"gte-multilingual-base ", "literal-pending"},
	} {
		t.Run(tc.model, func(t *testing.T) {
			t.Parallel()
			m, _, _, _ := outboundRig(t, "主人把备用电池收在蓝柜底层")
			m.embed.(*outboundFakeEmbed).model = tc.model
			m.warmSentences(context.Background()) // 按当前模型把句向量补齐：别的条件都满足，只剩模型这一条
			if got := m.GuardLevel(); got != tc.want {
				t.Fatalf("模型 %q：守卫能力=%q，要 %q", tc.model, got, tc.want)
			}
		})
	}
	if !GuardCalibrated("gte-multilingual-base") || GuardCalibrated("qwen3-embedding-0.6b-local") || GuardCalibrated("") {
		t.Fatal("标定过的模型表：只有 gte-multilingual-base（空串更不算）")
	}
}

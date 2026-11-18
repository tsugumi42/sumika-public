package memory

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"sumika-native/internal/embed"
)

type guardStatusEmbed struct {
	*outboundFakeEmbed
	status embed.Status
}

func (e guardStatusEmbed) Status() (embed.Status, string) { return e.status, "" }

func TestGuardLevelDependencies(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*Memory)
		want   string
	}{
		{"三步齐全且冷缓存可用", func(*Memory) {}, "full"},
		{"只缺嵌入", func(m *Memory) { m.embed = nil }, "literal-pending"},
		{"嵌入没选模型", func(m *Memory) { m.embed.(*outboundFakeEmbed).model = "" }, "literal-pending"},
		{"本地嵌入未就绪", func(m *Memory) { m.embed = guardStatusEmbed{m.embed.(*outboundFakeEmbed), embed.StatusDown} }, "literal-pending"},
		{"本地嵌入就绪", func(m *Memory) { m.embed = guardStatusEmbed{m.embed.(*outboundFakeEmbed), embed.StatusReady} }, "full"},
		{"缺生成器", func(m *Memory) { m.opt.Gen = nil }, ""},
		{"缺模型选择器", func(m *Memory) { m.opt.Model = nil }, ""},
		{"没选后台模型", func(m *Memory) { m.opt.Model = func() string { return "  " } }, ""},
		{"没有缓存", func(m *Memory) { m.index = nil }, ""},
		{"缓存已关闭", func(m *Memory) { m.index.Close() }, ""},
		{"句缓存表缺失", func(m *Memory) {
			if _, err := m.index.db.Exec(`DROP TABLE memory_sentences`); err != nil {
				t.Fatal(err)
			}
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, _, _, _ := outboundRig(t, "主人把备用电池收在蓝柜底层")
			m.warmSentences(context.Background()) // 句向量预热做完（没做完时 full 要降成 literal-pending，见 TestGuardLevelNeedsWarmup）
			tc.change(m)
			if got := m.GuardLevel(); got != tc.want {
				t.Fatalf("守卫能力=%q，要 %q", got, tc.want)
			}
		})
	}
}

func TestCheckRepeatWithoutConfirmation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, text string
		want       []string
	}{
		{"字面命中", "主人把备用电池收在蓝柜底层", []string{"f:100"}},
		{"相似度命中", "他留着那几节电池", []string{"f:100"}},
		{"两者都没中", "今天吃面条", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, e, g, _ := outboundRig(t, "主人把备用电池收在蓝柜底层")
			e.down = tc.name == "字面命中" // 字面必须在嵌入不可用时仍能拦住。
			e.vector = func(s string) []float32 {
				if strings.Contains(s, "面条") {
					return []float32{0, 1}
				}
				return []float32{1, 0}
			}
			calls := 0
			g.answer = func(string) string { calls++; return `{"leaked":"no"}` }
			got, err := m.CheckRepeat(context.Background(), tc.text, outboundGroup())
			if err != nil || !reflect.DeepEqual(got, tc.want) || calls != 0 || m.GuardRoute() != "normal" {
				t.Fatalf("复读拦下=%v，错误=%v，模型调用=%d，路=%s；要 %v、0 次模型调用", got, err, calls, m.GuardRoute(), tc.want)
			}
		})
	}
}

func TestOpenPendingEmbeddingKeepsVectors(t *testing.T) {
	t.Parallel()
	for _, status := range []embed.Status{embed.StatusStarting, embed.StatusDown} {
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			file := bunDb(t, [][]string{{"1", "fact", "active", "主人把备用电池收在蓝柜底层", "owner", "owner"}}, nil)
			x, err := OpenIndex(context.Background(), SidecarPath(file))
			if err != nil {
				t.Fatal(err)
			}
			seed := &outboundFakeEmbed{model: "编的旧模型", vector: func(string) []float32 { return []float32{0, 1} }}
			oldText := "主人以前把电池放在抽屉里"
			if n, err := x.SyncVectors(context.Background(), []Item{{Ref: "f:1", Text: oldText}}, seed, 64); err != nil || n != 1 {
				x.Close()
				t.Fatalf("准备旧向量：%d %v", n, err)
			}
			if err := x.Close(); err != nil {
				t.Fatal(err)
			}
			calls := 0
			e := &guardStatusEmbed{outboundFakeEmbed: &outboundFakeEmbed{model: "编的新模型", vector: func(string) []float32 {
				calls++
				return []float32{1, 0}
			}}, status: status}
			m, err := Open(context.Background(), file, e, nil, Options{
				Gen: &outboundFakeGen{}, Model: func() string { return "编的后台模型" },
			})
			if err != nil {
				t.Fatalf("嵌入未就绪不应阻塞打开记忆：%v", err)
			}
			defer m.Close()
			var model, hash string
			var vec []byte
			if err := m.index.db.QueryRow(`SELECT model, content_hash, vec FROM memory_vectors WHERE ref = 'f:1'`).Scan(&model, &hash, &vec); err != nil {
				t.Fatalf("打开记忆删了旧向量：%v", err)
			}
			if model != seed.model || hash != contentHash(oldText) || !reflect.DeepEqual(vec, encodeVec([]float32{0, 1})) || calls != 0 || m.GuardLevel() != "literal-pending" {
				t.Fatalf("未就绪时不应同步向量：模型=%q，指纹=%q，向量=%v，调用=%d，能力=%q", model, hash, vec, calls, m.GuardLevel())
			}
			e.status = embed.StatusReady
			got, err := m.CheckRepeat(context.Background(), "他留着那几节电池", outboundGroup())
			if err != nil || !reflect.DeepEqual(got, []string{"f:1"}) || calls == 0 || m.GuardLevel() != "full" {
				t.Fatalf("同一个嵌入恢复后应按需补句向量：拦下=%v，错误=%v，调用=%d，能力=%q", got, err, calls, m.GuardLevel())
			}
		})
	}
}

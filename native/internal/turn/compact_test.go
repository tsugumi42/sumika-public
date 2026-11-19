package turn

// 压缩里不碰库、不调模型的部分：切在哪、摘要靠不靠得住。照搬 Bun 的 test/compact.test.ts（同一组例子、同一组预期）

import (
	"reflect"
	"strings"
	"testing"

	"sumika-native/internal/history"
	"sumika-native/internal/llm"
	"sumika-native/internal/prompts"
)

// agent.context 接进来（规格第七节 T21）：零值用 Bun 的默认；窗口封顶取小的；本地闲聊满了压缩，关掉的、渠道的只带最近的话
func TestContextLimits(t *testing.T) {
	t.Parallel()
	m := llm.Model{ContextLength: 128_000}
	b := &Built{Local: true}
	b.refresh(Config{}, m)
	if b.Window != 128_000 || b.Hard != 108_800 || b.Soft != 89_600 || b.Reserve != 16_384 || b.CompactKind != "summary" {
		t.Fatalf("默认：%+v", b)
	}
	b.refresh(Config{Context: ContextLimits{Window: 64_000}}, m)
	if b.Window != 64_000 || b.Hard != 47_616 {
		t.Fatalf("封顶 64000：窗口 %d，硬门槛 %v", b.Window, b.Hard)
	}
	b.refresh(Config{Context: ContextLimits{Window: 1_000_000}}, m)
	if b.Window != 128_000 {
		t.Fatalf("封顶比模型的大，照模型的：%d", b.Window)
	}
	b.refresh(Config{Context: ContextLimits{NoAutoCompact: true}}, m)
	if b.CompactKind != "trim" {
		t.Fatal("关掉自动压缩只带最近的话")
	}
	ch := &Built{Local: false}
	ch.refresh(Config{}, m)
	if ch.CompactKind != "trim" {
		t.Fatal("渠道的会话只带最近的话")
	}
	if k := (ContextLimits{}).keepTokens(); k != 20_000 {
		t.Fatalf("keepTokens 默认 20000：%d", k)
	}
}

// 压缩提示词里的称呼不能是空的：内部子运行重建出来的 Built 没填 OwnerCall 时用"所有者"
func TestCompactOwnerLabel(t *testing.T) {
	t.Parallel()
	if got := (&Built{}).ownerLabel(); got != "所有者" {
		t.Fatalf("没填称呼时压缩提示词里是 %q，要 所有者", got)
	}
	if got := (&Built{OwnerCall: "主人"}).ownerLabel(); got != "主人" {
		t.Fatalf("填了称呼时压缩提示词里是 %q，要 主人", got)
	}
	ins, err := prompts.Store{Root: factoryRoot(t)}.Render("compact-chat", compactVars(&Built{}, "这一句"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ins.Text, "不是所有者说的话") || strings.Contains(ins.Text, "不是说的话") {
		t.Fatalf("压缩提示词里称呼空了：\n%s", ins.Text)
	}
}

func cev(seq int64, who, text string) history.Event {
	return history.Event{Seq: seq, Kind: "message", ActorKind: who, Text: text}
}

func TestCutPoint(t *testing.T) {
	t.Parallel()
	events := []history.Event{cev(1, "owner", strings.Repeat("一", 50)), cev(2, "character", strings.Repeat("二", 50)), cev(3, "owner", strings.Repeat("三", 50)),
		cev(4, "character", strings.Repeat("四", 50)), cev(5, "owner", "五"), cev(6, "character", "六")}
	for _, c := range []struct {
		name   string
		events []history.Event
		keep   int
		want   int64
	}{
		{"只留最近一轮", events, 20, 5},
		{"再往前留一轮（50 + 50 + 4×2 + 最近一轮）", events, 120, 3},
		{"全都在保留的范围里，不用压", events, 10_000, 0},
		{"只有一轮", events[:2], 20, 0},
		{"通知不算", append(append([]history.Event(nil), events...), history.Event{Seq: 7, Kind: "notice", ActorKind: "owner", Text: "通知"}), 20, 5},
	} {
		if got := cutPoint(c.events, c.keep, 0); got != c.want {
			t.Errorf("%s：切在 %d，应该是 %d", c.name, got, c.want)
		}
	}
}

func TestCutPointNotOnHerReply(t *testing.T) {
	t.Parallel()
	events := []history.Event{cev(1, "owner", "早"), cev(2, "character", "早呀"), cev(3, "owner", "吃了吗"), cev(4, "character", "吃了"), cev(5, "character", "你呢")}
	if got := cutPoint(events, 1, 0); got != 3 {
		t.Fatalf("最新的一条是她的话，也切在主人那句前面：%d", got)
	}
}

func TestCutPointMaxSeq(t *testing.T) {
	t.Parallel()
	events := []history.Event{cev(1, "owner", "一"), cev(2, "character", "二"), cev(3, "owner", "三"), cev(4, "character", "四"), cev(5, "owner", "五"), cev(6, "character", "六")}
	for _, c := range []struct{ maxSeq, want int64 }{{0, 5}, {4, 3}, {2, 0}} {
		if got := cutPoint(events, 1, c.maxSeq); got != c.want {
			t.Errorf("maxSeq %d：切在 %d，应该是 %d", c.maxSeq, got, c.want)
		}
	}
}

func TestPathsIn(t *testing.T) {
	t.Parallel()
	got := pathsIn(`改了 D:\Projects\notes\a.md 和 web/src/App.tsx，还有 ./docs/待办.md。`)
	if want := []string{`D:\Projects\notes\a.md`, "web/src/App.tsx", "./docs/待办.md"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("%q", got)
	}
}

func TestSummaryProblems(t *testing.T) {
	t.Parallel()
	source := `主人让我改 D:\notes\a.md 的标题`
	good := "## 目标\n改 D:\\notes\\a.md 的标题，统一成日期加题目"
	check := func(summary string, before, after int) string {
		return strings.Join(summaryProblems(summary, source, before, after, 10_000), "；")
	}
	if p := check(good, 5000, 1000); p != "" {
		t.Fatalf("正常的没问题：%s", p)
	}
	for name, c := range map[string]struct {
		summary       string
		before, after int
		want          string
	}{
		"太短":      {"好", 5000, 1000, "太短"},
		"套话":      {"## 目标\n我是 AI，没有记忆，所以记不住之前的事情了", 5000, 1000, "套话"},
		"找不到的路径":  {strings.Replace(good, `D:\notes\a.md`, `D:\notes\b.md`, 1), 5000, 1000, "找不到"},
		"没降到八成以下": {good, 20_000, 9000, "八成"},
		"没怎么变小":   {good, 1000, 1200, "没怎么变小"},
	} {
		if p := check(c.summary, c.before, c.after); !strings.Contains(p, c.want) {
			t.Errorf("%s：%q 里应该有「%s」", name, p, c.want)
		}
	}
	if p := check(strings.Replace(good, `D:\notes\a.md`, "d:/NOTES/a.md", 1), 5000, 1000); p != "" {
		t.Fatalf("斜杠方向、大小写不算不一样：%s", p)
	}
}

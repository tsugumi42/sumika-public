package chatui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 流式时的 Markdown 量排版缓存（段落排版缓存，body.go mdShapeCache）：每一步都得和整篇冷启动量出来的一模一样。
var mdStreamCorpus = []struct{ name, text string }{
	{"代码块中途", "先看一段代码：\n\n```go\nfunc main() {\n\tfmt.Println(\"你好\")\n}\n```\n\n然后是收尾的一句话。"},
	{"未闭合围栏", "前面一段。\n\n```python\nprint('a')\n\n这一行本来是正文但围栏没闭合\n"},
	{"围栏后补闭合", "说明文字\n```\nline1\nline2\n```\n后面还有字"},
	{"无序和有序列表", "清单：\n\n- 第一项 **加粗** 的内容\n- 第二项\n  续行内容跟在后面\n- 第三项\n\n1. 步骤一\n2. 步骤二\n3. 步骤三\n\n结束。"},
	{"列表续行", "- 项目一\n继续写同一项的下一行（懒续行）\n\n普通段落"},
	{"引用", "> 引用的第一行\n> 引用的第二行\n\n引用之后的正文，长到可能折行折行折行折行折行折行折行折行折行折行折行折行折行折行折行折行折行。"},
	{"表格", "| 名称 | 数值 |\n| --- | --- |\n| 甲 | 1 |\n| 乙 | 2 |\n\n表格后的话。"},
	{"标题和setext", "# 大标题\n\n正文第一段\n\n小标题\n---\n\n第二段\n=\n\n尾巴"},
	{"中英混排", "Streaming 输出 works well，中文 English 混排 `inline code` 与 **bold 粗体** 和 *italic*，https://example.com/a/b/c 链接。\n\nSecond paragraph 第二段 with more words to wrap around the line width limit of the bubble."},
	{"段间插空行", "第一段内容\n第二段内容\n\n\n\n第三段内容\n\n第四段"},
	{"脚注和引用定义", "正文里有脚注[^a]和链接[foo]。\n\n另一段也有[^b]。\n\n[^a]: 脚注甲\n[^b]: 脚注乙\n\n[foo]: https://example.com"},
	{"行内HTML", "文字<b>粗</b>和<sub>下标</sub>以及<br>换行，<span>未知标签</span>。"},
}

func mdTestHost() Host {
	return Host{T: Derive(DefaultSettings()), Column: 720, NoEnter: true, MD: NewMD()}
}

// measureMD 在一帧里量一条消息的 Markdown 正文。
func measureMD(h Host, id, text string, st State, width float32) mdLayout {
	var l mdLayout
	m := &Msg{ID: id, Text: text, Markdown: true, State: st}
	tt := ui.NewTester(func(c *ui.Context) { l = h.mdMeasure(m, width) }, 100, 100)
	tt.Frame()
	return l
}

// 逐个字追加，每一步的量测结果（高度估值、各段每行宽、行数）和同一段文字整篇冷启动量的完全相等。
func TestMDShapeCacheStreamEqualsFull(t *testing.T) {
	for _, cs := range mdStreamCorpus {
		for _, width := range []float32{180, 520} {
			h := mdTestHost()
			rs := []rune(cs.text)
			for i := 1; i <= len(rs); i++ {
				prefix := string(rs[:i])
				got := measureMD(h, "live", prefix, Streaming, width)
				want := measureMD(mdTestHost(), "cold", prefix, Streaming, width)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("%s 宽%.0f 第 %d 个字（…%q）增量 %+v 与整篇 %+v 不一致", cs.name, width, i, tail(prefix), got, want)
				}
			}
			// 流式结束（State 变了）也一样
			got := measureMD(h, "live", cs.text, Sent, width)
			want := measureMD(mdTestHost(), "cold", cs.text, Sent, width)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s 宽%.0f 结束后增量 %+v 与整篇 %+v 不一致", cs.name, width, got, want)
			}
		}
	}
}

// 中途改宽度、来回换宽度也和冷启动一致。
func TestMDShapeCacheWidthSwitch(t *testing.T) {
	h := mdTestHost()
	text := strings.Repeat("这是一个会折行的段落 with some English words mixed in。", 6) + "\n\n" + strings.Repeat("第二段文字，", 20)
	rs := []rune(text)
	for i := 10; i <= len(rs); i += 7 {
		w := []float32{200, 480, 200, 333}[i%4]
		p := string(rs[:i])
		got := measureMD(h, "live", p, Streaming, w)
		want := measureMD(mdTestHost(), "cold", p, Streaming, w)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("第 %d 个字宽 %.0f 不一致", i, w)
		}
	}
}

// 流式后段每帧排版次数不随正文变长：前 200 段已经定了，再长一段每帧最多重排最后一个还在长的段落。
func TestMDShapeCacheShapesPerFrameConstant(t *testing.T) {
	h := mdTestHost()
	var sb strings.Builder
	perFrame := func(extra string) int64 {
		sb.WriteString(extra)
		before := mdShapeCount.Load()
		measureMD(h, "live", sb.String(), Streaming, 480)
		return mdShapeCount.Load() - before
	}
	for i := 0; i < 20; i++ { // 先长出 20 段
		perFrame("这是已经写完的一个段落，写得稍微长一点点。\n\n")
	}
	early := int64(0)
	for i := 0; i < 5; i++ {
		early = max(early, perFrame("字"))
	}
	for i := 0; i < 200; i++ {
		perFrame("这是又一个已经写完的段落。\n\n")
	}
	late := int64(0)
	for i := 0; i < 5; i++ {
		late = max(late, perFrame("字"))
	}
	if early > 2 || late > 2 || late > early {
		t.Fatalf("每帧排版次数随正文变长：前段 %d，后段 %d", early, late)
	}
	// 缓存有上限：只留还在用的段落，不随历史无限长
	if n := len(h.MD.entry("live").shaped.m); n > 230 {
		t.Fatalf("缓存条目 %d 过多", n)
	}
}

// 缓存会淘汰：长出来又变掉的最后一段不留着。
func TestMDShapeCacheEvictsStale(t *testing.T) {
	h := mdTestHost()
	text := "固定的第一段。\n\n"
	for i := 0; i < 300; i++ {
		text += "字"
		measureMD(h, "live", text, Streaming, 480)
	}
	if n := len(h.MD.entry("live").shaped.m); n > 4 {
		t.Fatalf("缓存条目 %d，应只剩固定段和最近几步的最后一段", n)
	}
}

func tail(s string) string {
	r := []rune(s)
	if len(r) > 12 {
		r = r[len(r)-12:]
	}
	return string(r)
}

package main

// T-seg：segments_json 的解码（纯函数）。样本全是合成的，名字、编号、哈希都是编的。

import (
	"bytes"
	"encoding/json"
	"log"
	"reflect"
	"strings"
	"testing"

	"sumika-native/internal/api"
)

func strp(s string) *string { return &s }

func TestDecodeBodyAbsent(t *testing.T) {
	t.Parallel()
	// 没有这一列、空串：纯文字消息的常态，不是错误，也不记日志
	for name, raw := range map[string]*string{"nil": nil, "空串": strp("")} {
		b, err := decodeBody(raw)
		if err != nil || !reflect.DeepEqual(b, segBody{}) {
			t.Errorf("%s：得到 %+v, %v", name, b, err)
		}
	}
}

func TestDecodeBodyErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		// 出错时仍然留着的根上标记
		unsent  string
		aborted bool
	}{
		{"截断的 JSON", `{"segs":[{"type":"image","hash":"h1"`, "", false},
		{"根是数组，不是对象", `[{"type":"text","text":"x"}]`, "", false},
		{"根是字符串", `"abc"`, "", false},
		{"segs 里有非对象", `{"segs":[1,"a"],"unsent":"超时"}`, "超时", false},
		{"segs 不是数组", `{"segs":{"type":"text"}}`, "", false},
		{"super 写成字符串", `{"segs":[{"type":"face","id":"1","super":"yes"}],"aborted":true}`, "", true},
		{"width 写成字符串", `{"segs":[{"type":"image","hash":"h","width":"300"}]}`, "", false},
		{"face 的 id 写成数字", `{"segs":[{"type":"face","id":14}]}`, "", false},
		{"unsent 写成布尔", `{"segs":[{"type":"text","text":"x"}],"unsent":true}`, "", false},
	}
	for _, c := range cases {
		b, err := decodeBody(strp(c.raw))
		if err == nil {
			t.Errorf("%s：应该报错，得到 %+v", c.name, b)
			continue
		}
		if b.Segs != nil {
			t.Errorf("%s：出错时 Segs 要清空（不拿解了一半的段去画），得到 %+v", c.name, b.Segs)
		}
		if b.Unsent != c.unsent || b.Aborted != c.aborted {
			t.Errorf("%s：根上的标记 unsent=%q aborted=%v，要的 %q %v", c.name, b.Unsent, b.Aborted, c.unsent, c.aborted)
		}
	}
}

// captureLog 把标准日志接到缓冲里，返回缓冲和还原函数
func captureLog() (*bytes.Buffer, func()) {
	var buf bytes.Buffer
	w, f := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	return &buf, func() { log.SetOutput(w); log.SetFlags(f) }
}

// 解析失败的那一条退回 text 显示（segsOf 返回空），并记一行日志；日志里不带消息内容
func TestSegsOfFallbackAndLog(t *testing.T) {
	buf, restore := captureLog()
	defer restore()

	e := event{ID: "ev-bad", Text: "[图片]", SegmentsJSON: strp(`{"segs":[{"type":"image","hash":"秘密哈希"`)}
	if segs := segsOf(e); len(segs) != 0 {
		t.Fatalf("解析失败应退回 text（segs 为空），得到 %+v", segs)
	}
	out := buf.String()
	if strings.Count(out, "\n") != 1 || !strings.Contains(out, "ev-bad") || !strings.Contains(out, "退回 text") {
		t.Errorf("应记一行带事件 id 的日志，得到 %q", out)
	}
	if strings.Contains(out, "秘密哈希") {
		t.Errorf("日志不该带消息内容：%q", out)
	}

	// 好的、没有段的，都不记日志
	buf.Reset()
	segsOf(event{ID: "ev-ok", SegmentsJSON: strp(`{"segs":[{"type":"text","text":"x"}]}`)})
	segsOf(event{ID: "ev-none"})
	segsOf(event{ID: "ev-empty", SegmentsJSON: strp("")})
	if buf.Len() != 0 {
		t.Errorf("正常的不该记日志：%q", buf.String())
	}

	// 类型不符的那一条也退回 text，同样记日志
	segsOf(event{ID: "ev-type", SegmentsJSON: strp(`{"segs":[{"type":"face","id":"1","super":"yes"}]}`)})
	if !strings.Contains(buf.String(), "ev-type") {
		t.Errorf("类型不符应记日志：%q", buf.String())
	}
}

// 一页里有一条坏的，别的消息照常解出来（坏的只影响自己）
func TestSegsOfOneBadOneGood(t *testing.T) {
	_, restore := captureLog()
	defer restore()
	bad := event{ID: "a", SegmentsJSON: strp(`{"segs":[`)}
	good := event{ID: "b", SegmentsJSON: strp(`{"segs":[{"type":"face","id":"5"}]}`)}
	if len(segsOf(bad)) != 0 {
		t.Error("坏的应为空")
	}
	if s := segsOf(good); len(s) != 1 || s[0].Ref != "5" {
		t.Errorf("好的应照常解出，得到 %+v", s)
	}
}

func TestDisplayName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		s    api.DisplaySegment
		want string
	}{
		{api.DisplaySegment{Kind: "emoji", Ref: "market", Name: "挥手"}, "挥手"},
		{api.DisplaySegment{Kind: "emoji", Ref: "market"}, "表情包"},
		// 商城表情名字去首尾各一个括号，文件、视频名称保持原样。
		{api.DisplaySegment{Kind: "emoji", Ref: "market", Name: "[挥手]"}, "挥手"},
		{api.DisplaySegment{Kind: "emoji", Ref: "market", Name: "[]"}, "表情包"},
		{api.DisplaySegment{Kind: "emoji", Ref: "market", Name: "[[x]]"}, "[x]"},
		{api.DisplaySegment{Kind: "emoji", Ref: "market", Name: "[挥手"}, "挥手"},
		{api.DisplaySegment{Kind: "emoji", Ref: "market", Name: "a]"}, "a"},
		{api.DisplaySegment{Kind: "file", Name: "[草稿].txt"}, "[草稿].txt"},
		{api.DisplaySegment{Kind: "file", Name: "清单.txt"}, "清单.txt"},
		{api.DisplaySegment{Kind: "file"}, "文件"},
		{api.DisplaySegment{Kind: "video", Name: "a.mp4"}, "a.mp4"},
		{api.DisplaySegment{Kind: "video"}, ""},
	}
	for _, c := range cases {
		if got := segmentView(segFromDisplay(c.s), 0).DisplayName(); got != c.want {
			t.Errorf("%+v：得到 %q，要 %q", c.s, got, c.want)
		}
	}
}

// 文件段在气泡里的灰字：[文件：名字 · 大小]，缺名字写"文件"，大小不知道（0、负数）就不写
func TestFileNote(t *testing.T) {
	t.Parallel()
	cases := []struct {
		s    api.DisplaySegment
		want string
	}{
		{api.DisplaySegment{Kind: "file", Name: "清单.txt", Size: 2048}, "[文件：清单.txt · 2.0 KB]"},
		{api.DisplaySegment{Kind: "file", Name: "清单.txt"}, "[文件：清单.txt]"},
		{api.DisplaySegment{Kind: "file", Size: 2048}, "[文件：文件 · 2.0 KB]"},
		{api.DisplaySegment{Kind: "file"}, "[文件：文件]"},
		{api.DisplaySegment{Kind: "file", Name: "a", Size: -1}, "[文件：a]"},
		{api.DisplaySegment{Kind: "file", Name: "[草稿].txt", Size: 5}, "[文件：[草稿].txt · 5 B]"},
	}
	for _, c := range cases {
		if got := fileNote(segFromDisplay(c.s)); got != c.want {
			t.Errorf("%+v：得到 %q，要 %q", c.s, got, c.want)
		}
	}
}

func TestHumanSize(t *testing.T) {
	t.Parallel()
	cases := []struct {
		n    int64
		want string
	}{
		{-1, "0 B"}, {0, "0 B"}, {1, "1 B"}, {1023, "1023 B"},
		{1024, "1.0 KB"}, {1536, "1.5 KB"}, {12595, "12.3 KB"},
		{1048575, "1.0 MB"}, // 1023.999 KB 四舍五入成 1024.0 KB，要进到 MB
		{1048576, "1.0 MB"}, {5 * 1024 * 1024, "5.0 MB"},
		{1024*1024*1024 - 1, "1.0 GB"}, {1 << 30, "1.0 GB"}, {3 << 30, "3.0 GB"},
		{1 << 40, "1024.0 GB"}, // 最大一档是 GB，不再往上进
		{1 << 53, "8388608.0 GB"},
	}
	for _, c := range cases {
		if got := humanSize(c.n); got != c.want {
			t.Errorf("humanSize(%d) = %q，要 %q", c.n, got, c.want)
		}
	}
}

// 第零步 n：撤回通知的 reply_to 解得出来（合成样本，形状照 daemon/plugins/qq.ts 的 notice 事件）
func TestEventReplyTo(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"撤回通知，指向被撤回的那条", `{"seq":7,"id":"ev-notice","kind":"notice","text":"撤回了一条消息","reply_to":"ev-orig","segments_json":"{\"notice\":\"group_recall\"}"}`, "ev-orig"},
		{"后台给 null", `{"seq":8,"id":"ev-m","kind":"message","reply_to":null}`, ""},
		{"旧后台没有这个键", `{"seq":9,"id":"ev-m2","kind":"message"}`, ""},
	}
	for _, c := range cases {
		var e event
		if err := json.Unmarshal([]byte(c.raw), &e); err != nil {
			t.Errorf("%s：%v", c.name, err)
			continue
		}
		if e.ReplyTo != c.want {
			t.Errorf("%s：ReplyTo=%q，要 %q", c.name, e.ReplyTo, c.want)
		}
	}
	// 整页解码：reply_to 在事件数组里也解得出来
	var p qqPage
	raw := `{"kind":"group","title":"测试群","events":[{"seq":1,"id":"a","kind":"message"},{"seq":2,"id":"b","kind":"notice","reply_to":"a"}],"people":{}}`
	if err := json.Unmarshal([]byte(raw), &p); err != nil || len(p.Events) != 2 || p.Events[1].ReplyTo != "a" {
		t.Errorf("整页解码：%+v, %v", p, err)
	}
}

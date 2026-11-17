package desktopread

import (
	"context"
	"strings"
	"testing"

	"sumika-native/internal/api"
)

// 文件段的名字和大小从库里的中立段带到展示段（2026-11-02 真机：库里只有 {"kind":"file"}，界面写"[文件：文件]"）。
// 名字是外来文字，显示前照群名、昵称那样清一遍；视频、语音的 File 可能是标识不是名字，不往界面送。
// 旧代码对 file 只做 d.Name = seg.File，不清、不带大小，下面凡是期望 Size 或清过的名字的行都会红。
func TestDisplayFileSegments(t *testing.T) {
	t.Parallel()
	src := api.EventRef{}
	long := strings.Repeat("长", 41)
	cases := []struct {
		name, segs string
		wantName   string
		wantSize   int64
	}{
		{"名字和大小", `{"kind":"file","file":"清单.txt","file_id":"/f1","size":2048,"url":"https://a.invalid/x"}`, "清单.txt", 2048},
		{"旧行只有 kind", `{"kind":"file"}`, "", 0},
		{"只有名字", `{"kind":"file","file":"清单.txt"}`, "清单.txt", 0},
		{"只有大小", `{"kind":"file","size":7}`, "", 7},
		{"大小为 0", `{"kind":"file","file":"a","size":0}`, "a", 0},
		{"大小为负（坏行）", `{"kind":"file","file":"a","size":-5}`, "a", 0},
		{"方向控制字符被清掉", `{"kind":"file","file":"a‮txt.exe"}`, "atxt.exe", 0},
		{"冒号留着", `{"kind":"file","file":"录音 12:30.m4a"}`, "录音 12:30.m4a", 0},
		{"换行并成空格", `{"kind":"file","file":"a\nb.txt"}`, "a b.txt", 0},
		{"零宽字符被清掉", `{"kind":"file","file":"a​b.txt"}`, "ab.txt", 0},
		{"只有零宽和空白就是没有名字", `{"kind":"file","file":"​ ‎"}`, "", 0},
		{"超长截断加省略号", `{"kind":"file","file":"` + long + `"}`, strings.Repeat("长", 40) + "…", 0},
		{"视频保留大小但不显示 File 标识", `{"kind":"video","file":"0123ABCD-编的-uuid","url":"https://a.invalid/v","size":99}`, "", 99},
		{"语音的 File 不送界面", `{"kind":"voice","file":"voice.amr","size":99}`, "", 0},
		{"图的 File 照旧是名字", `{"kind":"image","file":"0123ABCD.jpg"}`, "0123ABCD.jpg", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := displaySegments(src, parseEnvelope(`{"segs":[`+tc.segs+`]}`), nil, nil)
			if len(got) != 1 || got[0].Name != tc.wantName || got[0].Size != tc.wantSize {
				t.Fatalf("%s\n得 %+v\n要名字 %q 大小 %d", tc.segs, got, tc.wantName, tc.wantSize)
			}
			// 文件标识和地址不送界面（界面只显示名字和大小）
			if got[0].Ref != "" {
				t.Fatalf("Ref 不该有值：%+v", got[0])
			}
		})
	}
}

// 导入来的旧事件：名字在旧段（import_meta）里，大小也是；名字同样要清，坏的大小不认；
// 旧段补名字只对 file 清，别的种类（mface 的 "[挥手]"）不动
func TestLegacyFileSegmentsDisplay(t *testing.T) {
	t.Parallel()
	src := api.EventRef{}
	env := func(kind, legacy string) string {
		return `{"segs":[{"kind":"` + kind + `"}],"import_meta":{"version":1,"legacy_segments":[` + legacy + `]}}`
	}
	cases := []struct {
		name, raw string
		wantName  string
		wantSize  int64
	}{
		{"名字和大小", env("file", `{"type":"file","name":"清单.txt","size":12,"fileId":"f"}`), "清单.txt", 12},
		{"名字被清", env("file", `{"type":"file","name":"a‮txt.exe\n","size":12}`), "atxt.exe", 12},
		{"名字清完是空的就不覆盖", env("file", `{"type":"file","name":"​","size":12}`), "", 12},
		{"大小是负数", env("file", `{"type":"file","name":"a","size":-1}`), "a", 0},
		{"大小是小数", env("file", `{"type":"file","name":"a","size":1.5}`), "a", 0},
		{"大小是字符串", env("file", `{"type":"file","name":"a","size":"12"}`), "a", 0},
		{"大小超过 2^53", env("file", `{"type":"file","name":"a","size":1e20}`), "a", 0},
		{"新段已有大小，旧段不覆盖", `{"segs":[{"kind":"file","size":5}],"import_meta":{"version":1,"legacy_segments":[{"type":"file","name":"a","size":99}]}}`, "a", 5},
		{"mface 的名字原样", env("emoji", `{"type":"mface","name":"[挥手]"}`), "[挥手]", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := displaySegments(src, parseEnvelope(tc.raw), nil, nil)
			if len(got) != 1 || got[0].Issue != "" || got[0].Name != tc.wantName || got[0].Size != tc.wantSize {
				t.Fatalf("%s\n得 %+v\n要名字 %q 大小 %d", tc.raw, got, tc.wantName, tc.wantSize)
			}
		})
	}
}

// 整条路径：中立段存进库，ReadMessages 读出来的展示段带着名字和大小
func TestReadMessagesCarriesFileNameAndSize(t *testing.T) {
	t.Parallel()
	s, set, _ := mediaFixture(t)
	qq, _ := set.Platform("qq")
	conv := "qq:group:90002"
	id := addSegs(t, qq, "qq", conv, map[string]any{"segs": []any{
		map[string]any{"kind": "file", "file": "清单‮.txt", "file_id": "/f1", "size": 2048},
		map[string]any{"kind": "file"},
	}}, "", []string{"a"})
	page, err := s.ReadMessages(context.Background(), api.ConversationRequest{Key: keyOf(conv), Limit: 10}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range page.Value.Items {
		if m.Ref.Event.EventID != id {
			continue
		}
		if len(m.Segments) != 2 || m.Segments[0].Name != "清单.txt" || m.Segments[0].Size != 2048 || m.Segments[1].Name != "" || m.Segments[1].Size != 0 {
			t.Fatalf("展示段：%+v", m.Segments)
		}
		return
	}
	t.Fatal("没读到刚存的事件")
}

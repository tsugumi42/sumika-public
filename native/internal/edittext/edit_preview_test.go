package edittext_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/approval"
	"sumika-native/internal/jsdiff"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/policy"
	"sumika-native/internal/store"
	"sumika-native/internal/tools"
)

// 形状 × 入口（测了 = 本测试真过预览/写入；处理了 = 共用入口，无独立算法）：
// 形状                     Describe / Ask / OnAuto / Execute / turn / checkpoint / bench-shell / 回归 / 只读评测
// $&、$1、$$、$'、$`        测了       测了  测了     测了      处理了  处理了       处理了        处理了  不可能
// 空 oldText、找不到、无变化 测了       测了  测了     测了      处理了  处理了       处理了        处理了  不可能
// 多处、重叠、顺序替换陷阱   测了       测了  测了     测了      处理了  处理了       处理了        处理了  不可能
// 模糊匹配、模糊后多处       测了       测了  测了     测了      处理了  处理了       处理了        处理了  不可能
// CRLF、BOM、非 UTF-8、超长 测了       测了  测了     测了      处理了  处理了       处理了        处理了  不可能
// Ask/OnAuto 是 Gate.Check 的两个预览入口；turn.prepareCall 默认执行及 Instructions
// 包装、app.executeCheckpointed、bench-shell 的 ExecTool 都调用 tools.Execute。
// gate/turn 回归走 Check，tools 回归走 Execute；只读评测 soak 仅允许 ls/read，
// memory-shadow 拒绝本地操作。失败执行后原文件不变，预览本身不能写文件。
// 本地私聊走 prepareCall -> Check -> Execute；本地群聊、QQ 私聊、QQ 群
// 在当前 turn 听众边界不可到达内置 edit（不可能），本次不改权限。
// 当前工具只接受 edits[].oldText/newText，无 replace_all；每项必须唯一且互不重叠。
func TestEditPreviewMatchesExecution(t *testing.T) {
	type pair struct {
		Old string `json:"oldText"`
		New string `json:"newText"`
	}
	cases := []struct {
		name, before string
		edits        []pair
		want         string
		failure      bool
	}{
		{"empty-old", "abc", []pair{{"", "x"}}, "abc", true},
		{"missing", "abc", []pair{{"z", "x"}}, "abc", true},
		{"multiple", "b b", []pair{{"b", "x"}}, "b b", true},
		{"overlap", "abc", []pair{{"ab", "x"}, {"bc", "y"}}, "abc", true},
		{"same-original", "a b", []pair{{"a", "b"}, {"b", "c"}}, "b c", false},
		{"fuzzy", "keep ‘quotes’  \nvalue: “old”\n", []pair{{`value: "old"`, "$& $$ $1 $' $`"}}, "keep ‘quotes’  \n$& $$ $1 $' $`\n", false},
		{"fuzzy-multiple", "“old” “old”", []pair{{`"old"`, "x"}}, "“old” “old”", true},
		{"crlf", "a\r\nb\r\nc\r\n", []pair{{"b\n", "$&\n$$\n"}}, "a\r\n$&\r\n$$\r\nc\r\n", false},
		{"bom", "\uFEFFabc\r\n", []pair{{"b", "$$"}}, "\uFEFFa$$c\r\n", false},
		{"invalid-utf8", "a\xffbc", []pair{{"b", "$&"}}, "a\xff$&c", false},
		{"unchanged", "abc", []pair{{"b", "b"}}, "abc", true},
		{"three-ok", "a\nb\nc\nd\n", []pair{{"a\n", "1\n"}, {"c\n", "3\n"}, {"d\n", "4\n"}}, "1\nb\n3\n4\n", false},
		{"second-of-three-fails", "a\nb\nc\n", []pair{{"a\n", "1\n"}, {"zz", "2"}, {"c\n", "3\n"}}, "a\nb\nc\n", true},
		{"long", "abc", []pair{{"b", strings.Repeat("$&", 4000)}}, "a" + strings.Repeat("$&", 4000) + "c", false},
	}
	for _, marker := range []string{"$&", "$1", "$$", "$'", "$`"} {
		cases = append(cases, struct {
			name, before string
			edits        []pair
			want         string
			failure      bool
		}{"literal-" + marker, "abc", []pair{{"b", marker}}, "a" + marker + "c", false})
	}
	for _, tc := range cases {
		for _, entry := range []string{"describe", "manual", "auto"} {
			t.Run(tc.name+"/"+entry, func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "a.txt")
				if err := os.WriteFile(path, []byte(tc.before), 0o600); err != nil {
					t.Fatal(err)
				}
				raw, err := json.Marshal(map[string]any{"purpose": "验证编辑预览", "path": "a.txt", "edits": tc.edits})
				if err != nil {
					t.Fatal(err)
				}
				args, err := jsjson.Parse(string(raw))
				if err != nil {
					t.Fatal(err)
				}
				args = tools.Prepare("edit", args)
				gate := approval.NewGateWith(filepath.Join(dir, "data"), nil)
				gate.Now = time.Now
				var req approval.Request
				if entry == "describe" {
					req = gate.Describe(dir, "fixture", "edit", "edit", args)
				} else {
					gate.Mode = entry
					gate.Ask = func(_ context.Context, r approval.Request, _ *approval.Pending) approval.Decision {
						req = r
						return approval.Decision{Kind: "approved"}
					}
					gate.OnAuto = func(r approval.Request, _ approval.Clearance) { req = r }
					if blocked, why := gate.Check(context.Background(), dir, "fixture", policy.Audience{Channel: "console", PrivateOk: true}, "console:fixture", approval.Call{ID: "edit", Name: "edit", Args: args}); blocked {
						t.Fatalf("approval blocked: %s", why)
					}
				}
				before, err := os.ReadFile(path)
				if err != nil || string(before) != tc.before {
					t.Fatalf("preview changed file: %v", err)
				}
				result, err := tools.Execute(context.Background(), dir, "edit", args)
				if err != nil || result.IsError != tc.failure {
					t.Fatalf("execution: %+v, %v", result, err)
				}
				after, err := os.ReadFile(path)
				if err != nil || string(after) != tc.want {
					t.Fatalf("written content differs from literal contract: %v", err)
				}
				kind, want := "content", result.Text
				if !tc.failure {
					kind = "diff"
					want, _, _ = jsdiff.GenerateDiffString(tc.before, string(after), 4)
				}
				truncated := store.JSLen(want) > 6000
				if truncated {
					want = store.JSSlice(want, 6000)
				}
				if req.Preview == nil || req.Preview.Kind != kind || req.Preview.Text != want || req.Preview.Truncated != truncated {
					t.Errorf("preview differs from execution: got %+v; want kind=%s text=%.200q truncated=%v", req.Preview, kind, want, truncated)
				}
			})
		}
	}
}

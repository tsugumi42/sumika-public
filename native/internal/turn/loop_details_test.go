package turn

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/tools"
)

// 真正改临时文件，经回合循环写 steps；下一次模型请求仍只收到原来的文字。
func TestEditDetailsPersistWithoutChangingModelText(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile(filepath.Join("..", "tools", "testdata", "editdiff.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, Before, Path, Text string
		After                    *string
		Edits, Details           json.RawMessage
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	c := cases[0]
	wd := t.TempDir()
	if err := os.WriteFile(filepath.Join(wd, c.Path), []byte(c.Before), 0o644); err != nil {
		t.Fatal(err)
	}
	args, err := json.Marshal(map[string]any{"path": c.Path, "edits": c.Edits})
	if err != nil {
		t.Fatal(err)
	}
	encodedArgs, err := json.Marshal(string(args))
	if err != nil {
		t.Fatal(err)
	}
	h := newHarnessOpts(t, harnessOpts{
		wd: wd, mode: "auto",
		replies: `[{"toolCalls":[{"id":"edit-details","name":"edit","args":[` + string(encodedArgs) + `]}],` + usage + `},
			{"toolCalls":[{"id":"read-plain","name":"read","args":["{\"path\":\"example.txt\"}"]}],` + usage + `},
			{"content":["好了"],` + usage + `}]`,
	})
	h.send("改好以后读一下")
	byID, _ := h.toolMsgs(1)
	if byID["edit-details"] != c.Text {
		t.Fatalf("下一次模型请求只应带 %q，得到 %q", c.Text, byID["edit-details"])
	}
	gotFile, err := os.ReadFile(filepath.Join(wd, c.Path))
	if err != nil || c.After == nil || string(gotFile) != *c.After {
		t.Fatalf("实际 edit 没写对文件：%q，%v", gotFile, err)
	}
	var preview string
	var details sql.NullString
	if err := h.st.Trace.QueryRow(`SELECT preview, details_json FROM steps WHERE provider_call_id = 'edit-details'`).Scan(&preview, &details); err != nil {
		t.Fatal(err)
	}
	var want, got struct {
		Diff, Patch      string
		FirstChangedLine int
	}
	if err := json.Unmarshal(c.Details, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(details.String), &got); err != nil {
		t.Fatalf("edit 的详情没存下来：%q，%v", details.String, err)
	}
	if preview != c.Text || got != want {
		t.Fatalf("落库结果不一样：预览 %q，详情 %+v；应该 %q，%+v", preview, got, c.Text, want)
	}
	if err := h.st.Trace.QueryRow(`SELECT details_json FROM steps WHERE provider_call_id = 'read-plain'`).Scan(&details); err != nil || details.Valid {
		t.Fatalf("普通 read 不应带 edit 详情：%+v，%v", details, err)
	}
	t.Log("真实 edit → steps → 下一次模型请求通过；普通 read 的详情是 NULL")
}

func TestLargeEditDetailsBoundedWithoutChangingModelText(t *testing.T) {
	t.Parallel()
	wd := t.TempDir()
	before := strings.Repeat("旧🙂\"\\\x01\n", 6000)
	after := strings.Repeat("新🙂\"\\\x01\n", 6000)
	path := filepath.Join(wd, "large.txt")
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	args, err := json.Marshal(map[string]any{"path": "large.txt", "edits": []map[string]string{{"oldText": before, "newText": after}}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(string(args))
	if err != nil {
		t.Fatal(err)
	}
	// 差分有步数上限，但低优先级跑在忙机器上也要好几秒；这里验的是详情的形状，工具时限放开，不把机器忙算成"详情被跳过"
	h := newHarnessOpts(t, harnessOpts{wd: wd, mode: "auto", replies: `[{"toolCalls":[{"id":"large-edit","name":"edit","args":[` + string(encoded) + `]}],` + usage + `},
		{"content":["好了"],` + usage + `}]`, mutate: func(c *Config) {
		c.ToolTimeout = func(string, time.Duration) time.Duration { return time.Hour }
	}})
	h.send("整体替换文件")
	want := "Successfully replaced 1 block(s) in large.txt."
	byID, _ := h.toolMsgs(1)
	if byID["large-edit"] != want {
		t.Fatalf("下一次模型请求里的成功文字变了：%q", byID["large-edit"])
	}
	gotFile, err := os.ReadFile(path)
	if err != nil || string(gotFile) != after {
		t.Fatalf("大文件整体替换没有写对：%v", err)
	}
	var preview, raw string
	if err := h.st.Trace.QueryRow(`SELECT preview, details_json FROM steps WHERE provider_call_id = 'large-edit'`).Scan(&preview, &raw); err != nil {
		t.Fatal(err)
	}
	var details struct {
		Diff, Patch      string
		FirstChangedLine int
		Truncated        bool
	}
	if err := json.Unmarshal([]byte(raw), &details); err != nil {
		t.Fatalf("详情不是合法 JSON：%v", err)
	}
	if len(raw) > 64*1024 || !details.Truncated || details.FirstChangedLine != 1 || preview != want {
		t.Fatalf("详情应在 65536 字节内并标截断，行号和模型文字保留：字节 %d，截断 %v，行号 %d，预览 %q", len(raw), details.Truncated, details.FirstChangedLine, preview)
	}
	if !utf8.ValidString(raw) || !strings.HasPrefix(details.Diff, "-   1 旧🙂") || !strings.HasPrefix(details.Patch, "--- large.txt\n+++ large.txt\n") || strings.Contains(details.Diff+details.Patch, "\uFFFD") {
		t.Fatalf("截断后应保留两份差分的开头和完整 UTF-8：diff %.80q，patch %.80q", details.Diff, details.Patch)
	}
	t.Logf("整体替换后 details JSON %d 字节，截断 %v，首行 %d；模型成功文字不变", len(raw), details.Truncated, details.FirstChangedLine)
}

func TestEditCommittedBeforeWrapperTimeout(t *testing.T) {
	t.Parallel()
	wd := t.TempDir()
	path := filepath.Join(wd, "a.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	args, err := jsjson.Parse(`{"path":"a.txt","edits":[{"oldText":"before","newText":"after"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	// 真实 edit 写入后用屏障拖住返回，覆盖外层计时器先到的分支。
	written, finished := make(chan struct{}), make(chan struct{})
	cfg := Config{ToolTimeout: func(_ string, _ time.Duration) time.Duration { return time.Second }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan tools.Result, 1)
	go func() {
		r, err := within(ctx, cfg, "edit", args, func(ctx context.Context) (tools.Result, error) {
			defer close(finished)
			r, err := tools.Execute(ctx, wd, "edit", args)
			close(written)
			<-ctx.Done()
			return r, err
		})
		if err != nil {
			r = tools.Result{Text: err.Error(), IsError: true}
		}
		result <- r
	}()
	select {
	case <-written:
	case <-time.After(time.Second):
		t.Fatal("真实 edit 没有完成写入")
	}
	select {
	case r := <-result:
		if r.IsError || r.Text != "Successfully replaced 1 block(s) in a.txt." || r.Details != nil {
			t.Fatalf("写入完成以后不应变成失败：%+v", r)
		}
	case <-time.After(barrierLimit):
		t.Fatal("写入后的工具没有收尾")
	}
	select {
	case <-finished:
	case <-time.After(barrierLimit):
		t.Fatal("外层时限到点以后没有取消工具")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "after\n" {
		t.Fatalf("时限到点后文件应是改好的内容：%q，%v", got, err)
	}
}

func TestToolResultAtTimeoutPrefersCompleted(t *testing.T) {
	t.Parallel()
	for _, isError := range []bool{false, true} {
		want := tools.Result{Text: "已经完成", IsError: isError, Details: "完整详情"}
		out := make(chan toolOutcome, 1)
		out <- toolOutcome{r: want}
		cancelled := false
		got, ok := toolResultAtTimeout(out, &tools.EditCommit{}, func() { cancelled = true })
		if !ok || got.err != nil || got.r.Text != want.Text || got.r.IsError != want.IsError || got.r.Details != want.Details || cancelled {
			t.Fatalf("到点时已有完整结果要优先保留：%+v，取到 %v，取消 %v；应该 %+v", got, ok, cancelled, want)
		}
	}
}

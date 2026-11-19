package app

import (
	"context"
	"sort"
	"strings"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/llm/llmtest"
)

// 输入框的斜杠命令补全名单、大段粘贴拼进正文后的发送路径。名字和文字都是编的。

// 补全名单直接取命令登记表：登记的命令都在，排好序，和 Parse 认的是同一批。
func TestSlashCommandsListComesFromRegistry(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text("好")})
	var lister api.SlashLister = &d.Reader // 界面按接口取名单：指针得实现它
	got := lister.SlashCommands()
	if !sort.SliceIsSorted(got, func(i, j int) bool { return got[i].Name < got[j].Name }) {
		t.Fatalf("该按名字排序：%+v", got)
	}
	names := map[string]bool{}
	for _, c := range got {
		names[c.Name] = true
		if strings.HasPrefix(c.Name, "/") || c.Summary == "" {
			t.Fatalf("名字不带斜杠、要有一句说明：%+v", c)
		}
	}
	for _, c := range a.slashes.reg.Commands() {
		if !names[c.Name] {
			t.Fatalf("登记的命令 %q 不在补全名单里：%+v", c.Name, got)
		}
	}
	if !names["goal"] || !names["loop"] {
		t.Fatalf("goal、loop 都是已登记的命令：%+v", got)
	}
}

// 折叠的粘贴由界面拼进这一块的正文再走 SubmitLocal：长文字（超过 3000 字符）一个字不少地进了模型的请求体。
// 正文的格式和 chatui.Block.SendText 一致（标题行加全文），格式本身由界面包的测试覆盖。
func TestSubmitLocalLongPastedTextReachesModel(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text("收到")})
	ctx := context.Background()
	key := localKey("console:" + testChar)
	pasted := strings.Repeat("编造的一大段话，不含任何真实信息。", 400) // 6800 字，超过折叠门槛
	text := "帮我总结\n\n【粘贴的内容，6800 字】\n" + pasted
	rec, err := d.Reader.SubmitLocal(ctx, key, api.LocalInputSubmission{Parts: []api.LocalPart{{Text: text}}, Mode: "start", ClientKey: "k-paste"})
	if err != nil || rec.RunID == "" {
		t.Fatalf("长文字该照常收下：%v %+v", err, rec)
	}
	waitFor(t, "模型收到请求", func() bool { return e.llmCalls() >= 1 })
	waitFor(t, "这一轮收尾", func() bool { return !a.hub.Running(testChar, key.Conversation) })
	e.mu.Lock()
	defer e.mu.Unlock()
	found := false
	for _, body := range e.bodies {
		if strings.Contains(body, pasted) && strings.Contains(body, "【粘贴的内容，6800 字】") {
			found = true
		}
	}
	if !found {
		t.Fatalf("模型的请求体里没有完整的粘贴内容（共 %d 个请求）", len(e.bodies))
	}
}

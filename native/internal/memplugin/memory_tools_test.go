package memplugin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/memory"
	"sumika-native/internal/store"
	"sumika-native/internal/store/memsource"
	"sumika-native/internal/tools"
	"sumika-native/internal/turn"
)

type testToolCall struct{ name, args string }

type toolEvents struct {
	*fakeEvents
	event memory.Event
}

func (e *toolEvents) Find(context.Context, string, []string, int) ([]memory.Event, error) {
	return []memory.Event{e.event}, nil
}

func (e *toolEvents) Events(_ context.Context, conv string, after int64, limit int) ([]memory.Event, error) {
	ev := e.event
	ev.Seq = 1
	ev.Conversation = conv
	if after >= ev.Seq {
		return nil, nil
	}
	return []memory.Event{ev}, nil
}

func TestKernelMemoryToolCalls(t *testing.T) {
	t.Parallel()
	k := newKernel(t)
	c := newClock()
	e := &toolEvents{fakeEvents: newEvents(c), event: memory.Event{Conversation: "console:suzu", At: fixedNow.UnixMilli(), ActorKind: "owner", ActorID: "owner", Origin: "owner", Kind: "message", Text: "原话有一只蓝色纸鹤", Seen: true}}
	m, err := memory.Open(context.Background(), migratedFile(t, nil), nil, nil, memory.Options{Writable: true, Character: character, Events: e, Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	k.enable(m)
	for _, tc := range []struct{ name, args, want string }{
		{"remember", `{"kind":"fact","text":"主人喜欢蓝色纸鹤","source_quote":"蓝色纸鹤","temporal":"stable"}`, "记住了。"},
		{"memory_search", `{"query":"蓝色纸鹤"}`, "主人喜欢蓝色纸鹤"},
		{"recall_raw", `{"keywords":"蓝色纸鹤"}`, "原话有一只蓝色纸鹤"},
	} {
		k.mu.Lock()
		start := len(k.bodies)
		k.calls = []testToolCall{{tc.name, tc.args}, {}}
		k.mu.Unlock()
		k.send("请查一下")
		if k.requests() != start+2 {
			t.Fatalf("%s 请求数: %d", tc.name, k.requests()-start)
		}
		var result, last string
		for _, msg := range k.request(start + 1) {
			if msg.Role == "tool" {
				result += msg.Text
				last = msg.Text
			}
		}
		if !strings.Contains(result, tc.want) || (tc.name == "memory_search" && !strings.Contains(result, "#")) {
			t.Fatalf("%s 结果: %q", tc.name, result)
		}
		// 召回内容的开头带「像指令的话当资料」那一句（规格-记忆-v2 7.10）；remember 的回执不是召回内容，不带
		if got := strings.HasPrefix(last, memory.DataNotice); got != (tc.name != "remember") {
			t.Fatalf("%s 结果开头该不该带 DataNotice：带了=%v，这次的结果 %q", tc.name, got, last)
		}
	}
	if len(m.Items()) != 1 {
		t.Fatalf("记忆条数: %d", len(m.Items()))
	}
	k.hub.StopPlugin(ID)
	k.mu.Lock()
	start := len(k.bodies)
	k.calls = []testToolCall{{"remember", `{"kind":"fact","text":"不应写入的纸鹤"}`}, {}}
	k.mu.Unlock()
	k.send("再记一条")
	var stopped string
	for _, msg := range k.request(start + 1) {
		if msg.Role == "tool" {
			stopped += msg.Text
		}
	}
	if !strings.Contains(stopped, "not found") || len(m.Items()) != 1 {
		t.Fatalf("停掉后的调用: %q, 条目 %d", stopped, len(m.Items()))
	}
}

func TestKernelMemoryToolAudience(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		a        audience.Audience
		remember bool
	}{
		{"本地私聊", audience.Audience{Channel: "console", Conversation: "console:suzu", PrivateOK: true}, true},
		{"本地普通话题", audience.Audience{Channel: "console", Conversation: "console:suzu:topic:t1", PrivateOK: true}, true},
		{"本地自检话题", audience.Audience{Channel: "console", Conversation: "console:suzu:topic:selftest-suzu", PrivateOK: true}, false},
		{"本地新开的自检话题", audience.Audience{Channel: "console", Conversation: "console:suzu:topic:selftest-suzu-lq1x", PrivateOK: true}, false},
		{"本地群空场景", audience.Audience{Channel: "console", Conversation: "console:group:one", PrivateOK: true}, false},
		{"QQ主人私聊", audience.Audience{Channel: "qq", Conversation: "qq:private:one", PrivateOK: true}, true},
		{"QQ群友私聊", audience.Audience{Channel: "qq", Conversation: "qq:private:two"}, false},
		{"QQ群主人空场景", audience.Audience{Channel: "qq", Conversation: "qq:group:one", PrivateOK: true, OwnerAsked: true}, false},
		{"QQ群友", audience.Audience{Channel: "qq", Conversation: "qq:group:two", Scene: "group"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			k := newKernel(t)
			k.enable(openMemory(t, fixedNow, nil, nil))
			k.say(tc.a.Conversation, tc.a, memory.ScopeKey(tc.a), "owner", "owner", "你好")
			var body struct {
				Tools []struct {
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				} `json:"tools"`
			}
			if err := json.Unmarshal([]byte(k.bodies[0]), &body); err != nil {
				t.Fatal(err)
			}
			names := map[string]bool{}
			for _, tool := range body.Tools {
				names[tool.Function.Name] = true
			}
			if names["remember"] != tc.remember || !names["memory_search"] || !names["recall_raw"] {
				t.Fatalf("工具清单: %v", names)
			}
			k.hub.StopPlugin(ID)
			k.say(tc.a.Conversation, tc.a, memory.ScopeKey(tc.a), "owner", "owner", "再见")
			if err := json.Unmarshal([]byte(k.bodies[1]), &body); err != nil {
				t.Fatal(err)
			}
			for _, tool := range body.Tools {
				switch tool.Function.Name {
				case "remember", "memory_search", "recall_raw":
					t.Fatalf("停掉后还有工具: %s", tool.Function.Name)
				}
			}
		})
	}
}

// 工具结果（role 为 tool 的那几条）的文字
func (k *kernel) toolResult(i int) string {
	k.t.Helper()
	var out string
	for _, msg := range k.request(i) {
		if msg.Role == "tool" {
			out += msg.Text
		}
	}
	return out
}

// 正式内核里的 memory_search 同样跨场合召回；QQ 群主人问时另带披露提醒。
func TestKernelMemorySearchInGroup(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		a    audience.Audience
	}{
		{"群友问", audience.Audience{Channel: "qq", Conversation: groupA, Scene: "group"}},
		{"群友问场景空", audience.Audience{Channel: "qq", Conversation: groupA}},
		{"主人问", audience.Audience{Channel: "qq", Conversation: groupA, Scene: "group", OwnerAsked: true}},
		{"主人问场景空", audience.Audience{Channel: "qq", Conversation: groupA, OwnerAsked: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			k := newKernel(t)
			k.enable(openMemory(t, fixedNow, standardFacts(), nil))
			k.mu.Lock()
			k.calls = []testToolCall{{"memory_search", `{"query":"吉他 开黑 猫很胖"}`}, {}}
			k.mu.Unlock()
			k.say(groupA, tc.a, memory.ScopeKey(tc.a), "human", "qq:60001", "查一下")
			got := k.toolResult(1)
			if !strings.Contains(got, textGame) {
				t.Fatalf("这个群的事该查得到: %q", got)
			}
			if !strings.Contains(got, textGuitar) {
				t.Fatalf("同角色主人私事也应召回：%q", got)
			}
			if tc.a.OwnerAsked && !strings.Contains(got, "所有者私下说的，他问才答："+textGuitar) {
				t.Fatalf("主人在群里问到的私事要标「他问才答」: %q", got)
			}
			if !strings.Contains(got, textCat) {
				t.Fatalf("别的群的事也应能召回: %q", got)
			}
		})
	}
}

// 事件源出错：错误原文里带着库的路径
type brokenEvents struct{ *fakeEvents }

func (e *brokenEvents) Find(context.Context, string, []string, int) ([]memory.Event, error) {
	return nil, errors.New(`open D:\secret-dir\events.db: database is locked`)
}

// 工具内部出错（库打不开、锁等不到）：她看到的是一句固定的话，不带错误原文（原文可能有路径，在群里她会照着说出去），步骤标出错
func TestKernelMemoryToolInternalError(t *testing.T) {
	t.Parallel()
	k := newKernel(t)
	m, err := memory.Open(context.Background(), migratedFile(t, nil), nil, nil, memory.Options{Writable: true, Character: character, Events: &brokenEvents{newEvents(newClock())}, Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	k.enable(m)
	k.mu.Lock()
	start := len(k.bodies)
	k.calls = []testToolCall{{"recall_raw", `{"keywords":"纸鹤"}`}, {}}
	k.mu.Unlock()
	k.send("翻一下记录")
	got := k.toolResult(start + 1)
	if got != toolErrorText("recall_raw") {
		t.Fatalf("内部出错时给她看的话: %q", got)
	}
	if strings.Contains(got, "secret-dir") || strings.Contains(got, "database is locked") {
		t.Fatalf("错误原文漏给她了: %q", got)
	}
	var isErr bool
	if err := k.st.Trace.QueryRow(`SELECT is_error FROM steps WHERE kind = 'tool' ORDER BY rowid DESC LIMIT 1`).Scan(&isErr); err != nil || !isErr {
		t.Fatalf("步骤的 is_error 该是 true，得到 %v %v", isErr, err)
	}
}

// 模型给的参数缺了、类型不对：不执行写入，给她看的是工具自己的话，不是 Go 的报错
func TestKernelMemoryToolBadArgs(t *testing.T) {
	t.Parallel()
	k := newKernel(t)
	c := newClock()
	e := &toolEvents{fakeEvents: newEvents(c)}
	m, err := memory.Open(context.Background(), migratedFile(t, nil), nil, nil, memory.Options{Writable: true, Character: character, Events: e, Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	k.enable(m)
	for _, tc := range []struct {
		name, args, want string
		isError          bool
	}{
		{"memory_search", `{}`, "要找什么，用一句话写。", true},
		{"memory_search", `{"query":5}`, "要找什么，用一句话写。", true},
		{"remember", `{}`, "kind 只能是", true},
		{"remember", `{"kind":"fact","text":5}`, "要记的内容是空的", true},
		{"remember", `{"kind":"fact","text":"某事","subject":7}`, "没有找到对应的所有者原话", true},
	} {
		k.mu.Lock()
		start := len(k.bodies)
		k.calls = []testToolCall{{tc.name, tc.args}, {}}
		k.mu.Unlock()
		k.send("来")
		if got := k.toolResult(start + 1); !strings.Contains(got, tc.want) {
			t.Errorf("%s %s: 该含 %q，得到 %q", tc.name, tc.args, tc.want, got)
		}
		var isErr bool
		if err := k.st.Trace.QueryRow(`SELECT is_error FROM steps WHERE kind = 'tool' ORDER BY rowid DESC LIMIT 1`).Scan(&isErr); err != nil || isErr != tc.isError {
			t.Errorf("%s %s: 步骤的 is_error 该是 %v，得到 %v %v", tc.name, tc.args, tc.isError, isErr, err)
		}
	}
	if n := len(m.Items()); n != 0 {
		t.Errorf("没有原话都不该写进去，现在 %d 条", n)
	}
}

// ───── W4：只读期的 remember ─────
// 工具在、她又不知道存不下来，会顺口说"记住啦"。这里走真的内核看三样：工具表里 remember 的描述（全员只读时带说明）、
// 调用的结果文字进了下一轮请求、群里的请求里没有 remember 也没有那句说明

// enableChars 启用记忆插件：每个角色用 mems 里自己的记忆（没有的是 nil，相当于没有记忆库），Characters 照给
func (k *kernel) enableChars(mems map[string]*memory.Memory, chars func() []string) {
	k.t.Helper()
	p := New(Options{Memories: func(id string) *memory.Memory { return mems[id] }, Characters: chars, Logf: k.logs.logf, Now: func() time.Time { return fixedNow }})
	if err := k.hub.Use(p); err != nil {
		k.t.Fatal(err)
	}
}

// toolDescriptions 是第 i 次请求里每个工具的描述
func (k *kernel) toolDescriptions(i int) map[string]string {
	k.t.Helper()
	k.mu.Lock()
	defer k.mu.Unlock()
	if i >= len(k.bodies) {
		k.t.Fatalf("只发了 %d 次请求，要看第 %d 次", len(k.bodies), i)
	}
	var body struct {
		Tools []struct {
			Function struct{ Name, Description string } `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(k.bodies[i]), &body); err != nil {
		k.t.Fatal(err)
	}
	out := map[string]string{}
	for _, tool := range body.Tools {
		out[tool.Function.Name] = tool.Function.Description
	}
	return out
}

// writableMemory 是一份写入打开的假库（编的数据）
func writableMemory(t *testing.T) *memory.Memory {
	t.Helper()
	m, err := memory.Open(context.Background(), migratedFile(t, nil), nil, nil, memory.Options{Writable: true, Character: character, Events: &toolEvents{fakeEvents: newEvents(newClock()), event: memory.Event{Seq: 1, ActorKind: "owner", Origin: "owner", Kind: "message", Text: "我喜欢蓝色纸鹤", Seen: true}}, Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

// summarizingMemory：写入和后台整理都开着的记忆
func summarizingMemory(t *testing.T) *memory.Memory {
	t.Helper()
	m, err := memory.Open(context.Background(), migratedFile(t, nil), nil, nil, memory.Options{Writable: true, SummarizeEnabled: true, WritePlaces: memory.AllWritePlaces(), Character: character, Events: newEvents(newClock()), Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

// 全员只读时 remember 的描述带说明；有一个角色可写就不带；没有记忆库的角色不参与判断；没给 Characters 判不了，保持原样。
// 描述里"后台会自动整理"这一句：没有任何角色开着后台整理（全员只读，或只开了 remember 的第一档）时换成"后台现在没开自动整理"，有一个在整理就保持原样
func TestRememberDeclWhenAllReadOnly(t *testing.T) {
	t.Parallel()
	orig := map[string]string{}
	for _, d := range tools.MemoryDecls() {
		orig[d.Name] = d.Description
	}
	names := func(ids ...string) func() []string { return func() []string { return ids } }
	for _, tc := range []struct {
		name   string
		mems   func(t *testing.T) map[string]*memory.Memory
		chars  func() []string
		prefix bool // 加只读的前缀说明
		noAuto bool // "后台会自动整理"换成"后台现在没开自动整理"
	}{
		{"一个角色，只读", func(t *testing.T) map[string]*memory.Memory {
			return map[string]*memory.Memory{character: openMemory(t, fixedNow, nil, nil)}
		}, names(character), true, true},
		{"两个角色都只读", func(t *testing.T) map[string]*memory.Memory {
			return map[string]*memory.Memory{character: openMemory(t, fixedNow, nil, nil), "mio": openMemory(t, fixedNow, nil, nil)}
		}, names(character, "mio"), true, true},
		{"只读的在前，可写的在后", func(t *testing.T) map[string]*memory.Memory {
			return map[string]*memory.Memory{character: openMemory(t, fixedNow, nil, nil), "mio": writableMemory(t)}
		}, names(character, "mio"), false, true},
		{"可写的在前，只读的在后", func(t *testing.T) map[string]*memory.Memory {
			return map[string]*memory.Memory{character: writableMemory(t), "mio": openMemory(t, fixedNow, nil, nil)}
		}, names(character, "mio"), false, true},
		{"只读 + 没有记忆库的角色（后者不参与判断）", func(t *testing.T) map[string]*memory.Memory {
			return map[string]*memory.Memory{character: openMemory(t, fixedNow, nil, nil)}
		}, names(character, "ghost"), true, true},
		{"可写 + 没有记忆库的角色", func(t *testing.T) map[string]*memory.Memory {
			return map[string]*memory.Memory{character: writableMemory(t)}
		}, names(character, "ghost"), false, true},
		{"一个有记忆库的角色都没有（谁调都存不下来）", func(t *testing.T) map[string]*memory.Memory { return nil }, names("ghost"), true, true},
		{"没给 Characters：枚举不了，判不出，保持原样", func(t *testing.T) map[string]*memory.Memory {
			return map[string]*memory.Memory{character: openMemory(t, fixedNow, nil, nil)}
		}, nil, false, false},
		{"可写且开着后台整理：描述保持原样", func(t *testing.T) map[string]*memory.Memory {
			return map[string]*memory.Memory{character: summarizingMemory(t)}
		}, names(character), false, false},
		{"一个可写不整理、一个在整理：有人在整理就保持原样", func(t *testing.T) map[string]*memory.Memory {
			return map[string]*memory.Memory{character: writableMemory(t), "mio": summarizingMemory(t)}
		}, names(character, "mio"), false, false},
		{"开着整理的角色不在 Characters 里：不算", func(t *testing.T) map[string]*memory.Memory {
			return map[string]*memory.Memory{character: writableMemory(t), "mio": summarizingMemory(t)}
		}, names(character), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := newKernel(t)
			k.enableChars(tc.mems(t), tc.chars)
			k.send("你好")
			got := k.toolDescriptions(0)
			want := orig["remember"]
			if tc.noAuto {
				want = strings.Replace(want, autoSummaryClaim, noAutoSummaryClaim, 1)
			}
			if tc.prefix {
				want = readOnlyRememberNote + want
			}
			if got["remember"] != want {
				t.Fatalf("remember 的描述：\n得 %q\n要 %q", got["remember"], want)
			}
			for _, name := range []string{"memory_search", "recall_raw"} {
				if got[name] != orig[name] {
					t.Fatalf("%s 的描述不该变：%q", name, got[name])
				}
			}
		})
	}
	// 换句子靠字面匹配：原描述里没有这句，替换就悄悄不生效了
	if !strings.Contains(orig["remember"], autoSummaryClaim) {
		t.Fatalf("remember 的原描述里没有 %q，plugin.go 的替换会悄悄失效：%q", autoSummaryClaim, orig["remember"])
	}
	if strings.Contains(noAutoSummaryClaim, "自动整理。") || strings.Contains(readOnlyRememberNote, autoSummaryClaim) {
		t.Errorf("换上去的句子不该再说会自动整理")
	}
	// 说明里不带库路径、配置键名
	for _, bad := range []string{"memory.write", "Writable", "characters", ".db", "config"} {
		if strings.Contains(readOnlyRememberNote, bad) {
			t.Errorf("说明里不该有 %q", bad)
		}
	}
}

// 端到端：只读的记忆，主人在私聊里让她记，模型调 remember，下一轮请求里带着只读的话，什么都没存；
// 同一个内核里群的请求没有 remember，也没有那句说明；写入打开时成功文字不退化，描述不带说明
func TestKernelRememberReadOnlyEndToEnd(t *testing.T) {
	t.Parallel()
	readOnly := func(t *testing.T) {
		k := newKernel(t)
		m := openMemory(t, fixedNow, nil, nil)
		k.enableChars(map[string]*memory.Memory{character: m}, func() []string { return []string{character} })
		k.mu.Lock()
		k.calls = []testToolCall{{"remember", `{"kind":"fact","text":"主人喜欢蓝色纸鹤","source_quote":"蓝色纸鹤","temporal":"stable"}`}, {}}
		k.mu.Unlock()
		k.send("记住我喜欢蓝色纸鹤")
		if k.requests() != 2 {
			t.Fatalf("请求数 %d", k.requests())
		}
		if d := k.toolDescriptions(0)["remember"]; !strings.HasPrefix(d, readOnlyRememberNote) {
			t.Fatalf("工具表里 remember 的描述：%q", d)
		}
		got := k.toolResult(1)
		if !strings.Contains(got, "这句话没有存下来") || !strings.Contains(got, "如实告诉对方") {
			t.Fatalf("下一轮请求里该带着只读的话：%q", got)
		}
		for _, bad := range []string{"记住了", "过一会儿", "出了点问题"} {
			if strings.Contains(got, bad) {
				t.Fatalf("结果里不该有 %q：%q", bad, got)
			}
		}
		if n := len(m.Items()); n != 0 {
			t.Fatalf("什么都不该存：%d 条", n)
		}

		// 群里：工具表里没有 remember，整份请求里也没有那句说明（群友看不出现在只读）
		a := audience.Audience{Channel: "qq", Conversation: groupA, Scene: "group", OwnerAsked: true}
		k.say(groupA, a, memory.ScopeKey(a), "owner", "owner", "记住我喜欢蓝色纸鹤")
		last := k.requests() - 1
		if _, has := k.toolDescriptions(last)["remember"]; has {
			t.Fatal("群里不该有 remember")
		}
		k.mu.Lock()
		body := k.bodies[last]
		k.mu.Unlock()
		if strings.Contains(body, "记忆写入没打开") || strings.Contains(body, "记忆是只读") {
			t.Fatalf("群的请求里不该有只读的说明：%q", body)
		}
	}
	writable := func(t *testing.T) {
		k := newKernel(t)
		m := writableMemory(t)
		k.enableChars(map[string]*memory.Memory{character: m}, func() []string { return []string{character} })
		k.mu.Lock()
		k.calls = []testToolCall{{"remember", `{"kind":"fact","text":"主人喜欢蓝色纸鹤","source_quote":"蓝色纸鹤","temporal":"stable"}`}, {}}
		k.mu.Unlock()
		k.send("记住我喜欢蓝色纸鹤")
		if d := k.toolDescriptions(0)["remember"]; strings.Contains(d, "没打开") || !strings.Contains(d, noAutoSummaryClaim) || strings.Contains(d, autoSummaryClaim) {
			t.Fatalf("只开了 remember（整理没开）：描述不带只读说明，但不再说后台会自动整理：%q", d)
		}
		if got := k.toolResult(1); got != "记住了。" {
			t.Fatalf("成功文字：%q", got)
		}
		if n := len(m.Items()); n != 1 {
			t.Fatalf("该存下一条：%d", n)
		}
	}
	t.Run("只读", readOnly)
	t.Run("写入打开", writable)
}

func TestRememberBindingSurvivesLaterStoredMessage(t *testing.T) {
	t.Parallel()
	var k *kernel
	k = newKernelWith(t, func(cfg *turn.Config) {
		cfg.OnTool = func(_, name string) {
			if name == "remember" {
				_, err := k.st.AppendEvent(store.NewEvent{Channel: "console", Conversation: "console:" + character, ActorKind: "owner", ActorID: "owner", Origin: "owner", Kind: "message", Text: "我喜欢咖啡", CreatedAt: fixedNow.UnixMilli()}, []string{character})
				if err != nil {
					t.Error(err)
				}
			}
		}
	})
	source, err := memsource.New(k.st, character)
	if err != nil {
		t.Fatal(err)
	}
	m, err := memory.Open(context.Background(), migratedFile(t, nil), nil, nil, memory.Options{Writable: true, Character: character, Events: source, Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	k.enable(m)
	k.mu.Lock()
	k.calls = []testToolCall{{"remember", `{"kind":"preference","text":"主人喜欢茶","source_quote":"我喜欢茶","temporal":"stable","input_seqs":[99999]}`}, {}}
	k.mu.Unlock()
	k.send("我喜欢茶")
	items := m.Items()
	if len(items) != 1 || len(items[0].SourceSeqs) != 1 {
		t.Fatalf("bound write: %+v", items)
	}
	var raw string
	if err := k.st.Events.QueryRow(`SELECT text FROM events WHERE seq=?`, items[0].SourceSeqs[0]).Scan(&raw); err != nil || raw != "我喜欢茶" {
		t.Fatalf("source=%q %v", raw, err)
	}
	k.mu.Lock()
	k.calls = []testToolCall{{"remember", `{"kind":"preference","text":"主人喜欢咖啡","source_quote":"我喜欢咖啡","temporal":"stable","input_seqs":[99999]}`}, {}}
	k.mu.Unlock()
	k.send("仍然只让你记这句茶")
	if len(m.Items()) != 1 {
		t.Fatal("unadmitted later event was used as source")
	}
}

// 自检会话（sumika-ctl chat）里：remember 不在工具表里，模型硬调也记不进去，记忆库一条没多；两条事件都带 selftest 标记，
// 记忆的事件视图读出来 Selftest 为真（整理按这个丢掉）。对照：同样的话在主窗口日常里 remember 在工具表里
func TestSelftestConversationCannotRemember(t *testing.T) {
	t.Parallel()
	conv := audience.SelftestConversation(character, audience.SelftestTopicID(character, ""))
	k := newKernel(t)
	m := openMemory(t, fixedNow, nil, nil)
	k.enable(m)
	before := len(m.Items())
	k.mu.Lock()
	k.calls = []testToolCall{{"remember", `{"kind":"fact","text":"自检里让记的一条"}`}, {}}
	k.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if o := <-k.hub.SendLocal(ctx, character, conv, "请记住：我喜欢蓝色", nil); o.Err != nil {
		t.Fatal(o.Err)
	}
	has := func(i int, name string) bool {
		var body struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.Unmarshal([]byte(k.bodies[i]), &body); err != nil {
			t.Fatal(err)
		}
		for _, tool := range body.Tools {
			if tool.Function.Name == name {
				return true
			}
		}
		return false
	}
	if has(0, "remember") || !has(0, "memory_search") {
		t.Fatal("自检会话的工具表里不该有 remember（memory_search 照常有）")
	}
	if len(m.Items()) != before {
		t.Fatalf("自检会话里记进了长期记忆：%d → %d", before, len(m.Items()))
	}
	src, err := memsource.New(k.st, character)
	if err != nil {
		t.Fatal(err)
	}
	evs, err := src.Events(ctx, conv, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("该有主人的话和她的回复两条：%d", len(evs))
	}
	for _, e := range evs {
		if !e.Selftest {
			t.Fatalf("事件视图里这一条没有标成自检：%+v", e)
		}
	}
	// 对照：主窗口日常里 remember 在
	k.mu.Lock()
	start := len(k.bodies)
	k.mu.Unlock()
	k.send("普通的一句")
	if !has(start, "remember") {
		t.Fatal("对照失败：主窗口日常的工具表里该有 remember，这个测试没有意义")
	}
}

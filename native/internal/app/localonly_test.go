package app

// P2-1 第 1 步：只开本地（Options.LocalOnly）。验收 1、3、4、5 的 app 部分（规格-不带QQ也能用）。
// 号、角色、句子都是编的；数据目录只用 t.TempDir()，不连真协议端。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/applog"
	"sumika-native/internal/approval"
	"sumika-native/internal/audience"
	"sumika-native/internal/channel"
	"sumika-native/internal/channel/sendlock"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/embed"
	"sumika-native/internal/filelog"
	"sumika-native/internal/kernel"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/paths"
)

// drainableSnapshot 记一个目录里所有文件（相对路径 → 内容），用来断言“一个文件都没写”。
func dirSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	got := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			got[rel] = "<dir>"
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		got[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func sameSnapshot(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func openLocal(t *testing.T, e *testEnv) *App {
	t.Helper()
	a, err := Open(context.Background(), e.localOnlyOptions())
	if err != nil {
		t.Fatalf("只开本地 Open：%v", err)
	}
	return a
}

// 验收 1：目录里只有 config 和一张卡、没有身份证，配置里没有 qq.accounts。只开本地打开后：
// 身份证写好了，local 库可用，QQ 单元（适配器、令牌、名册、补录）一个都没有；本地私聊经假模型回一句、remember 能写；
// 关掉再打开照旧能用。
func TestLocalOnlyFreshDataDir(t *testing.T) {
	t.Parallel()
	e := newLocalEnv(t)
	if _, err := os.Stat(filepath.Join(e.data, paths.MetaFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("夹具不该先写身份证：%v", err)
	}
	// 假模型第一次回工具调用，第二次回一句；重开后第三次回一句
	models := []json.RawMessage{json.RawMessage(`{"id":"` + testModel + `","context_length":32768}`)}
	srv := httptest.NewServer(llmtest.Handler(llmtest.Script{
		Models: models,
		Replies: []llmtest.Reply{
			llmtest.Call("m1", "remember", `{"kind":"preference","text":"主人喜欢喝乌龙茶","source_quote":"我喜欢喝乌龙茶"}`),
			llmtest.Text("记好了。"),
			llmtest.Text("还在。"),
		},
	}, &e.bodies, &e.mu))
	t.Cleanup(srv.Close)
	e.llm = srv

	a := openLocal(t, e)
	ctx := context.Background()
	key := localKey("console:" + testChar)
	d := a.Desktop()
	if _, err := os.Stat(filepath.Join(e.data, paths.MetaFile)); err != nil {
		t.Fatalf("只开本地该初始化身份证：%v", err)
	}
	if _, ok := a.set.Platform("local"); !ok {
		t.Fatal("local 库不可用")
	}
	if a.adapter != nil || len(a.tokens) != 0 || len(a.roster.Accounts) != 0 || a.backfill != nil {
		t.Fatalf("只开本地不该装 QQ 单元：adapter=%v tokens=%v roster=%v backfill=%v", a.adapter, a.tokens, a.roster.Accounts, a.backfill)
	}
	if len(a.channels) != 0 {
		t.Fatalf("只开本地不该有渠道：%+v", a.channels)
	}
	if a.alerts == nil {
		t.Fatal("接收模式的通知出口该建好")
	}
	if m := a.memories[testChar]; m == nil || !m.Writable() {
		t.Fatalf("remember 默认开，记忆库该可写：%+v", m)
	}
	if err := d.Reader.SendLocal(ctx, key, "我喜欢喝乌龙茶，记一下", nil); err != nil {
		t.Fatal(err)
	}
	first := lastToolMessage(t, bodyAt(t, e, 1))
	if !strings.Contains(first, "记住了") || strings.Contains(first, "没有记忆库") || strings.Contains(first, "只读") {
		t.Fatalf("remember 该写成功，工具结果：%q", first)
	}
	page, err := d.Reader.ReadMessages(ctx, api.ConversationRequest{Key: key, Limit: 20}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Value.Items) != 2 || page.Value.Items[1].ActorKind != "character" || page.Value.Items[1].Text != "记好了。" {
		t.Fatalf("本地私聊该有她的回复：%+v", page.Value.Items)
	}
	// 本地回合用的是会话里的角色，不读 a.character（只开本地时它是空串）
	if !strings.Contains(bodyAt(t, e, 0), "示例角色") && !strings.Contains(bodyAt(t, e, 1), "示例角色") {
		t.Fatalf("请求里没有会话角色的卡：%s", bodyAt(t, e, 0))
	}

	d.Close()
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	// 关掉再打开：同一个数据目录，照旧能用
	b := openLocal(t, e)
	defer b.Close()
	bd := b.Desktop()
	defer bd.Close()
	if err := bd.Reader.SendLocal(ctx, key, "还在吗", nil); err != nil {
		t.Fatal(err)
	}
	page, err = bd.Reader.ReadMessages(ctx, api.ConversationRequest{Key: key, Limit: 20}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Value.Items) == 0 || page.Value.Items[len(page.Value.Items)-1].Text != "还在。" {
		t.Fatalf("重开后本地私聊没有回复：%+v", page.Value.Items)
	}
}

// 目录完全不存在、或只有 models/：openDataDir 只对 ErrUninitialized 初始化。app.Open 还需要角色卡，所以这里单独钉住这一段。
func TestOpenDataDirInitializesMissingDirectory(t *testing.T) {
	t.Parallel()
	t.Run("目录不存在", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "不存在")
		p, initialized, err := openDataDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if !initialized || p.Root == "" {
			t.Fatalf("不存在的目录该报告这次初始化了：initialized=%v root=%q", initialized, p.Root)
		}
		if _, err := os.Stat(filepath.Join(dir, paths.MetaFile)); err != nil {
			t.Fatalf("不存在的目录该初始化：%v", err)
		}
	})
	t.Run("只有 models", func(t *testing.T) {
		dir := t.TempDir()
		model := filepath.Join(dir, "models", "编的模型", "model.onnx")
		if err := os.MkdirAll(filepath.Dir(model), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(model, []byte("编的"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, initialized, err := openDataDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if !initialized {
			t.Fatal("只有 models/ 的目录该报告这次初始化了")
		}
		if _, err := os.Stat(filepath.Join(dir, paths.MetaFile)); err != nil {
			t.Fatalf("只有 models/ 的目录该初始化：%v", err)
		}
		if _, err := os.Stat(model); err != nil {
			t.Fatalf("初始化不该删模型：%v", err)
		}
	})
}

// app 层真的初始化目录时也要在日志里留一行“新建了数据目录 <路径>（写了身份证）”。
func TestOpenLogsDataDirInit(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	if err := os.Remove(filepath.Join(e.data, paths.MetaFile)); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	logs := applog.New(filelog.New(e.data, "go", &buf, time.Now), time.Now)
	defer logs.Close()
	opt := e.options()
	opt.Logs = logs
	a, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if !strings.Contains(buf.String(), "新建了数据目录") || !strings.Contains(buf.String(), "写了身份证") {
		t.Fatalf("日志里没有初始化那一行：%s", buf.String())
	}
}

// 目录不存在时的完整 Open：只开本地会先初始化，然后因为没有角色卡停在“一个角色都没有”。
// 全新安装复制示例卡归第 2 步宿主；这一步钉住现状，不改卡的问题。
func TestOpenMissingDataDirNeedsCharacterCard(t *testing.T) {
	t.Parallel()
	e := newLocalEnv(t)
	opt := e.localOnlyOptions()
	opt.DataDir = filepath.Join(t.TempDir(), "全新")
	a, err := Open(context.Background(), opt)
	if a != nil {
		a.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "一个角色都没有") {
		t.Fatalf("没有角色卡该报一个角色都没有：%v", err)
	}
	if _, err := os.Stat(filepath.Join(opt.DataDir, paths.MetaFile)); err != nil {
		t.Fatalf("完整 Open 遇到不存在的目录也该先初始化：%v", err)
	}
}

// 验收 4（app 层）：旧布局、没恢复完、有数据没身份证的目录用只开本地打开都报原来的错，一个文件都不写。
func TestLocalOnlyRefusesUninitializedLookalikes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		set  func(t *testing.T, data string)
	}{
		{"旧布局", func(t *testing.T, data string) {
			if err := os.MkdirAll(filepath.Join(data, "shared"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(data, "shared", "events.db"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"没恢复完", func(t *testing.T, data string) {
			marker := filepath.Join(data, paths.RestoreDir, paths.RestoreIncompleteFile)
			if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(marker, []byte(`{"state":"restoring"}`), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"有数据", func(t *testing.T, data string) {
			if err := os.MkdirAll(filepath.Join(data, "go"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"有 db", func(t *testing.T, data string) {
			if err := os.WriteFile(filepath.Join(data, "qq.db"), []byte("编的"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"身份证读坏", func(t *testing.T, data string) {
			if err := os.WriteFile(filepath.Join(data, paths.MetaFile), []byte(`{"format":`), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newLocalEnv(t)
			tc.set(t, e.data)
			before := dirSnapshot(t, e.data)
			a, err := Open(context.Background(), e.localOnlyOptions())
			if a != nil {
				a.Close()
			}
			if err == nil {
				t.Fatal("该报错")
			}
			if !strings.Contains(err.Error(), "没有") && !strings.Contains(err.Error(), "恢复") && !strings.Contains(err.Error(), "旧布局") && !strings.Contains(err.Error(), "读不懂") {
				t.Fatalf("该是原来的错：%v", err)
			}
			if after := dirSnapshot(t, e.data); !sameSnapshot(before, after) {
				t.Fatalf("报错的目录被写了文件：之前 %v，之后 %v", before, after)
			}
			if tc.name == "身份证读坏" {
				// 本来就有一个读坏的身份证：要原样留着，不能被覆盖成格式 1
				b, err := os.ReadFile(filepath.Join(e.data, paths.MetaFile))
				if err != nil || string(b) != `{"format":` {
					t.Fatalf("读坏的身份证被改了：%q（%v）", b, err)
				}
			} else if _, err := os.Stat(filepath.Join(e.data, paths.MetaFile)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("不该写身份证：%v", err)
			}
		})
	}
}

// 验收 5：账号非空时缺 URL、缺令牌、号没配置都在建库之前失败（assertNoStores 那套）。
func TestReceiveAccountFailuresBeforeStores(t *testing.T) {
	t.Parallel()
	t.Run("号没配置", func(t *testing.T) {
		e := newEnv(t)
		opt := e.options()
		opt.Account = "599"
		a, err := Open(context.Background(), opt)
		if a != nil {
			a.Close()
		}
		if err == nil || !strings.Contains(err.Error(), "不在 qq.accounts") {
			t.Fatalf("该报号不在配置里：%v", err)
		}
		assertNoStores(t, opt.DataDir)
	})
	t.Run("缺 URL", func(t *testing.T) {
		e := newEnv(t)
		file := filepath.Join(e.data, "config", "sumika.yml")
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		text := strings.Replace(string(b), `url: "`+e.peer.url()+`"`, `url: ""`, 1)
		if text == string(b) {
			t.Fatal("没改到 URL")
		}
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		opt := e.options()
		a, err := Open(context.Background(), opt)
		if a != nil {
			a.Close()
		}
		if err == nil || !strings.Contains(err.Error(), "没有写 qq.accounts[].url") {
			t.Fatalf("该报缺 URL：%v", err)
		}
		assertNoStores(t, opt.DataDir)
	})
	t.Run("缺令牌", func(t *testing.T) {
		e := newEnv(t)
		e.writeConfig("")
		opt := e.options()
		a, err := Open(context.Background(), opt)
		if a != nil {
			a.Close()
		}
		if err == nil || !strings.Contains(err.Error(), "访问令牌") {
			t.Fatalf("该报取不到令牌：%v", err)
		}
		assertNoStores(t, opt.DataDir)
	})
}

// 加一张第二角色的卡和配置项：本地群聊至少要两个角色。
func addSecondCharacter(t *testing.T, e *testEnv) {
	t.Helper()
	src := filepath.Join(e.root, "characters", "example")
	dst := filepath.Join(e.data, "characters", "second", "card")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"character.yml", "character.md"} {
		b, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(e.data, "config", "sumika.yml")
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(b), "characters:\n", "characters:\n  second:\n    model: "+testModel+"\n", 1)
	if text == string(b) {
		t.Fatal("没改到配置")
	}
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// linkProbeStored 是空账号、空角色那一行的探针：Actor.ID 用 "owner" 才走得到 CanTrigger 之后，
// 群会话放进 linkReader.expand.conversations 才能让 Expand.Allows 放行。
func linkProbeStored() (channel.Stored, types.Conversation) {
	conv := types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: testGroup}
	return channel.Stored{
		Seq:       1,
		Inbound:   types.Inbound{Conv: conv, Account: "", Kind: types.InboundMessage, PlatformID: "1"},
		Character: "",
		Actor:     channel.Actor{Kind: channel.ActorOwner, ID: "owner"},
		Origin:    channel.OriginOwner,
	}, conv
}

// linkWhitelist 只把测试群放进展开白名单；只开本地的配置也能读它。
func linkWhitelist() string {
	return fmt.Sprintf("linkReader:\n  expand:\n    enabled: true\n    mode: whitelist\n    conversations: [\"qq:group:%s\"]\n", testGroup)
}

// writeLocalSendAll 把只开本地的配置改成发送条件全开：goSend、群档位 speak。
func writeLocalSendAll(t *testing.T, e *testEnv) {
	t.Helper()
	file := filepath.Join(e.data, "config", "sumika.yml")
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if strings.Count(text, "  privateAllow: []\n") != 1 || strings.Count(text, "  mode: dry\n") != 1 {
		t.Fatalf("测试配置形状变了：%s", text)
	}
	text = strings.Replace(text, "  privateAllow: []\n", "  privateAllow: []\n  goSend: true\n", 1)
	text = strings.Replace(text, "  mode: dry\n", "  mode: speak\n", 1)
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 验收 3：四种场合（本地私聊、本地群聊、QQ 私聊、QQ 群）加空账号、空角色，三种入口（只开本地、完整接收、只看历史）。
// 每格断言能不能写、发送门结果；完整接收那一列的门槛仍是 send57_* 原样通过。
func TestWriteAndSendGateMatrix(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"只开本地", "完整接收", "只看历史"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			var (
				a     *App
				e     *testEnv
				close func()
			)
			switch mode {
			case "只开本地":
				e = newLocalEnv(t)
				e.extraConfig = linkWhitelist()
				e.writeConfigNoQQ()
				writeLocalSendAll(t, e)
				addSecondCharacter(t, e)
				a = openLocal(t, e)
				// 意图文件写“交给这个号的 Go”：出口仍不该有发送权（只开本地不拿锁、不装适配器）
				writeSenderIntent(t, e.data, testAccount, sendlock.HolderGo)
				close = func() { a.Close() }
			case "完整接收":
				e = newEnv(t)
				addSecondCharacter(t, e)
				a = e.open()
				close = func() { a.Close() }
			case "只看历史":
				opt := archiveFixture(t, "matrix")
				var err error
				a, err = Open(context.Background(), opt)
				if err != nil {
					t.Fatal(err)
				}
				close = func() { a.Close() }
			}
			defer close()
			ctx := context.Background()
			key := localKey("console:" + testChar)

			// 本地私聊：只开本地、完整接收能写；只看历史不能写
			err := a.Desktop().Reader.SendLocal(ctx, key, "在吗", nil)
			switch mode {
			case "只看历史":
				if !errors.Is(err, api.ErrLocalChatUnsupported) {
					t.Fatalf("只看历史的本地私聊该拒绝：%v", err)
				}
			default:
				if err != nil {
					t.Fatalf("本地私聊该能写：%v", err)
				}
			}

			// 本地群聊：只开本地、完整接收能建能写；只看历史没有本地群服务
			groupKey := api.ConversationKey{Platform: "local", Channel: "console", Scene: "group", Conversation: ""}
			view, err := a.Desktop().Reader.CreateLocalGroup(ctx, api.LocalGroupCreate{Name: "编的群", Characters: []string{testChar, "second"}})
			switch mode {
			case "只看历史":
				if !errors.Is(err, api.ErrLocalGroupUnsupported) {
					t.Fatalf("只看历史该没有本地群写入：%v", err)
				}
			default:
				if err != nil {
					t.Fatalf("本地群聊该能建：%v", err)
				}
				groupKey = view.Key
				if err := a.Desktop().Reader.SendLocalGroup(ctx, api.LocalGroupSend{Key: groupKey, Text: "@" + testChar + " 在吗"}); err != nil {
					t.Fatalf("本地群聊该能写：%v", err)
				}
			}

			// QQ 单元：只有完整接收才有。只开本地这一列故意把配置全开（goSend、群档位 speak）并写了意图文件，
			// QQ 两个场合仍然不该说话，发送锁、适配器都不该有。
			qqPrivate := types.Conversation{Channel: "qq", Scene: types.ScenePrivate, ID: testOwner, Self: testAccount}
			qqGroup := types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: testGroup}
			localPrivate := types.Conversation{Channel: "console", Scene: types.ScenePrivate, ID: testOwner}
			localGroup := types.Conversation{Channel: "console", Scene: types.SceneGroup, ID: "group"}
			switch mode {
			case "只开本地":
				if a.adapter != nil || len(a.roster.Accounts) != 0 || a.backfill != nil || a.sendLock != nil {
					t.Fatalf("只开本地装上了 QQ 单元：adapter=%v roster=%v backfill=%v sendLock=%v", a.adapter, a.roster.Accounts, a.backfill, a.sendLock)
				}
			case "完整接收":
				if a.adapter == nil || len(a.roster.Accounts) != 1 || a.backfill == nil || a.outlet == nil {
					t.Fatalf("完整接收该装 QQ 单元：adapter=%v roster=%v backfill=%v outlet=%v", a.adapter, a.roster.Accounts, a.backfill, a.outlet)
				}
			case "只看历史":
				if a.adapter != nil || a.backfill != nil || a.sendLock != nil || a.outlet != nil || a.modeOf != nil {
					t.Fatalf("只看历史不该装 QQ 单元或发送门：adapter=%v backfill=%v sendLock=%v outlet=%v modeOf 非 nil", a.adapter, a.backfill, a.sendLock, a.outlet)
				}
			}
			if mode == "只看历史" {
				// 发送门断言：只看历史连出口都没有，QQ 两格不可能有发送档位
				if a.outlet != nil || a.modeOf != nil {
					t.Fatal("只看历史不该有出口或发送档位")
				}
			} else {
				if got := a.modeOf(localPrivate); got != channel.ModeSpeak {
					t.Fatalf("本地私聊发送门 %q，要 speak", got)
				}
				if got := a.modeOf(localGroup); got != channel.ModeSpeak {
					t.Fatalf("本地群聊发送门 %q，要 speak", got)
				}
				if got := a.modeOf(qqPrivate); got == channel.ModeSpeak {
					t.Fatalf("QQ 私聊不该说话：%q", got)
				}
				if got := a.modeOf(qqGroup); got == channel.ModeSpeak {
					t.Fatalf("QQ 群不该说话：%q", got)
				}
			}
			if mode == "只开本地" {
				for _, conv := range []types.Conversation{qqPrivate, qqGroup} {
					_, err := a.outlet.Send(ctx, testChar, types.OriginCharacter, conv, nil)
					if err == nil || !strings.Contains(err.Error(), "没有适配器") {
						t.Fatalf("只开本地出口发 %s 该报没有适配器：%v", conv.String(), err)
					}
				}
			}

			// 空账号、空角色两行（探针形状：Actor.ID=owner、群进展开白名单）：任何模式都不该成立
			probe, probeConv := linkProbeStored()
			if a.admitLink(probe) || a.admitLink(channel.Stored{}) {
				t.Fatal("空账号、空角色不能进链接展开")
			}
			if mode != "只看历史" {
				if got := a.outletMode("", linkOrigin, probeConv); got != channel.ModeRecord {
					t.Fatalf("空角色的链接发送门 %q，要 record", got)
				}
			}
			if a.lifeOpenGroup(kernel.Invocation{Kind: kernel.KindForeground, Scope: kernel.Scope{Channel: "qq", Platform: "qq", Scene: "group", Conversation: "qq:group:" + testGroup, AccountID: "", CharacterID: "", ActorKind: "human", ActorID: testGroup}}, audience.Audience{Channel: "qq", Conversation: "qq:group:" + testGroup, Scene: "group"}) {
				t.Fatal("空账号、空角色的 QQ 群不该放开所在地")
			}
		})
	}
}

// 只开本地时角色状态写“这次没开 QQ”，不再把所有人标成“没有接收”。
func TestLocalOnlyDisplayCharactersStatus(t *testing.T) {
	t.Parallel()
	e := newLocalEnv(t)
	a := openLocal(t, e)
	defer a.Close()
	for _, r := range a.displayCharacters() {
		if r.Value.Status != "这次没开 QQ" {
			t.Fatalf("角色 %s 状态 %q，要“这次没开 QQ”", r.Value.ID, r.Value.Status)
		}
	}
}

// 设计第 2 条“空对空”清单里会成立的几处：只开本地时 a.account、a.character 都是空串，
// 比较前必须先看非空，或者先判渠道。每一处一个用例。
func TestLocalOnlyEmptyAccountNeverOpens(t *testing.T) {
	t.Parallel()
	e := newLocalEnv(t)
	// 群进链接展开白名单，链接发送门/准入的探针才能走到角色、账号比较
	e.extraConfig = linkWhitelist()
	e.writeConfigNoQQ()
	// 打开主人 QQ 私聊审批，就是为了证明绑定处会挡空账号（不是靠“配置没开”）；
	// 带一个小号，证明客户端自知是卡在空账号，不是配置里没有号。
	file := filepath.Join(e.data, "config", "sumika.yml")
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(b), "qq:\n  owner: "+testOwner+"\n", "qq:\n  owner: "+testOwner+"\n  accounts:\n    - id: "+testAccount+"\n      character: "+testChar+"\n  remoteApproval:\n    enabled: true\n    awayAfterSeconds: 1\n", 1)
	if text == string(b) {
		t.Fatal("没改到配置")
	}
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	a := openLocal(t, e)
	defer a.Close()

	t.Run("链接发送门不把空角色当自己", func(t *testing.T) {
		_, conv := linkProbeStored()
		if got := a.outletMode("", linkOrigin, conv); got != channel.ModeRecord {
			t.Fatalf("空角色链接发送门是 %q，要 record", got)
		}
	})
	t.Run("链接准入不认空账号的空消息", func(t *testing.T) {
		stored, _ := linkProbeStored()
		if a.admitLink(stored) {
			t.Fatal("空账号、空角色的消息不该进链接展开")
		}
	})
	t.Run("QQ 群所在地不放行", func(t *testing.T) {
		conv := "qq:group:" + testGroup
		in := kernel.Invocation{Kind: kernel.KindForeground, Scope: kernel.Scope{
			Channel: "qq", Platform: "qq", Scene: "group", Conversation: conv,
			AccountID: "", CharacterID: "", ActorKind: "human", ActorID: testGroup,
		}}
		if a.lifeOpenGroup(in, audience.Audience{Channel: "qq", Conversation: conv, Scene: "group"}) {
			t.Fatal("空账号、空角色的 QQ 群不该放开所在地")
		}
	})
	t.Run("主人 QQ 私聊审批不绑定空账号", func(t *testing.T) {
		if _, ok := a.remoteBinding(testChar); ok {
			t.Fatal("空账号不该绑上主人 QQ 私聊审批")
		}
	})
	t.Run("提醒不求空账号的 QQ 会话", func(t *testing.T) {
		if _, why := a.qqOwnerConv(testChar); why == "" {
			t.Fatal("空账号不该拼出主人 QQ 私聊")
		}
	})
	t.Run("客户端自知不认空账号的 QQ 私聊", func(t *testing.T) {
		conv := "qq:private:" + testAccount + ":" + testOwner
		in := kernel.Invocation{Kind: kernel.KindForeground, Scope: kernel.Scope{
			Channel: "qq", Platform: "qq", Scene: "private", Conversation: conv, ScopeID: "s1",
			AccountID: "", CharacterID: testChar, ActorKind: "owner", ActorID: "owner",
		}}
		aud := audience.Audience{Channel: "qq", Conversation: conv, Scene: "private", PrivateOK: true, OwnerAsked: true}
		if a.clientInfoOwner(in, aud) {
			t.Fatal("空账号不该被当成主人 QQ 私聊")
		}
		if a.lifeQQBindingCurrent(in) {
			t.Fatal("空账号不该通过 QQ 绑定核对")
		}
	})
	t.Run("客户端自知不认非数字账号", func(t *testing.T) {
		// 空账号那一半和后面的 conv.Self == a.account 一起挡；这里把号换成非数字，
		// 让 validClientQQID(a.account) 成为唯一挡住的判断（删掉它这个用例必须红）。
		account := testAccount + "x"
		a.account, a.character = account, testChar
		defer func() { a.account, a.character = "", "" }()
		conv := "qq:private:" + account + ":" + testOwner
		in := kernel.Invocation{Kind: kernel.KindForeground, Scope: kernel.Scope{
			Channel: "qq", Platform: "qq", Scene: "private", Conversation: conv, ScopeID: "s1",
			AccountID: account, CharacterID: testChar, ActorKind: "owner", ActorID: "owner",
		}}
		aud := audience.Audience{Channel: "qq", Conversation: conv, Scene: "private", PrivateOK: true, OwnerAsked: true}
		if a.clientInfoOwner(in, aud) {
			t.Fatal("非数字账号不该被当成主人 QQ 私聊")
		}
	})
}

// 完整接收时 QQ 角色卡读坏：必须停在审批门、记忆、嵌入之前（checkModels 改动前的位置），
// 不能等 QQ 单元装配才失败，也不能悄悄退回只开本地。
func TestReceiveQQUnitFailureDoesNotFallBackToLocal(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	card := filepath.Join(e.data, "characters", testChar, "card", "character.yml")
	if err := os.WriteFile(card, []byte("name: ["), 0o644); err != nil {
		t.Fatal(err)
	}
	// 打开嵌入，验证失败发生在起嵌入子进程之前
	file := filepath.Join(e.data, "config", "sumika.yml")
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(b), "embed:\n  enabled: false", "embed:\n  enabled: true", 1)
	if text == string(b) {
		t.Fatal("没改到嵌入配置")
	}
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	embedStarted, gateOpened := false, false
	opt := e.options()
	opt.NewEmbed = func(cfg embed.LocalConfig, _ func(string, ...any)) LocalEmbedding {
		embedStarted = true
		return &guardLocal{cfg: cfg}
	}
	opt.OnGate = func(*approval.Gate) { gateOpened = true }
	a, err := Open(context.Background(), opt)
	if a != nil {
		a.Close()
	}
	if err == nil {
		t.Fatal("QQ 单元失败时启动该失败，不该退回只开本地")
	}
	if !strings.Contains(err.Error(), "要连的角色") {
		t.Fatalf("该报账号绑的角色读不了：%v", err)
	}
	if embedStarted {
		t.Fatal("角色卡坏该在起嵌入子进程之前失败")
	}
	if gateOpened {
		t.Fatal("角色卡坏该在建审批门之前失败")
	}
	// 记忆是在审批门、嵌入之后才开的：这两个库都不该出现
	for _, db := range []string{filepath.Join(e.data, "go", "characters", testChar, "memory.db"), filepath.Join(e.data, "shared", "owner-memory.db")} {
		if _, err := os.Stat(db); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("角色卡坏不该开记忆：%s 已存在（%v）", db, err)
		}
	}
}

// 只开本地时即使配置里有一个小号，补录也保持 nil，补录页拿空的账号、回“这次没开 QQ”。
func TestLocalOnlyBackfillStaysOff(t *testing.T) {
	t.Parallel()
	e := newLocalEnv(t)
	file := filepath.Join(e.data, "config", "sumika.yml")
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(b), "qq:\n  owner: "+testOwner+"\n", "qq:\n  owner: "+testOwner+"\n  accounts:\n    - id: "+testAccount+"\n      character: "+testChar+"\n", 1)
	if text == string(b) {
		t.Fatal("没改到配置")
	}
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	a := openLocal(t, e)
	defer a.Close()
	if a.backfill != nil || a.media != nil || a.forwards != nil {
		t.Fatalf("只开本地不该建 QQ 单元：backfill=%v media=%v forwards=%v", a.backfill, a.media, a.forwards)
	}
	if got := a.backfillProvider().Account; got != "" {
		t.Fatalf("补录页拿到的账号 %q，要空串", got)
	}
	v, err := a.alignment(context.Background())
	var be *api.BackfillError
	if err != nil && (!errors.As(err, &be) || be.Code != "unavailable") {
		t.Fatalf("补录页该回不可用：%v", err)
	}
	if v.ReasonCode != "unavailable" || v.CanRun {
		t.Fatalf("补录页该回不可用：%+v", v)
	}
}

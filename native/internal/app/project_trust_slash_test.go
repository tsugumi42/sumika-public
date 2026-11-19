package app

// /trust（规格-审批分级 8.5）：项目说明拒绝过以后想改主意，不用重启——清掉“这次运行里不再问”，下一次发消息重新弹卡。
// 形状：被拒后重问、已经信任、没有说明文件、项目说明没开、带多余参数、命令不发给模型、不当场替主人信任。
// 数据目录只用 t.TempDir()。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/llm/llmtest"
)

type trustEnv struct {
	t    *testing.T
	e    *testEnv
	a    *App
	d    *Desktop
	key  api.ConversationKey
	sub  api.Subscription[[]api.LocalApproval]
	done chan error
	seen map[string]bool // 已经拿过的卡：订阅里可能还压着回答之前的快照，不能把旧卡再当新卡
}

func newTrustEnv(t *testing.T, withFile bool) *trustEnv {
	t.Helper()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text("好的")})
	if withFile {
		if err := os.WriteFile(filepath.Join(a.workdir(), "AGENTS.md"), []byte("编的项目规矩：先说再做"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sub, err := d.Reader.WatchLocalApprovals(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sub.Close() })
	return &trustEnv{t: t, e: e, a: a, d: d, key: localKey("console:" + testChar), sub: sub, seen: map[string]bool{}}
}

func (x *trustEnv) send(text string) chan error {
	done := make(chan error, 1)
	go func() { done <- x.d.Reader.SendLocal(context.Background(), x.key, text, nil) }()
	return done
}

func (x *trustEnv) card() api.LocalApproval {
	for {
		for _, c := range localRecv(x.t, x.sub.Updates()).Value {
			if !x.seen[c.ID] {
				x.seen[c.ID] = true
				return c
			}
		}
	}
}

func (x *trustEnv) bodies() string {
	x.e.mu.Lock()
	defer x.e.mu.Unlock()
	return strings.Join(x.e.bodies, "\n")
}

func (x *trustEnv) lastNotice() string {
	rows := slashRows(x.t, x.a, x.key.Conversation)
	for i := len(rows) - 1; i >= 0; i-- {
		if strings.HasPrefix(rows[i], "notice/system/") {
			return rows[i]
		}
	}
	return ""
}

// 拒绝后这次运行里不再问；发 /trust 以后下一次发消息重新弹卡，这回答记住，之后读得到、不再问
func TestSlashTrustReasksAfterDeny(t *testing.T) {
	t.Parallel()
	x := newTrustEnv(t, true)
	ctx := context.Background()
	done := x.send("第一句")
	c := x.card()
	if c.Tool != "project_trust" {
		t.Fatalf("信任卡：%+v", c)
	}
	if err := x.d.Reader.DecideLocal(ctx, x.key, c.ID, api.ApproveDeny); err != nil {
		t.Fatal(err)
	}
	localRecv(t, done)
	localRecv(t, x.send("第二句")) // 拒绝过：不再问，整轮直接结束
	if strings.Contains(x.bodies(), "编的项目规矩") {
		t.Fatal("拒绝以后不该读说明文件")
	}
	before := len(x.e.bodiesCopy())
	// /trust：只回一句话，不发给模型，不当场信任
	localRecv(t, x.send("/trust"))
	if n := len(x.e.bodiesCopy()); n != before {
		t.Fatalf("/trust 不该发给模型：%d → %d", before, n)
	}
	if note := x.lastNotice(); !strings.Contains(note, "重新问一次") || !strings.Contains(note, "1 个文件") {
		t.Fatalf("回复该说明会重新问：%q", note)
	}
	if rules, _ := x.d.Reader.ListApprovalRules(ctx); len(rules) != 0 {
		t.Fatalf("/trust 不能替主人信任：%+v", rules)
	}
	// 下一次发消息重新弹卡
	done = x.send("第三句")
	c = x.card()
	if c.Tool != "project_trust" {
		t.Fatalf("/trust 之后该再问：%+v", c)
	}
	if err := x.d.Reader.DecideLocal(ctx, x.key, c.ID, api.ApproveAlways); err != nil {
		t.Fatal(err)
	}
	localRecv(t, done)
	if !strings.Contains(x.bodies(), "编的项目规矩") {
		t.Fatal("这回答了记住，这一轮就该带上说明文件")
	}
	localRecv(t, x.send("第四句")) // 不再问
	// 已经信任：/trust 说明已信任，不再弹卡
	localRecv(t, x.send("/trust"))
	if note := x.lastNotice(); !strings.Contains(note, "已经信任了") {
		t.Fatalf("已经信任该这么说：%q", note)
	}
}

// 答“允许一次”以后 /trust：说明是这次运行里允许的，没有规则可撤
func TestSlashTrustAfterAllowOnce(t *testing.T) {
	t.Parallel()
	x := newTrustEnv(t, true)
	ctx := context.Background()
	done := x.send("第一句")
	c := x.card()
	if err := x.d.Reader.DecideLocal(ctx, x.key, c.ID, api.ApproveOnce); err != nil {
		t.Fatal(err)
	}
	localRecv(t, done)
	localRecv(t, x.send("/trust"))
	if note := x.lastNotice(); !strings.Contains(note, "允许过一次") || strings.Contains(note, "撤销") {
		t.Fatalf("允许一次的说法不对：%q", note)
	}
}

func TestSlashTrustNoInstructionFiles(t *testing.T) {
	t.Parallel()
	x := newTrustEnv(t, false)
	localRecv(t, x.send("/trust"))
	if note := x.lastNotice(); !strings.Contains(note, "没有项目说明文件") {
		t.Fatalf("没有说明文件该这么说：%q", note)
	}
}

func TestSlashTrustRejectsArgs(t *testing.T) {
	t.Parallel()
	x := newTrustEnv(t, true)
	localRecv(t, x.send("/trust 别的目录"))
	if note := x.lastNotice(); !strings.Contains(note, "不带参数") {
		t.Fatalf("多余参数该说清楚：%q", note)
	}
	// 没问过的目录，被拒的命令也不该当成信任
	if rules, _ := x.d.Reader.ListApprovalRules(context.Background()); len(rules) != 0 {
		t.Fatalf("不该有规则：%+v", rules)
	}
}

// 项目说明整体关着：回一句没开，不崩
func TestSlashTrustWhenProjectContextOff(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text("好的")})
	a.projectTrustDyn = nil // 模拟 projectContext.enabled: false（装配时就是 nil）
	key := localKey("console:" + testChar)
	if err := d.Reader.SendLocal(context.Background(), key, "/trust", nil); err != nil {
		t.Fatal(err)
	}
	rows := slashRows(t, a, key.Conversation)
	if len(rows) == 0 || !strings.Contains(rows[len(rows)-1], "项目说明没开") {
		t.Fatalf("该回没开：%v", rows)
	}
}

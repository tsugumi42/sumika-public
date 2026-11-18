package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/store"
)

// 按会话选工作目录（规格-按会话选工作目录）：入口、场合、验收 1、3、6。

func writeCall(id, path, content string) llmtest.Reply {
	args, _ := json.Marshal(map[string]string{"purpose": "写测试文件", "path": path, "content": content})
	return llmtest.Call(id, "write", string(args))
}

func addTopic(t *testing.T, a *App, id string) api.ConversationKey {
	t.Helper()
	st, _ := a.set.For("console")
	if _, err := st.Events.Exec(`INSERT INTO local_topics(id,character_id,created_at) VALUES (?,?,1)`, id, testChar); err != nil {
		t.Fatal(err)
	}
	return localKey("console:" + testChar + ":topic:" + id)
}

// 发一句话、等审批卡、批准、等这一轮结束；返回审批卡
func sendAndApprove(t *testing.T, d *Desktop, key api.ConversationKey, text string) api.LocalApproval {
	t.Helper()
	ctx := context.Background()
	sub, err := d.Reader.WatchLocalApprovals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	done := make(chan error, 1)
	go func() { done <- d.Reader.SendLocal(ctx, key, text, nil) }()
	var card api.LocalApproval
	for card.ID == "" {
		snap := localRecv(t, sub.Updates())
		for _, c := range snap.Value {
			if c.Key == key {
				card = c
			}
		}
	}
	if err := d.Reader.DecideLocal(ctx, key, card.ID, api.ApproveOnce); err != nil {
		t.Fatal(err)
	}
	if err := localRecv(t, done); err != nil {
		t.Fatal(err)
	}
	return card
}

// 验收 1：两个会话各开一个项目，互不影响，审批卡显示对的目录。
func TestSessionWorkspaceTwoProjects(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Replies: []llmtest.Reply{writeCall("w1", "same.txt", "甲的内容"), llmtest.Text("好"), writeCall("w2", "same.txt", "乙的内容"), llmtest.Text("好")}})
	ctx := context.Background()
	daily, topic := localKey("console:"+testChar), addTopic(t, a, "owned")
	dirA, dirB := t.TempDir(), t.TempDir()
	if _, err := d.Reader.SetLocalWorkspace(ctx, daily, dirA); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Reader.SetLocalWorkspace(ctx, topic, dirB); err != nil {
		t.Fatal(err)
	}
	cardA := sendAndApprove(t, d, daily, "写")
	cardB := sendAndApprove(t, d, topic, "写")
	if cardA.Workdir != dirA || cardB.Workdir != dirB {
		t.Fatalf("审批卡目录不对：%q %q", cardA.Workdir, cardB.Workdir)
	}
	for dir, want := range map[string]string{dirA: "甲的内容", dirB: "乙的内容"} {
		b, err := os.ReadFile(filepath.Join(dir, "same.txt"))
		if err != nil || string(b) != want {
			t.Errorf("%s 里的文件不对：%q %v", dir, b, err)
		}
	}
	if _, err := os.Stat(filepath.Join(a.workdir(), "same.txt")); err == nil {
		t.Fatal("写到了全局默认目录")
	}
	// 列表带出项目名
	for i, k := range []api.ConversationKey{daily, topic} {
		info, err := d.Reader.ReadLocalWorkspace(ctx, k)
		want := []string{dirA, dirB}[i]
		if err != nil || !info.Bound || info.CWD != want || info.Name != filepath.Base(want) || info.Problem != "" {
			t.Errorf("读回来不对：%+v %v", info, err)
		}
	}
}

// 验收 3：绑定的目录被删，发送被拒、这句话不进库、不退到全局默认；删掉绑定回到默认。
func TestSessionWorkspaceMissingRefuses(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text("好")})
	ctx := context.Background()
	key := localKey("console:" + testChar)
	gone := filepath.Join(t.TempDir(), "项目")
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Reader.SetLocalWorkspace(ctx, key, gone); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	err := d.Reader.SendLocal(ctx, key, "在吗", nil)
	if err == nil || !strings.Contains(err.Error(), gone) {
		t.Fatalf("应当拒绝并带路径：%v", err)
	}
	var owners int
	st, _ := a.set.For("console")
	if err := st.Events.QueryRow(`SELECT count(*) FROM events WHERE actor_kind='owner'`).Scan(&owners); err != nil || owners != 0 {
		t.Fatalf("被拒的话不该进库：%d %v", owners, err)
	}
	info, err := d.Reader.ReadLocalWorkspace(ctx, key)
	if err != nil || info.Problem == "" || !info.Bound {
		t.Fatalf("药丸要能标红：%+v %v", info, err)
	}
	// 恢复默认后能发
	if _, err := d.Reader.ClearLocalWorkspace(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := d.Reader.SendLocal(ctx, key, "在吗", nil); err != nil {
		t.Fatalf("恢复默认后应当能发：%v", err)
	}
}

// 联接改了指向：这一轮拒掉，不跟着走到别的目录。
func TestSessionWorkspaceJunctionRetargetRefuses(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	_, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text("好")})
	ctx := context.Background()
	key := localKey("console:" + testChar)
	one, other, base := t.TempDir(), t.TempDir(), t.TempDir()
	link := filepath.Join(base, "link")
	if err := os.Symlink(one, link); err != nil {
		t.Skipf("这台机器建不了符号链接：%v", err)
	}
	if _, err := d.Reader.SetLocalWorkspace(ctx, key, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	if err := d.Reader.SendLocal(ctx, key, "在吗", nil); err == nil || !strings.Contains(err.Error(), "指向别处") {
		t.Fatalf("联接改了指向应当拒绝：%v", err)
	}
}

// 场合表：QQ、本地群聊、别人的话题、伪造的键都不能设，也不返回路径。
func TestSessionWorkspaceScopes(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text("好")})
	ctx := context.Background()
	st, _ := a.set.For("console")
	if _, err := st.Events.Exec(`INSERT INTO local_topics(id,character_id,created_at) VALUES ('foreign','invented',1)`); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bad := []api.ConversationKey{
		{}, localKey(""), localKey("console:"), localKey("console:" + testChar + ":group:x"), localKey("console:" + testChar + ":topic:foreign"), localKey("console:" + testChar + ":topic:无"),
		{Platform: "qq", Channel: "qq", Scene: "private", Conversation: "qq:private:100:200"},
		{Platform: "qq", Channel: "qq", Scene: "group", Conversation: "qq:group:300"},
		{Platform: "qq", Channel: "console", Scene: "private", Conversation: "console:" + testChar},
		{Platform: "local", Channel: "console", Scene: "group", Conversation: "console:" + testChar},
	}
	for _, k := range bad {
		if info, err := d.Reader.SetLocalWorkspace(ctx, k, dir); err == nil || info.CWD != "" {
			t.Errorf("Set 放行了 %+v", k)
		}
		if info, err := d.Reader.ReadLocalWorkspace(ctx, k); err == nil || info.CWD != "" {
			t.Errorf("Read 放行了 %+v", k)
		}
		if _, err := d.Reader.ClearLocalWorkspace(ctx, k); err == nil {
			t.Errorf("Clear 放行了 %+v", k)
		}
	}
	// 库里被塞了 QQ 或群的绑定，列表也不带路径
	local, _ := a.set.ForConversation("console:" + testChar)
	for _, conv := range []string{"qq:group:300", "console:" + testChar + ":group:g1"} {
		if err := local.SetLocalWorkspace(ctx, store.LocalWorkspace{Conversation: conv, CWD: dir, Root: dir, RealPath: dir, UpdatedAt: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Reader.ReadLocalWorkspace(ctx, localKey("console:"+testChar)); err != nil {
		t.Fatal(err)
	}
}

// /cd：看、换、恢复；不合格的写法只报错，不绑定。
func TestSessionWorkspaceCDCommand(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text("不该被调用")})
	ctx := context.Background()
	key := localKey("console:" + testChar)
	dir := t.TempDir()
	if err := d.Reader.SendLocal(ctx, key, "/cd \""+dir+"\"", nil); err != nil {
		t.Fatal(err)
	}
	if info, err := d.Reader.ReadLocalWorkspace(ctx, key); err != nil || !info.Bound || info.CWD != filepath.Clean(dir) {
		t.Fatalf("/cd 应当绑定（带引号也行）：%+v %v", info, err)
	}
	for _, bad := range []string{"relative/dir", `C:foo`, `\foo`, filepath.Join(dir, "不存在"), filepath.Join(dir, "..", "..", "不存在的")} {
		_ = d.Reader.SendLocal(ctx, key, "/cd "+bad, nil)
		if info, _ := d.Reader.ReadLocalWorkspace(ctx, key); info.CWD != filepath.Clean(dir) {
			t.Fatalf("/cd %s 不该改动绑定：%+v", bad, info)
		}
	}
	if err := d.Reader.SendLocal(ctx, key, "/cd 默认", nil); err != nil {
		t.Fatal(err)
	}
	if info, err := d.Reader.ReadLocalWorkspace(ctx, key); err != nil || info.Bound || info.CWD != a.workdir() {
		t.Fatalf("/cd 默认 应当恢复：%+v %v", info, err)
	}
	// 命令不发给模型
	e.mu.Lock()
	n := len(e.bodies)
	e.mu.Unlock()
	if n != 0 {
		t.Fatalf("/cd 被当成消息发给了模型：%d", n)
	}
}

func TestNormalizeWorkspaceDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, ok := range []string{dir, " " + dir + " ", `"` + dir + `"`, dir + string(filepath.Separator) + "." + string(filepath.Separator) + "x" + string(filepath.Separator) + ".."} {
		if got, err := normalizeWorkspaceDir(ok); err != nil || got != filepath.Clean(dir) {
			t.Errorf("%q 应当整理成 %q：%q %v", ok, dir, got, err)
		}
	}
	for _, bad := range []string{"", "   ", "x", `C:foo`, `\foo`, "..\\x", dir + "\x00", dir + "\nx", "\xff" + dir} {
		if _, err := normalizeWorkspaceDir(bad); err == nil {
			t.Errorf("%q 应当拒绝", bad)
		}
	}
	if got, err := normalizeWorkspaceDir("~"); err != nil || !filepath.IsAbs(got) {
		t.Errorf("~ 应当展开：%q %v", got, err)
	}

}

// 最近用过的目录：新的在前、去重（回归：曾经按平台名 local 取库，真后台里取不到）。
func TestSessionWorkspaceRecent(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text("好")})
	ctx := context.Background()
	daily, topic := localKey("console:"+testChar), addTopic(t, a, "owned")
	one, two := t.TempDir(), t.TempDir()
	for _, step := range []struct {
		k   api.ConversationKey
		dir string
	}{{daily, one}, {topic, two}, {daily, two}} {
		if _, err := d.Reader.SetLocalWorkspace(ctx, step.k, step.dir); err != nil {
			t.Fatal(err)
		}
	}
	got, err := d.Reader.RecentWorkspaces(ctx)
	if err != nil || len(got) != 1 || got[0] != two {
		t.Fatalf("最近目录不对（两个会话最后都在 two）：%v %v", got, err)
	}
}

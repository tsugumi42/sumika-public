package memview

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/memory"
)

var bg = context.Background()

func ms(t time.Time) int64 { return t.UnixMilli() }

// 角色没有记忆库（路径空、文件不存在）：概览里 HasDB 为假，不报错；读它的别的接口报"还没有记忆库"
func TestOverviewCharacterWithoutDB(t *testing.T) {
	d := newDB(t, true)
	s := newService(t, []Character{
		{ID: "suzune", Name: "铃音", DB: d.file, Browse: opts()},
		{ID: "norecord", Name: "没库的", DB: ""},
		{ID: "missing", Name: "文件不在", DB: d.file + ".nope"},
	}, Options{Poll: time.Hour})
	sub, err := s.WatchMemory(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	snap := <-sub.Updates()
	if snap.Err != nil || snap.State != api.Ready || len(snap.Value.Characters) != 3 {
		t.Fatalf("概览 %+v", snap)
	}
	for _, c := range snap.Value.Characters {
		switch c.ID {
		case "suzune":
			if !c.HasDB || c.Err != "" {
				t.Errorf("有库的角色 %+v", c)
			}
		default:
			if c.HasDB || c.Err != "" || c.Total != 0 {
				t.Errorf("%s：没有库应是 HasDB 假、无错、零条：%+v", c.ID, c)
			}
		}
	}
	if r, err := s.ListMemory(bg, api.MemoryListRequest{Character: "norecord"}); err == nil || r.State != api.Failed {
		t.Errorf("没有库的角色列表应失败：%+v %v", r, err)
	}
	if _, err := s.ListMemory(bg, api.MemoryListRequest{Character: "nobody"}); !errors.Is(err, ErrParam) {
		t.Errorf("不认识的角色应是参数错误：%v", err)
	}
	for _, bad := range []string{"../x", "a/b", "a:b", ""} {
		if _, err := s.ListMemory(bg, api.MemoryListRequest{Character: bad}); !errors.Is(err, ErrParam) {
			t.Errorf("角色 id %q 应是参数错误：%v", bad, err)
		}
	}
}

// 概览：计数、迁移与否、确认次数换算、两个下拉里的名字和打码
func TestOverviewCountsAndChoices(t *testing.T) {
	d := newDB(t, true)
	d.fact("a", "active", "喜欢草莓", "owner", "console:suzune", "owner", "owner", ms(now)-1000)
	d.fact("b", "candidate", "群友说的事", "qq:10002", "qq:group:30001", "conv:qq:group:30001", "", ms(now)-2000)
	d.fact("c", "active", "她提升的", "qq:10002", "qq:group:30001", "conv:qq:group:30001", "ai", ms(now)-3000)
	d.fact("d", "revoked", "撤销的", "", "qq:group:30001", "owner", "", ms(now)-4000)
	names := func(kind, id string) string {
		switch {
		case kind == "person" && id == "qq:10002":
			return "小乙"
		case kind == "conv" && id == "qq:group:30001":
			return "编的测试群"
		}
		return ""
	}
	br := opts()
	br.ConfirmOccasions = 1
	s := newService(t, []Character{{ID: "suzune", Name: "铃音", DB: d.file, Browse: br}}, Options{Poll: time.Hour, Names: names})
	sub, _ := s.WatchMemory(bg)
	defer sub.Close()
	c := (<-sub.Updates()).Value.Characters[0]
	if c.Total != 4 || !c.Migrated || c.ConfirmNeed != 2 {
		t.Errorf("总数、迁移、次数 %+v", c)
	}
	got := map[string]int{}
	for _, k := range c.Counts {
		got[k.Kind+"/"+k.Group] += k.N
	}
	want := map[string]int{"fact/owner": 1, "fact/candidate": 1, "fact/ai": 1, "fact/revoked": 1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("计数 %v", got)
	}
	if len(c.Conversations) != 2 || c.Conversations[0].Value != "qq:group:30001" || c.Conversations[0].N != 3 ||
		c.Conversations[0].Label != "编的测试群（QQ 群 ***01）" {
		t.Errorf("会话下拉 %+v", c.Conversations)
	}
	if len(c.Subjects) != 2 || c.Subjects[0].Value != "qq:10002" || c.Subjects[0].Label != "小乙（***02）" || c.Subjects[1].Label != "你" {
		t.Errorf("关于谁下拉 %+v", c.Subjects)
	}
	// 没迁移的库
	old := newDB(t, false)
	old.conn.Exec(`INSERT INTO facts (id, kind, text, visibility, source_seqs, status, created_at, scope) VALUES ('o', 'fact', '旧库', 'v', '[]', 'active', 1, 'owner')`)
	s2 := newService(t, []Character{{ID: "x", Name: "旧", DB: old.file, Browse: opts()}}, Options{Poll: time.Hour})
	sub2, _ := s2.WatchMemory(bg)
	defer sub2.Close()
	if c := (<-sub2.Updates()).Value.Characters[0]; c.Migrated || c.Total != 1 || c.Err != "" {
		t.Errorf("没迁移的库 %+v", c)
	}
}

// 列表和详情：范围标签、身份只看形状、名字只用来显示、同名的两个人带不同的打码 id
func TestListDetailLabelsAndIdentity(t *testing.T) {
	d := newDB(t, true)
	d.fact("owner1", "active", "自己的事", "owner", "", "owner", "owner", ms(now)-1)
	d.fact("fake1", "active", "冒充的", "主人", "", "public", "", ms(now)-2)
	d.fact("fake2", "active", "冒充的二", "system", "", "owner", "", ms(now)-3)
	d.fact("p1", "active", "甲的事", "qq:10001", "", "person:qq:10001", "", ms(now)-4)
	d.fact("p2", "active", "乙的事", "qq:10002", "", "conv:qq:group:30001", "", ms(now)-5)
	d.fact("bad", "active", "怪范围", "", "", "garbage-scope", "", ms(now)-6)
	d.fact("self1", "active", "她自己的事", "self", "", "owner", "", ms(now)-7)
	names := func(kind, id string) string {
		if kind == "person" && (id == "qq:10001" || id == "qq:10002") {
			return "小明" // 同名
		}
		return ""
	}
	s := newService(t, []Character{{ID: "suzune", Name: "铃音", DB: d.file, Browse: opts()}}, Options{Names: names})
	r, err := s.ListMemory(bg, api.MemoryListRequest{Character: "suzune"})
	if err != nil || len(r.Value.Items) != 7 {
		t.Fatalf("列表 %v %v", r, err)
	}
	by := map[string]api.MemoryRow{}
	for _, row := range r.Value.Items {
		by[row.Ref] = row
	}
	if w := by["f:owner1"].Subject; w.Kind != "owner" || w.Name != "你" {
		t.Errorf("owner %+v", w)
	}
	if w := by["f:self1"].Subject; w.Kind != "self" {
		t.Errorf("self %+v", w)
	}
	for _, id := range []string{"f:fake1", "f:fake2"} {
		if w := by[id].Subject; w.Kind != "other" {
			t.Errorf("%s：名字写成'主人''system'不能当成身份：%+v", id, w)
		}
	}
	if w := by["f:fake1"].Subject; w.Raw != "主人" {
		t.Errorf("other 原样显示 %+v", w)
	}
	p1, p2 := by["f:p1"].Subject, by["f:p2"].Subject
	if p1.Kind != "person" || p2.Kind != "person" || p1.Name != "小明" || p1.Name != p2.Name || p1.ID == p2.ID || p1.ID == "" {
		t.Errorf("同名的两个人要带不同的打码 id：%+v %+v", p1, p2)
	}
	if strings.Contains(p1.ID, "10001") {
		t.Errorf("id 没打码：%q", p1.ID)
	}
	scopeKinds := map[string]string{"f:owner1": "owner", "f:fake1": "public", "f:p1": "person", "f:p2": "conv", "f:bad": "unknown"}
	for ref, k := range scopeKinds {
		if got := by[ref].Scopes[0].Kind; got != k {
			t.Errorf("%s 范围类型 %s，应 %s", ref, got, k)
		}
	}
	if l := by["f:p2"].Scopes[0].Label; strings.Contains(l, "30001") || !strings.Contains(l, "QQ 群") {
		t.Errorf("conv 来源标签（号要打码）%q", l)
	}
	if l := by["f:bad"].Scopes[0].Label; l != "范围不认识（garbage-scope）" {
		t.Errorf("unknown 标签 %q", l)
	}
	// 详情：场合表、想不想得起、出处标签
	det, err := s.ReadMemoryDetail(bg, "suzune", "f:p2")
	if err != nil {
		t.Fatal(err)
	}
	if len(det.Value.Places) != 4 || det.Value.Places[0].Place != "local-private" || det.Value.Places[0].Visible != "yes" ||
		det.Value.Places[3].Visible != "yes" || !det.Value.Recall.Can || !det.Value.Migrated {
		t.Errorf("详情 %+v", det.Value)
	}
	// 畸形 ref、游标：参数错误；错误文字里没有搜索词
	if _, err := s.ReadMemoryDetail(bg, "suzune", "x:1"); !errors.Is(err, ErrParam) {
		t.Errorf("畸形 ref：%v", err)
	}
	_, err = s.ListMemory(bg, api.MemoryListRequest{Character: "suzune", Query: "绝不能出现在错误里的搜索词", Cursor: "garbage"})
	if !errors.Is(err, ErrParam) || strings.Contains(err.Error(), "绝不能") {
		t.Errorf("坏游标：%v", err)
	}
}

// 搜索词能解析成人时，按人的 id 也找得到
func TestSearchResolvesPerson(t *testing.T) {
	d := newDB(t, true)
	d.fact("a", "active", "和名字无关的正文", "qq:10002", "", "owner", "", ms(now)-1)
	d.fact("b", "active", "别的", "qq:10003", "", "owner", "", ms(now)-2)
	resolve := func(term string) []string {
		if term == "小乙" {
			return []string{"qq:10002"}
		}
		return nil
	}
	s := newService(t, []Character{{ID: "suzune", DB: d.file, Browse: opts()}}, Options{ResolvePerson: resolve})
	r, err := s.ListMemory(bg, api.MemoryListRequest{Character: "suzune", Query: "小乙"})
	if err != nil || len(r.Value.Items) != 1 || r.Value.Items[0].Ref != "f:a" {
		t.Errorf("按人找 %+v %v", r.Value.Items, err)
	}
	r, _ = s.ListMemory(bg, api.MemoryListRequest{Character: "suzune", Query: "没这个词"})
	if len(r.Value.Items) != 0 || r.State != api.Ready {
		t.Errorf("空结果应是成功的空 %+v", r)
	}
}

// 修订原因保留原文并过滤控制字符；展示标签不固定措辞。
func TestRevisionText(t *testing.T) {
	d := newDB(t, true)
	d.fact("a", "active", "x", "", "", "owner", "", ms(now))
	for _, reason := range []string{"攒够 3 次不同场合", "原因里有\x00控制字符"} {
		if _, err := d.conn.Exec(`INSERT INTO fact_revisions (fact_id, op, at, by, reason) VALUES ('a','confirm',1,'summary',?)`, reason); err != nil {
			t.Fatal(err)
		}
	}
	s := newService(t, []Character{{ID: "suzune", DB: d.file, Browse: opts()}}, Options{})
	r, err := s.ReadMemoryRevisions(bg, "suzune", "f:a", "")
	if err != nil || len(r.Value.Items) != 2 {
		t.Fatalf("%+v %v", r, err)
	}
	if reason := r.Value.Items[0].Reason; r.Value.Items[1].Reason != "攒够 3 次不同场合" || strings.ContainsRune(reason, 0) || !strings.Contains(reason, "原因里有") || !strings.Contains(reason, "控制字符") {
		t.Fatalf("修订原因丢失或未过滤控制字符：%+v", r.Value.Items)
	}
}

// 订阅：别的连接提交以后再给一份新快照，Rev 加一、计数更新；订阅活着时关服务，库文件能立刻删
func TestWatchPublishesOnForeignCommitAndCloseFreesFile(t *testing.T) {
	d := newDB(t, true)
	d.fact("a", "active", "第一条", "", "", "owner", "", ms(now)-1)
	s := New([]Character{{ID: "suzune", Name: "铃音", DB: d.file, Browse: opts()}}, Options{Poll: 20 * time.Millisecond, Now: func() time.Time { return now }})
	sub, err := s.WatchMemory(bg)
	if err != nil {
		t.Fatal(err)
	}
	first := <-sub.Updates()
	if first.Version != 1 || first.Value.Characters[0].Total != 1 || first.Value.Characters[0].Rev != 0 {
		t.Fatalf("第一份 %+v", first)
	}
	// 没有变化时不发新快照
	select {
	case snap := <-sub.Updates():
		t.Fatalf("没变化却来了新快照 %+v", snap)
	case <-time.After(150 * time.Millisecond):
	}
	d.fact("b", "active", "第二条", "", "", "owner", "", ms(now)-2)
	var second api.Snapshot[api.MemoryOverview]
	select {
	case second = <-sub.Updates():
	case <-time.After(3 * time.Second):
		t.Fatal("别的连接提交以后一直没来新快照")
	}
	if c := second.Value.Characters[0]; c.Total != 2 || c.Rev == 0 || second.Version <= first.Version {
		t.Errorf("第二份 %+v 版本 %d", c, second.Version)
	}
	// 订阅活着时关服务：订阅的通道关掉、库文件立刻能删
	d.conn.Close()
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("订阅活着时 Close 一直没返回")
	}
	for range sub.Updates() {
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(d.file + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("关了以后删 %s：%v", suffix, err)
		}
	}
	if _, err := s.ListMemory(bg, api.MemoryListRequest{Character: "suzune"}); !errors.Is(err, ErrClosed) {
		t.Errorf("关了以后再来应是 ErrClosed：%v", err)
	}
	if _, err := s.WatchMemory(bg); !errors.Is(err, ErrClosed) {
		t.Errorf("关了以后再订阅应是 ErrClosed：%v", err)
	}
	s.Close() // 重复关不出事
	sub.Close()
}

// 查询超时：闸被占着等不到就失败，不一直挂着；结果带失败状态
func TestQueryTimeoutWhileGateBusy(t *testing.T) {
	d := newDB(t, true)
	d.fact("a", "active", "x", "", "", "owner", "", ms(now))
	s := newService(t, []Character{{ID: "suzune", DB: d.file, Browse: opts()}}, Options{Timeout: 50 * time.Millisecond})
	s.gate <- struct{}{} // 另一个查询占着闸
	start := time.Now()
	r, err := s.ListMemory(bg, api.MemoryListRequest{Character: "suzune"})
	if err == nil || r.State != api.Failed || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("应超时失败：%+v %v", r, err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("超时用了 %v", time.Since(start))
	}
	<-s.gate
	if _, err := s.ListMemory(bg, api.MemoryListRequest{Character: "suzune"}); err != nil {
		t.Errorf("闸放开以后应成功：%v", err)
	}
	// 取消的上下文：查完也不给结果
	cctx, cancel := context.WithCancel(bg)
	cancel()
	if r, err := s.ListMemory(cctx, api.MemoryListRequest{Character: "suzune"}); err == nil || r.State != api.Failed {
		t.Errorf("取消的上下文应失败：%+v %v", r, err)
	}
}

// 读不挡写：服务一直读（翻页、详情），另一个连接同时写，读不报错、写不超时
func TestServiceReadsWhileForeignWrites(t *testing.T) {
	d := newDB(t, true)
	for i := 0; i < 60; i++ {
		d.fact(fmt.Sprintf("f%03d", i), "active", "编的", "", "", "owner", "", ms(now)-int64(i))
	}
	s := newService(t, []Character{{ID: "suzune", DB: d.file, Browse: opts()}}, Options{Poll: 10 * time.Millisecond})
	sub, _ := s.WatchMemory(bg)
	defer sub.Close()
	go func() {
		for range sub.Updates() {
		}
	}()
	var wg sync.WaitGroup
	errs := make(chan error, 1)
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			cursor := api.Cursor("")
			for {
				r, err := s.ListMemory(bg, api.MemoryListRequest{Character: "suzune", Limit: 10, Cursor: cursor})
				if err != nil {
					errs <- err
					return
				}
				if !r.Value.HasMore {
					break
				}
				cursor = r.Value.NextCursor
			}
			if _, err := s.ReadMemoryDetail(bg, "suzune", "f:f000"); err != nil {
				errs <- err
				return
			}
		}
	}()
	start := time.Now()
	for i := 0; i < 40; i++ {
		d.fact(fmt.Sprintf("w%03d", i), "active", "读的时候写的", "", "", "owner", "", ms(now)+int64(i))
	}
	took := time.Since(start)
	close(stop)
	wg.Wait()
	select {
	case err := <-errs:
		t.Fatal(err)
	default:
	}
	if took > 15*time.Second {
		t.Errorf("边读边写 40 次用了 %v", took)
	}
	r, _ := s.ListMemory(bg, api.MemoryListRequest{Character: "suzune", Limit: 200})
	if len(r.Value.Items) != 100 {
		t.Errorf("写完应有 100 条，读到 %d", len(r.Value.Items))
	}
}

// 出处原话：ok / recalled / missing / unreadable 逐格；别的角色没看见的、别的平台前缀、坏参数
func TestQuote(t *testing.T) {
	const qgroup, qpriv = "qq:group:30001", "qq:private:20001:10002"
	ev := &events{evs: []memory.Event{
		{Seq: 10, Conversation: qgroup, At: 1000, ActorKind: "human", ActorID: "qq:10002", ActorName: "小乙", Origin: "peer", Kind: "message", Text: "我下周搬家", Seen: true},
		{Seq: 11, Conversation: qgroup, At: 1001, ActorKind: "human", ActorID: "qq:10002", ActorName: "小乙", Kind: "message", Text: "这句后来撤回了的话", Seen: true, Revoked: true},
		{Seq: 12, Conversation: qgroup, At: 1002, ActorKind: "human", ActorID: "qq:10003", ActorName: "小丙", Kind: "message", Text: "她没看见的", Seen: false},
		{Seq: 13, Conversation: qgroup, At: 1003, ActorKind: "owner", ActorID: "owner", Kind: "message", Text: "你说的 [图片]", Seen: true, Images: []memory.Image{{Hash: "h", Brief: "一只猫", Ready: true}, {Hash: "h2", Brief: "不该出现", Ready: false}}},
		{Seq: 14, Conversation: qgroup, At: 1004, ActorKind: "character", ActorID: "suzune", Kind: "message", Text: "她自己说的", Seen: true},
		{Seq: 15, Conversation: qgroup, At: 1005, ActorKind: "human", ActorID: "qq:10002", ActorName: "主人", Kind: "message", Text: "名字写成主人的群友", Seen: true},
		{Seq: 16, Conversation: qgroup, At: 1006, ActorKind: "human", ActorID: "qq:10002", Kind: "notice", Text: "撤回通知", Seen: true},
		{Seq: 17, Conversation: qgroup, At: 1007, ActorKind: "human", ActorID: "qq:10002", ActorName: "小乙", Kind: "message", Text: "长\n" + strings.Repeat("字", 400) + "\u200b", Seen: true},
		{Seq: 20, Conversation: "console:suzune", At: 2000, ActorKind: "owner", ActorID: "owner", Kind: "message", Text: "本地私聊里的话"},
		{Seq: 21, Conversation: "console:other", At: 2001, ActorKind: "owner", ActorID: "owner", Kind: "message", Text: "别的角色的本地私聊", Seen: true},
		{Seq: 30, Conversation: qpriv, At: 3000, ActorKind: "owner", ActorID: "owner", Kind: "message", Text: "QQ 私聊里的话", Seen: true},
	}}
	chars := []Character{{ID: "suzune", Name: "铃音", Events: ev}, {ID: "noevents", Name: "没聊天库"}}
	s := newService(t, chars, Options{})
	q := func(char, conv string, seq int64) api.MemoryQuote {
		t.Helper()
		r, err := s.ReadMemoryQuote(bg, char, api.MemorySource{Conversation: conv, Seq: seq})
		if err != nil {
			t.Fatalf("%s %s#%d：%v", char, conv, seq, err)
		}
		return r.Value
	}
	cases := []struct {
		name   string
		conv   string
		seq    int64
		status string
		who    string
		text   string
	}{
		{"取到", qgroup, 10, "ok", "person", "我下周搬家"},
		{"撤回的不给正文", qgroup, 11, "recalled", "", ""},
		{"她没看见的", qgroup, 12, "missing", "", ""},
		{"序号不在库里", qgroup, 99, "missing", "", ""},
		{"后面的序号不能顶替", qgroup, 9, "missing", "", ""},
		{"你的话带识图简述", qgroup, 13, "ok", "owner", "你说的 [图片]（图：一只猫）"},
		{"她自己说的", qgroup, 14, "ok", "self", "她自己说的"},
		{"名字写成主人的群友不是你", qgroup, 15, "ok", "person", "名字写成主人的群友"},
		{"不是消息", qgroup, 16, "missing", "", ""},
		{"本地会话自己的", "console:suzune", 20, "ok", "owner", "本地私聊里的话"},
		{"别的角色的本地会话", "console:other", 21, "missing", "", ""},
		{"QQ 私聊", qpriv, 30, "ok", "owner", "QQ 私聊里的话"},
		{"认不出平台的会话", "nope:group:1", 1, "missing", "", ""},
		{"没有冒号的会话", "garbage", 1, "missing", "", ""},
	}
	for _, c := range cases {
		got := q("suzune", c.conv, c.seq)
		if got.Status != c.status || (c.who != "" && got.Who.Kind != c.who) || (c.status == "ok" && got.Text != c.text) {
			t.Errorf("%s：%+v，应 %s / %s / %q", c.name, got, c.status, c.who, c.text)
		}
		if c.status != "ok" && (got.Text != "" || got.Who.Kind != "") {
			t.Errorf("%s：没取到却带了内容 %+v", c.name, got)
		}
	}
	if got := q("suzune", qgroup, 10); got.Who.Name != "小乙" || strings.Contains(got.Who.ID, "10002") || got.Who.ID == "" || got.At != 1000 {
		t.Errorf("说话人 %+v", got)
	}
	if got := q("suzune", qgroup, 17); got.Status != "ok" || strings.ContainsAny(got.Text, "\u200b") || len([]rune(got.Text)) > quoteRunes+1 || !strings.HasSuffix(got.Text, "…") {
		t.Errorf("长原话要截断并清控制字符：%d 字", len([]rune(got.Text)))
	}
	// 事件来源读不到：只那一块写读不到，写明平台
	if got := q("noevents", qgroup, 10); got.Status != "unreadable" || got.Platform != "qq" {
		t.Errorf("没有事件来源 %+v", got)
	}
	ev.fail = errors.New("平台库没挂")
	if got := q("suzune", qgroup, 10); got.Status != "unreadable" || got.Platform != "qq" {
		t.Errorf("读不到 %+v", got)
	}
	ev.fail = nil
	// 坏参数
	for _, bad := range []api.MemorySource{{Seq: 1}, {Conversation: qgroup}, {Conversation: qgroup, Seq: -3}, {Conversation: qgroup, Seq: 1 << 60}, {Conversation: strings.Repeat("x:", 200), Seq: 1}} {
		if _, err := s.ReadMemoryQuote(bg, "suzune", bad); !errors.Is(err, ErrParam) {
			t.Errorf("%+v 应是参数错误：%v", bad, err)
		}
	}
	if _, err := s.ReadMemoryQuote(bg, "nobody", api.MemorySource{Conversation: qgroup, Seq: 10}); !errors.Is(err, ErrParam) {
		t.Errorf("不认识的角色：%v", err)
	}
}

// 证据行自带的会话和事实的会话不一样：接口原样给出证据行的（取原话时用它，不用事实的）
func TestQuoteUsesEvidenceConversation(t *testing.T) {
	d := newDB(t, true)
	d.fact("a", "candidate", "x", "qq:10002", "qq:group:30001", "conv:qq:group:30001", "", ms(now))
	d.conn.Exec(`INSERT INTO fact_evidence (fact_id, day, conversation, seq, speaker, by_subject, verdict, at) VALUES ('a', '2026-11-01', 'qq:group:30009', 77, 'qq:10003', 0, 'ok', 5)`)
	s := newService(t, []Character{{ID: "suzune", DB: d.file, Browse: opts()}}, Options{})
	r, err := s.ReadMemoryEvidence(bg, "suzune", "f:a", "")
	if err != nil || len(r.Value.Items) != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	e := r.Value.Items[0]
	if e.Conversation.Conversation != "qq:group:30009" || e.Conversation.Seq != 77 || e.Speaker.Kind != "person" || e.BySubject || r.Value.Total != 1 {
		t.Errorf("证据行 %+v", e)
	}
}

// 最近变动
func TestRecent(t *testing.T) {
	d := newDB(t, true)
	d.fact("a", "active", "喜欢草莓", "", "", "owner", "", ms(now))
	d.conn.Exec(`INSERT INTO fact_revisions (fact_id, op, at, by, reason) VALUES ('a', 'add', 1, 'summary', '整理写下')`)
	d.conn.Exec(`INSERT INTO fact_revisions (fact_id, op, at, by) VALUES ('gone', 'purged', 2, 'owner')`)
	s := newService(t, []Character{{ID: "suzune", DB: d.file, Browse: opts()}}, Options{})
	r, err := s.ReadMemoryRecent(bg, "suzune")
	if err != nil || len(r.Value) != 2 || r.Value[0].Did != "真删" || r.Value[0].Exists || r.Value[1].Text != "喜欢草莓" || r.Value[1].Ref != "f:a" {
		t.Errorf("%+v %v", r.Value, err)
	}
}

// 片段没有修订表也没有 source_seqs：出处是 from_seq..to_seq，只给头尾各最多 3 句，总数是这一段的句数
func TestEpisodeSourcesAreHeadAndTail(t *testing.T) {
	d := newDB(t, true)
	add := func(id string, from, to int64) {
		if _, err := d.conn.Exec(`INSERT INTO episodes (id, conversation, text, visibility, from_seq, to_seq, created_at, scope, origin, status) VALUES (?, 'qq:group:30001', '一个片段', 'v', ?, ?, 1, 'owner', 't', 'active')`, id, from, to); err != nil {
			t.Fatal(err)
		}
	}
	add("long", 10, 40)
	add("short", 5, 7)
	add("five", 1, 5)
	add("one", 9, 9)
	add("bad", 9, 3)
	s := newService(t, []Character{{ID: "suzune", DB: d.file, Browse: opts()}}, Options{})
	seqs := func(id string) ([]int64, int) {
		r, err := s.ReadMemoryDetail(bg, "suzune", "e:"+id)
		if err != nil {
			t.Fatal(err)
		}
		var out []int64
		for _, src := range r.Value.Sources {
			out = append(out, src.Seq)
			if src.Conversation != "qq:group:30001" {
				t.Errorf("%s：会话 %q", id, src.Conversation)
			}
		}
		return out, r.Value.SourcesTotal
	}
	for id, w := range map[string]struct {
		seqs  []int64
		total int
	}{
		"long":  {[]int64{10, 11, 12, 38, 39, 40}, 31},
		"short": {[]int64{5, 6, 7}, 3},
		"five":  {[]int64{1, 2, 3, 4, 5}, 5},
		"one":   {[]int64{9}, 1},
		"bad":   {nil, 0},
	} {
		got, total := seqs(id)
		if !reflect.DeepEqual(got, w.seqs) || total != w.total {
			t.Errorf("%s：得到 %v（共 %d），应 %v（%d）", id, got, total, w.seqs, w.total)
		}
	}
}

func TestOriginText(t *testing.T) {
	for in, want := range map[string]string{"owner": "你说的", "peer": "对方说的", "human": "群友说的", "external": "外部来的话", "character": "她自己说的", "automation:dry-run": "", "": "", "test": ""} {
		if got := originText(in); got != want {
			t.Errorf("originText(%q) = %q，应 %q", in, got, want)
		}
	}
}

// 某个角色的概览没算成（闸被占着、5 秒时限到了）以后，库没有新提交也会隔一会儿再算：恢复以后快照里的错误清掉、Rev 加一；
// 一直失败时不重复发一模一样的快照（复核 2026-11-02：以前算失败也标成“算过了”，库不再被提交就一直显示读不了）
func TestWatchRetriesFailedOverview(t *testing.T) {
	d := newDB(t, true)
	d.fact("a", "active", "第一条", "", "", "owner", "", ms(now)-1)
	s := newService(t, []Character{{ID: "suzune", Name: "铃音", DB: d.file, Browse: opts()}},
		Options{Poll: 10 * time.Millisecond, Timeout: 30 * time.Millisecond, Retry: 60 * time.Millisecond})
	s.gate <- struct{}{} // 别的查询占着闸：概览等不到闸，算失败
	sub, err := s.WatchMemory(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	first := <-sub.Updates()
	if c := first.Value.Characters[0]; c.Err == "" || !c.HasDB {
		t.Fatalf("闸被占着，第一份该带错误：%+v", c)
	}
	// 一直失败的这段时间（好几个重试间隔）：不发一模一样的快照
	select {
	case snap := <-sub.Updates():
		t.Fatalf("一直失败却又来了一份快照 %+v", snap.Value.Characters)
	case <-time.After(400 * time.Millisecond):
	}
	<-s.gate // 放开闸；库没有任何新提交
	// 机器忙时放开后的第一次重算也可能撞上 30 毫秒时限、先发一份别的错误；这里只要求 3 秒内恢复，不要求第一份就好
	// （2026-11-03 整套 91 个包一起跑时红过一次，输出在 data/dev/test-runs/20261103-memview-watch-retry-flake.txt）
	deadline := time.After(3 * time.Second)
	for {
		select {
		case second := <-sub.Updates():
			c := second.Value.Characters[0]
			if c.Err != "" {
				continue
			}
			if c.Total != 1 || c.Rev == 0 {
				t.Errorf("恢复后该数出 1 条、Rev 加一：%+v", c)
			}
			return
		case <-deadline:
			t.Fatal("闸放开以后一直没恢复：库没有新提交，角色就一直显示读不了")
		}
	}
}

// 变化轮询不挡后台写：库是回滚日志（DELETE）模式时，读要抢共享锁、写提交要等读放开。
// 轮询调到 2 毫秒一次，同时另一个连接连提交 150 次，每次都成功（busy_timeout 5 秒内等得到）。
// 不比总耗时：墙钟会把机器负载当成"被挡住"；轮询把锁攥住不放的情形由下面的 TestPollHoldsNoLockBetweenPollsInDeleteMode 按事件判断
func TestPollDoesNotBlockForeignWriterInDeleteMode(t *testing.T) {
	d := newDB(t, true)
	if _, err := d.conn.Exec(`PRAGMA journal_mode = DELETE`); err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := d.conn.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || !strings.EqualFold(mode, "delete") {
		t.Fatalf("库没切到 DELETE 模式：%q %v", mode, err)
	}
	d.fact("a", "active", "第一条", "", "", "owner", "", ms(now)-1)
	s := newService(t, []Character{{ID: "suzune", DB: d.file, Browse: opts()}}, Options{Poll: 2 * time.Millisecond})
	sub, err := s.WatchMemory(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	go func() {
		for range sub.Updates() {
		}
	}()
	start := time.Now()
	for i := 0; i < 150; i++ {
		if _, err := d.conn.Exec(`INSERT INTO facts (id, kind, text, visibility, source_seqs, status, created_at) VALUES (?, 'fact', '写的', 'owner', '[]', 'active', ?)`, fmt.Sprintf("w%03d", i), ms(now)+int64(i)); err != nil {
			t.Fatalf("第 %d 次提交失败（被轮询挡住了？）：%v", i, err)
		}
	}
	t.Logf("DELETE 模式下 150 次提交 %v，轮询 2 毫秒一次", time.Since(start).Round(time.Millisecond))
}

// 轮询两次之间不攥着库的锁：DELETE 模式下读事务不放，写提交就要一直等。
// 轮询间隔调到一小时，等第一份概览出来（第一轮读完）以后，写连接把 busy_timeout 设成 0，150 次提交必须每次立刻成功——
// 此时没有任何读在跑，哪怕有一次 SQLITE_BUSY 都说明锁被留着了。这个判断不看时间，机器多忙结果都一样
func TestPollHoldsNoLockBetweenPollsInDeleteMode(t *testing.T) {
	d := newDB(t, true)
	if _, err := d.conn.Exec(`PRAGMA journal_mode = DELETE`); err != nil {
		t.Fatal(err)
	}
	d.fact("a", "active", "第一条", "", "", "owner", "", ms(now)-1)
	s := newService(t, []Character{{ID: "suzune", DB: d.file, Browse: opts()}}, Options{Poll: time.Hour})
	sub, err := s.WatchMemory(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if first := <-sub.Updates(); first.Value.Characters[0].Total != 1 {
		t.Fatalf("第一份概览不对：%+v", first.Value.Characters)
	}
	if _, err := d.conn.Exec(`PRAGMA busy_timeout = 0`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 150; i++ {
		if _, err := d.conn.Exec(`INSERT INTO facts (id, kind, text, visibility, source_seqs, status, created_at) VALUES (?, 'fact', '写的', 'owner', '[]', 'active', ?)`, fmt.Sprintf("w%03d", i), ms(now)+int64(i)); err != nil {
			t.Fatalf("第 %d 次提交失败（轮询读完后锁没放？）：%v", i, err)
		}
	}
}

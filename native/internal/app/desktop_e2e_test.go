package app

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"sumika-native/internal/api"
)

// groupFrame 一条不 @ 她的群消息（只入库、按频率过闸门）
func groupFrame(at time.Time, msgID, uid, text string) string {
	return fmt.Sprintf(`{"post_type":"message","message_type":"group","time":%d,"self_id":%s,"user_id":%s,"group_id":%s,"message_id":%q,"sender":{"nickname":"编的乙"},"message":[{"type":"text","data":{"text":%q}}]}`,
		at.Unix(), testAccount, uid, testGroup, msgID, text)
}

// 只读端到端（规格-界面接Go后台第 5 步、T2 / T9）：假协议端 → 真实存储 → 生产只读接口的订阅。
// 假协议端推的、库里存的、订阅最终看得到的三组正文集合要一样；撤回能经订阅改到已加载的行上；
// 全程假协议端没收到任何发送 action；先关界面消费者再关后台，之后同一组还能按 archive 再开。
func TestDesktopReceiveEndToEnd(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.writeConfigFreq(testToken, "0") // 频率 0：群消息只入库，不叫模型
	e.expectNoActions(t)
	// 清理后注册先跑：这一条要在停后台（e.start 注册的）之后，所以先注册
	t.Cleanup(func() {
		opt := e.options()
		opt.Mode, opt.Account = Archive, ""
		b, err := Open(context.Background(), opt)
		if err != nil {
			t.Errorf("退出后同一组 archive 开不了：%v", err)
			return
		}
		b.Close()
	})
	a := e.start()
	d := a.Desktop()
	conv := "qq:group:" + testGroup
	key := api.ConversationKey{Platform: "qq", Channel: "qq", Scene: "group", Conversation: conv}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub, err := d.Reader.WatchConversation(ctx, api.ConversationRequest{Key: key, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	conn := e.peer.accept(t)

	const n = 260 // 多过一页（200）：被撤回的第 3 条不在最新一页里，只能靠"已加载行变化"改到界面上
	sent := map[string]bool{}
	for i := 0; i < n; i++ {
		text := fmt.Sprintf("编的第 %d 条", i)
		sent[text] = true
		e.peer.send(t, conn, groupFrame(e.now.Add(time.Duration(i)*time.Second), fmt.Sprint(7000+i), "20001", text))
	}
	// 撤回第 3 条：没有新的消息行
	e.peer.send(t, conn, fmt.Sprintf(`{"post_type":"notice","notice_type":"group_recall","time":%d,"self_id":%s,"user_id":20001,"operator_id":20001,"group_id":%s,"message_id":"7003"}`, e.now.Add(time.Minute).Unix(), testAccount, testGroup))

	seen := map[string]api.MessageView{}
	deadline := time.After(90 * time.Second)
	for {
		select {
		case snap, ok := <-sub.Updates():
			if !ok {
				t.Fatal("订阅提前关了")
			}
			if snap.Err != nil {
				t.Fatal(snap.Err)
			}
			for _, m := range append(snap.Value.Page.Items, snap.Value.Changed...) {
				if old, ok := seen[m.Ref.Event.EventID]; !ok || m.Version >= old.Version {
					seen[m.Ref.Event.EventID] = m
				}
			}
		case <-deadline:
			t.Fatalf("90 秒内订阅只看到 %d 条", len(seen))
		}
		var recalled bool
		texts := 0
		for _, m := range seen {
			if m.Kind == "message" {
				texts++
				if m.Text == "编的第 3 条" && m.Recalled {
					recalled = true
				}
			}
		}
		if texts >= n && recalled {
			break
		}
	}

	st, err := a.set.For("qq")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := st.Events.Query(`SELECT text FROM events WHERE conversation=? AND kind='message'`, conv)
	if err != nil {
		t.Fatal(err)
	}
	var stored []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		stored = append(stored, s)
	}
	rows.Close()
	var visible, input []string
	for _, m := range seen {
		if m.Kind == "message" {
			visible = append(visible, m.Text)
		}
	}
	for s := range sent {
		input = append(input, s)
	}
	sort.Strings(stored)
	sort.Strings(visible)
	sort.Strings(input)
	if fmt.Sprint(input) != fmt.Sprint(stored) || fmt.Sprint(stored) != fmt.Sprint(visible) {
		t.Fatalf("集合对不上：输入 %d、库里 %d、订阅可见 %d", len(input), len(stored), len(visible))
	}
	t.Logf("假协议端输入 %d、落库 %d、订阅可见 %d，丢失 0、重复 0", len(input), len(stored), len(visible))

	// 收尾顺序：界面消费者先退，再停后台（e.start 的清理），最后同一组按 archive 再开一次（上面先注册的清理）
	sub.Close()
	d.Close()
}

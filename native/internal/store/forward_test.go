package store

import (
	"context"
	"sumika-native/internal/channel/types"
	"testing"
)

func TestForwardTreeAndMediaCommitTogether(t *testing.T) {
	t.Parallel()
	set, _ := goSet(t)
	st, _ := set.Platform("qq")
	ctx := context.Background()
	tree := types.ForwardTree{ID: "atomic", Nodes: []types.ForwardMessageNode{{Segs: []types.Seg{{Kind: "image", URL: "https://gchat.qpic.cn/one"}, {Kind: "image", URL: "https://gchat.qpic.cn/two"}}}}}
	if _, err := st.Events.Exec(`CREATE TRIGGER reject_forward_media BEFORE INSERT ON forward_media_fetch WHEN NEW.idx=1 BEGIN SELECT RAISE(ABORT,'编的登记失败'); END`); err != nil {
		t.Fatal(err)
	}
	if err := st.PutForward(ctx, tree, 1000, "qq:group:90002"); err == nil {
		t.Fatal("登记第二张图失败却提交了树")
	}
	if _, have, err := st.ReadForward(ctx, tree.ID); err != nil || have {
		t.Fatalf("树未随任务回滚：have=%v err=%v", have, err)
	}
	var tasks int
	if err := st.Events.QueryRow(`SELECT count(*) FROM forward_media_fetch WHERE forward_id=?`, tree.ID).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if tasks != 0 {
		t.Fatalf("第一张任务未回滚：%d", tasks)
	}
	if _, err := st.Events.Exec(`DROP TRIGGER reject_forward_media`); err != nil {
		t.Fatal(err)
	}
	if err := st.PutForward(ctx, tree, 1000, "qq:group:90002"); err != nil {
		t.Fatal(err)
	}
	if err := st.Events.QueryRow(`SELECT count(*) FROM forward_media_fetch WHERE forward_id=? AND state='pending'`, tree.ID).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if tasks != 2 {
		t.Fatalf("提交树时没有一起登记两张图：%d", tasks)
	}
}

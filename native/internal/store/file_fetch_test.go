package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"sumika-native/internal/channel/types"
)

func TestFileCompletionBindsOriginalIdentity(t *testing.T) {
	t.Parallel()
	set, _ := goSet(t)
	st, _ := set.Platform("qq")
	ctx := context.Background()
	conv := "qq:group:90002"
	original := types.Seg{Kind: types.SegFile, File: "清单.txt", FileID: "one", FileHash: "token"}
	for _, change := range []func(*types.Seg){func(s *types.Seg) { s.File = "程序.exe" }, func(s *types.Seg) { s.FileID = "two" }, func(s *types.Seg) { s.FileHash = "other" }, func(s *types.Seg) { s.FileGroup = "90004" }, func(s *types.Seg) { s.URL = "https://gchat.qpic.cn/other" }} {
		changed := original
		change(&changed)
		body, err := json.Marshal([]types.Seg{changed})
		if err != nil {
			t.Fatal(err)
		}
		seq := newMediaEvent(t, st, conv, "", 100, string(body))
		row := MediaFetch{EventSeq: seq, Idx: 0, Kind: types.SegFile, URLSig: MediaSegSig(original), State: MediaDone, Hash: strings.Repeat("a", 64), Format: "text"}
		ok, err := st.FinishSegHash(seq, 0, types.SegFile, row.URLSig, row.Hash, row)
		if err != nil || ok {
			t.Fatalf("正文迟到结果写到另一文件：%v %v", ok, err)
		}
		tree := types.ForwardTree{ID: "identity-" + changed.File + changed.FileID + changed.FileHash + changed.URL, Nodes: []types.ForwardMessageNode{{Segs: []types.Seg{changed}}}}
		if err := st.PutForward(ctx, tree, 100, conv); err != nil {
			t.Fatal(err)
		}
		ok, err = st.FinishForwardSegHash(tree.ID, 0, conv, row)
		if err != nil || ok {
			t.Fatalf("转发迟到结果写到另一文件：%v %v", ok, err)
		}
	}
	// 升级前的只读归档还没有 format，读消息仍应成功且不能猜文件类型。
	if _, err := st.Events.Exec(`ALTER TABLE media_fetch DROP COLUMN format`); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := st.Events.QueryRow(`SELECT id FROM events ORDER BY seq DESC LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	row, ok, err := st.ReadDisplayEvent(ctx, DisplayScope{Platform: "qq", Conversation: conv, Characters: []string{"a"}}, id)
	if err != nil || !ok || row.MediaMeta[0].Format != "" {
		t.Fatalf("旧归档读文件失败或猜类型：%+v %v", row, err)
	}
}

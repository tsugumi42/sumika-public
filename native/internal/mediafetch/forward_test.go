package mediafetch

import (
	"context"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
	"testing"
)

func TestForwardImageDownloadAndGuard(t *testing.T) {
	e := newEnv(t)
	content := png("编的转发图")
	e.serve("/forward", 200, content)
	tree := types.ForwardTree{ID: "outer", Nodes: []types.ForwardMessageNode{{Name: "编的甲", Segs: []types.Seg{imgSeg("/forward")}}}}
	conv := "qq:group:90002"
	if err := e.st.PutForward(context.Background(), tree, 1000, conv); err != nil {
		t.Fatal(err)
	}
	for _, row := range []store.MediaFetch{{Idx: 0, Kind: "file", URLSig: store.MediaURLSig(tree.Nodes[0].Segs[0].URL)}, {Idx: 0, Kind: "image", URLSig: "wrong"}, {Idx: 1, Kind: "image", URLSig: store.MediaURLSig(tree.Nodes[0].Segs[0].URL)}} {
		row.Hash = sha(content)
		row.State = store.MediaDone
		ok, err := e.st.FinishForwardSegHash("outer", 0, conv, row)
		if err != nil || ok {
			t.Fatalf("错目标回填：%+v %v %v", row, ok, err)
		}
	}
	e.start()
	if err := e.f.OfferForward(tree, conv); err != nil {
		t.Fatal(err)
	}
	e.idle()
	got, ok, err := e.st.ReadForward(context.Background(), "outer")
	if err != nil || !ok || got.Tree.Nodes[0].Segs[0].Hash != sha(content) {
		t.Fatalf("转发图未回填：%+v %v", got, err)
	}
	rows, err := e.st.ForwardMediaRows(context.Background(), "outer")
	if err != nil || rows[[2]int{0, 0}].State != store.MediaDone {
		t.Fatalf("转发图状态：%v %v", rows, err)
	}
}

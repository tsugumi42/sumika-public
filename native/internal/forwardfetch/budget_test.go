package forwardfetch

import (
	"context"
	"fmt"
	"strings"
	"sumika-native/internal/channel/types"
	"testing"
	"time"
)

func TestForwardNestedRetryKeepsRootLimits(t *testing.T) {
	for _, mode := range []string{"nodes", "images", "depth"} {
		t.Run(mode, func(t *testing.T) {
			st := forwardStore(t)
			reader := &treeReader{trees: map[string]types.ForwardTree{}}
			root := types.ForwardTree{ID: "outer", Nodes: []types.ForwardMessageNode{{Segs: []types.Seg{{Kind: "forward", Ref: "inner"}}}}}
			via := []string{"outer"}
			if mode == "nodes" {
				root.Nodes = append(root.Nodes, make([]types.ForwardMessageNode, 199)...)
			}
			if mode == "images" {
				for i := range 60 {
					root.Nodes[0].Segs = append(root.Nodes[0].Segs, types.Seg{Kind: "image", URL: fmt.Sprintf("https://gchat.qpic.cn/%d", i), Hash: strings.Repeat("a", 64)})
				}
			}
			if mode == "depth" {
				root.Nodes[0].Segs[0].Ref = "level1"
				for i := 1; i < 9; i++ {
					id := fmt.Sprintf("level%d", i)
					next := "inner"
					if i < 8 {
						next = fmt.Sprintf("level%d", i+1)
					}
					tree := types.ForwardTree{ID: id, Nodes: []types.ForwardMessageNode{{Segs: []types.Seg{{Kind: "forward", Ref: next}}}}}
					if err := st.PutForward(context.Background(), tree, time.Now().UnixMilli(), "qq:group:90002"); err != nil {
						t.Fatal(err)
					}
					via = append(via, id)
				}
			}
			if err := st.PutForward(context.Background(), root, time.Now().UnixMilli(), "qq:group:90002"); err != nil {
				t.Fatal(err)
			}
			reader.trees["inner"] = types.ForwardTree{ID: "inner", Nodes: []types.ForwardMessageNode{{Segs: []types.Seg{{Kind: "image", URL: "https://gchat.qpic.cn/new"}, {Kind: "forward", Ref: "too-deep"}}}}}
			reader.trees["too-deep"] = types.ForwardTree{ID: "too-deep", Nodes: []types.ForwardMessageNode{{Name: "不该取的节点"}}}
			f := New(Options{Store: st, Reader: reader, Account: "100", Logf: t.Logf})
			f.Start()
			defer f.Close()
			if _, err := f.Fetch(context.Background(), "inner", "qq:group:90002", via, 5*time.Second); err != nil {
				t.Fatal(err)
			}
			got, have, err := st.ReadForward(context.Background(), "inner")
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "nodes":
				if have || len(reader.calls) != 0 {
					t.Fatalf("用完根节点预算后仍取里层：have=%v calls=%v", have, reader.calls)
				}
			case "images":
				if !have || got.Tree.Nodes[0].Segs[0].Raw != "forward_limit" {
					t.Fatalf("已下载的60张没有扣根预算：%+v", got.Tree)
				}
			case "depth":
				if _, ok, err := st.ReadForward(context.Background(), "too-deep"); err != nil || ok {
					t.Fatalf("里层重取越过第10层：have=%v err=%v calls=%v", ok, err, reader.calls)
				}
			}
		})
	}
}

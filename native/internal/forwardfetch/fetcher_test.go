package forwardfetch

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

type treeReader struct {
	mu              sync.Mutex
	trees           map[string]types.ForwardTree
	calls           []string
	emptyUntilOuter bool
}

func (r *treeReader) Forward(_ context.Context, _, id string) (types.ForwardTree, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, id)
	if r.emptyUntilOuter && id == "inner" {
		return types.ForwardTree{}, errors.New("payload_empty")
	}
	if id == "outer" {
		r.emptyUntilOuter = false
	}
	tree, ok := r.trees[id]
	if !ok {
		return tree, errors.New("unavailable")
	}
	return tree, nil
}
func forwardStore(t *testing.T) *store.Store {
	t.Helper()
	p, err := paths.Init(filepath.Join(t.TempDir(), "数据"))
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { set.Close() })
	st, _ := set.Platform("qq")
	return st
}
func TestForwardFetchLimitsAndAncestors(t *testing.T) {
	for _, mode := range []string{"预算", "深度", "祖先", "图片"} {
		t.Run(mode, func(t *testing.T) {
			st := forwardStore(t)
			r := &treeReader{trees: map[string]types.ForwardTree{}}
			root := "outer"
			var via []string
			switch mode {
			case "预算":
				outer := types.ForwardTree{ID: "outer", Nodes: make([]types.ForwardMessageNode, 150)}
				outer.Nodes[0].Segs = []types.Seg{{Kind: "forward", Ref: "inner"}}
				r.trees["outer"] = outer
				r.trees["inner"] = types.ForwardTree{ID: "inner", Nodes: make([]types.ForwardMessageNode, 100)}
			case "深度":
				for i := range 11 {
					id := fmt.Sprint(i)
					r.trees[id] = types.ForwardTree{ID: id, Nodes: []types.ForwardMessageNode{{Segs: []types.Seg{{Kind: "forward", Ref: fmt.Sprint(i + 1)}}}}}
				}
				root = "0"
			case "图片":
				segs := make([]types.Seg, 61)
				for i := range segs {
					segs[i] = types.Seg{Kind: "image", URL: fmt.Sprintf("https://gchat.qpic.cn/%d", i)}
				}
				r.trees["outer"] = types.ForwardTree{ID: "outer", Nodes: []types.ForwardMessageNode{{Segs: segs}}}
			case "祖先":
				root = "inner"
				via = []string{"outer"}
				r.emptyUntilOuter = true
				r.trees["outer"] = types.ForwardTree{ID: "outer", Nodes: []types.ForwardMessageNode{{Segs: []types.Seg{{Kind: "forward", Ref: "inner"}}}}}
				r.trees["inner"] = types.ForwardTree{ID: "inner", Nodes: []types.ForwardMessageNode{{Name: "编的名字", Segs: []types.Seg{{Kind: "text", Text: "编的话"}}}}}
			}
			f := New(Options{Store: st, Reader: r, Account: "100", Logf: t.Logf})
			f.Start()
			defer f.Close()
			state, err := f.Fetch(context.Background(), root, "qq:group:90002", via, 5*time.Second)
			if err != nil || state != "ready" {
				t.Fatalf("取树：%s %v", state, err)
			}
			switch mode {
			case "预算":
				inner, ok, err := st.ReadForward(context.Background(), "inner")
				if err != nil || !ok || len(inner.Tree.Nodes) != 50 || inner.Tree.Missing != 50 {
					t.Fatalf("跨层预算：%+v %v", inner, err)
				}
			case "深度":
				if len(r.calls) != 10 {
					t.Fatalf("取了 %d 层", len(r.calls))
				}
				if _, ok, _ := st.ReadForward(context.Background(), "10"); ok {
					t.Fatal("第11层被保存")
				}
			case "图片":
				tree, ok, err := st.ReadForward(context.Background(), root)
				if err != nil || !ok {
					t.Fatalf("读图片预算树：%+v %v", tree, err)
				}
				for i, s := range tree.Tree.Nodes[0].Segs {
					if (s.Raw == "forward_limit") != (i == 60) {
						t.Fatalf("第%d张的预算标记是 %q", i+1, s.Raw)
					}
				}
			case "祖先":
				if !reflect.DeepEqual(r.calls, []string{"inner", "outer", "inner"}) {
					t.Fatalf("先取外层再取里层：%v", r.calls)
				}
			}
		})
	}
}

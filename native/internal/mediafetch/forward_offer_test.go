package mediafetch

import (
	"reflect"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
	"testing"
	"time"
)

func TestForwardMediaOfferPreservesTask(t *testing.T) {
	for _, state := range []string{store.MediaPending, store.MediaFailed, store.MediaExpired, store.MediaBlocked, store.MediaTooBig} {
		t.Run(state, func(t *testing.T) {
			e := newEnv(t)
			s := imgSeg("/same")
			tree := types.ForwardTree{ID: "same", Nodes: []types.ForwardMessageNode{{Segs: []types.Seg{s}}}}
			before := store.MediaFetch{Idx: 0, Kind: "image", URLSig: store.MediaURLSig(s.URL), State: state, Tries: 4, NextAt: e.clk.Now().Add(time.Hour).UnixMilli(), UpdatedAt: 456, LastError: "编的原因"}
			if err := e.st.PutForwardMedia(tree.ID, 0, testConv, before); err != nil {
				t.Fatal(err)
			}
			if err := e.f.OfferForward(tree, testConv); err != nil {
				t.Fatal(err)
			}
			read := func() store.MediaFetch {
				var m store.MediaFetch
				err := e.st.Events.QueryRow(`SELECT idx,kind,url_sig,state,tries,next_at,updated_at,coalesce(last_error,'') FROM forward_media_fetch WHERE forward_id=?`, tree.ID).Scan(&m.Idx, &m.Kind, &m.URLSig, &m.State, &m.Tries, &m.NextAt, &m.UpdatedAt, &m.LastError)
				if err != nil {
					t.Fatal(err)
				}
				return m
			}
			if got := read(); !reflect.DeepEqual(got, before) {
				t.Fatalf("同URL任务被重置：got=%+v want=%+v", got, before)
			}
			if len(e.f.queue) != 0 {
				t.Fatalf("已有终局或尚未到期任务被重新排队：%d", len(e.f.queue))
			}
			tree.Nodes[0].Segs[0].URL = testSegURL("/new")
			if err := e.f.OfferForward(tree, testConv); err != nil {
				t.Fatal(err)
			}
			got := read()
			if got.State != store.MediaPending || got.Tries != 0 || got.NextAt != 0 || got.URLSig != store.MediaURLSig(tree.Nodes[0].Segs[0].URL) || got.LastError != "" {
				t.Fatalf("新URL没初始化：%+v", got)
			}
		})
	}
}

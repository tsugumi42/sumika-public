package desktopread

import (
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/store"
)

// 主人用原本的名字，存下的名字都没有才写"主人"；@ 段的名字来自 store 给的 AtNames，没有就空（界面显示号码）
func TestOwnerNameAndAtNames(t *testing.T) {
	t.Parallel()
	s := &Service{}
	if got := s.displayName(store.DisplayRow{ActorKind: "owner", DisplayName: "主人的群名片"}); got != "主人的群名片" {
		t.Errorf("有名字的主人显示 %q", got)
	}
	if got := s.displayName(store.DisplayRow{ActorKind: "owner"}); got != "主人" {
		t.Errorf("没名字的主人显示 %q", got)
	}
	segs := displaySegments(api.EventRef{}, parseEnvelope(`{"segs":[{"kind":"at","target":"90040"},{"kind":"at","target":"90053"}]}`), map[string]string{"90040": "主人的群名片"}, nil)
	if len(segs) != 2 || segs[0].Name != "主人的群名片" || segs[1].Name != "" || segs[1].Target != "90053" {
		t.Fatalf("@ 段：%+v", segs)
	}
}

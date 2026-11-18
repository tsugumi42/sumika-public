package memory

import (
	"reflect"
	"testing"
)

// 缺 Scene 时表达判断与显式场合一致；召回不恢复旧过滤。
func TestDisclosureScopesSceneEmptyReadsConversationID(t *testing.T) {
	t.Parallel()
	for _, a := range []Audience{
		{Channel: "qq", Conversation: GA, PersonIDs: []string{peerA}},
		{Channel: "qq", Conversation: ownerDM, PrivateOK: true, PersonIDs: []string{"owner"}},
		{Channel: "console", Conversation: "console:group:h", PrivateOK: true},
		ownerHere,
	} {
		explicit := a
		explicit.Scene = "private"
		if PlaceOf(a.Channel, a.Conversation, "") == LocalGroup || PlaceOf(a.Channel, a.Conversation, "") == PlatformGroup {
			explicit.Scene = "group"
		}
		if !reflect.DeepEqual(DisclosureScopes(a), DisclosureScopes(explicit)) {
			t.Errorf("场合口径不同：%+v", a)
		}
		if AllowedScopes(a) != nil {
			t.Errorf("缺 Scene 恢复了召回过滤：%+v", a)
		}
	}
}

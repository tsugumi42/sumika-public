package channel

import (
	"reflect"
	"testing"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/turn"
)

func TestSeenScenes(t *testing.T) {
	t.Parallel()
	k := newFakeKernel(t, &callLog{}, nil)
	h := newMH(t, func(c *mhCfg) { c.seen = k })
	for _, tc := range []struct {
		name string
		conv types.Conversation
		want bool
	}{
		{"local-private", types.Conversation{Channel: "console", Scene: types.ScenePrivate, ID: "fictional-peer", Self: "fictional-account"}, false},
		{"local-group", types.Conversation{Channel: "console", Scene: types.SceneGroup, ID: "fictional-group"}, false},
		{"qq-private", private(acctA, friend), true},
		{"qq-group", group("fictional-group"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k.mu.Lock()
			k.seen = nil
			k.mu.Unlock()
			s := Stored{Inbound: msg(acctA, tc.conv, "fictional-id", friend, "hello"), Character: "ca", Seq: 123, Observed: true}
			h.pl.OnStored(s)
			var want []turn.Seen
			if tc.want {
				want = []turn.Seen{{CharacterID: "ca", Conversation: tc.conv.String(), Seq: 123}}
			}
			if got := k.seenEvents(); !reflect.DeepEqual(got, want) {
				t.Fatalf("seen = %+v, want %+v", got, want)
			}
		})
	}
}

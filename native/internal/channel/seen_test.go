package channel

import (
	"context"
	"reflect"
	"testing"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/turn"
)

func TestSeenInbound(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                              string
		backfill, self, duplicate, record bool
		tags                              []string
		want                              bool
	}{
		{name: "normal", want: true},
		{name: "backfill", backfill: true, want: true},
		{name: "self", self: true, want: true},
		{name: "duplicate", duplicate: true},
		{name: "record", record: true},
		{name: "stranger", tags: []string{"stranger"}, want: true},
		{name: "selftest", tags: []string{"selftest"}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			k := newFakeKernel(t, &callLog{}, nil)
			h := newMH(t, func(c *mhCfg) {
				c.seen = k
				c.admit = func(types.Inbound) Admission { return Admission{NoObserve: len(tc.tags) > 0, Tags: tc.tags} }
			})
			if tc.record {
				h.setMode(ModeRecord)
			}
			in := msg(acctA, group("fictional-group"), "fictional-message", friend, "hello")
			if tc.self {
				in.From.ID = acctA
				in.Kind = types.InboundSelf
			}
			tk, err := h.in.Submit(context.Background(), in, Options{Backfill: tc.backfill})
			if err != nil {
				t.Fatal(err)
			}
			result, err := tk.Wait(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err = h.in.Idle(context.Background()); err != nil {
				t.Fatal(err)
			}
			if tc.duplicate {
				k.mu.Lock()
				k.seen = nil
				k.mu.Unlock()
				in.Account = acctB
				tk, err = h.in.Submit(context.Background(), in, Options{})
				if err != nil {
					t.Fatal(err)
				}
				result, err = tk.Wait(context.Background())
				if err != nil || !result.Duplicate {
					t.Fatalf("duplicate: %+v %v", result, err)
				}
				if err = h.in.Idle(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			var want []turn.Seen
			if tc.want {
				want = []turn.Seen{{CharacterID: "ca", Conversation: in.Conv.String(), Seq: result.Seq, Backfill: tc.backfill, FromSelf: tc.self, Stranger: tc.name == "stranger", Selftest: tc.name == "selftest"}}
			}
			if got := k.seenEvents(); !reflect.DeepEqual(got, want) {
				t.Fatalf("seen = %+v, want %+v", got, want)
			}
		})
	}
}

func TestSeenOutbound(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, mode, origin string
		self, notify       bool
	}{
		{"reply", ModeSpeak, types.OriginCharacter, true, true},
		{"dry", ModeDryRun, types.OriginCharacter, false, false},
		{"automation", ModeSpeak, "automation:fictional", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			k := newFakeKernel(t, &callLog{}, nil)
			h := newOH(t, types.Caps{}, func(c *OutletConfig) {
				c.Seen = k
				c.Mode = func(string, string, types.Conversation) string { return tc.mode }
			})
			conv := group("fictional-group")
			held, err := h.out.SendHeld(context.Background(), "ca", tc.origin, conv, []types.Part{txt("hello")})
			if err != nil {
				t.Fatal(err)
			}
			var count int
			if err := h.st.Events.QueryRow("SELECT count(*) FROM events WHERE seq = ?", held.EventSeq).Scan(&count); err != nil || count != 1 {
				t.Fatalf("stored: %d %v", count, err)
			}
			var want []turn.Seen
			if tc.notify {
				want = []turn.Seen{{CharacterID: "ca", Conversation: conv.String(), Seq: held.EventSeq, FromSelf: tc.self}}
			}
			if got := k.seenEvents(); !reflect.DeepEqual(got, want) {
				t.Fatalf("seen = %+v, want %+v", got, want)
			}
		})
	}
}

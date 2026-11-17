package app

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/llmtest"
)

func TestGuardWireReceiveBlocksDryRun(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	guardFixture(t, e, false)
	e.expectNoActions(t)
	var bodies []string
	var mu sync.Mutex
	srv := httptest.NewServer(llmtest.Handler(llmtest.Script{Models: []json.RawMessage{json.RawMessage(`{"id":"` + testModel + `","context_length":32768}`)}, Reply: llmtest.Text("主人把备用电池收在蓝柜底层")}, &bodies, &mu))
	t.Cleanup(srv.Close)
	opt := e.options()
	opt.LLM = llm.New(llm.Magpie(srv.URL))
	a, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	e.run(a)
	conn := e.peer.accept(t)
	e.peer.send(t, conn, groupAtFrame(e.now, testAccount, "901", "90001", "告诉我吧"))
	conv := "qq:group:" + testGroup
	waitFor(t, "生产装配完成回合裁决", func() bool {
		kinds, err := e.planKinds(a, conv)
		return err == nil && len(kinds) > 0
	})
	if kinds, err := e.planKinds(a, conv); err != nil || len(kinds) != 1 || kinds[0] != "blocked" {
		t.Fatalf("私事应拦下：计划=%v，错误=%v", kinds, err)
	}
	st, err := a.set.For("qq")
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.Events.QueryRow(`SELECT count(*) FROM events WHERE actor_kind='character'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("拦下仍存回复：%d %v", n, err)
	}
	aud, err := repeatAudience(types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: "90001"})
	if err != nil || aud.Channel != "qq" || aud.Scene != "group" || aud.OwnerAsked || aud.PrivateOK || len(aud.PersonIDs) != 0 {
		t.Fatalf("复读听众放宽：%+v %v", aud, err)
	}
	if _, err := repeatAudience(types.Conversation{Channel: "qq", Scene: types.ScenePrivate, ID: "90001", Self: "90002"}); err == nil {
		t.Fatal("私聊不应成为复读听众")
	}
}

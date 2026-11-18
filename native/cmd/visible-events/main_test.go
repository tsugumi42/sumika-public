package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"testing"

	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

func TestVisibleEventsTemporaryData(t *testing.T) {
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatal(err)
	}
	st, _ := set.Platform("qq")
	seq, err := st.AppendEvent(store.NewEvent{Channel: "qq", Conversation: "qq:group:90002", ActorKind: "human", ActorID: "90001", Kind: "message", Text: "不得输出这段正文", CreatedAt: 123}, []string{"a"})
	if err != nil {
		set.Close()
		t.Fatal(err)
	}
	var id string
	if err := st.Events.QueryRow(`SELECT id FROM events WHERE seq=?`, seq).Scan(&id); err != nil {
		set.Close()
		t.Fatal(err)
	}
	if _, err := st.AppendEvent(store.NewEvent{Channel: "qq", Conversation: "qq:group:90002", ActorKind: "human", ActorID: "90001", Kind: "message", Text: "hidden", CreatedAt: 124}, nil); err != nil {
		set.Close()
		t.Fatal(err)
	}
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run(context.Background(), []string{"-data", p.Root, "-conversation", "qq:group:90002", "-characters", "a"}, &out); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("qq\t%s\t1\t123\tfalse\n", id)
	if out.String() != want {
		t.Fatalf("输出身份 got=%q want=%q", out.String(), want)
	}
	cmd := exec.Command("go", "run", ".", "-data", p.Root, "-conversation", "qq:group:90002", "-characters", "a")
	actual, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("诊断进程失败: %v\n%s", err, actual)
	}
	if string(actual) != want {
		t.Fatalf("诊断进程输出 got=%q want=%q", actual, want)
	}
	t.Logf("visible-events 进程输出: %s", actual)
}

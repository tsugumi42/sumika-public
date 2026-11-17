package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/config"
	"sumika-native/internal/turn"
)

// 真工具读不到文件时，交回必须留下出错会话、回合、请求和工具原文；相邻正常会话不误报。
func TestFailureEvidence(t *testing.T) {
	root, err := config.FindRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Join(root, "vendor", "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	dir := t.TempDir()
	k, err := newKernel(dir, 2, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer k.close()
	if err = os.Remove(k.sessions[0].file); err != nil {
		t.Fatal(err)
	}
	var rounds []*roundRecord
	for _, s := range k.sessions {
		text := "[session=" + s.id + "] [round=0] 暗号：" + s.secret
		seq, e := k.append(s, text)
		if e != nil {
			t.Fatal(e)
		}
		out := k.hub.Respond(turn.Input{CharacterID: s.character, Conversation: s.id, Seqs: []int64{seq}, Text: text, Audience: audience.Console(s.id), Addressed: true, Scope: turn.ConsoleScope})
		select {
		case outcome := <-out:
			if outcome.Err != nil {
				t.Fatal(outcome.Err)
			}
			rounds = append(rounds, &roundRecord{Session: s.id, Round: 1, TurnID: outcome.TurnID})
		case <-time.After(30 * time.Second):
			dump(dir, &roundRecord{Session: s.id, Round: 1}, "诊断测试")
			t.Fatal("诊断回合超过 30 秒")
		}
	}
	if err = k.evidence(dir, rounds); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "failures.json"))
	if err != nil {
		t.Fatal(err)
	}
	var failures []struct {
		Round    *roundRecord
		Requests []requestRecord
		Steps    []map[string]any
	}
	if err = json.Unmarshal(b, &failures); err != nil {
		t.Fatal(err)
	}
	if len(failures) != 1 || failures[0].Round.Session != k.sessions[0].id || failures[0].Round.Round != 1 || failures[0].Round.TurnID != rounds[0].TurnID {
		t.Fatalf("只应报读不到文件的那一轮，实际 %s", b)
	}
	f := failures[0]
	var requestWithTools, failedRead bool
	for _, req := range f.Requests {
		if req.Session != f.Round.Session {
			t.Fatalf("失败证据串了会话：%+v", req)
		}
		requestWithTools = requestWithTools || (strings.Contains(req.Body, `"role":"tool"`) && strings.Contains(req.Body, "ENOENT") && strings.Contains(req.Body, "book.txt"))
	}
	for _, step := range f.Steps {
		failedRead = failedRead || (step["name"] == "read" && step["is_error"] == float64(1) && strings.Contains(step["preview"].(string), "book.txt"))
	}
	if !requestWithTools || !failedRead || len(f.Round.Errors) == 0 {
		t.Fatalf("失败诊断缺少请求、工具错误原文或原因：%s", b)
	}
	t.Logf("失败会话 1，失败回合 1，关联请求 %d，关联步骤 %d；正常会话未误报", len(f.Requests), len(f.Steps))
}

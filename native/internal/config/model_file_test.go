package config

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestModelFileExample(t *testing.T) {
	example, err := os.ReadFile("../../../sumika.example.yml")
	if err != nil {
		t.Fatal(err)
	}
	example = bytes.ReplaceAll(example, []byte("\r\n"), []byte("\n"))
	cases := []struct {
		ch       ModelChange
		old, new string
	}{
		{ModelChange{Field: CharacterModel, Character: "a", String: new("fake/new")}, "characters: {}", "characters:\n  \"a\":\n    \"model\": fake/new"},
		{ModelChange{Field: CharacterBackground, Character: "a", String: new("fake/bg")}, "characters: {}", "characters:\n  \"a\":\n    \"background\": fake/bg"},
		{ModelChange{Field: BackgroundModel, String: new("fake/bg")}, "  fallbackModels: []\n", "  fallbackModels: []\n  \"backgroundModel\": fake/bg\n"},
		{ModelChange{Field: BackgroundThinking, String: new("high")}, "backgroundThinking: low", "backgroundThinking: high"},
		{ModelChange{Field: ThinkingLocal, String: new("high")}, "local: medium", "local: high"},
		{ModelChange{Field: ThinkingPrivate, String: new("medium")}, "private: low", "private: medium"},
		{ModelChange{Field: ThinkingGroup, String: new("high")}, "group: low", "group: high"},
		{ModelChange{Field: ContextWindow, Int: new(100000)}, "    autoCompactChat: true   # 本地闲聊每整理完一批记忆就把整理过的压掉、快满了也压缩；关掉的话满了只带最近的话", "    autoCompactChat: true   # 本地闲聊每整理完一批记忆就把整理过的压掉、快满了也压缩；关掉的话满了只带最近的话\n    \"window\": 100000"},
		{ModelChange{Field: ContextReserve, Int: new(1000)}, "reserve: 16384", "reserve: 1000"},
		{ModelChange{Field: ContextKeepTokens, Int: new(3000)}, "keepTokens: 20000", "keepTokens: 3000"},
		{ModelChange{Field: ContextSoftRatio, Float: new(0.6)}, "softRatio: 0.7", "softRatio: 0.6"},
		{ModelChange{Field: ContextHardRatio, Float: new(0.9)}, "hardRatio: 0.85", "hardRatio: 0.9"},
		{ModelChange{Field: ContextAutoCompact, Bool: new(false)}, "autoCompactChat: true", "autoCompactChat: false"},
	}
	for _, nl := range []string{"\n", "\r\n"} {
		for _, tt := range cases {
			t.Run(string(tt.ch.Field)+strings.ReplaceAll(nl, "\n", "LF"), func(t *testing.T) {
				raw := bytes.ReplaceAll(example, []byte("\n"), []byte(nl))
				dir := t.TempDir()
				file := filepath.Join(dir, "sumika.yml")
				history := filepath.Join(dir, "history")
				if err := os.WriteFile(file, raw, 0600); err != nil {
					t.Fatal(err)
				}
				c, err := SaveModel(context.Background(), file, history, ContentVersion(raw), tt.ch, time.Unix(1234, 0))
				if err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				want := strings.Replace(string(raw), strings.ReplaceAll(tt.old, "\n", nl), strings.ReplaceAll(tt.new, "\n", nl), 1)
				if string(got) != want {
					t.Fatalf("目标以外字节改变：\ngot %s\nwant %s", got, want)
				}
				if c.Source != file {
					t.Fatal(c.Source)
				}
				entries, err := os.ReadDir(history)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 1 {
					t.Fatal(entries)
				}
				backup, err := os.ReadFile(filepath.Join(history, entries[0].Name()))
				if err != nil || !bytes.Equal(backup, raw) {
					t.Fatalf("备份不等于旧文件：%v", err)
				}
			})
		}
	}
}
func TestModelFileBoundaries(t *testing.T) {
	raw := []byte("# 秘密保持原样\ncharacters:\n  a:\n    model: fake/old\n    background: fake/bg  # 跟全局\ndefaults:\n  backgroundThinking: low\nagent:\n  thinking:\n    local: medium\n")
	for _, ch := range []ModelChange{
		{Field: "qq.goSend", Bool: new(true)}, {Field: CharacterModel, Character: "a", String: new("")},
		{Field: ThinkingPrivate, String: new("wrong")}, {Field: ContextSoftRatio, Float: new(0.99)},
		{Field: ContextWindow, String: new("1000")}, {Field: ContextWindow, Int: new(999)},
		{Field: ContextReserve, Int: new(-1)}, {Field: ContextKeepTokens, Int: new(999)},
		{Field: ThinkingLocal}, {Field: ThinkingLocal, String: new("high"), Bool: new(true)},
	} {
		if _, _, err := PatchModel(raw, "fixture.yml", ch); err == nil {
			t.Fatalf("接受了坏值：%+v", ch)
		}
	}
	out, _, err := PatchModel(raw, "fixture.yml", ModelChange{Field: CharacterBackground, Character: "a", String: new("")})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != strings.Replace(string(raw), "    background: fake/bg  # 跟全局", "    # 跟全局", 1) {
		t.Fatalf("删除目标：%s", out)
	}
	for _, input := range []string{"defaults: &x {backgroundThinking: low}\nagent: {thinking: *x}\n", "defaults: {backgroundThinking: low}\n---\nqq: {}\n"} {
		if _, _, err := PatchModel([]byte(input), "fixture.yml", ModelChange{Field: ThinkingLocal, String: new("high")}); err == nil {
			t.Fatal("接受了无法安全修改的 YAML")
		}
	}
	for _, input := range []string{"defaults: {backgroundThinking: low}\n", "defaults:\n  'backgroundThinking': 'low'    # 对齐\n", "defaults:\n  backgroundThinking: low"} {
		out, c, err := PatchModel([]byte(input), "fixture.yml", ModelChange{Field: BackgroundThinking, String: new("high")})
		if err != nil || c.Defaults.BackgroundThinking != "high" {
			t.Fatalf("%s：%s %v", input, out, err)
		}
	}
	for _, input := range []string{"characters:\n  a:\n    model: vendor/a,b}\n", "characters: {a: {model: 'vendor/a,b}'}}\n"} {
		out, c, err := PatchModel([]byte(input), "fixture.yml", ModelChange{Field: CharacterModel, Character: "a", String: new("fake/new")})
		if err != nil || c.Characters["a"].Model != "fake/new" {
			t.Fatalf("模型 ID 标点：%s %v", out, err)
		}
	}
}
func TestModelFileVersionAndHistory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "sumika.yml")
	history := filepath.Join(dir, "history")
	raw := []byte("defaults:\n  backgroundThinking: low\n")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1234, 0)
	ch := ModelChange{Field: BackgroundThinking, String: new("high")}
	if _, err := SaveModel(context.Background(), file, history, "stale", ch, now); !errors.Is(err, ErrConfigVersion) {
		t.Fatal(err)
	}
	if _, err := SaveModel(context.Background(), file, history, ContentVersion(raw), ch, now); err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	base := ContentVersion(current)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, s := range []string{"low", "medium"} {
		wg.Add(1)
		go func(s string) {
			defer wg.Done()
			_, err := SaveModel(context.Background(), file, history, base, ModelChange{Field: BackgroundThinking, String: &s}, now)
			results <- err
		}(s)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrConfigVersion) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
	files, err := os.ReadDir(history)
	if err != nil || len(files) != 2 {
		t.Fatalf("同秒备份 %v %v", files, err)
	}
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(history, f.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(b, raw) && !bytes.Equal(b, current) {
			t.Fatalf("备份内容错误：%s", b)
		}
	}
}

// 文件里没有 defaults 这一段时，把后台模型清空（写空串就是删掉这一项）不改文件、不报错；
// 以前自检的预期表会凭空多出 defaults: {}，报“目标以外的键发生变化”。
func TestModelClearAbsentKey(t *testing.T) {
	raw := []byte("agent: {}\n")
	empty := ""
	for _, ch := range []ModelChange{{Field: BackgroundModel, String: &empty}, {Field: CharacterBackground, Character: "example", String: &empty}} {
		out, _, err := PatchModel(raw, "fixture.yml", ch)
		if err != nil {
			t.Fatalf("%s：%v", ch.Field, err)
		}
		if string(out) != string(raw) {
			t.Fatalf("%s：文件不该变：\n%s", ch.Field, out)
		}
	}
}

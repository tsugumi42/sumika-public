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

	"sumika-native/internal/channel/types"
)

func TestLinkSaveAndModelConflict(t *testing.T) {
	for _, nl := range []string{"\n", "\r\n"} {
		t.Run(strings.ReplaceAll(nl, "\r", "CR"), func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "sumika.yml")
			history := filepath.Join(dir, "history")
			raw := []byte(strings.ReplaceAll("# 编造的秘密和未知键保持原字节\nunknown: 'keep # exactly'\nlinkReader:\n  expand:\n    mode: whitelist  # 模式\n    conversations: []  # 名单\n", "\n", nl))
			if e := os.WriteFile(file, raw, 0600); e != nil {
				t.Fatal(e)
			}
			c, e := SaveLink(context.Background(), file, history, ContentVersion(raw), LinkChange{Key: LinkList, Mode: "blacklist", Conversations: []string{"qq:group:90002"}}, time.Unix(1234, 0))
			if e != nil {
				t.Fatal(e)
			}
			if c.LinkReader.Expand.Mode != "blacklist" || len(c.LinkReader.Expand.Conversations) != 1 {
				t.Fatal(c.LinkReader.Expand)
			}
			after, e := os.ReadFile(file)
			if e != nil {
				t.Fatal(e)
			}
			if !bytes.HasPrefix(after, raw[:bytes.Index(raw, []byte("linkReader:"))]) || !bytes.Contains(after, []byte("  # 模式"+nl)) || !bytes.Contains(after, []byte("  # 名单"+nl)) {
				t.Fatalf("非目标字节损坏：%s", after)
			}
			if nl == "\r\n" && bytes.Contains(bytes.ReplaceAll(after, []byte("\r\n"), nil), []byte("\n")) {
				t.Fatal("插入了裸LF")
			}
			backups, e := os.ReadDir(history)
			if e != nil || len(backups) != 1 {
				t.Fatal(backups, e)
			}
			backup, e := os.ReadFile(filepath.Join(history, backups[0].Name()))
			if e != nil || !bytes.Equal(backup, raw) {
				t.Fatal("备份与原文件不同", e)
			}
			version := ContentVersion(after)
			start := make(chan struct{})
			results := make(chan error, 2)
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				_, e := SaveLink(context.Background(), file, history, version, LinkChange{Key: LinkEnabled, Bool: true}, time.Unix(1234, 0))
				results <- e
			}()
			go func() {
				defer wg.Done()
				<-start
				_, e := SaveModel(context.Background(), file, history, version, ModelChange{Field: BackgroundThinking, String: new("high")}, time.Unix(1234, 0))
				results <- e
			}()
			close(start)
			wg.Wait()
			close(results)
			success, conflict := 0, 0
			for e := range results {
				if e == nil {
					success++
				} else if errors.Is(e, ErrConfigVersion) {
					conflict++
				} else {
					t.Fatal(e)
				}
			}
			if success != 1 || conflict != 1 {
				t.Fatal("两页旧版本互相覆盖", success, conflict)
			}
			final, e := os.ReadFile(file)
			if e != nil {
				t.Fatal(e)
			}
			c, e = Parse(final, file)
			if e != nil {
				t.Fatal(e)
			}
			if (c.LinkReader.Expand.Enabled) == (c.Defaults.BackgroundThinking == "high") || c.LinkReader.Expand.Mode != "blacklist" || !bytes.Contains(final, []byte("unknown: 'keep # exactly'")) {
				t.Fatal("覆盖了另一个保存或未知字段")
			}
			backups, e = os.ReadDir(history)
			if e != nil || len(backups) != 2 {
				t.Fatal("同秒备份覆盖", len(backups), e)
			}
			if e := os.WriteFile(filepath.Join(dir, "not-a-directory"), []byte("x"), 0600); e != nil {
				t.Fatal(e)
			}
			_, e = SaveLink(context.Background(), file, filepath.Join(dir, "not-a-directory", "history"), ContentVersion(final), LinkChange{Key: LinkPrivateStatus, Text: "编的状态"}, time.Unix(1235, 0))
			if e == nil {
				t.Fatal("不可写history仍声称保存成功")
			}
			unchanged, e := os.ReadFile(file)
			if e != nil || !bytes.Equal(final, unchanged) {
				t.Fatal("备份失败后仍修改配置", e)
			}
			t.Logf("并发保存：成功=%d，版本拒绝=%d，备份=%d，旧文件=%d字节", success, conflict, len(backups), len(raw))
		})
	}
}
func TestLinkPolicyAndYAMLBoundaries(t *testing.T) {
	c := DefaultLinks()
	convs := []types.Conversation{{Channel: "console", Scene: "private", Self: "owner", ID: "a"}, {Channel: "console", Scene: "group", ID: "room"}, {Channel: "qq", Scene: "private", Self: "90001", ID: "90003"}, {Channel: "qq", Scene: "group", ID: "90002"}}
	for _, conv := range convs {
		if c.Expand.Allows(conv) {
			t.Fatal("默认发送", conv)
		}
	}
	c.Expand.Enabled = true
	for _, mode := range []string{"whitelist", "blacklist"} {
		c.Expand.Mode = mode
		for _, conv := range convs {
			c.Expand.Conversations = nil
			want := conv.Channel == "qq" && mode == "blacklist"
			if c.Expand.Allows(conv) != want {
				t.Fatal(mode, conv, "空名单")
			}
			c.Expand.Conversations = []string{conv.String()}
			want = conv.Channel == "qq" && mode == "whitelist"
			if c.Expand.Allows(conv) != want {
				t.Fatal(mode, conv, "已选名单")
			}
		}
	}
	for _, raw := range []string{"linkReader: {expand: {enabled: null}}", "linkReader: {expand: {enabled: 'false'}}", "linkReader: {expand: {maxLinks: 0}}", "linkReader: {expand: {maxLinks: '3'}}", "linkReader: {expand: {mode: all}}", "linkReader: {expand: {conversations: [qq:group:0]}}", "linkReader: {expand: {video: {timeoutSeconds: 601}}}"} {
		if _, e := Parse([]byte(raw), "fixture.yml"); e == nil {
			t.Fatal("接受了非法形状", raw)
		}
	}
	for _, raw := range []string{"linkReader: &x {expand: {enabled: false}}\n", "linkReader: {expand: {enabled: false}}\n---\nother: 1\n"} {
		if _, _, e := PatchLink([]byte(raw), "fixture.yml", LinkChange{Key: LinkEnabled, Bool: true}); e == nil {
			t.Fatal("接受了不能定向修改的YAML")
		}
	}
	if _, _, e := PatchLink([]byte("{}\n"), "fixture.yml", LinkChange{Key: "qq.goSend", Bool: true}); e == nil {
		t.Fatal("插件写入口扩大了键白名单")
	}
}

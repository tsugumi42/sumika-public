package snowluma

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"sumika-native/internal/channel/types"
)

func TestFileURLQueriesAndRejectsUnusableReplies(t *testing.T) {
	for _, tc := range []struct{ name, conv, hash, action string }{
		{"group", groupConv().String(), "", "get_group_file_url"},
		{"private", privateConv().String(), "opaque-hash", "get_private_file_url"},
		{"forward-with-private-hash", "", "opaque-hash", "get_private_file_url"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newSendEnv(t, func(a sentAction) (string, bool) {
				return `{"status":"ok","retcode":0,"data":{"url":"https://multimedia.nt.qq.com.cn/file?rkey=madeup"}}`, a.Action == tc.action
			})
			u, err := e.ad.FileURL(context.Background(), sendAccount, tc.conv, types.Seg{Kind: types.SegFile, FileID: "opaque-id", FileHash: tc.hash})
			if err != nil || !strings.Contains(u, "/file?") {
				t.Fatalf("没有取得直链：%q %v", u, err)
			}
			found := false
			for _, a := range e.peer.rawActions() {
				if a.Action != tc.action {
					continue
				}
				found = true
				var id string
				_ = json.Unmarshal(a.Params["file_id"], &id)
				if id != "opaque-id" {
					t.Fatal("文件标识改变")
				}
				if tc.action == "get_private_file_url" {
					var hash string
					_ = json.Unmarshal(a.Params["file_hash"], &hash)
					if hash != tc.hash {
						t.Fatal("私聊取址缺 hash")
					}
				}
				if tc.name == "forward-with-private-hash" {
					if _, has := a.Params["group_id"]; has {
						t.Fatal("猜了原群")
					}
				}
			}
			if !found || len(e.peer.actions()) != 0 {
				t.Fatal("文件动作未查询或发送了消息")
			}
		})
	}
	for _, body := range []string{
		`{"status":"ok","retcode":0,"data":{"url":"file:///C:/fake.txt"}}`,
		`{"status":"ok","retcode":0,"data":{"url":123}}`,
		`{"status":"ok","retcode":0,"data":{}}`,
		`{"status":"failed","retcode":100,"wording":"fake-secret"}`,
	} {
		t.Run(body, func(t *testing.T) {
			e := newSendEnv(t, func(a sentAction) (string, bool) { return body, a.Action == "get_group_file_url" })
			u, err := e.ad.FileURL(context.Background(), sendAccount, groupConv().String(), types.Seg{Kind: types.SegFile, FileID: "opaque"})
			if err == nil || u != "" || strings.Contains(err.Error(), "fake-secret") {
				t.Fatalf("假成功或泄漏：%q %v", u, err)
			}
		})
	}
	e := newSendEnv(t, func(sentAction) (string, bool) { return "", false })
	for _, seg := range []types.Seg{{Kind: types.SegFile}, {Kind: types.SegFile, FileID: "opaque"}, {Kind: types.SegFile, FileID: "bad\nid", FileHash: "h"}, {Kind: types.SegFile, FileID: "opaque", FileHash: "bad\nhash"}} {
		if _, err := e.ad.FileURL(context.Background(), sendAccount, "", seg); err == nil {
			t.Fatal("无来源凭据被接受")
		}
	}
	for _, a := range e.peer.rawActions() {
		if a.Action == "get_private_file_url" || a.Action == "get_group_file_url" {
			t.Fatal("坏凭据发起查询")
		}
	}
	for _, tc := range []struct{ raw, want string }{
		{`90004`, "90004"}, {`"90004"`, "90004"}, {`0`, ""}, {`null`, ""}, {`""`, ""}, {`"bad"`, ""}, {`{}`, ""}, {`[]`, ""},
	} {
		frame := []byte(`{"status":"ok","retcode":0,"data":{"messages":[{"group_id":` + tc.raw + `,"user_id":90003,"message":[{"type":"file","data":{"name":"清单.txt","file_id":"opaque","file_hash":"h"}}]}]}}`)
		tree, err := forwardFrame(frame, "fake-forward")
		if err != nil || len(tree.Nodes) != 1 || tree.Nodes[0].Segs[0].FileGroup != tc.want || tree.Nodes[0].Segs[0].FileHash != "h" {
			t.Fatalf("转发原群字段未正确保留：%+v %v", tree, err)
		}
	}
}

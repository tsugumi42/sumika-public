package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/llm/llmtest"
)

type failFirstApproval struct {
	api.Reader
	api.LocalChat
	calls int
}

func (w *failFirstApproval) DecideLocal(ctx context.Context, k api.ConversationKey, id string, choice api.ApprovalChoice) error {
	w.calls++
	if w.calls == 1 {
		return errors.New("编造的提交失败")
	}
	return w.LocalChat.DecideLocal(ctx, k, id, choice)
}

// 提交失败不等于工具执行成功；原审批仍可展开、重试并只执行一次。
func TestLocalApprovalFailureDetailsAndRetry(t *testing.T) {
	name := "approval-retry.txt"
	args, err := json.Marshal(map[string]string{"purpose": "编造的审批重试", "path": name, "content": "编造重试后的实际文件"})
	if err != nil {
		t.Fatal(err)
	}
	h := newLocalUI(t, llmtest.Script{Replies: []llmtest.Reply{llmtest.Call("retry-write", "write", string(args)), llmtest.Text("编造的重试完成")}}, nil, 0)
	target := filepath.Join(h.workdir, name)
	writer := &failFirstApproval{Reader: h.a.reader, LocalChat: h.a.reader.(api.LocalChat)}
	h.a.reader = writer
	h.send(t, "编造的审批请求")
	h.until(t, "出现审批", func() bool { return h.tt.HasText("允许一次") })
	h.click(t, "审批详情")
	h.until(t, "详情展开", func() bool {
		for _, state := range h.a.approvalState {
			if state.detail {
				return h.tt.HasText("编造重试后的实际文件")
			}
		}
		return false
	})
	h.click(t, "允许一次")
	h.until(t, "失败可见", func() bool { return h.tt.HasText("编造的提交失败") })
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("失败被当成批准：%v", err)
	}
	h.click(t, "允许一次")
	h.until(t, "重试完成", func() bool { return !h.a.localRun("console:char00").busy && h.tt.HasText("编造的重试完成") })
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "编造重试后的实际文件" || writer.calls != 2 {
		t.Fatalf("重试未执行或重复：%q %v calls=%d", data, err, writer.calls)
	}
	if h.tt.HasText("编造的提交失败") || h.tt.HasText("允许一次") {
		t.Fatal("已结束审批仍有错误/操作")
	}
}

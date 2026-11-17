package config

import (
	"strings"
	"testing"
)

// 发送流程四个键：不写用默认（2 秒、不翻历史、两个实验开着）；写了读得回来；类型错、越界整份配置不通过并点名键
func TestLinkSendKeys(t *testing.T) {
	c, err := Parse([]byte("linkReader:\n  expand:\n    mode: whitelist\n"), "sumika.yml")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.LinkReader.Expand.Send; got != DefaultLinkSend() || got.EchoWaitMS != 2000 || got.HistoryCheck || !got.ForwardVideo || !got.ForwardQuoteReply {
		t.Fatalf("默认值不对：%+v", got)
	}
	c, err = Parse([]byte("linkReader:\n  expand:\n    send:\n      echoWaitMS: 3500\n      historyCheck: true\n      forwardVideo: false\n      forwardQuoteReply: false\n"), "sumika.yml")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.LinkReader.Expand.Send; got != (LinkSend{EchoWaitMS: 3500, HistoryCheck: true}) {
		t.Fatalf("没读回来：%+v", got)
	}
	for name, body := range map[string]string{
		"字符串": "echoWaitMS: \"2000\"",
		"负数":  "echoWaitMS: -1",
		"太小":  "echoWaitMS: 499",
		"太大":  "echoWaitMS: 10001",
	} {
		_, err := Parse([]byte("linkReader:\n  expand:\n    send:\n      "+body+"\n"), "sumika.yml")
		if err == nil || !strings.Contains(err.Error(), "linkReader.expand.send.") {
			t.Errorf("%s 应被拒绝并点名键，实际 %v", name, err)
		}
	}
	// 开关写成别的类型：YAML 解码这一步就拒（和别的开关键一样，报错里是行列号）
	for _, body := range []string{"historyCheck: 1", "forwardVideo: \"yes\"", "forwardQuoteReply: []"} {
		if _, err := Parse([]byte("linkReader:\n  expand:\n    send:\n      "+body+"\n"), "sumika.yml"); err == nil {
			t.Errorf("%s 应被拒绝", body)
		}
	}
}

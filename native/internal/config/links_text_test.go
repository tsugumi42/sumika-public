package config

import (
	"strings"
	"testing"
)

// 文字排版设置：默认去结尾 hashtag；没写 text 块的旧配置读出来是默认；写 false 能关。
func TestLinkTextDefaultsAndOff(t *testing.T) {
	if !DefaultLinks().Expand.Text.StripTrailingHashtags {
		t.Fatal("默认应去结尾 hashtag")
	}
	c, err := Parse([]byte("linkReader:\n  expand:\n    mode: whitelist\n"), "sumika.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !c.LinkReader.Expand.Text.StripTrailingHashtags {
		t.Fatal("没写 text 块的旧配置应读出默认（开）")
	}
	c, err = Parse([]byte("linkReader:\n  expand:\n    mode: whitelist\n    text:\n      stripTrailingHashtags: false\n"), "sumika.yml")
	if err != nil {
		t.Fatal(err)
	}
	if c.LinkReader.Expand.Text.StripTrailingHashtags {
		t.Fatal("写 false 应关掉")
	}
}

// 直接发的图数上限：默认 3；没写的旧配置读出 3；1～9 以外报配置错，错误带键名。
func TestDirectImageMaxConfig(t *testing.T) {
	if DefaultLinks().Expand.DirectImageMax != 3 {
		t.Fatal("默认应是 3")
	}
	c, err := Parse([]byte("linkReader:\n  expand:\n    mode: whitelist\n"), "sumika.yml")
	if err != nil || c.LinkReader.Expand.DirectImageMax != 3 {
		t.Fatalf("旧配置：%v %d", err, c.LinkReader.Expand.DirectImageMax)
	}
	for n, ok := range map[string]bool{"1": true, "9": true, "0": false, "10": false, "-1": false} {
		_, err := Parse([]byte("linkReader:\n  expand:\n    mode: whitelist\n    directImageMax: "+n+"\n"), "sumika.yml")
		if (err == nil) != ok {
			t.Errorf("directImageMax %s：%v", n, err)
		}
		if err != nil && !strings.Contains(err.Error(), "linkReader.expand.directImageMax") {
			t.Errorf("错误要带键名：%v", err)
		}
	}
}

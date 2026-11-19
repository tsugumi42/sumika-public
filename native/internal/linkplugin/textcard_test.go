package linkplugin

import (
	"testing"

	"sumika-native/internal/config"
)

// tweetCard.textOnly 接到排版上（ADR-399）：默认开，关掉排版就收不到。
func TestBuildOptionsTextCard(t *testing.T) {
	e := config.DefaultLinks().Expand
	if !BuildOptions(e).TextCard {
		t.Fatal("默认配置要把纯文字卡片打开")
	}
	e.TweetCard.TextOnly = false
	if BuildOptions(e).TextCard {
		t.Fatal("配置关了排版不该再开")
	}
}

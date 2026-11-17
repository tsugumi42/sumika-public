package archcheck

import (
	"strings"
	"testing"
)

// 规格-群聊引用和路由 6.1：路由是纯函数，mention 包不依赖存储和模型，也不 import 项目里别的包。
func TestMentionIsPure(t *testing.T) {
	info, ok := moduleInfo(t)["sumika-native/internal/mention"]
	if !ok {
		t.Fatal("没找到 internal/mention：包改名、搬家了就把这里一起改，不然这个测试什么也没查")
	}
	for _, group := range [][]string{info.Imports, info.TestImports, info.XTestImports} {
		for _, imp := range group {
			if strings.HasPrefix(imp, "sumika-native/") {
				t.Errorf("internal/mention 不许 import 项目里的包，但引了 %s", imp)
			}
		}
	}
}

// 规格-群聊引用和路由 第十节第 1 条：悄悄话处理没有任何发 QQ 的出口。whisper 包的整个依赖闭包里没有 QQ 渠道的发送链路
// （channel 中间件、qq 适配器、协议端、发送锁）、群聊调度、链接插件：想挂发送能力就得先让这个测试红。
func TestWhisperHasNoSendPath(t *testing.T) {
	info, ok := moduleInfo(t)["sumika-native/internal/whisper"]
	if !ok {
		t.Fatal("没找到 internal/whisper：包改名、搬家了就把这里一起改，不然这个测试什么也没查")
	}
	banned := []string{
		"sumika-native/internal/channel", "sumika-native/internal/channel/qq", "sumika-native/internal/channel/snowluma", "sumika-native/internal/channel/sendlock",
		"sumika-native/internal/channel/repeat", "sumika-native/internal/localgroup", "sumika-native/internal/linkplugin", "sumika-native/internal/approvalrelay",
		"sumika-native/internal/remind", "sumika-native/internal/proactive",
	}
	for _, dep := range append(append([]string{}, info.Deps...), info.Imports...) {
		for _, b := range banned {
			if dep == b {
				t.Errorf("internal/whisper 的依赖里出现了 %s：悄悄话不能有任何发 QQ、叫醒群聊调度的路径", b)
			}
		}
	}
}

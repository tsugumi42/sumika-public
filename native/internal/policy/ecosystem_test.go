package policy

import (
	"sumika-native/internal/jsjson"
	"testing"
)

var ownerConsole = Audience{Channel: "console", PrivateOk: true}

// 查网默认放行；mcp_call、task 在授权后面按模式走（手动问、自动只放只读、YOLO 放）；
// 不是主人发起的什么都不吃（ADR-269、330）。
func TestEcosystemTiers(t *testing.T) {
	obj := jsjson.Value{Kind: jsjson.Object}
	for _, tool := range []string{"web_search", "web_fetch"} {
		for _, mode := range []string{"manual", "auto", "yolo"} {
			for _, grant := range []string{"", "conversation", "always"} {
				d := Decide(tool, Specs[tool], obj, Env{Mode: mode, Declared: true, Granted: grant, Audience: ownerConsole})
				if d.Kind != "allow" || d.By != ByDefault {
					t.Fatalf("%s/%s/%s 应当默认放行：%+v", tool, mode, grant, d)
				}
			}
		}
	}
	for _, tool := range []string{"mcp_call", "task"} {
		for _, mode := range []string{"manual", "auto", "yolo"} {
			for _, grant := range []string{"", "conversation", "always"} {
				d := Decide(tool, Specs[tool], obj, Env{Mode: mode, Declared: true, Granted: grant, Audience: ownerConsole})
				switch {
				case grant == "conversation" && d.By != ByConversation, grant == "always" && d.By != ByAlways:
					t.Fatalf("%s/%s/%s 授权没生效：%+v", tool, mode, grant, d)
				case grant != "" && d.Kind != "allow":
					t.Fatalf("%s/%s/%s 授权没放行：%+v", tool, mode, grant, d)
				case grant == "" && mode == "yolo" && (d.Kind != "allow" || d.By != ByMode):
					t.Fatalf("%s YOLO 和其他工具一致，不问：%+v", tool, d)
				case grant == "" && mode == "manual" && d.Kind != "ask":
					t.Fatalf("%s 手动要问：%+v", tool, d)
				}
			}
		}
	}
	// mcp_call 没有可读的效果：自动模式也问；task 的 reader / research 只读，自动放行
	if d := Decide("mcp_call", Specs["mcp_call"], obj, Env{Mode: "auto", Declared: true, Audience: ownerConsole}); d.Kind != "ask" {
		t.Fatalf("mcp_call 自动模式要问：%+v", d)
	}
	for agent, want := range map[string]string{"reader": "allow", "research": "allow", "worker": "ask", "": "ask", "x y": "ask"} {
		a := jsjson.Value{Kind: jsjson.Object, Obj: map[string]jsjson.Value{"agent": {Kind: jsjson.String, Str: agent}}}
		if d := Decide("task", Specs["task"], a, Env{Mode: "auto", Declared: true, Audience: ownerConsole}); d.Kind != want {
			t.Fatalf("task/%q 自动模式应 %s：%+v", agent, want, d)
		}
	}
}

// 不是主人发起的（本地群聊、角色接角色）：默认档、授权、模式全不吃，只问一次。
func TestEcosystemNotOwnerNeverAuto(t *testing.T) {
	obj := jsjson.Value{Kind: jsjson.Object}
	for _, tool := range []string{"mcp_call", "web_search", "web_fetch", "task"} {
		for _, mode := range []string{"manual", "auto", "yolo"} {
			for _, grant := range []string{"", "conversation", "always"} {
				d := Decide(tool, Specs[tool], obj, Env{Mode: mode, Declared: true, Granted: grant, Audience: ownerConsole, NotOwner: true})
				if d.Kind != "ask" || d.Limit != "once" {
					t.Fatalf("%s/%s/%s 不是主人发起的也放行了：%+v", tool, mode, grant, d)
				}
			}
		}
	}
}

// 可用场合不放宽：非主人主窗口一律拒绝，包括默认放行的查网。
func TestEcosystemAudienceUnchanged(t *testing.T) {
	for _, tool := range []string{"skill", "mcp_call", "web_search", "web_fetch", "task"} {
		for _, a := range []Audience{{Channel: "qq", PrivateOk: true}, {Channel: "qq", PrivateOk: false}, {Channel: "console", PrivateOk: false}, {}} {
			for _, mode := range []string{"manual", "auto", "yolo"} {
				d := Decide(tool, Specs[tool], jsjson.Value{Kind: jsjson.Object}, Env{Mode: mode, Declared: true, Audience: a})
				if d.Kind != "deny" {
					t.Fatalf("非主人主窗口得到生态能力 %s %s %+v", tool, mode, a)
				}
			}
		}
	}
}

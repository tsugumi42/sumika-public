package approval

// 写文件、命令的范围档（这个对话、记住）：给不给由审批门按同一类、受保护路径、灾难性命令、是不是主人发起来定，
// 桌面卡片照卡上的 Options（ADR-330 之后放开）。这里测卡上的 Options 本身。

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/policy"
)

func scopeOptions(t *testing.T, g *Gate, workdir, conversation string, aud policy.Audience, tool, argsJSON string) (asked bool, options []string) {
	t.Helper()
	args, err := jsjson.Parse(argsJSON)
	if err != nil {
		t.Fatal(err)
	}
	g.Ask = func(_ context.Context, req Request, _ *Pending) Decision {
		asked, options = true, req.Options
		return Decision{Kind: "denied"}
	}
	g.Check(context.Background(), workdir, "suzu", aud, conversation, Call{ID: "c1", Name: tool, Args: args})
	return
}

func TestWriteAndShellScopeOptions(t *testing.T) {
	an := policy.NewAnalyzer()
	defer an.Close()
	if an.Analyze("Get-Date", time.Minute) == nil {
		t.Skip("这台机器起不来拆命令的 pwsh")
	}
	data, work := t.TempDir(), t.TempDir()
	g := NewGateWith(data, an)
	owner := policy.Audience{Channel: "console", PrivateOk: true}
	conv := "console:suzu"
	cases := []struct {
		name, tool, args string
		want             []string
	}{
		{"工作目录里写文件：这个对话和记住", "write", `{"path":` + quote(filepath.Join(work, "a.txt")) + `,"content":"x"}`, []string{"conversation", "always"}},
		{"改文件同理", "edit", `{"path":` + quote(filepath.Join(work, "a.txt")) + `,"edits":[{"oldText":"a","newText":"b"}]}`, []string{"conversation", "always"}},
		{"受保护路径只能一次", "write", `{"path":` + quote(filepath.Join(data, "config", "x.yml")) + `,"content":"x"}`, nil},
		{"命令：git 带子命令", "powershell", `{"command":"git status"}`, []string{"conversation", "always"}},
		{"命令带管道：只给这个对话", "powershell", `{"command":"git status | Select-String a"}`, []string{"conversation"}},
		{"碰受保护路径的命令只能一次", "powershell", `{"command":` + quote("Get-Content "+filepath.Join(data, "config", "sumika.yml")) + `}`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g.Mode = "manual"
			asked, got := scopeOptions(t, g, work, conv, owner, c.tool, c.args)
			if !asked {
				t.Fatal("手动档应当问")
			}
			if !reflect.DeepEqual(got, c.want) && !(len(got) == 0 && len(c.want) == 0) {
				t.Fatalf("Options = %v，想要 %v", got, c.want)
			}
		})
	}
	t.Run("不是主人发起的只问一次、不给范围", func(t *testing.T) {
		for _, tool := range []string{"write", "powershell"} {
			args := `{"path":` + quote(filepath.Join(work, "b.txt")) + `,"content":"x"}`
			if tool == "powershell" {
				args = `{"command":"git status"}`
			}
			args2, _ := jsjson.Parse(args)
			var got []string
			asked := false
			g.Ask = func(_ context.Context, req Request, _ *Pending) Decision {
				asked, got = true, req.Options
				return Decision{Kind: "denied"}
			}
			id := kernel.Invocation{Scope: kernel.Scope{Scene: "group", Conversation: "console:suzu:group:g"}}
			g.Check(context.Background(), work, "suzu", owner, "console:suzu:group:g", Call{Identity: id, ID: "c2", Name: tool, Args: args2})
			if len(got) != 0 {
				t.Fatalf("%s：群聊给了范围档 %v（问过=%v）", tool, got, asked)
			}
		}
	})
}

func quote(s string) string { b, _ := json.Marshal(s); return string(b) }

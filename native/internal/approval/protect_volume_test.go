package approval

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/policy"
	"sumika-native/internal/testutil/volpath"
)

// 备份目录写成卷标识写法（\\?\Volume{GUID}\x）：并进受保护目录的是它解出来的真实路径（protectBackupDir 的做法）。
// 本地私聊里的工具读：真实路径下的文件要问；卷标识写法下的同一个文件也要问（不透明路径一律问，不因为写法特殊就放行）
func TestVolumeGUIDBackupDirToolReadAsks(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	for _, key := range []string{"HOME", "USERPROFILE"} {
		t.Setenv(key, home)
	}
	base := t.TempDir()
	if r, err := RealStrict(base); err == nil {
		base = r
	}
	wd := filepath.Join(base, "work")
	backupDir := filepath.Join(base, "卷写法备份")
	for _, d := range []string{wd, home, filepath.Join(base, "data"), filepath.Join(backupDir, "每天-1", "data", "config")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	guidDir, err := volpath.GUIDPath(backupDir)
	if err != nil {
		t.Skipf("拼不出卷标识写法：%v", err)
	}
	an := policy.NewAnalyzer()
	defer an.Close()
	scope := kernel.Scope{CharacterID: "suzu", Conversation: "console:suzu", ScopeID: "console", Platform: "local", Channel: "console", Scene: "private", ActorKind: "owner", ActorID: "owner"}
	aud := policy.Audience{Channel: "console", PrivateOk: true}
	read := func(g *Gate, file string) (blocked bool, asked int) {
		g.Mode = "auto"
		g.Ask = func(_ context.Context, req Request, _ *Pending) Decision {
			asked++
			return Decision{Kind: "cancelled"}
		}
		id := kernel.Invocation{Scope: scope, InputID: "in", RootID: "root", RunID: "run", StepID: "step", ToolID: "tool", Generation: 1, Kind: kernel.KindForeground}
		args, err := jsjson.Parse(`{"path":` + jsonQuote(file) + `}`)
		if err != nil {
			t.Fatal(err)
		}
		blocked, _ = g.Check(context.Background(), wd, "suzu", aud, "console:suzu", Call{Identity: id, ID: "call-1", Name: "read", Args: args})
		return blocked, asked
	}
	plainFile := filepath.Join(backupDir, "每天-1", "data", "config", "mcp.json")
	guidFile := filepath.Join(guidDir, "每天-1", "data", "config", "mcp.json")

	// 前提：没并进去时读真实路径直接放行（这就是要堵的口子）
	before := NewGateWith(filepath.Join(base, "data"), an)
	if blocked, asked := read(before, plainFile); blocked || asked != 0 {
		t.Fatalf("前提不成立：没并进去时读真实路径该直接放行：blocked=%v asked=%d", blocked, asked)
	}
	after := NewGateWith(filepath.Join(base, "data"), an)
	after.Protect(guidDir, backupDir) // 卷标识写法的原样 + 解出来的真实路径
	if blocked, asked := read(after, plainFile); !blocked || asked != 1 {
		t.Errorf("并进真实路径以后，读真实路径该问一次：blocked=%v asked=%d", blocked, asked)
	}
	if blocked, asked := read(after, guidFile); !blocked || asked != 1 {
		t.Errorf("读卷标识写法下的同一个文件也该问一次：blocked=%v asked=%d", blocked, asked)
	}
}

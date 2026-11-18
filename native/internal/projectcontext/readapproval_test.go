package projectcontext

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/testutil/volpath"
)

// dirs 把几个目录包成 ReadApproval 要的“取清单的函数”（固定清单）
func dirs(d ...string) func() []string { return func() []string { return d } }

// ReadApproval 每次读都现取受保护目录清单：创建之后才并进去的目录（备份目录）也算数；清单是 nil 函数时没有受保护目录
func TestReadApprovalReadsProtectedDirsEachTime(t *testing.T) {
	root, backupDir := t.TempDir(), t.TempDir()
	file := put(t, backupDir, "每天-1/data/config/sumika.yml", "明文配置")
	var protected []string
	approve := ReadApproval(func() []string { return protected })
	if err := approve(context.Background(), file); err != nil {
		t.Fatalf("还没并进去时不该拒绝：%v", err)
	}
	protected = append(protected, root, backupDir) // 装配时晚一步并进去的
	err := approve(context.Background(), file)
	if err == nil || !strings.Contains(err.Error(), "审批未通过") {
		t.Fatalf("并进去以后该拒读备份目录里的文件：%v", err)
	}
	if err := approve(context.Background(), put(t, t.TempDir(), "AGENTS.md", "别处的")); err != nil {
		t.Fatalf("别处的文件不受影响：%v", err)
	}
	if err := ReadApproval(nil)(context.Background(), file); err != nil {
		t.Fatalf("没有清单就没有受保护目录：%v", err)
	}
}

// 受保护目录是网络共享：不去解析它的真实路径（解析会真的连过去，每读一个项目说明多等一次网络超时）；普通目录照旧解析。
// 本机的设备路径（\\.\D:\x、\\?\Volume{GUID}\x）不是网络共享：照样解析，不然解不成普通路径就比不上受保护目录
func TestReadApprovalDoesNotResolveNetworkShares(t *testing.T) {
	var resolved []string
	old := realStrict
	realStrict = func(p string) (string, error) { resolved = append(resolved, p); return old(p) }
	t.Cleanup(func() { realStrict = old })

	local := t.TempDir()
	file := put(t, t.TempDir(), "AGENTS.md", "x")
	network := []string{`\\10.255.255.1\share\备份`, `//host/share/x`, `\\?\UNC\host\share\x`, `\\.\UNC\host\share`}
	if err := ReadApproval(dirs(network...))(context.Background(), file); err != nil {
		t.Fatalf("不相干的文件不该被拒绝：%v", err)
	}
	if len(resolved) != 0 {
		t.Fatalf("网络共享不该被解析：%v", resolved)
	}
	if err := ReadApproval(dirs(append(network, local)...))(context.Background(), file); err != nil {
		t.Fatalf("不相干的文件不该被拒绝：%v", err)
	}
	if len(resolved) != 1 || resolved[0] != local {
		t.Fatalf("只该解析普通目录 %s：%v", local, resolved)
	}
	if err := ReadApproval(dirs(append(network, local)...))(context.Background(), filepath.Join(local, "AGENTS.md")); err == nil {
		t.Fatal("普通受保护目录里的文件该照旧拒绝")
	}
	// 设备路径要解析（不管解不解得开：这里只数调用）
	resolved = nil
	_ = ReadApproval(dirs(`\\.\D:\备份`, `\\?\Volume{00000000-0000-0000-0000-000000000000}\备份`))(context.Background(), file)
	if len(resolved) != 2 {
		t.Fatalf("设备路径该照样解析：%v", resolved)
	}
}

// 卷标识写法（\\?\Volume{GUID}\…）的受保护目录：读它下面的 AGENTS.md 要被拒（解析成普通路径再比）。
// 以前解析得开、会拒；只因“不透明路径”一律跳过解析就放行了，这条钉住不能放宽
func TestReadApprovalRefusesVolumeGUIDProtectedDir(t *testing.T) {
	root := t.TempDir()
	guidDir, err := volpath.GUIDPath(root)
	if err != nil {
		t.Skipf("拼不出卷标识写法：%v", err)
	}
	file := put(t, root, "AGENTS.md", "受保护目录里的说明")
	err = ReadApproval(dirs(guidDir))(context.Background(), file)
	if err == nil || !strings.Contains(err.Error(), "审批未通过") {
		t.Fatalf("卷标识写法的受保护目录该按真实路径拒读 %s：%v", file, err)
	}
	if err := ReadApproval(dirs(guidDir))(context.Background(), put(t, t.TempDir(), "AGENTS.md", "别处")); err != nil {
		t.Fatalf("别处的文件不受影响：%v", err)
	}
}

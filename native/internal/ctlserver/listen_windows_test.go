package ctlserver

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"sumika-native/internal/ctlproto"
)

// ───────────── 验收 2：权限 ─────────────

// 安全描述只有当前用户一个 SID：断言字符串
func TestSDDLIsCurrentUserOnly(t *testing.T) {
	sid, err := ctlproto.CurrentSID()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sid, "S-1-5-") {
		t.Fatalf("当前用户 SID 的样子不对：%q", sid)
	}
	want := "D:P(A;;GA;;;" + sid + ")"
	if got := SDDL(sid); got != want {
		t.Fatalf("SDDL = %q，应是 %q", got, want)
	}
	// 只有一条允许项，没有 Everyone（WD）、已认证用户（AU）、匿名（AN）、管理员（BA）、SYSTEM（SY）、Users（BU）
	for _, bad := range []string{";;;WD", ";;;AU", ";;;AN", ";;;BA", ";;;SY", ";;;BU", ";;;IU", ";;;NU"} {
		if strings.Contains(SDDL(sid), bad) {
			t.Errorf("SDDL 里不该有 %s：%s", bad, SDDL(sid))
		}
	}
	if n := strings.Count(SDDL(sid), "(A;"); n != 1 {
		t.Errorf("应该只有一条允许项，有 %d 条", n)
	}
}

// 真管道上的访问控制表和 SDDL(当前用户) 一致：只有一条允许项、就是当前用户、没有继承。
// 用 READ_CONTROL 打开管道读它的安全描述（Windows 会把 GA 展开成这类管道的具体权限，所以不比字符串，比结构）
func TestLivePipeDACLIsCurrentUserOnly(t *testing.T) {
	f := &fakeBackend{}
	name, _ := start(t, f, Options{})
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	var h windows.Handle
	for i := 0; i < 200; i++ { // 服务端刚起，下一个管道实例可能还没建好：等它（winio.DialPipe 也是这样重试）
		h, err = windows.CreateFile(p, windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
		if err != windows.ERROR_PIPE_BUSY {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("打开管道读安全描述：%v", err)
	}
	defer windows.CloseHandle(h)
	sd, err := windows.GetSecurityInfo(h, windows.SE_KERNEL_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("读安全描述：%v", err)
	}
	ctl, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	if ctl&windows.SE_DACL_PROTECTED == 0 {
		t.Errorf("DACL 没有设成保护（会继承别处的权限）：control=%#x", ctl)
	}
	sdd := sd.String()
	t.Logf("管道的 DACL：%s", sdd)
	sid, _ := ctlproto.CurrentSID()
	// 当前用户若是众所周知的账号（比如 CI 机器上的内置管理员，RID 500），SDDL 会印成缩写 LA 而不是 S-1-5-…：
	// 让系统自己把这个 SID 印一遍，拿它印出来的写法比
	ref, err := windows.SecurityDescriptorFromString("D:(A;;FA;;;" + sid + ")")
	if err != nil {
		t.Fatal(err)
	}
	printed := regexp.MustCompile(`;;;([^)]+)\)`).FindStringSubmatch(ref.String())
	if printed == nil {
		t.Fatalf("参照的安全描述读不出 SID：%s", ref.String())
	}
	if !regexp.MustCompile(`^D:P?(AR)?\(A;;[A-Z0-9x]+;;;` + regexp.QuoteMeta(printed[1]) + `\)$`).MatchString(sdd) {
		t.Errorf("管道的 DACL 不是「只有当前用户一条允许项」：%s", sdd)
	}
}

// 拒绝远程客户端：这个标志是 go-winio 建服务端管道时固定带上的（pipe.go 的 makeServerPipeHandle），
// 我们的代码没有地方能设它，所以这里钉住：go-winio 的版本是我们看过的这个，并且它的源码里还在带这个标志。
// 升级 go-winio 时这个测试会红：先看新版本的 pipe.go 里有没有 FILE_PIPE_REJECT_REMOTE_CLIENTS，再改下面两个常量
const (
	pinnedWinioVersion = "v0.6.4"
	winioRejectRemote  = "typ := uint32(windows.FILE_PIPE_REJECT_REMOTE_CLIENTS)"
)

func TestPipeRejectsRemoteClientsPinnedToWinioVersion(t *testing.T) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Version}}\n{{.Dir}}", "github.com/Microsoft/go-winio").Output()
	if err != nil {
		t.Fatalf("找不到 go-winio：%v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 {
		t.Fatalf("go list 的输出不对：%q", out)
	}
	if v := strings.TrimSpace(lines[0]); v != pinnedWinioVersion {
		t.Fatalf("go-winio 是 %s，这个测试钉的是 %s：先确认新版本的 pipe.go 里服务端管道还带拒绝远程客户端的标志，再改 pinnedWinioVersion", v, pinnedWinioVersion)
	}
	src, err := os.ReadFile(filepath.Join(strings.TrimSpace(lines[1]), "pipe.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), winioRejectRemote) {
		t.Fatalf("go-winio 的 pipe.go 里找不到 %q：服务端管道可能不再拒绝远程客户端", winioRejectRemote)
	}
}

// 管道名已经被占着（别的进程抢先用了这个名字）：开不出来，不去接手它
func TestListenFailsWhenNameIsTaken(t *testing.T) {
	needWindows(t)
	name := testPipe(t)
	first, err := ListenPipe(name)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := ListenPipe(name)
	if err == nil {
		second.Close()
		t.Fatal("同一个管道名开了两遍：名字被占时应该报错")
	}
}

// 管道名：带当前用户 SID，演示的另起一条
func TestPipeNames(t *testing.T) {
	sid, err := ctlproto.CurrentSID()
	if err != nil {
		t.Fatal(err)
	}
	real, err := ctlproto.PipeName(false)
	if err != nil {
		t.Fatal(err)
	}
	demo, _ := ctlproto.PipeName(true)
	if real != `\\.\pipe\sumika-ctl-`+sid || demo != real+"-demo" {
		t.Fatalf("管道名不对：%q %q", real, demo)
	}
}

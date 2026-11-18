package backup

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/paths"
)

func newPaths(t *testing.T) (paths.Paths, string) {
	t.Helper()
	base := t.TempDir()
	p, err := paths.Init(filepath.Join(base, "data"))
	if err != nil {
		t.Fatal(err)
	}
	return p, base
}

func TestResolveDirDefaultIsSibling(t *testing.T) {
	p, base := newPaths(t)
	got, err := ResolveDir(p, "", false)
	if err != nil || got != filepath.Join(base, "Sumika-备份") {
		t.Fatalf("空配置该落在数据目录旁边：%q %v", got, err)
	}
}

func TestResolveDirUsesConfiguredDir(t *testing.T) {
	p, _ := newPaths(t)
	elsewhere := filepath.Join(t.TempDir(), "我的备份")
	got, err := ResolveDir(p, elsewhere, false)
	if err != nil || got != elsewhere {
		t.Fatalf("配置了就用配置的：%q %v", got, err)
	}
}

func TestResolveDirRelativeResolvesBesideData(t *testing.T) {
	p, base := newPaths(t)
	got, err := ResolveDir(p, "bk", false)
	if err != nil || got != filepath.Join(base, "bk") {
		t.Fatalf("相对路径按数据目录的上一级解：%q %v", got, err)
	}
}

// SUMIKA_DATA_DIR 指向临时数据目录时一律放在它旁边，不管配置写的是哪（免得临时数据混进主人的备份）
func TestResolveDirTempDataSibling(t *testing.T) {
	p, base := newPaths(t)
	configured := filepath.Join(t.TempDir(), "主人真实的备份")
	got, err := ResolveDir(p, configured, true)
	if err != nil || got != filepath.Join(base, "Sumika-备份") {
		t.Fatalf("临时数据目录该一律落在旁边，实际 %q %v", got, err)
	}
}

// 备份绝不能放进数据目录里面
func TestResolveDirRefusesInsideData(t *testing.T) {
	p, _ := newPaths(t)
	for name, dir := range map[string]string{
		"直接在里面":     filepath.Join(p.Root, "backups-new"),
		"更深一层（还没建）": filepath.Join(p.Root, "a", "b", "c"),
		"就是数据目录":    p.Root,
		"带 .. 绕回来":  filepath.Join(p.Root, "..", "data", "x"),
		"相对路径指进去":   filepath.Join("data", "x"),
	} {
		_, err := ResolveDir(p, dir, false)
		if !errors.Is(err, ErrDirInsideData) {
			t.Errorf("%s：该拒绝，实际 %v", name, err)
		}
	}
	if strings.ToUpper(p.Root) != p.Root { // 大小写骗法只在不分大小写的系统上有意义
		if _, err := ResolveDir(p, strings.ToUpper(filepath.Join(p.Root, "x")), false); err == nil && isWindows() {
			t.Error("Windows 上改大小写不该骗得过去")
		}
	}
}

// 联接、符号链接指进数据目录，也算在里面
func TestResolveDirInsideDataViaLink(t *testing.T) {
	p, base := newPaths(t)
	link := filepath.Join(base, "看起来在外面")
	makeLink(t, p.Root, link)
	if _, err := ResolveDir(p, filepath.Join(link, "bk"), false); !errors.Is(err, ErrDirInsideData) {
		t.Fatalf("经链接指进数据目录该拒绝：%v", err)
	}
}

// 备份目录是盘符根、数据目录的上级（含经链接指到上级）：拒绝（ErrDirTooBroad）。它们并进审批门的受保护目录会让整个盘 / 整个项目变成每次问；
// 数据目录旁边的兄弟目录、更深一层的子目录照常可以
func TestResolveDirRefusesTooBroad(t *testing.T) {
	p, base := newPaths(t)
	root := filepath.VolumeName(base) + string(filepath.Separator)
	if root == string(filepath.Separator) {
		root = "/"
	}
	for name, dir := range map[string]string{
		"盘符根":      root,
		"数据目录的上级":  base,
		"上上级":      filepath.Dir(base),
		"带 .. 的上级": filepath.Join(p.Root, ".."),
	} {
		if _, err := ResolveDir(p, dir, false); !errors.Is(err, ErrDirTooBroad) {
			t.Errorf("%s（%s）：该拒绝为太宽，实际 %v", name, dir, err)
		}
	}
	link := filepath.Join(t.TempDir(), "指向上级的联接")
	makeLink(t, base, link)
	if _, err := ResolveDir(p, link, false); !errors.Is(err, ErrDirTooBroad) {
		t.Errorf("经链接指到上级也该拒绝：%v", err)
	}
	for name, dir := range map[string]string{
		"兄弟目录":       filepath.Join(base, "Sumika-备份"),
		"不存在的兄弟目录":   filepath.Join(base, "别的", "备份"),
		"别的盘上专门的文件夹": filepath.Join(t.TempDir(), "备份"),
	} {
		if _, err := ResolveDir(p, dir, false); err != nil {
			t.Errorf("%s：不该拒绝：%v", name, err)
		}
	}
	// 临时数据目录的规则（一律放旁边）不受影响：旁边那个目录本来就不会太宽
	if _, err := ResolveDir(p, base, true); err != nil {
		t.Errorf("临时数据目录一律放旁边：%v", err)
	}
}

// checkReal 只比字符串：另一块盘的盘符根（和数据目录不在同一块盘，不是它的上级）也要拒绝；同一块盘上的上级、兄弟都照常判
func TestCheckRealRootOfAnotherDrive(t *testing.T) {
	if !isWindows() {
		t.Skip("不同盘符是 Windows 的情况")
	}
	data := `D:\work\app\data`
	for name, tc := range map[string]struct {
		dir  string
		want error
	}{
		"另一块盘的根":    {`E:\`, ErrDirTooBroad},
		"本盘的根":      {`D:\`, ErrDirTooBroad},
		"数据目录的上级":   {`D:\work\app`, ErrDirTooBroad},
		"另一块盘的子文件夹": {`E:\app-备份`, nil},
		"兄弟目录":      {`D:\work\app\备份`, nil},
		"数据目录里面":    {`D:\work\app\data\备份`, ErrDirInsideData},
		"就是数据目录":    {data, ErrDirInsideData},
		"大小写不同的上级":  {`d:\work`, ErrDirTooBroad},
	} {
		got := checkReal(tc.dir, tc.dir, data)
		if !errors.Is(got, tc.want) || (tc.want == nil && got != nil) {
			t.Errorf("%s（%s）：想要 %v 得到 %v", name, tc.dir, tc.want, got)
		}
	}
}

// 配置里写成 \\?\D:\x、\\.\D:\x 这类设备路径前缀：ResolveDir 还原成普通写法，返回的目录不带前缀
func TestResolveDirStripsDevicePrefix(t *testing.T) {
	if !isWindows() {
		t.Skip("设备路径前缀是 Windows 的写法")
	}
	p, _ := newPaths(t)
	plain := filepath.Join(t.TempDir(), "我的备份")
	for _, prefix := range []string{`\\?\`, `\\.\`} {
		got, err := ResolveDir(p, prefix+plain, false)
		if err != nil || got != plain {
			t.Errorf("%s 前缀：想要 %q 得到 %q %v", prefix, plain, got, err)
		}
	}
	// 前缀写法指进数据目录 / 指到上级也照样拒绝（不因为写法不同就绕过）
	if _, err := ResolveDir(p, `\\?\`+filepath.Join(p.Root, "x"), false); !errors.Is(err, ErrDirInsideData) {
		t.Errorf("前缀写法指进数据目录该拒绝：%v", err)
	}
	if _, err := ResolveDir(p, `\\?\`+filepath.Dir(p.Root), false); !errors.Is(err, ErrDirTooBroad) {
		t.Errorf("前缀写法指到上级该拒绝：%v", err)
	}
}

func TestResolveDirDataRootIsLink(t *testing.T) {
	// 数据目录本身是联接（允许，和 paths.Open 一样）：备份目录落在联接的真实位置里面也要拒绝
	realData := t.TempDir()
	if _, err := paths.Init(realData); err != nil {
		t.Fatal(err)
	}
	holder := t.TempDir()
	link := filepath.Join(holder, "data")
	makeLink(t, realData, link)
	p, err := paths.Open(link)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveDir(p, filepath.Join(realData, "bk"), false); !errors.Is(err, ErrDirInsideData) {
		t.Fatalf("经真实路径指进数据目录该拒绝：%v", err)
	}
}

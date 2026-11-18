package main

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// testPNG 造一张 n×n 的纯色 PNG，颜色按边长变，两张图不会长得一样
func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, w, h))
	c := color.NRGBA{R: uint8(w), G: uint8(h), B: 0x80, A: 0xff}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			im.SetNRGBA(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func testImages(t *testing.T, sizes ...int) []iconImage {
	t.Helper()
	var out []iconImage
	for _, n := range sizes {
		im, err := newIconImage(testPNG(t, n, n))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, im)
	}
	return out
}

// resEntry 是资源目录走到底的一项：类型、编号、语言和数据
type resEntry struct {
	typ, id, lang uint32
	data          []byte
}

// walkResources 按资源目录的格式（三层：类型、编号、语言）读出所有数据项。
// base 是节的相对虚拟地址：链接好的 exe 里数据项存的是 RVA，减去节的地址才是节内偏移；.syso 里存的就是节内偏移，base 传 0。
// 这是对着 PE 文档写的读法，不和 buildIconSyso 共用任何代码
func walkResources(t *testing.T, sec []byte, base uint32) []resEntry {
	t.Helper()
	le := binary.LittleEndian
	var out []resEntry
	var walk func(off uint32, level int, path [3]uint32)
	walk = func(off uint32, level int, path [3]uint32) {
		if int(off)+16 > len(sec) {
			t.Fatalf("资源目录 %d 超出节的范围 %d", off, len(sec))
		}
		n := int(le.Uint16(sec[off+12:])) + int(le.Uint16(sec[off+14:]))
		for i := 0; i < n; i++ {
			e := off + 16 + uint32(i)*8
			name, to := le.Uint32(sec[e:]), le.Uint32(sec[e+4:])
			p := path
			p[level] = name
			if to&0x80000000 != 0 {
				if level == 2 {
					t.Fatalf("语言层下面还有子目录")
				}
				walk(to&0x7fffffff, level+1, p)
				continue
			}
			if level != 2 {
				t.Fatalf("第 %d 层就到了数据项", level)
			}
			rva, size := le.Uint32(sec[to:]), le.Uint32(sec[to+4:])
			out = append(out, resEntry{p[0], p[1], p[2], sec[rva-base : rva-base+size]})
		}
	}
	walk(0, 0, [3]uint32{})
	return out
}

func checkIconEntries(t *testing.T, got []resEntry, images []iconImage) {
	t.Helper()
	sorted := slices.Clone(images)
	slices.SortFunc(sorted, func(a, b iconImage) int { return a.size - b.size })
	var groups []resEntry
	icons := map[uint32][]byte{}
	for _, e := range got {
		if e.lang != langEnUS {
			t.Errorf("类型 %d 编号 %d 的语言是 %#x，要 %#x", e.typ, e.id, e.lang, langEnUS)
		}
		switch e.typ {
		case rtIcon:
			icons[e.id] = e.data
		case rtGroupIcon:
			groups = append(groups, e)
		default:
			t.Errorf("多出一类资源 %d（只该有图标）", e.typ)
		}
	}
	if len(groups) != 1 || groups[0].id != 1 {
		t.Fatalf("图标组应该正好一项且编号是 1（mygo 的窗口类用 LoadIcon(实例, 1)），得到 %+v", groups)
	}
	if len(icons) != len(sorted) {
		t.Fatalf("RT_ICON 有 %d 项，要 %d", len(icons), len(sorted))
	}
	g := groups[0].data
	le := binary.LittleEndian
	if le.Uint16(g[0:]) != 0 || le.Uint16(g[2:]) != 1 || int(le.Uint16(g[4:])) != len(sorted) {
		t.Fatalf("图标组头不对：% x", g[:6])
	}
	if len(g) != 6+14*len(sorted) {
		t.Fatalf("图标组长 %d，要 %d", len(g), 6+14*len(sorted))
	}
	for i, im := range sorted {
		e := g[6+14*i:]
		want := byte(im.size)
		if im.size == 256 {
			want = 0
		}
		if e[0] != want || e[1] != want {
			t.Errorf("第 %d 项宽高字节是 %d×%d，边长 %d 要 %d", i, e[0], e[1], im.size, want)
		}
		if le.Uint16(e[4:]) != 1 || le.Uint16(e[6:]) != 32 {
			t.Errorf("第 %d 项 planes、位数不是 1、32", i)
		}
		if int(le.Uint32(e[8:])) != len(im.png) {
			t.Errorf("第 %d 项字节数 %d，要 %d", i, le.Uint32(e[8:]), len(im.png))
		}
		id := uint32(le.Uint16(e[12:]))
		if !bytes.Equal(icons[id], im.png) {
			t.Errorf("第 %d 项指向的 RT_ICON %d 不是 %d×%d 那张 PNG", i, id, im.size, im.size)
		}
	}
}

// .syso 本身（目标文件）：能被 debug/pe 读，只有一个 .rsrc 节，每个数据项一条重定位，机器类型对
func TestSysoIsCoffObjectWithIcons(t *testing.T) {
	images := testImages(t, 48, 16, 256, 32, 24)
	for goarch, machine := range map[string]uint16{"amd64": pe.IMAGE_FILE_MACHINE_AMD64, "arm64": pe.IMAGE_FILE_MACHINE_ARM64, "386": pe.IMAGE_FILE_MACHINE_I386} {
		syso, err := buildIconSyso(images, goarch)
		if err != nil {
			t.Fatal(err)
		}
		f, err := pe.NewFile(bytes.NewReader(syso))
		if err != nil {
			t.Fatalf("%s：debug/pe 读不了：%v", goarch, err)
		}
		if f.Machine != machine || len(f.Sections) != 1 || f.Sections[0].Name != ".rsrc" {
			t.Fatalf("%s：机器 %#x，节 %d 个（%v）", goarch, f.Machine, len(f.Sections), f.Sections[0].Name)
		}
		sec, err := f.Sections[0].Data()
		if err != nil {
			t.Fatal(err)
		}
		got := walkResources(t, sec, 0)
		checkIconEntries(t, got, images)
		if int(f.Sections[0].NumberOfRelocations) != len(got) {
			t.Errorf("%s：重定位 %d 条，数据项 %d 个", goarch, f.Sections[0].NumberOfRelocations, len(got))
		}
		again, _ := buildIconSyso(slices.Clone(images), goarch)
		if !bytes.Equal(syso, again) {
			t.Errorf("%s：同样的输入两次生成的字节不一样", goarch)
		}
	}
}

func TestSysoRejectsBadInput(t *testing.T) {
	if _, err := buildIconSyso(nil, "amd64"); err == nil {
		t.Error("没有图标应该报错")
	}
	if _, err := buildIconSyso(testImages(t, 16), "mips"); err == nil {
		t.Error("不认识的 GOARCH 应该报错")
	}
	if _, err := buildIconSyso(testImages(t, 16, 32, 16), "amd64"); err == nil {
		t.Error("有两张同样边长的图应该报错")
	}
	for name, data := range map[string][]byte{
		"不是 PNG":  []byte("not a png"),
		"不是正方形":   testPNG(t, 16, 32),
		"超过 256":  testPNG(t, 257, 257),
		"空":       nil,
		"截断的 PNG": testPNG(t, 16, 16)[:20],
	} {
		if _, err := newIconImage(data); err == nil {
			t.Errorf("%s：应该报错", name)
		}
	}
}

// 真链接一遍：造一个只有 main 的小程序，放上 .syso，用 go build 交叉编译成 Windows 的 exe（任何系统上都能做），
// 再从 exe 的 .rsrc 里把图标读出来对。普通的 go build 能把它链进去，是整个办法成立的前提
func TestSysoLinksIntoExeWithGoBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("要现编一个 exe，放在每日整套")
	}
	images := testImages(t, 16, 24, 32, 48, 256)
	syso, err := buildIconSyso(images, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, content := range map[string]string{"go.mod": "module probe\n\ngo 1.27\n", "main.go": "package main\n\nfunc main() {}\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "icon_windows_amd64.syso"), syso, 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "probe.exe")
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w -H windowsgui", "-o", exe, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0", "GOFLAGS=", "GOTOOLCHAIN=local")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build 失败：%v\n%s", err, out)
	}
	f, err := pe.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var rsrc *pe.Section
	for _, s := range f.Sections {
		if s.Name == ".rsrc" {
			rsrc = s
		}
	}
	if rsrc == nil {
		t.Fatal("exe 里没有 .rsrc 节：.syso 没被链接进去")
	}
	sec, err := rsrc.Data()
	if err != nil {
		t.Fatal(err)
	}
	got := walkResources(t, sec, rsrc.VirtualAddress)
	checkIconEntries(t, got, images)
	for _, e := range got {
		if e.typ != rtIcon && e.typ != rtGroupIcon {
			t.Errorf("exe 里多出资源类型 %d", e.typ)
		}
	}
}

// 仓库里提交的两个 .syso 是不是用提交的那批图标 PNG 生成的：改了图标没重新生成，这里就红
func TestCommittedSysoMatchesIconPNGs(t *testing.T) {
	iconDir := filepath.Join("..", "..", "assets", "icon")
	names, err := filepath.Glob(filepath.Join(iconDir, "icon-*.png"))
	if err != nil || len(names) == 0 {
		t.Fatalf("找不到 %s 下的图标 PNG：%v", iconDir, err)
	}
	var images []iconImage
	for _, p := range names {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		im, err := newIconImage(data)
		if err != nil {
			t.Fatalf("%s：%v", p, err)
		}
		// 文件名里的数字要和图的边长一致，不然改名或换错图了没人发现
		if want := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), "icon-"), ".png"); want != strconv.Itoa(im.size) {
			t.Errorf("%s 的边长是 %d", filepath.Base(p), im.size)
		}
		images = append(images, im)
	}
	for _, goarch := range []string{"amd64", "arm64"} {
		want, err := buildIconSyso(images, goarch)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join("..", "..", "rsrc_windows_"+goarch+".syso")
		have, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读不到提交的 %s：%v", path, err)
		}
		if !bytes.Equal(have, want) {
			t.Errorf("%s 和 assets/icon 里的 PNG 对不上：重新生成（步骤见 docs/assets/logo/README.md）", path)
		}
	}
}

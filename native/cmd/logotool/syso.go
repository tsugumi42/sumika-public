package main

// 把几张 PNG 打成只含图标的 Windows 资源（.syso，一个 COFF 目标文件）：Go 链接器会把包目录里 GOOS、GOARCH 对得上的
// .syso 链进 exe，普通的 go build 就带上了 exe 文件本身的图标（资源管理器、固定到开始菜单和任务栏看到的），
// 窗口类的图标也取自资源 1（mygo 的 registerClasses 用 LoadIconW(实例, 1)）。
//
// COFF 和资源目录的布局照 mygo cmd/mygo/winres.go 的 winresSyso 写的，只留图标。那段代码 Copyright (c) 2026 MyGo contributors，
// MIT 许可，全文在同目录的 LICENSE-mygo。故意不带 mygo build 会写的另外两样：
//   - 版本信息里的 MyGoIdentifier：mygo 靠它认"这个程序是 mygo build 打的包"（internal/windows/app.go 的 Package），
//     有了它 App.IsPackaged 变真，开机自启的注册表项名、单实例的名字都从应用名换成标识符，和现在装过、勾过的对不上；
//   - 清单（UTF-8 代码页、公共控件 6、DPI 声明）：现在的 exe 没有清单，DPI 由 mygo 启动时调接口设置，加了会改变进程的行为。
// 两样都不要，图标之外 exe 的行为和 go build 出来的一模一样。

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image/png"
	"os"
	"slices"
)

const (
	rtIcon      = 3
	rtGroupIcon = 14
	langEnUS    = 0x0409 // 和 mygo 的默认一样；图标按语言找不到时系统会退到别的语言的那一份
)

// iconImage 是要打进资源的一张图：边长和 PNG 原文（Windows Vista 起图标可以直接存 PNG）
type iconImage struct {
	size int
	png  []byte
}

// coffArch 是 GOARCH → COFF 的机器类型和"相对虚拟地址"重定位的类型
var coffArch = map[string]struct{ machine, relocation uint16 }{
	"amd64": {0x8664, 0x0003}, // IMAGE_REL_AMD64_ADDR32NB
	"arm64": {0xAA64, 0x0002}, // IMAGE_REL_ARM64_ADDR32NB
	"386":   {0x014C, 0x0007}, // IMAGE_REL_I386_DIR32NB
}

func cmdSyso(args []string) error {
	fs := flag.NewFlagSet("syso", flag.ContinueOnError)
	out := fs.String("out", "", "输出的 .syso 文件")
	arch := fs.String("arch", "amd64", "GOARCH：amd64、arm64、386")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" || fs.NArg() == 0 {
		return fmt.Errorf("syso 需要 -out 和至少一张 PNG")
	}
	var images []iconImage
	for _, p := range fs.Args() {
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		im, err := newIconImage(data)
		if err != nil {
			return fmt.Errorf("%s：%w", p, err)
		}
		images = append(images, im)
	}
	syso, err := buildIconSyso(images, *arch)
	if err != nil {
		return err
	}
	return os.WriteFile(*out, syso, 0o644)
}

// newIconImage 检查 PNG 是正方形、边长 1～256（图标目录里边长只有一个字节，256 记成 0）
func newIconImage(data []byte) (iconImage, error) {
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return iconImage{}, fmt.Errorf("不是 PNG：%w", err)
	}
	if cfg.Width != cfg.Height {
		return iconImage{}, fmt.Errorf("图标要是正方形，这张是 %d×%d", cfg.Width, cfg.Height)
	}
	if cfg.Width < 1 || cfg.Width > 256 {
		return iconImage{}, fmt.Errorf("图标边长要在 1～256，这张是 %d", cfg.Width)
	}
	return iconImage{size: cfg.Width, png: data}, nil
}

// buildIconSyso 生成只含图标的 .syso：RT_ICON 一张图一项（编号从 1 起，按边长从小到大），RT_GROUP_ICON 一项（编号 1）。
// 同样的输入出同样的字节（时间戳写 0）
func buildIconSyso(images []iconImage, goarch string) ([]byte, error) {
	arch, ok := coffArch[goarch]
	if !ok {
		return nil, fmt.Errorf("不支持的 GOARCH %q", goarch)
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("没有图标")
	}
	images = slices.Clone(images)
	slices.SortFunc(images, func(a, b iconImage) int { return a.size - b.size })
	for i := 1; i < len(images); i++ {
		if images[i].size == images[i-1].size {
			return nil, fmt.Errorf("有两张 %d×%d 的图", images[i].size, images[i].size)
		}
	}

	le := binary.LittleEndian
	res := map[uint16]map[uint16][]byte{rtIcon: {}}
	var group bytes.Buffer
	_ = binary.Write(&group, le, [3]uint16{0, 1, uint16(len(images))}) // 保留、类型 1 = 图标、张数
	for i, im := range images {
		id := uint16(i + 1)
		res[rtIcon][id] = im.png
		dim := byte(im.size)
		if im.size >= 256 {
			dim = 0
		}
		group.Write([]byte{dim, dim, 0, 0}) // 宽、高、调色板数、保留
		_ = binary.Write(&group, le, [2]uint16{1, 32})
		_ = binary.Write(&group, le, uint32(len(im.png)))
		_ = binary.Write(&group, le, id)
	}
	res[rtGroupIcon] = map[uint16][]byte{1: group.Bytes()}

	// 资源节：三层目录（类型、编号、语言），然后是数据项，最后是数据。目录项用节内偏移指向子目录；
	// 数据项里的地址是相对虚拟地址，由链接器按重定位算
	types := sortedKeys(res)
	dirSize := func(n int) int { return 16 + 8*n }
	size := dirSize(len(types))
	count := 0
	for _, t := range types {
		size += dirSize(len(res[t])) + len(res[t])*dirSize(1)
		count += len(res[t])
	}
	entriesAt := size
	dataAt := entriesAt + 16*count

	raw := make([]byte, dataAt)
	dir := func(at, n int) { le.PutUint16(raw[at+14:], uint16(n)) } // NumberOfIdEntries
	var relocs []uint32
	var data bytes.Buffer
	dir(0, len(types))
	next := dirSize(len(types))
	entry := entriesAt
	for i, t := range types {
		le.PutUint32(raw[16+8*i:], uint32(t))
		le.PutUint32(raw[16+8*i+4:], uint32(next)|0x80000000)
		ids := sortedKeys(res[t])
		idDir := next
		dir(idDir, len(ids))
		next += dirSize(len(ids))
		for j, id := range ids {
			le.PutUint32(raw[idDir+16+8*j:], uint32(id))
			le.PutUint32(raw[idDir+16+8*j+4:], uint32(next)|0x80000000)
			dir(next, 1)
			le.PutUint32(raw[next+16:], langEnUS)
			le.PutUint32(raw[next+20:], uint32(entry))
			next += dirSize(1)

			for data.Len()%8 != 0 {
				data.WriteByte(0)
			}
			le.PutUint32(raw[entry:], uint32(dataAt+data.Len())) // OffsetToData：节内偏移，链接器加上节的地址
			le.PutUint32(raw[entry+4:], uint32(len(res[t][id])))
			relocs = append(relocs, uint32(entry))
			entry += 16
			data.Write(res[t][id])
		}
	}
	raw = append(raw, data.Bytes()...)
	for len(raw)%4 != 0 {
		raw = append(raw, 0)
	}

	var out bytes.Buffer
	const headerSize, sectionHeaderSize = 20, 40
	rawAt := headerSize + sectionHeaderSize
	relocAt := rawAt + len(raw)
	symbolsAt := relocAt + 10*len(relocs)
	_ = binary.Write(&out, le, struct {
		Machine, Sections                   uint16
		Time, Symbols, SymbolCount          uint32
		OptionalHeaderSize, Characteristics uint16
	}{arch.machine, 1, 0, uint32(symbolsAt), 1, 0, 0})
	_ = binary.Write(&out, le, struct {
		Name                                        [8]byte
		VirtualSize, VirtualAddress, RawSize, RawAt uint32
		RelocAt, LineNumbersAt                      uint32
		Relocs, LineNumbers                         uint16
		Characteristics                             uint32
	}{[8]byte{'.', 'r', 's', 'r', 'c'}, 0, 0, uint32(len(raw)), uint32(rawAt), uint32(relocAt), 0, uint16(len(relocs)), 0, 0x40000040})
	out.Write(raw)
	for _, r := range relocs {
		_ = binary.Write(&out, le, r)         // VirtualAddress：要改的地方在节内的偏移
		_ = binary.Write(&out, le, uint32(0)) // 符号 0：这个节自己
		_ = binary.Write(&out, le, arch.relocation)
	}
	// 一个节符号，然后是只有长度字段的空字符串表
	out.Write([]byte{'.', 'r', 's', 'r', 'c', 0, 0, 0})
	_ = binary.Write(&out, le, uint32(0)) // Value
	_ = binary.Write(&out, le, uint16(1)) // SectionNumber
	_ = binary.Write(&out, le, uint16(0)) // Type
	out.Write([]byte{3, 0})               // IMAGE_SYM_CLASS_STATIC，没有附加符号
	_ = binary.Write(&out, le, uint32(4))
	return out.Bytes(), nil
}

func sortedKeys[V any](m map[uint16]V) []uint16 {
	keys := make([]uint16, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

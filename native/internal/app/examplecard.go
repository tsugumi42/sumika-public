package app

// P2-1：全新目录、配置里没有 QQ 小号时，从资源根复制一份示例卡（第 1 步复核：不然全新安装只报“一个角色都没有”）。
// 只在这次启动刚初始化、而且还一张卡都没有时动手；源文件必须是普通文件、大小有上限；先写进数据目录根下的
// 临时目录（不能放进 characters/，那会带 character.yml 的目录被当成角色），全部写完再改名成
// characters/example/card，中途失败不留半张卡。绝不覆盖已有文件。

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"sumika-native/internal/paths"
)

// exampleCardFiles 是资源根示例卡里的两个文件（characters/example/）
var exampleCardFiles = []string{"character.yml", "character.md"}

// exampleCardMaxSize 是示例卡每个文件的大小上限：示例卡是两张小文本，超过就说明源不对，不复制
const exampleCardMaxSize = 1 << 20

// exampleCardWriteFile 是写临时文件的出口，测试注入写失败用；生产就是 os.WriteFile
var exampleCardWriteFile = os.WriteFile

// EnsureExampleCharacter 在 copy 为真时，把资源根的示例卡复制到 characters/example/card。
// 调用方只在“这次启动刚初始化、而且配置里没有 QQ 小号”时传 true：有小号的机器，QQ 小号绑的角色要主人自己放卡。
// 已经有一张卡、目标目录已存在时什么都不做；源文件不是普通文件、读不了或超过上限时报错，不复制。
func EnsureExampleCharacter(p paths.Paths, opt Options, copy bool) error {
	if !copy {
		return nil
	}
	ids, err := characterIDsIn(p.CharactersDir())
	if err != nil {
		return fmt.Errorf("看角色卡：%w", err)
	}
	if len(ids) > 0 {
		return nil
	}
	location, err := ResourceRoot(opt.Root)
	if err != nil {
		return err
	}
	srcDir := filepath.Join(location.Dir, "characters", "example")
	contents := make([][]byte, len(exampleCardFiles))
	for i, name := range exampleCardFiles {
		b, err := readExampleCard(filepath.Join(srcDir, name))
		if err != nil {
			return fmt.Errorf("数据目录里还没有角色卡：资源根里没有可用的示例卡（%s）：%w；把角色卡放进 %s 再打开",
				srcDir, err, filepath.Join(p.CharactersDir(), "<名字>", "card"))
		}
		contents[i] = b
	}
	charsDir := p.CharactersDir()
	if err := os.MkdirAll(charsDir, 0o755); err != nil {
		return fmt.Errorf("建角色目录 %s：%w", charsDir, err)
	}
	parent := filepath.Join(charsDir, "example")
	dstDir := filepath.Join(parent, "card")
	if _, err := os.Lstat(dstDir); err == nil {
		return nil // 已经有这张卡（哪怕是半张）：不覆盖
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("看示例卡目标 %s：%w", dstDir, err)
	}
	tmp, err := os.MkdirTemp(filepath.Dir(charsDir), ".example-tmp-")
	if err != nil {
		return fmt.Errorf("建示例卡临时目录：%w", err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(tmp)
		}
	}()
	for i, name := range exampleCardFiles {
		if err := exampleCardWriteFile(filepath.Join(tmp, name), contents[i], 0o644); err != nil {
			return fmt.Errorf("写示例卡 %s：%w", name, err)
		}
	}
	_, statErr := os.Lstat(parent)
	parentExisted := statErr == nil
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("建示例卡目录 %s：%w", parent, err)
	}
	if err := os.Rename(tmp, dstDir); err != nil {
		if !parentExisted {
			_ = os.Remove(parent) // 只在空的时候成功：不留一个没有卡的角色目录
		}
		return fmt.Errorf("把示例卡放到 %s：%w", dstDir, err)
	}
	keep = true
	// MkdirTemp 建的是 0700；改名以后按普通角色目录的权限补一次
	if err := os.Chmod(dstDir, 0o755); err != nil {
		return fmt.Errorf("改示例卡目录权限 %s：%w", dstDir, err)
	}
	return nil
}

// readExampleCard 读一个源文件：符号链接、目录、超过上限都拒绝
func readExampleCard(file string) ([]byte, error) {
	info, err := os.Lstat(file)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s 不是普通文件（链接、目录都不复制）", file)
	}
	if info.Size() > exampleCardMaxSize {
		return nil, fmt.Errorf("%s 有 %d 字节，超过示例卡上限 %d 字节", file, info.Size(), exampleCardMaxSize)
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// 打开以后再确认一次：Lstat 之后被换成链接/目录的情况不读
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > exampleCardMaxSize {
		return nil, fmt.Errorf("%s 在读取时不是普通文件或超过示例卡上限 %d 字节", file, exampleCardMaxSize)
	}
	b, err := io.ReadAll(io.LimitReader(f, exampleCardMaxSize+1))
	if err != nil {
		return nil, err
	}
	if len(b) > exampleCardMaxSize {
		return nil, fmt.Errorf("%s 超过示例卡上限 %d 字节", file, exampleCardMaxSize)
	}
	return b, nil
}

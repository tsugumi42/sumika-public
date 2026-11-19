package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sumika-native/internal/fileatomic"
)

type Mode string

const (
	Archive Mode = "archive"
	Receive Mode = "receive"
)

var ErrArguments = errors.New("启动参数错误")

func ParseMode(value string) (Mode, error) {
	switch Mode(value) {
	case "", Archive:
		return Archive, nil
	case Receive:
		return Receive, nil
	default:
		return "", fmt.Errorf("%w：-mode 只允许 archive 或 receive", ErrArguments)
	}
}

// RootLocation 保留启动时的选择，设置保存后也不在当前进程切换数据。
type RootLocation struct {
	Dir    string
	Source string
	File   string
}

// ResourceRoot 只供桌面默认入口使用；命令行工具仍从当前目录找根。
func ResourceRoot(root string) (RootLocation, error) {
	if root != "" {
		return ResourceRootFrom(root, "")
	}
	exe, err := os.Executable()
	if err != nil {
		return RootLocation{}, fmt.Errorf("定位程序资源根：%w", err)
	}
	return ResourceRootFrom(root, filepath.Dir(exe))
}

// ResourceRootFrom 注入 exe 目录，避免测试依赖工作目录和真实安装位置。
func ResourceRootFrom(root, exeDir string) (RootLocation, error) {
	if root != "" {
		dir, err := filepath.Abs(root)
		return RootLocation{Dir: dir, Source: "命令行 -root"}, err
	}
	file := filepath.Join(exeDir, "sumika.root")
	loc := RootLocation{File: file, Source: "sumika.root"}
	b, err := os.ReadFile(file)
	if err == nil {
		loc.Dir, err = parseRootFile(file, exeDir, string(b))
		return loc, err
	}
	if !errors.Is(err, os.ErrNotExist) {
		return loc, fmt.Errorf("%s：%w", file, err)
	}
	for dir := exeDir; ; dir = filepath.Dir(dir) {
		info, err := os.Stat(filepath.Join(dir, "sumika.example.yml"))
		if err == nil && !info.IsDir() {
			return RootLocation{Dir: dir, Source: "往上找", File: file}, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return loc, err
		}
		if filepath.Dir(dir) == dir {
			return RootLocation{Dir: exeDir, Source: "exe 旁边", File: file}, nil
		}
	}
}

func parseRootFile(file, exeDir, text string) (string, error) {
	text = strings.TrimPrefix(text, "\ufeff")
	path, line := "", 0
	lineNumber := 0
	for raw := range strings.Lines(text) {
		lineNumber++
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if line != 0 {
			return "", fmt.Errorf("%s 第 %d 行：只允许一行非空路径（首行在第 %d 行）", file, lineNumber, line)
		}
		path, line = value, lineNumber
	}
	if line == 0 {
		return exeDir, nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(exeDir, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("%s 第 %d 行：%w", file, line, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s 第 %d 行：%s 不是目录", file, line, path)
	}
	info, err = os.Stat(filepath.Join(path, "sumika.example.yml"))
	if err != nil {
		return "", fmt.Errorf("%s 第 %d 行：%w", file, line, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s 第 %d 行：sumika.example.yml 不是文件", file, line)
	}
	return filepath.Clean(path), nil
}

// WriteRootFile 与读取共用输入边界；同目录写完、关闭后改名，失败保留原文件。
func WriteRootFile(exeDir, text string) error {
	file := filepath.Join(exeDir, "sumika.root")
	if _, err := parseRootFile(file, exeDir, text); err != nil {
		return err
	}
	text = strings.TrimSpace(strings.TrimPrefix(text, "\ufeff"))
	if text != "" {
		text += "\n"
	}
	tmp, err := os.CreateTemp(exeDir, "sumika.root.tmp-*")
	if err != nil {
		return fmt.Errorf("写 %s：%w", file, err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	_, err = tmp.WriteString(text)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = fileatomic.Replace(name, file, 2*time.Second)
	}
	if err != nil {
		return fmt.Errorf("写 %s：%w", file, err)
	}
	return nil
}

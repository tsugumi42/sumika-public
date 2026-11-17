package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"sumika-native/internal/config"
)

// 实跑入口明确写主工作区的开发输出；单元测试仍只用 t.TempDir。
func stressOutput(t *testing.T) string {
	t.Helper()
	root, err := config.FindRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "data", "dev", "stress", time.Now().Format("20060102-150405.000"))
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Logf("实跑输出：%s", dir)
	return dir
}

func TestStressRun(t *testing.T) {
	if os.Getenv("SUMIKA_STRESS_RUN") != "1" {
		t.Skip("设置 SUMIKA_STRESS_RUN=1 连续执行两遍三档各三次")
	}
	dir := stressOutput(t)
	binary := filepath.Join(dir, "stress")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 22*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, ".")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("构建失败：%v\n%s", err, b)
	}
	// 单次矩阵失败也继续第二遍；每遍独立进程，保留该次退出状态和全部输出。
	for repeat := 1; repeat <= 2; repeat++ {
		out := filepath.Join(dir, fmt.Sprintf("matrix-%d", repeat))
		if err := os.MkdirAll(out, 0755); err != nil {
			t.Fatal(err)
		}
		log, err := os.Create(filepath.Join(out, "console.log"))
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		cmd := exec.CommandContext(ctx, binary, "-diagnose-handles", "-out", out)
		cmd.Stdout = io.MultiWriter(log, &b)
		cmd.Stderr = cmd.Stdout
		err = cmd.Run()
		log.Close()
		code := -1
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		}
		if e := writeJSON(filepath.Join(out, "exit.json"), struct {
			Code  int
			Error string
		}{code, fmt.Sprint(err)}); e != nil {
			t.Fatal(e)
		}
		t.Logf("第 %d 遍命令输出：\n%s", repeat, b.String())
		if err != nil {
			t.Errorf("第 %d 遍未过线：%v；证据：%s", repeat, err, out)
		}
	}
}

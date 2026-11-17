// memory-migrate 给真实数据目录里每个角色的记忆库（memory.db；memory-v2.db 是向量索引，不归这里管）跑写入侧迁移。
// 记忆库不在启动时自动迁移（migrate.go 的 ErrNeedsMigration），新迁移进来以后用这个补上：
// 先 sumika-ctl quit、备份 data/go/characters，再跑；Sumika 还开着（数据目录锁被占）就直接退出，什么都不改。
// 每个库走升级护栏（规格-库版本与升级护栏 第 8 条）：迁移前拍快照、失败换回；一个失败接着做下一个，最后列出失败的角色。
//
//	go run ./cmd/memory-migrate -data <仓库>\data
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sumika-native/internal/datalock"
	"sumika-native/internal/memory"
	"sumika-native/internal/paths"
)

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr))
}

// runCLI 解析参数并跑迁移；退出码 2 是用法、1 是失败、0 是全部迁好。
func runCLI(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("memory-migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	data := fs.String("data", "", "数据目录（含 go/characters）")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *data == "" {
		fmt.Fprintln(stderr, "用法：memory-migrate -data <数据目录>")
		return 2
	}
	if err := run(context.Background(), *data, stdout); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func run(ctx context.Context, data string, out io.Writer) error {
	// 会写每个角色的 memory.db：Bun 管本地私聊时它也在写记忆（remember、memory_forget），所以 local.lock 也要拿到，拿不到就拒绝
	lock, err := datalock.LockExistingForWrite(data)
	switch {
	case errors.Is(err, datalock.ErrDataInUse):
		return errors.New("Sumika 还开着，先 sumika-ctl quit 再跑")
	case errors.Is(err, datalock.ErrLocalInUse):
		return errors.New("本地私聊的 Bun 后台还开着（占着 go/local.lock），先关掉它再跑")
	case err != nil:
		return fmt.Errorf("锁数据目录：%w", err)
	}
	defer lock.Close()
	files, err := filepath.Glob(filepath.Join(data, "go", "characters", "*", "memory.db"))
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("%s 下没有角色的 memory.db", filepath.Join(data, "go", "characters"))
	}
	sort.Strings(files)
	snapshotDir := paths.Paths{Root: data}.UpgradeSnapshotDir()
	var failures []string
	for _, f := range files {
		char := filepath.Base(filepath.Dir(f))
		rel, err := filepath.Rel(data, f)
		if err != nil {
			rel = filepath.Base(f)
		}
		if _, err := memory.MigrateGuarded(ctx, f, snapshotDir, filepath.ToSlash(rel)); err != nil {
			failures = append(failures, fmt.Sprintf("%s：%v", char, err))
			continue
		}
		if err := memory.CheckSchema(ctx, f); err != nil {
			failures = append(failures, fmt.Sprintf("%s：迁移后检查：%v", char, err))
			continue
		}
		fmt.Fprintf(out, "迁好了：%s\n", char)
	}
	if len(failures) > 0 {
		return fmt.Errorf("%d 个角色的记忆库没迁成：\n%s", len(failures), strings.Join(failures, "\n"))
	}
	return nil
}

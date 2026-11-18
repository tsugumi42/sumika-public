package main

// -work 目录的使用：默认位置、标记文件、上次崩溃遗留的副本清理。
//
//	<work>/.memory-shadow   标记文件：这是本工具建的目录（清理只认带它的）
//	<work>/.keep            -keep 的运行结束时记下：这份副本是特意留给人看的，以后的运行不当崩溃遗留去清
//	<work>/copy/            副本：一个迷你的数据目录（go/chat、go/trace.db、go/characters/<id>/…），默认跑完就删，-keep 才留
//	<work>/out/             报告：report.md、summary.json、marks.csv，不删
//
// 副本目录里 go/backend.lock 由本次运行持到结束，所以"这个副本还有没有人在用"可以直接问锁，不靠 PID 猜进程活没活着

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"sumika-native/internal/datalock"
)

const (
	markerName = ".memory-shadow"
	keepName   = ".keep"
)

// lstat 是 cleanStale 看 .keep 在不在用的那一次查询。包变量只为测试：Windows 上造不出"不是不存在的错"（权限、网络盘），
// 读不清 .keep 时不清副本这个分支要靠换一个会出错的来走到
var lstat = os.Lstat

// defaultWorkParent 是默认的 -work 的父目录，崩溃遗留的清理也只管它下面
func defaultWorkParent(data string) string { return filepath.Join(data, "dev", "memory-shadow") }

// defaultWork：<父目录>/<时间>；同一秒里跑第二次（或者目录已经在了）就加 -2、-3，不去撞已有的
func defaultWork(data string, now time.Time) string {
	base := filepath.Join(defaultWorkParent(data), now.Format("20060102-150405"))
	p := base
	for i := 2; ; i++ {
		if _, err := os.Lstat(p); err != nil {
			return p
		}
		p = fmt.Sprintf("%s-%d", base, i)
	}
}

// staleMarkerAge：副本里还没有锁文件（刚建好、还没来得及拿锁）的目录，标记比这个新就当它还在启动，不清
const staleMarkerAge = time.Hour

type marker struct {
	Started string `json:"started"`
	Tool    string `json:"tool"`
}

// prepareWork 过 -work 的位置守卫，要求目录是空的（或不存在），写标记文件，返回绝对路径
func prepareWork(data, work string, ignored ignoreCheck, now time.Time) (string, error) {
	abs, err := checkWorkDir(data, work, ignored)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return "", err
	}
	if len(entries) > 0 {
		return "", fmt.Errorf("-work（%s）不是空目录：不往已有东西的目录里写，换一个或先清掉", abs)
	}
	b, _ := json.Marshal(marker{Started: now.Format(time.RFC3339), Tool: "memory-shadow"})
	if err := os.WriteFile(filepath.Join(abs, markerName), b, 0o600); err != nil {
		return "", err
	}
	return abs, nil
}

// markKeep 在 -keep 的运行结束时记下 .keep。要赶在放锁之前写：-keep 留下的副本正好是"带标记、锁没人持"的形状，
// 和崩溃遗留一模一样，先放锁再记，中间被另一次运行的 cleanStale 撞上就会被当遗留删掉
func markKeep(work string) error {
	return os.WriteFile(filepath.Join(work, keepName), []byte("-keep 留下的副本，以后的运行不清；用完请删 copy/\n"), 0o600)
}

// cleanStale 清掉默认父目录下、带标记、没人在用的副本（只删 copy/，out/ 里的报告留着）。-keep 留下的（有 .keep）不清，记一行日志。
// 返回清了几个
func cleanStale(data string, now time.Time, logf func(string, ...any)) int {
	parent := defaultWorkParent(data)
	entries, err := os.ReadDir(parent)
	if err != nil {
		return 0
	}
	removed := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		run := filepath.Join(parent, e.Name())
		mfi, err := os.Stat(filepath.Join(run, markerName))
		if err != nil {
			continue // 不是本工具建的
		}
		cp := filepath.Join(run, "copy")
		if _, err := os.Stat(cp); err != nil {
			continue
		}
		// 看不清有没有 .keep（不是"不存在"的错）也不清：删了拿不回来，留着只多占点盘
		if _, err := lstat(filepath.Join(run, keepName)); err == nil {
			logf("跳过 -keep 留下的副本 %s", e.Name())
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			logf("读不了 %s 的 .keep（%v），当它是 -keep 留下的，不清", e.Name(), err)
			continue
		}
		lock, err := datalock.LockExisting(cp)
		switch {
		case errors.Is(err, datalock.ErrDataInUse), errors.Is(err, datalock.ErrLocalInUse):
			continue // 另一次运行还在用
		case err != nil:
			// 没有锁文件：刚建好还没拿锁，或者建到一半崩了。标记够旧才清
			if now.Sub(mfi.ModTime()) < staleMarkerAge {
				continue
			}
		default:
			lock.Close()
		}
		if err := os.RemoveAll(cp); err != nil {
			logf("清理上次遗留的副本 %s 失败：%v", e.Name(), err)
			continue
		}
		logf("清掉上次遗留的副本 %s", e.Name())
		removed++
	}
	return removed
}

// findRoot 从 start 往上找项目根（有 prompts/summarize-v2.md 的那一层）
func findRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "prompts", summaryPromptName+".md")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("从 %s 往上找不到 prompts/%s.md：用 -root 指项目根", start, summaryPromptName)
		}
		dir = parent
	}
}

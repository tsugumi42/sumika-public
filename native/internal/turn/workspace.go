package turn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Workspace 是宿主交给一轮的值快照；工具不能改变它。零值表示无工作区。
type Workspace struct {
	ID       string
	CWD      string
	Root     string
	Revision uint64
}

// NewWorkspace 每轮只探测一次根，不沿 .git 文件去找另一个 checkout。
func NewWorkspace(id, cwd string) (Workspace, error) {
	if !filepath.IsAbs(cwd) {
		return Workspace{}, fmt.Errorf("工作目录必须是绝对路径：%s", cwd)
	}
	cwd = filepath.Clean(cwd)
	w := Workspace{ID: id, CWD: cwd, Root: cwd}
	for dir := cwd; ; dir = filepath.Dir(dir) {
		_, err := os.Stat(filepath.Join(dir, ".git"))
		if err == nil {
			w.Root = dir
			return w, nil
		}
		if !os.IsNotExist(err) {
			return Workspace{}, fmt.Errorf("探测工作区根失败：%w", err)
		}
		if filepath.Dir(dir) == dir {
			return w, nil
		}
	}
}

// dirCheckTimeout 是看目录能不能用的时限；网络路径没响应时报"没有响应"，不算不存在。
// listDirNames 单独成变量，测试用它模拟卡住的路径，不靠真的等。
var (
	dirCheckTimeout = 3 * time.Second
	listDirNames    = func(dir string) error {
		f, err := os.Open(dir)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.Readdirnames(1)
		if err == io.EOF {
			return nil
		}
		return err
	}
)

// CheckDir 看一个会话要用的目录现在能不能用：绝对路径、存在、是文件夹、列得出内容。
// 写权限不查（只读的项目也能看）。出错信息带路径，界面原样显示；调用方不许因此退到别的目录。
func CheckDir(dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("工作目录必须是绝对路径：%s", dir)
	}
	done := make(chan error, 1)
	list := listDirNames // 在这里取一次：卡住的那个协程可能在调用返回后才跑到这一行，测试会在那之后换掉包级变量
	go func() {
		fi, err := os.Stat(dir)
		switch {
		case err != nil:
			done <- err
		case !fi.IsDir():
			done <- errNotDir
		default:
			done <- list(dir)
		}
	}()
	t := time.NewTimer(dirCheckTimeout)
	defer t.Stop()
	select {
	case err := <-done:
		switch {
		case err == nil:
			return nil
		case errors.Is(err, errNotDir):
			return fmt.Errorf("工作目录 %s 不是文件夹", dir)
		case errors.Is(err, os.ErrNotExist):
			return fmt.Errorf("工作目录 %s 不存在（被删除或移动了）", dir)
		case errors.Is(err, os.ErrPermission):
			return fmt.Errorf("工作目录 %s 没有权限读取", dir)
		}
		return fmt.Errorf("工作目录 %s 用不了：%w", dir, err)
	case <-t.C:
		return fmt.Errorf("工作目录 %s 没有响应", dir)
	}
}

var errNotDir = errors.New("不是文件夹")

// freezeWorkspace 在每轮开头把工作区冻成值快照。主人本地私聊没有显式目录时，问 cfg.WorkspaceFor
// 这个会话绑了哪个目录：绑定的目录用不了就这一轮不跑，不退到全局默认；没绑定才用 cfg.Workdir。
// 已经带目录的（在跑的实例、子任务继承）不再问，所以改目录从下一轮生效，在跑的一轮不受影响。
func (in Input) freezeWorkspace(ctx context.Context, cfg Config) (Input, error) {
	if !in.local() {
		in.Workspace = Workspace{}
		return in, nil
	}
	if in.Workspace.CWD == "" && cfg.WorkspaceFor != nil && ownerConsoleAudience(in.Audience) {
		cwd, err := cfg.WorkspaceFor(ctx, in.Conversation)
		if err != nil {
			return in, err
		}
		if cwd != "" {
			if err := CheckDir(cwd); err != nil {
				return in, err
			}
			in.Workspace.CWD = cwd
		}
	}
	cwd, id := in.Workspace.CWD, in.Workspace.ID
	if cwd == "" {
		cwd = cfg.Workdir
	}
	if id == "" {
		id = in.Conversation
	}
	w, err := NewWorkspace(id, cwd)
	w.Revision = in.Workspace.Revision
	in.Workspace = w
	return in, err
}

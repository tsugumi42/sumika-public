package app

// 按会话选工作目录（规格-按会话选工作目录）。绑定存在 local 库的 local_workspaces；
// 内核每轮冻结时通过 turn.Config.WorkspaceFor 来这里问，所以这里是"这个会话用哪个目录"的唯一来源。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"sumika-native/internal/api"
	"sumika-native/internal/slash"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

// recentWorkspaceLimit 是选目录菜单里最近目录的个数
const recentWorkspaceLimit = 8

// workspaceStore 取这个会话所在平台的库；本地私聊才有目录，别的会话一律拒绝（不回路径）。
func (a *App) workspaceStore(conv string) (*store.Store, error) {
	if !slashEligible(api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: conv}) {
		return nil, store.ErrDisplayScope
	}
	return a.set.ForConversation(conv)
}

// workspaceFor 是 turn.Config.WorkspaceFor：返回这个会话绑定的目录，没绑定返回空串（用全局默认）。
// 绑定存着但读不了、联接改了指向，都返回错误，这一轮不跑；不退到别的目录。
func (a *App) workspaceFor(ctx context.Context, conv string) (string, error) {
	st, err := a.workspaceStore(conv)
	if err != nil {
		return "", err
	}
	w, bound, err := st.LocalWorkspaceOf(ctx, conv)
	if err != nil {
		return "", fmt.Errorf("读不到这个会话的工作目录设置：%w", err)
	}
	if !bound {
		return "", nil
	}
	if err := turn.CheckDir(w.CWD); err != nil {
		return "", err
	}
	if real, err := realDir(w.CWD); err != nil || !strings.EqualFold(real, w.RealPath) {
		return "", fmt.Errorf("工作目录 %s 现在指向别处了（选的时候是 %s），请重新选一次", w.CWD, w.RealPath)
	}
	return w.CWD, nil
}

// realDir 是目录的真实路径（解开联接和符号链接）。
func realDir(dir string) (string, error) {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	return filepath.Clean(real), nil
}

// normalizeWorkspaceDir 把主人输入的路径整理成绝对、干净的写法；不合格报错。
// 资源管理器复制来的带引号路径去引号，~ 展开成用户目录；相对路径、C:foo、\foo 一律拒绝，不按进程目录猜。
func normalizeWorkspaceDir(raw string) (string, error) {
	dir := strings.TrimSpace(raw)
	dir = strings.TrimSpace(strings.Trim(dir, "\"'“”"))
	if dir == "" {
		return "", errors.New("没有写目录")
	}
	if !utf8.ValidString(dir) || strings.ContainsFunc(dir, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", errors.New("目录里有不能用的字符")
	}
	if dir == "~" || strings.HasPrefix(dir, "~/") || strings.HasPrefix(dir, "~"+string(filepath.Separator)) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("找不到用户目录：%w", err)
		}
		dir = filepath.Join(home, dir[1:])
	}
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("要写完整路径（带盘符）：%s", dir)
	}
	return filepath.Clean(dir), nil
}

// setWorkspace 绑定一个会话的目录；校验在这里做，库只存。
func (a *App) setWorkspace(ctx context.Context, conv, raw string) (api.WorkspaceInfo, error) {
	st, err := a.workspaceStore(conv)
	if err != nil {
		return api.WorkspaceInfo{}, err
	}
	dir, err := normalizeWorkspaceDir(raw)
	if err != nil {
		return api.WorkspaceInfo{}, err
	}
	if err := turn.CheckDir(dir); err != nil {
		return api.WorkspaceInfo{}, err
	}
	real, err := realDir(dir)
	if err != nil {
		return api.WorkspaceInfo{}, fmt.Errorf("工作目录 %s 用不了：%w", dir, err)
	}
	ws, err := turn.NewWorkspace(conv, dir)
	if err != nil {
		return api.WorkspaceInfo{}, err
	}
	if err := st.SetLocalWorkspace(ctx, store.LocalWorkspace{Conversation: conv, CWD: dir, Root: ws.Root, RealPath: real, UpdatedAt: a.now().UnixMilli()}); err != nil {
		return api.WorkspaceInfo{}, err
	}
	a.statusChanged(conv)
	return a.workspaceInfo(ctx, conv)
}

func (a *App) clearWorkspace(ctx context.Context, conv string) (api.WorkspaceInfo, error) {
	st, err := a.workspaceStore(conv)
	if err != nil {
		return api.WorkspaceInfo{}, err
	}
	if _, err := st.ClearLocalWorkspace(ctx, conv); err != nil {
		return api.WorkspaceInfo{}, err
	}
	a.statusChanged(conv)
	return a.workspaceInfo(ctx, conv)
}

// workspaceInfo 是这个会话现在用的目录和它好不好用。读文件系统，界面只在换会话、改完后读一次。
func (a *App) workspaceInfo(ctx context.Context, conv string) (api.WorkspaceInfo, error) {
	st, err := a.workspaceStore(conv)
	if err != nil {
		return api.WorkspaceInfo{}, err
	}
	w, bound, err := st.LocalWorkspaceOf(ctx, conv)
	if err != nil {
		return api.WorkspaceInfo{}, err
	}
	info := api.WorkspaceInfo{CWD: a.workdir(), Bound: bound}
	if bound {
		info.CWD, info.Root = w.CWD, w.Root
	} else if ws, err := turn.NewWorkspace(conv, info.CWD); err == nil {
		info.Root = ws.Root
	} else {
		info.Root = info.CWD
	}
	info.Name = filepath.Base(info.Root)
	if info.Name == "" || info.Name == "." || info.Name == string(filepath.Separator) {
		info.Name = info.Root
	}
	// 绑定的目录和全局默认都照实标出问题；全局默认坏了不拦聊天，只是药丸标红
	if _, err := a.workspaceFor(ctx, conv); err != nil {
		info.Problem = err.Error()
	} else if !bound {
		if err := turn.CheckDir(info.CWD); err != nil {
			info.Problem = err.Error()
		}
	}
	return info, nil
}

// slashCD 是 /cd：看、换、恢复这个会话的工作目录。只有主人在本地私聊输入框能用（slashEligible），她说的话不过命令表。
func (a *App) slashCD(ctx context.Context, c slashCall) (slashResult, error) {
	arg := strings.TrimSpace(c.Inv.Arg)
	switch strings.ToLower(arg) {
	case "":
		info, err := a.workspaceInfo(ctx, c.Conv)
		if err != nil {
			return slashResult{}, err
		}
		where := "全局默认"
		if info.Bound {
			where = "这个会话选的"
		}
		reply := "工作目录（" + where + "）：" + info.CWD
		if info.Problem != "" {
			reply += "。现在用不了：" + info.Problem
		}
		return slashResult{Reply: reply}, nil
	case "默认", "default":
		info, err := a.clearWorkspace(ctx, c.Conv)
		if err != nil {
			return slashResult{}, err
		}
		return slashResult{Reply: "已恢复全局默认目录：" + info.CWD + "。下一轮起生效"}, nil
	}
	info, err := a.setWorkspace(ctx, c.Conv, arg)
	if err != nil {
		return slashResult{}, err
	}
	return slashResult{Reply: "工作目录改成 " + info.CWD + "。下一轮起生效，这一轮还在原来的目录"}, nil
}

var cdSlashCommand = slash.Command{Name: "cd", Summary: "换这个会话的工作目录（/cd 默认 恢复，/cd 单独看当前）"}

// precheckWorkspace 在发送前、落库前查一次这个会话的目录：用不了就把原因回给输入框，草稿不丢，这句话也不进库。
// 冻结时还会再查一次（排队、自动续轮、重启恢复走那里）。
func (a *App) precheckWorkspace(ctx context.Context, conv string) error {
	_, err := a.workspaceFor(ctx, conv)
	return err
}

// ---- DesktopReader：api.LocalWorkspaces ----

func (r *DesktopReader) ReadLocalWorkspace(ctx context.Context, k api.ConversationKey) (api.WorkspaceInfo, error) {
	if _, err := r.localCharacter(ctx, k); err != nil {
		return api.WorkspaceInfo{}, err
	}
	return r.desk.app.workspaceInfo(ctx, k.Conversation)
}

func (r *DesktopReader) SetLocalWorkspace(ctx context.Context, k api.ConversationKey, dir string) (api.WorkspaceInfo, error) {
	if err := r.bunOwned(); err != nil {
		return api.WorkspaceInfo{}, err
	}
	_, done, err := r.desk.enterChat()
	if err != nil {
		return api.WorkspaceInfo{}, err
	}
	defer done()
	if _, err := r.localCharacter(ctx, k); err != nil {
		return api.WorkspaceInfo{}, err
	}
	return r.desk.app.setWorkspace(ctx, k.Conversation, dir)
}

func (r *DesktopReader) ClearLocalWorkspace(ctx context.Context, k api.ConversationKey) (api.WorkspaceInfo, error) {
	if err := r.bunOwned(); err != nil {
		return api.WorkspaceInfo{}, err
	}
	_, done, err := r.desk.enterChat()
	if err != nil {
		return api.WorkspaceInfo{}, err
	}
	defer done()
	if _, err := r.localCharacter(ctx, k); err != nil {
		return api.WorkspaceInfo{}, err
	}
	return r.desk.app.clearWorkspace(ctx, k.Conversation)
}

func (r *DesktopReader) RecentWorkspaces(ctx context.Context) ([]string, error) {
	st, err := r.desk.app.set.For("console")
	if err != nil {
		return nil, err
	}
	return st.RecentLocalWorkspaces(ctx, recentWorkspaceLimit)
}

var _ api.LocalWorkspaces = (*DesktopReader)(nil)

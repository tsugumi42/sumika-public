package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"sumika-native/internal/api"
	"sumika-native/internal/config"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/secrets"
)

// 密钥库的命令行入口（规格-数据目录与备份 5.3.3 “谁写”）：主程序是唯一的写者，sumika-ctl 的
// secrets 命令都经控制接口交到这里，所有读写都走 internal/secrets（同一路径同一个库对象，
// 包内登记表保证）。错误信息只说“有没有、锁着、格式坏”，不出现值、主密码、恢复码。

// SecretsStatus 是 sumika-ctl secrets status：文件在不在、锁着还是解开、有没有主密码、
// 本机副本能不能用、名字个数。文件不存在不算错误；文件在但解不开也照实报，让主人知道下一步。
func (a *App) SecretsStatus(context.Context) (ctlproto.SecretsStatus, error) {
	st, err := secrets.Open(a.paths.SecretsFile())
	switch {
	case err == nil:
		out := ctlproto.SecretsStatus{
			File: true, Unlocked: true, HasPassphrase: st.HasPassphrase(),
			// 能打开就说明文件里的本机副本在这台电脑上可用；重包失败时下面补一句原因。
			LocalCopy: true,
			Names:     len(st.Names()),
		}
		if e := st.LocalCopyError(); e != nil {
			out.Note = e.Error()
		}
		return out, nil
	case errors.Is(err, secrets.ErrNotFound):
		return ctlproto.SecretsStatus{}, nil
	case errors.Is(err, secrets.ErrLocked):
		var locked *secrets.LockedError
		out := ctlproto.SecretsStatus{File: true}
		if errors.As(err, &locked) {
			out.HasPassphrase = locked.HasPassphrase
		}
		return out, nil
	case errors.Is(err, secrets.ErrFormat):
		return ctlproto.SecretsStatus{File: true, Note: "密钥文件格式不对，读不出来（可能被别的程序改坏了）"}, nil
	}
	return ctlproto.SecretsStatus{}, ctlproto.Errorf(ctlproto.CodeFailed, fmt.Sprintf("读密钥文件失败：%v", err))
}

// SecretsList 是 sumika-ctl secrets list：只给名字，不给值；锁着时先解锁。
func (a *App) SecretsList(context.Context) (ctlproto.SecretsList, error) {
	st, err := secrets.Open(a.paths.SecretsFile())
	if err != nil {
		return ctlproto.SecretsList{}, secretsErr(err)
	}
	return ctlproto.SecretsList{Names: st.Names()}, nil
}

// SecretsSet 是 sumika-ctl secrets set：文件不存在就新建，值当场落盘。锁着时不做：先解锁。
func (a *App) SecretsSet(_ context.Context, name, value string) (ctlproto.SecretsWritten, error) {
	st, err := secrets.Create(a.paths.SecretsFile())
	if err != nil {
		return ctlproto.SecretsWritten{}, secretsErr(err)
	}
	if err := st.Set(name, value); err != nil {
		return ctlproto.SecretsWritten{}, secretsErr(err)
	}
	a.logf("密钥已存下：%s（值不记日志）", secretLogName(name))
	a.notifyEnvChanged()
	return ctlproto.SecretsWritten{Name: name}, nil
}

// SecretsDelete 是 sumika-ctl secrets delete：名字本来就不在时什么也不做；不因删除新建文件。
func (a *App) SecretsDelete(_ context.Context, name string) (ctlproto.SecretsWritten, error) {
	st, err := secrets.Open(a.paths.SecretsFile())
	if err != nil {
		return ctlproto.SecretsWritten{}, secretsErr(err)
	}
	if err := st.Delete(name); err != nil {
		return ctlproto.SecretsWritten{}, secretsErr(err)
	}
	a.logf("密钥已删掉：%s", secretLogName(name))
	a.notifyEnvChanged()
	return ctlproto.SecretsWritten{Name: name}, nil
}

// SecretsSetPassphrase 是 sumika-ctl secrets set-passphrase：返回的新恢复码只出现在这一条回复里，
// 不写日志、不出现在别的地方。文件不存在时先建空库（非 Windows 上这是写密钥的前提）。
func (a *App) SecretsSetPassphrase(_ context.Context, passphrase string) (ctlproto.SecretsPassphrase, error) {
	st, err := secrets.Create(a.paths.SecretsFile())
	if err != nil {
		return ctlproto.SecretsPassphrase{}, secretsErr(err)
	}
	code, err := st.SetPassphrase(passphrase)
	if err != nil {
		return ctlproto.SecretsPassphrase{}, secretsErr(err)
	}
	a.logf("密钥库的主密码已设置（恢复码只给主人看一次，不记日志）")
	a.notifyEnvChanged()
	return ctlproto.SecretsPassphrase{RecoveryCode: code}, nil
}

// SecretsUnlock 是 sumika-ctl secrets unlock：主密码或恢复码解开锁着的库，本机加密可用时
// 顺手重包本机副本写回（失败不影响这次解开，回复里说明）。解锁后 Q 令牌等处都经同一个库对象
// 读取，所以 QQ 要重新开始接收才会用上新解开的令牌。
func (a *App) SecretsUnlock(_ context.Context, secret string, recovery bool) (ctlproto.SecretsUnlocked, error) {
	kind := secrets.Passphrase
	if recovery {
		kind = secrets.RecoveryCode
	}
	st, err := secrets.Unlock(a.paths.SecretsFile(), secret, kind)
	if err != nil {
		return ctlproto.SecretsUnlocked{}, secretsErr(err)
	}
	out := ctlproto.SecretsUnlocked{Rewrapped: true}
	if e := st.LocalCopyError(); e != nil {
		out.Rewrapped, out.Note = false, e.Error()
	}
	a.logf("密钥库已用%s解开", map[bool]string{true: "恢复码", false: "主密码"}[recovery])
	a.notifyEnvChanged()
	return out, nil
}

// secretLogName 是日志里的密钥名字：qq.<号>.token 的号照界面规则打码（config.MaskID），
// 别的名字原样。日志里出现完整号码等于把账号写在日志里。
func secretLogName(name string) string {
	rest, ok := strings.CutPrefix(name, "qq.")
	if !ok {
		return name
	}
	id, ok := strings.CutSuffix(rest, ".token")
	if !ok || id == "" || strings.Contains(id, ".") {
		return name
	}
	return "qq." + config.MaskID(id) + ".token"
}

// secretsErr 把密钥库的错误变成带码的回复原因：只用固定的说法，不带口令、值或文件内容。
func secretsErr(err error) error {
	switch {
	case errors.Is(err, secrets.ErrNotFound):
		return ctlproto.Errorf(ctlproto.CodeFailed, "还没有密钥文件：用 sumika-ctl secrets set <名字> 存一个（值从标准输入读）")
	case errors.Is(err, secrets.ErrLocked):
		return ctlproto.Errorf(ctlproto.CodeFailed, fmt.Sprintf("%v：用 sumika-ctl secrets unlock 输入主密码，加 --recovery 用恢复码", err))
	case errors.Is(err, secrets.ErrWrongSecret):
		return ctlproto.Errorf(ctlproto.CodeFailed, "主密码或恢复码不对，文件没有动")
	case errors.Is(err, secrets.ErrChangedOnDisk):
		return ctlproto.Errorf(ctlproto.CodeFailed, err.Error())
	case errors.Is(err, secrets.ErrNeedPassphrase), errors.Is(err, secrets.ErrFormat), errors.Is(err, secrets.ErrUnsupported):
		return ctlproto.Errorf(ctlproto.CodeFailed, err.Error())
	}
	return ctlproto.Errorf(ctlproto.CodeFailed, fmt.Sprintf("密钥操作失败：%v", err))
}

// ───── 桌面设置页（设置 · 数据 · 密钥）─────

var _ api.SecretsAdmin = (*DesktopReader)(nil)

// secretsApp 取后台对象；界面在后台还没装好时不崩，照实报一句。
func (r *DesktopReader) secretsApp() (*App, error) {
	if r == nil || r.desk == nil || r.desk.app == nil {
		return nil, errors.New("后台还没开好，读不了密钥库")
	}
	return r.desk.app, nil
}

func (r *DesktopReader) SecretsStatus(ctx context.Context) (ctlproto.SecretsStatus, error) {
	if err := ctx.Err(); err != nil {
		return ctlproto.SecretsStatus{}, err
	}
	a, err := r.secretsApp()
	if err != nil {
		return ctlproto.SecretsStatus{}, err
	}
	return a.SecretsStatus(ctx)
}

func (r *DesktopReader) SecretsList(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a, err := r.secretsApp()
	if err != nil {
		return nil, err
	}
	out, err := a.SecretsList(ctx)
	if err != nil {
		return nil, err
	}
	return out.Names, nil
}

func (r *DesktopReader) SecretsSet(ctx context.Context, name, value string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a, err := r.secretsApp()
	if err != nil {
		return err
	}
	_, err = a.SecretsSet(ctx, name, value)
	return err
}

func (r *DesktopReader) SecretsDelete(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a, err := r.secretsApp()
	if err != nil {
		return err
	}
	_, err = a.SecretsDelete(ctx, name)
	return err
}

func (r *DesktopReader) SecretsSetPassphrase(ctx context.Context, passphrase string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	a, err := r.secretsApp()
	if err != nil {
		return "", err
	}
	out, err := a.SecretsSetPassphrase(ctx, passphrase)
	if err != nil {
		return "", err
	}
	return out.RecoveryCode, nil
}

func (r *DesktopReader) SecretsUnlock(ctx context.Context, secret string, recovery bool) (ctlproto.SecretsUnlocked, error) {
	if err := ctx.Err(); err != nil {
		return ctlproto.SecretsUnlocked{}, err
	}
	a, err := r.secretsApp()
	if err != nil {
		return ctlproto.SecretsUnlocked{}, err
	}
	return a.SecretsUnlock(ctx, secret, recovery)
}

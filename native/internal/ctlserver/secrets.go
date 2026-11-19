package ctlserver

import (
	"context"
	"regexp"

	"sumika-native/internal/ctlproto"
)

// SecretsBackend 是 secrets 命令的可选扩展：密钥库只在正在运行的桌面程序里，没接的宿主
// （演示、只看历史）不实现它。主程序是唯一的写者（规格-数据目录与备份 5.3.3）：这里的每个
// 方法都由主程序经 internal/secrets 执行，ctl 客户端从不打开密钥文件。
// 值、主密码、恢复码已经在 options 这一层校验过（非空、不超长），后端拿到的就是能直接用的一串。
type SecretsBackend interface {
	SecretsStatus(ctx context.Context) (ctlproto.SecretsStatus, error)
	SecretsList(ctx context.Context) (ctlproto.SecretsList, error)
	SecretsSet(ctx context.Context, name, value string) (ctlproto.SecretsWritten, error)
	SecretsDelete(ctx context.Context, name string) (ctlproto.SecretsWritten, error)
	SecretsSetPassphrase(ctx context.Context, passphrase string) (ctlproto.SecretsPassphrase, error)
	SecretsUnlock(ctx context.Context, secret string, recovery bool) (ctlproto.SecretsUnlocked, error)
}

const secretsUsage = `用法：secrets status | secrets list | secrets set <名字> | secrets delete <名字> | secrets set-passphrase | secrets unlock [--recovery]（值、主密码、恢复码从标准输入读，不写在命令行参数里；PowerShell 5.1 的管道默认不是 UTF-8，用 PowerShell 7 或先 chcp 65001）`

// secretName 和密钥库里的名字规则一样：非空、只许字母、数字和 . _ -、最长 128。
var secretName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// secrets 校验 secrets 请求：子命令、名字、值都在这一层收紧，缺哪样都 invalid、不调后端。
// 值、主密码、恢复码只认请求的 Secrets 字段；出现在 Args 里（命令行参数）一律不认。
// 演示数据模式一律不做：那里没有主程序，也就没有密钥文件。
func (s *Server) secrets(req ctlproto.Request) (ctlproto.Reply, func()) {
	if len(req.Args) == 0 {
		return invalid("%s", secretsUsage)
	}
	sub := req.Args[0]
	switch sub {
	case ctlproto.SecretsStatusCmd, ctlproto.SecretsListCmd:
		if len(req.Args) != 1 || req.Secrets != nil {
			return invalid("%s", secretsUsage)
		}
		if r, ok := s.demo("secrets " + sub); ok {
			return r, nil
		}
		b, ok := s.b.(SecretsBackend)
		if !ok {
			return fail(ctlproto.CodeFailed, "密钥接口不可用"), nil
		}
		if sub == ctlproto.SecretsStatusCmd {
			out, err := b.SecretsStatus(s.ctx)
			if err != nil {
				return s.fromErr(err), nil
			}
			out.Note = ctlproto.Clean(out.Note)
			return success(out), nil
		}
		out, err := b.SecretsList(s.ctx)
		if err != nil {
			return s.fromErr(err), nil
		}
		for i := range out.Names {
			out.Names[i] = ctlproto.Clean(out.Names[i])
		}
		return success(out), nil
	case ctlproto.SecretsSetCmd:
		if len(req.Args) != 2 || !secretName.MatchString(req.Args[1]) {
			return invalid("名字只能由字母、数字和 . _ - 组成，最长 %d 个字符；用法：%s", ctlproto.SecretsMaxNameBytes, secretsUsage)
		}
		value, r := s.secretInput(req, "值不能是空的：不想要这一条用 secrets delete", false)
		if r != nil {
			return *r, nil
		}
		if r, ok := s.demo("secrets set"); ok {
			return r, nil
		}
		b, ok := s.b.(SecretsBackend)
		if !ok {
			return fail(ctlproto.CodeFailed, "密钥接口不可用"), nil
		}
		out, err := b.SecretsSet(s.ctx, req.Args[1], value)
		if err != nil {
			return s.fromErr(err), nil
		}
		out.Name = ctlproto.Clean(out.Name)
		return success(out), nil
	case ctlproto.SecretsDeleteCmd:
		if len(req.Args) != 2 || !secretName.MatchString(req.Args[1]) || req.Secrets != nil {
			return invalid("%s", secretsUsage)
		}
		if r, ok := s.demo("secrets delete"); ok {
			return r, nil
		}
		b, ok := s.b.(SecretsBackend)
		if !ok {
			return fail(ctlproto.CodeFailed, "密钥接口不可用"), nil
		}
		out, err := b.SecretsDelete(s.ctx, req.Args[1])
		if err != nil {
			return s.fromErr(err), nil
		}
		out.Name = ctlproto.Clean(out.Name)
		return success(out), nil
	case ctlproto.SecretsSetPassphraseCmd:
		if len(req.Args) != 1 {
			return invalid("%s", secretsUsage)
		}
		passphrase, r := s.secretInput(req, "主密码不能是空的", false)
		if r != nil {
			return *r, nil
		}
		if r, ok := s.demo("secrets set-passphrase"); ok {
			return r, nil
		}
		b, ok := s.b.(SecretsBackend)
		if !ok {
			return fail(ctlproto.CodeFailed, "密钥接口不可用"), nil
		}
		out, err := b.SecretsSetPassphrase(s.ctx, passphrase)
		if err != nil {
			return s.fromErr(err), nil
		}
		out.RecoveryCode = ctlproto.Clean(out.RecoveryCode)
		return success(out), nil
	case ctlproto.SecretsUnlockCmd:
		if len(req.Args) != 1 {
			return invalid("%s", secretsUsage)
		}
		secret, r := s.secretInput(req, "主密码或恢复码不能是空的", true)
		if r != nil {
			return *r, nil
		}
		recovery := req.Secrets.Recovery
		if r, ok := s.demo("secrets unlock"); ok {
			return r, nil
		}
		b, ok := s.b.(SecretsBackend)
		if !ok {
			return fail(ctlproto.CodeFailed, "密钥接口不可用"), nil
		}
		out, err := b.SecretsUnlock(s.ctx, secret, recovery)
		if err != nil {
			return s.fromErr(err), nil
		}
		out.Note = ctlproto.Clean(out.Note)
		return success(out), nil
	}
	return invalid("%s", secretsUsage)
}

// secretInput 取请求里的值、主密码或恢复码：只认专门的字段，非空、不超长；返回 nil 回复表示通过。
// allowRecovery 只有 unlock 是真：别的子命令带着“这是恢复码”的标记就是写错了，不悄悄忽略。
func (s *Server) secretInput(req ctlproto.Request, empty string, allowRecovery bool) (string, *ctlproto.Reply) {
	if req.Secrets == nil || req.Secrets.Secret == "" || (req.Secrets.Recovery && !allowRecovery) {
		r, _ := invalid("%s（从标准输入读，写在命令行参数里不认）", empty)
		return "", &r
	}
	if len(req.Secrets.Secret) > ctlproto.SecretsMaxValueBytes {
		r, _ := invalid("值最长 %d 字节", ctlproto.SecretsMaxValueBytes)
		return "", &r
	}
	return req.Secrets.Secret, nil
}

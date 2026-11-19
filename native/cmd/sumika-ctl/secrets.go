package main

import (
	"fmt"
	"io"
	"strings"

	"sumika-native/internal/ctlproto"
)

const secretsUsage = "用法：secrets status | list | set <名字> | delete <名字> | set-passphrase | unlock [--recovery]（值、主密码、恢复码从标准输入读，不写在命令行参数里；PowerShell 5.1 的管道默认不是 UTF-8，用 PowerShell 7 或先 chcp 65001）"

// planSecrets 在发请求以前收紧 secrets 的形状：子命令、名字个数，值、主密码、恢复码不许写在
// 命令行参数里（会留在进程列表和命令历史里）。返回的函数在连上主程序以后才执行：从标准输入
// 读值、主密码、恢复码，主程序没在跑就不会白让人输一遍。
func planSecrets(o *options) (func(io.Writer) (*ctlproto.SecretsArgs, error), error) {
	if len(o.req.Args) == 0 {
		return nil, usageError{secretsUsage}
	}
	sub := o.req.Args[0]
	recovery := o.req.Secrets != nil && o.req.Secrets.Recovery
	rejectRecovery := func() error {
		if recovery {
			return usageError{"--recovery 只能用于 secrets unlock"}
		}
		return nil
	}
	rejectExtra := func() error {
		// 不回显多出来的那个参数：有人会把值、主密码、恢复码拼在命令后面，别把密钥材料打到屏幕上。
		return usageError{"值、主密码、恢复码从标准输入读，不写在命令行参数里（多出来的参数不认）"}
	}
	switch sub {
	case ctlproto.SecretsStatusCmd, ctlproto.SecretsListCmd:
		if len(o.req.Args) != 1 {
			return nil, rejectExtra()
		}
		if err := rejectRecovery(); err != nil {
			return nil, err
		}
		return nil, nil
	case ctlproto.SecretsSetCmd:
		if len(o.req.Args) < 2 {
			return nil, usageError{"secrets set 后面要跟名字，比如 qq.100.token；值从标准输入读"}
		}
		if len(o.req.Args) > 2 {
			return nil, rejectExtra()
		}
		if err := rejectRecovery(); err != nil {
			return nil, err
		}
		return func(stderr io.Writer) (*ctlproto.SecretsArgs, error) {
			value, err := newSecretReader(stderr).readOne("值（输入不显示）：")
			if err != nil {
				return nil, err
			}
			return &ctlproto.SecretsArgs{Secret: value}, nil
		}, nil
	case ctlproto.SecretsDeleteCmd:
		if len(o.req.Args) != 2 {
			if len(o.req.Args) > 2 {
				return nil, rejectExtra()
			}
			return nil, usageError{"secrets delete 后面要跟名字，比如 qq.100.token"}
		}
		if err := rejectRecovery(); err != nil {
			return nil, err
		}
		return nil, nil
	case ctlproto.SecretsSetPassphraseCmd:
		if len(o.req.Args) != 1 {
			return nil, rejectExtra()
		}
		if err := rejectRecovery(); err != nil {
			return nil, err
		}
		return func(stderr io.Writer) (*ctlproto.SecretsArgs, error) {
			r := newSecretReader(stderr)
			first, err := r.read("新主密码（至少 8 个字符，输入不显示）：")
			if err != nil {
				return nil, err
			}
			again, err := r.read("再输一次：")
			if err != nil {
				return nil, err
			}
			if first != again {
				return nil, usageError{"两次输入的主密码不一样，没有改"}
			}
			return &ctlproto.SecretsArgs{Secret: first}, nil
		}, nil
	case ctlproto.SecretsUnlockCmd:
		if len(o.req.Args) != 1 {
			return nil, rejectExtra()
		}
		return func(stderr io.Writer) (*ctlproto.SecretsArgs, error) {
			prompt := "主密码（输入不显示）："
			if recovery {
				prompt = "恢复码（输入不显示）："
			}
			secret, err := newSecretReader(stderr).readOne(prompt)
			if err != nil {
				return nil, err
			}
			return &ctlproto.SecretsArgs{Secret: secret, Recovery: recovery}, nil
		}, nil
	}
	return nil, usageError{secretsUsage}
}

// renderSecrets 打印 secrets 命令的回复。名字、说明都是读出来的，只当数据看。--json 不走这里
func renderSecrets(w, stderr io.Writer, req ctlproto.Request, r ctlproto.Reply) {
	if len(req.Args) == 0 {
		return
	}
	switch req.Args[0] {
	case ctlproto.SecretsStatusCmd:
		var v ctlproto.SecretsStatus
		if !decode(stderr, r, &v) {
			return
		}
		if !v.File {
			fmt.Fprintln(w, "密钥文件：还没有（用 sumika-ctl secrets set <名字> 存第一个，值从标准输入读）")
			return
		}
		fmt.Fprintln(w, "密钥文件：有")
		if v.Unlocked {
			fmt.Fprintf(w, "状态：已解开（存了 %d 个名字）\n", v.Names)
		} else if v.HasPassphrase {
			fmt.Fprintln(w, "状态：锁着（这台电脑解不开本机那一份）；用 sumika-ctl secrets unlock 输入主密码，卡里存的恢复码加 --recovery")
		} else {
			fmt.Fprintln(w, "状态：锁着（这台电脑解不开本机那一份，也没有设主密码：密钥只能重新存一遍）")
		}
		if v.HasPassphrase {
			fmt.Fprintln(w, "主密码：已设")
		} else {
			fmt.Fprintln(w, "主密码：没设（换电脑或重装系统后密钥解不开）")
		}
		if v.LocalCopy {
			fmt.Fprintln(w, "本机副本：这台电脑能用（下次启动不用输密码）")
		} else {
			fmt.Fprintln(w, "本机副本：不在这台电脑（解不开或还没有重新包好）")
		}
		if v.Note != "" {
			fmt.Fprintln(w, "说明："+ctlproto.Clean(v.Note))
		}
	case ctlproto.SecretsListCmd:
		var v ctlproto.SecretsList
		if !decode(stderr, r, &v) {
			return
		}
		if len(v.Names) == 0 {
			fmt.Fprintln(w, "（没有存任何密钥）")
			return
		}
		for _, name := range v.Names {
			fmt.Fprintln(w, ctlproto.Clean(name))
		}
	case ctlproto.SecretsSetCmd:
		var v ctlproto.SecretsWritten
		if !decode(stderr, r, &v) {
			return
		}
		fmt.Fprintf(w, "已存下 %s（值不显示，也不写日志）。\n", ctlproto.Clean(v.Name))
	case ctlproto.SecretsDeleteCmd:
		var v ctlproto.SecretsWritten
		if !decode(stderr, r, &v) {
			return
		}
		fmt.Fprintf(w, "已删掉 %s。\n", ctlproto.Clean(v.Name))
	case ctlproto.SecretsSetPassphraseCmd:
		var v ctlproto.SecretsPassphrase
		if !decode(stderr, r, &v) {
			return
		}
		fmt.Fprintln(w, "主密码已设置。")
		fmt.Fprintf(w, "恢复码（只显示这一次）：%s\n", ctlproto.Clean(v.RecoveryCode))
		fmt.Fprintln(w, "抄下来，存到别处：忘了主密码、又在这台电脑之外时，只能用它解开。")
	case ctlproto.SecretsUnlockCmd:
		var v ctlproto.SecretsUnlocked
		if !decode(stderr, r, &v) {
			return
		}
		if v.Rewrapped {
			fmt.Fprintln(w, "密钥已解开，本机副本已重新包好（这台电脑下次启动不用再输）。")
		} else {
			// 只解开了这一次：文件里本机那一份还解不开，紧接着的 list、set 仍会报锁着。
			note := strings.TrimPrefix(ctlproto.Clean(v.Note), "本机副本没包上：")
			fmt.Fprintln(w, "这次解开了，但这台电脑的本机副本没包上，下次还要输主密码；原因："+note)
		}
		fmt.Fprintln(w, "要让 QQ 用上新令牌，重新开始接收：sumika-ctl receive start")
	}
}

package ctlserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"time"

	"sumika-native/internal/ctlproto"
)

// BackupBackend 是备份命令的可选扩展：备份服务在正在运行的程序里，没接的宿主不实现它。
// BackupNow 做一份手动备份，最多等 wait（到点还没做完回 Settled=false，它在后台接着做）；BackupList 只读；
// BackupRestore 把一份备份恢复成新目录并核对（不切换、不碰现在的数据目录）；BackupVerify 抽验：恢复到临时目录、核对、删掉、记下结果。
// 后两个也是后台做、最多等 wait，核对没通过时 OK 为假、问题写在返回值里（不是 error）；开工前就被拒绝（备份名不对、目标不合适……）才回 error
type BackupBackend interface {
	BackupNow(ctx context.Context, media string, wait time.Duration) (ctlproto.BackupNow, error)
	BackupList(ctx context.Context) (ctlproto.BackupList, error)
	BackupRestore(ctx context.Context, name, to string, wait time.Duration) (ctlproto.BackupRestored, error)
	BackupVerify(ctx context.Context, name string, wait time.Duration) (ctlproto.BackupVerified, error)
}

const backupUsage = "用法：backup now [--media none|stickers|all] [--wait[=60s]] | backup list | backup restore <备份名> --to <新目录的绝对路径> [--wait[=60s]] | backup verify [<备份名>] [--wait[=60s]]"

// backupWait 解 --wait（不写用默认 60 秒）；写得不对回 invalid
func backupWait(req ctlproto.Request) (time.Duration, *ctlproto.Reply) {
	if req.Wait == "" {
		return ctlproto.BackupDefaultWait, nil
	}
	d, err := time.ParseDuration(req.Wait)
	if err != nil || d <= 0 || d > ctlproto.MaxWait {
		r, _ := invalid("--wait 要写成大于 0、不超过 %v 的时长，比如 60s", ctlproto.MaxWait)
		return 0, &r
	}
	return d, nil
}

func cleanDBChecks(dbs []ctlproto.BackupDBCheck) {
	for i := range dbs {
		dbs[i].Path = ctlproto.Clean(dbs[i].Path)
		dbs[i].Integrity = ctlproto.Clean(dbs[i].Integrity)
		for j := range dbs[i].Mismatch {
			dbs[i].Mismatch[j] = ctlproto.Clean(dbs[i].Mismatch[j])
		}
	}
}

// failedWithData：核对没通过：回复是失败（退出码 1），当时的核对结果随回复一起给出
func failedWithData(msg string, data any) ctlproto.Reply {
	r := fail(ctlproto.CodeFailed, ctlproto.Clean(msg))
	if b, err := json.Marshal(data); err == nil {
		r.Data = b
	}
	return r
}

// backup 校验 backup 请求：子命令认 now、list、restore、verify，--media 只用于 now、--to 只用于 restore，参数形状不对一律 invalid，不调后端。
// backup list 只读（readOnly 表）；演示数据模式一律不做。
func (s *Server) backup(req ctlproto.Request) (ctlproto.Reply, func()) {
	if len(req.Args) == 0 {
		return invalid("%s", backupUsage)
	}
	media, to := "", ""
	if req.Backup != nil {
		media, to = req.Backup.Media, req.Backup.To
	}
	sub := req.Args[0]
	if sub != ctlproto.BackupNowCmd && media != "" {
		return invalid("--media 只用于 backup now")
	}
	if sub != ctlproto.BackupRestoreCmd && to != "" {
		return invalid("--to 只用于 backup restore")
	}
	switch sub {
	case ctlproto.BackupNowCmd:
		if len(req.Args) != 1 {
			return invalid("%s", backupUsage)
		}
		if media != "" && media != "none" && media != "stickers" && media != "all" {
			return invalid("--media 只认 none、stickers、all")
		}
		wait, bad := backupWait(req)
		if bad != nil {
			return *bad, nil
		}
		if r, ok := s.demo("backup now"); ok {
			return r, nil
		}
		b, ok := s.b.(BackupBackend)
		if !ok {
			return fail(ctlproto.CodeFailed, "备份接口不可用"), nil
		}
		out, err := b.BackupNow(s.ctx, media, wait)
		if err != nil {
			return s.fromErr(err), nil
		}
		for i := range out.Problems {
			out.Problems[i] = ctlproto.Clean(out.Problems[i])
		}
		out.Name = ctlproto.Clean(out.Name)
		return success(out), nil
	case ctlproto.BackupListCmd:
		if len(req.Args) != 1 {
			return invalid("%s", backupUsage)
		}
		if req.Wait != "" {
			return invalid("--media、--wait 只用于 backup now")
		}
		if r, ok := s.demo("backup list"); ok {
			return r, nil
		}
		b, ok := s.b.(BackupBackend)
		if !ok {
			return fail(ctlproto.CodeFailed, "备份接口不可用"), nil
		}
		l, err := b.BackupList(s.ctx)
		if err != nil {
			return s.fromErr(err), nil
		}
		for i := range l.Rows {
			r := &l.Rows[i]
			r.Name = ctlproto.Clean(r.Name)
			for j := range r.Problems {
				r.Problems[j] = ctlproto.Clean(r.Problems[j])
			}
		}
		l.Overview.Dir = ctlproto.Clean(l.Overview.Dir)
		l.Overview.DirError = ctlproto.Clean(l.Overview.DirError)
		l.Overview.Drill.Backup = ctlproto.Clean(l.Overview.Drill.Backup)
		for i := range l.Overview.Drill.Problems {
			l.Overview.Drill.Problems[i] = ctlproto.Clean(l.Overview.Drill.Problems[i])
		}
		return success(l), nil
	case ctlproto.BackupRestoreCmd:
		if len(req.Args) != 2 || req.Args[1] == "" {
			return invalid("%s", backupUsage)
		}
		if to == "" {
			return invalid("backup restore 要写 --to <新目录>，恢复成一个还不存在或为空的新目录")
		}
		if !filepath.IsAbs(to) {
			return invalid("--to 要写绝对路径（命令行的相对路径按谁的工作目录算不清）")
		}
		wait, bad := backupWait(req)
		if bad != nil {
			return *bad, nil
		}
		if r, ok := s.demo("backup restore"); ok {
			return r, nil
		}
		b, ok := s.b.(BackupBackend)
		if !ok {
			return fail(ctlproto.CodeFailed, "备份接口不可用"), nil
		}
		out, err := b.BackupRestore(s.ctx, req.Args[1], to, wait)
		if err != nil {
			return s.fromErr(err), nil
		}
		out.Name, out.Target = ctlproto.Clean(out.Name), ctlproto.Clean(out.Target)
		for i := range out.Problems {
			out.Problems[i] = ctlproto.Clean(out.Problems[i])
		}
		for i := range out.Notes {
			out.Notes[i] = ctlproto.Clean(out.Notes[i])
		}
		cleanDBChecks(out.DBs)
		if out.Settled && !out.OK {
			msg := "恢复没有通过核对"
			if len(out.Problems) > 0 {
				msg += "：" + out.Problems[0]
			}
			return failedWithData(msg, out), nil
		}
		return success(out), nil
	case ctlproto.BackupVerifyCmd:
		if len(req.Args) > 2 {
			return invalid("%s", backupUsage)
		}
		name := ""
		if len(req.Args) == 2 {
			name = req.Args[1]
			if name == "" {
				return invalid("%s", backupUsage)
			}
		}
		wait, bad := backupWait(req)
		if bad != nil {
			return *bad, nil
		}
		if r, ok := s.demo("backup verify"); ok {
			return r, nil
		}
		b, ok := s.b.(BackupBackend)
		if !ok {
			return fail(ctlproto.CodeFailed, "备份接口不可用"), nil
		}
		out, err := b.BackupVerify(s.ctx, name, wait)
		if err != nil {
			return s.fromErr(err), nil
		}
		out.Name = ctlproto.Clean(out.Name)
		for i := range out.Problems {
			out.Problems[i] = ctlproto.Clean(out.Problems[i])
		}
		cleanDBChecks(out.DBs)
		if out.Settled && !out.OK {
			msg := "抽验没有通过"
			if len(out.Problems) > 0 {
				msg += "：" + out.Problems[0]
			}
			return failedWithData(msg, out), nil
		}
		return success(out), nil
	}
	return invalid("%s", backupUsage)
}

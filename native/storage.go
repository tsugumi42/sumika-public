package main

// 运行页存储统计（规格-运行页 第五节）接进桌面程序：整个程序一份 storagestat.Service，
// 它累计媒体新增字节（mediastore 的回调），后台扫描大目录，结果写进 trace.db 的 maintenance_runs。
// 放在外壳而不放进 backend.App：App 会随接收模式开关重开，增量不能跟着丢。

import (
	"errors"

	"sumika-native/internal/config"
	"sumika-native/internal/paths"
	"sumika-native/internal/storagestat"
)

// storageService 返回当前数据目录的存储统计服务；数据目录换了就关掉旧的（写回增量）再建新的
func (s *shell) storageService() (*storagestat.Service, error) {
	ds := s.settings.Load()
	if ds == nil {
		return nil, errors.New("数据目录还没就绪")
	}
	p, err := paths.Open(ds.Dir())
	if err != nil {
		return nil, errors.New("数据目录打不开")
	}
	s.storageMu.Lock()
	defer s.storageMu.Unlock()
	if s.storage != nil && s.storageDir == p.Root {
		return s.storage, nil
	}
	if s.storage != nil {
		if err := s.storage.Close(); err != nil {
			s.logf("存储统计：换数据目录时写回增量失败：%v", err)
		}
		s.storage = nil
	}
	root, dataDir := s.opt.Root, s.opt.DataDir
	s.storage = storagestat.New(p, storagestat.Options{
		Store: storagestat.TraceStore{File: p.GoTraceDB(), Logf: s.logf},
		Watch: true,
		Extra: func() storagestat.Extra { // 每次统计现读配置：模型目录、备份目录改了不用重启
			cfg, err := config.LoadIn(root, dataDir)
			if err != nil {
				return storagestat.Extra{}
			}
			return storagestat.Extra{ModelsRoot: cfg.Embed.ModelsRoot, BackupDir: cfg.Backup.Dir}
		},
	})
	s.storageDir = p.Root
	return s.storage, nil
}

// closeStorage 退出时调：取消正在跑的统计、把"快照加增量"写回
func (s *shell) closeStorage() {
	s.storageMu.Lock()
	svc := s.storage
	s.storage = nil
	s.storageMu.Unlock()
	if svc == nil {
		return
	}
	if err := svc.Close(); err != nil {
		s.logf("存储统计：退出时写回增量失败：%v", err)
	}
}

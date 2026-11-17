package main

// 远端后台（规格-Bun后台第1批 第五节）：-backend-url 时界面不开本地后台、不拿数据锁，只当协议客户端连 Go 或 Bun 后台。
// 后台没登记的方法（Go 的 -rpc-listen 只开读方法）界面按"只读"降级。

import (
	"context"
	"errors"
	"os"
	"time"

	"sumika-native/internal/paths"
	"sumika-native/internal/rpcclient"
)

// dialRemote 按 -backend-url 连：auto 读 -data 目录的 run/rpc.json；ws:// 地址的令牌从 SUMIKA_RPC_TOKEN 读（不放命令行，免得进进程列表）
func dialRemote(ctx context.Context, target, dataDir string) (*rpcclient.Client, error) {
	if target == "auto" {
		if dataDir == "" {
			return nil, errors.New("-backend-url auto 要和 -data 一起给：从数据目录的 run/rpc.json 找后台")
		}
		p, err := paths.Open(dataDir)
		if err != nil {
			return nil, err
		}
		return rpcclient.DialRunFile(ctx, p.RPCRunFile(), "sumika-desktop")
	}
	tok := os.Getenv("SUMIKA_RPC_TOKEN")
	if tok == "" {
		return nil, errors.New("连 ws:// 地址要在环境变量 SUMIKA_RPC_TOKEN 里给令牌")
	}
	return rpcclient.Dial(ctx, target, tok, "sumika-desktop")
}

// connectRemote 在后台线程连，连上了回界面线程换数据源；断了在状态区写明，不自动重连
func (s *shell) connectRemote() {
	ctx, cancel := context.WithTimeout(s.a.ctx, 10*time.Second)
	c, err := dialRemote(ctx, s.remote, s.opt.DataDir)
	cancel()
	if err != nil {
		s.a.post(func() { s.a.status = "连不上远端后台：" + err.Error() })
		return
	}
	impl := c.Server.Impl
	s.a.post(func() {
		s.a.attach(rpcclient.NewFull(c)) // 后台登记了的写方法都能用，没登记的按"不支持"降级（规格-Bun后台第2批 4.2）
		s.a.status = "远端后台（" + impl + "）"
	})
	go func() {
		<-c.Done()
		s.a.post(func() { s.a.status = "和远端后台（" + impl + "）的连接断了，重开界面再连" })
	}()
}

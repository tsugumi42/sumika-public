// e2e-run 在自建数据目录和假 OneBot 上运行真实后台；模型请求仍走真实网关。
package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"sumika-native/internal/app"
	"sumika-native/internal/config"
	"sumika-native/internal/embed"
	"sumika-native/internal/mediafetch"
	"sumika-native/internal/memory"
	"sumika-native/internal/paths"
)

type options struct {
	root, out, model, gateway, scenario, modelsRoot, modelsExe, ort string
	delete                                                          bool
	timeout                                                         time.Duration
}

func main() {
	var o options
	flag.StringVar(&o.root, "root", "", "资源根；默认从工作目录向上找")
	flag.StringVar(&o.out, "out", "", "运行目录的父目录，必填；每次新建，不复用已有数据")
	flag.StringVar(&o.model, "model", "auto/deepseek-v4.1-flash", "真实网关模型")
	flag.StringVar(&o.gateway, "gateway", "http://127.0.0.1:3425/v1", "OpenAI 网关，含 /v1")
	flag.StringVar(&o.scenario, "scenario", "full", "full、smoke 或 JSON 剧本文件")
	flag.StringVar(&o.modelsRoot, "models-root", os.Getenv("LOCAL_MODELS_ROOT"), "已有小型 gte 嵌入模型根；必填，不下载")
	flag.StringVar(&o.modelsExe, "models-exe", "", "已有 sumika-models 可执行文件，必填")
	flag.StringVar(&o.ort, "ort-library", "", "官方 onnxruntime.dll；默认同 models-exe 目录")
	flag.DurationVar(&o.timeout, "turn-timeout", 3*time.Minute, "单步最长等待")
	flag.BoolVar(&o.delete, "delete", false, "收尾后只删本次 data，保留报告和证据")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, o, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(ctx context.Context, o options, out io.Writer) (result error) {
	if o.timeout <= 0 {
		return errors.New("turn-timeout 必须大于 0")
	}
	if o.out == "" {
		return errors.New("需 -out：运行目录的父目录放在临时目录或会话草稿目录，不放真实数据目录")
	}
	if o.modelsRoot == "" || o.modelsExe == "" {
		return errors.New("需 -models-root 和 -models-exe：现有 QQ 发送门要求真实 gte 嵌入 full，关闭嵌入不能跑私聊回复")
	}
	var err error
	if o.root == "" {
		o.root, err = config.FindRoot(".")
		if err != nil {
			return err
		}
	}
	o.root, err = filepath.Abs(o.root)
	if err != nil {
		return err
	}
	for _, s := range []*string{&o.modelsRoot, &o.modelsExe, &o.ort} {
		if *s != "" {
			*s, err = filepath.Abs(*s)
			if err != nil {
				return err
			}
		}
	}
	s, raw, err := loadScenario(o.scenario)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(o.out, 0755); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(o.out, time.Now().Format("20060102-150405.000000000")+"-")
	if err != nil {
		return err
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return err
	}
	r := report{Name: s.Name, Model: o.model, Gateway: o.gateway, Started: time.Now(), Evidence: map[string][]map[string]any{}, Backfill: "未执行断线场景"}
	logs := &logbook{}
	calls := &callRecorder{}
	cpu, _, statErr := processStats()
	if statErr != nil {
		return statErr
	}
	var p paths.Paths
	defer func() {
		r.Ended = time.Now()
		r.Calls = calls.snapshot()
		r.Logs = logs.snapshot()
		if result != nil {
			r.Error = result.Error()
		}
		end, peak, e := processStats()
		if e != nil {
			result = errors.Join(result, e)
		} else {
			r.CPU = end - cpu
			r.Peak = peak
		}
		if p.Root != "" {
			cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			e = collect(cctx, p, &r)
			cancel()
			if e != nil {
				result = errors.Join(result, e)
				r.Error = result.Error()
			}
			e = filepath.WalkDir(filepath.Join(p.Root, "go", "media"), func(path string, d fs.DirEntry, e error) error {
				if os.IsNotExist(e) {
					return nil
				}
				if e != nil {
					return e
				}
				if !d.IsDir() {
					rel, e := filepath.Rel(dir, path)
					if e != nil {
						return e
					}
					r.Files = append(r.Files, rel)
				}
				return nil
			})
			if e != nil {
				result = errors.Join(result, e)
			}
			if o.delete {
				if e = os.RemoveAll(p.Root); e != nil {
					result = errors.Join(result, e)
				} else {
					r.Deleted = true
				}
			}
		}
		if result != nil {
			r.Error = result.Error()
		}
		for _, item := range []struct {
			name string
			b    []byte
		}{{"backend.log", []byte(strings.Join(r.Logs, "\n") + "\n")}, {"report.md", []byte(renderReport(r))}} {
			result = errors.Join(result, writeFile(filepath.Join(dir, item.name), item.b))
		}
		result = errors.Join(result, writeJSON(filepath.Join(dir, "results.json"), r))
		fmt.Fprintf(out, "报告：%s\n", filepath.Join(dir, "report.md"))
		fmt.Fprintf(out, "运行 %s；步骤 %d；模型调用 %d；CPU %s；工作集峰值 %.2f MiB\n", r.Ended.Sub(r.Started).Round(time.Millisecond), len(r.Rounds), len(r.Calls), r.CPU.Round(time.Millisecond), float64(r.Peak)/(1<<20))
	}()
	if err = writeFile(filepath.Join(dir, "scenario.json"), raw); err != nil {
		return err
	}
	peer := newPeer()
	defer peer.close()
	gateway, err := newGateway(o.gateway, calls)
	if err != nil {
		return err
	}
	defer gateway.Close()
	imageServer, downloader, err := newImages()
	if err != nil {
		return err
	}
	defer imageServer.Close()
	p, err = prepare(ctx, o, dir, peer.url(), gateway.URL)
	r.Data = p.Root
	if err != nil {
		return err
	}
	// 配置环境变量优先于显式目录，所以锁定到刚生成的文件，避免继承用户的真实配置。
	old, had := os.LookupEnv("SUMIKA_CONFIG")
	if err = os.Setenv("SUMIKA_CONFIG", p.ConfigFile()); err != nil {
		return err
	}
	defer func() {
		if had {
			os.Setenv("SUMIKA_CONFIG", old)
		} else {
			os.Unsetenv("SUMIKA_CONFIG")
		}
	}()
	var local *embed.Local
	appctx, cancel := context.WithCancel(ctx)
	defer cancel()
	a, err := app.Open(appctx, app.Options{Root: o.root, DataDir: p.Root, Mode: app.Receive, Account: "100", Logf: logs.printf, MediaDownloader: downloader,
		NewEmbed: func(c embed.LocalConfig, logf func(string, ...any)) app.LocalEmbedding {
			local = embed.NewLocal(c, logf)
			return local
		},
	})
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(appctx) }()
	// Run 负责资源收尾；必须等到关库后，才读取最终证据或删除本次 data。
	defer func() { cancel(); result = errors.Join(result, <-done) }()
	startupCtx, startupCancel := context.WithTimeout(ctx, 180*time.Second)
	err = until(startupCtx, func() (bool, error) {
		status, why := local.Status()
		if (status == embed.StatusDown || status == embed.StatusOff) && why != "" {
			return false, fmt.Errorf("真实 gte 嵌入未就绪：%s", why)
		}
		return status == embed.StatusReady, nil
	})
	startupCancel()
	if err != nil {
		return err
	}
	obs, err := openObservations(p)
	if err != nil {
		return err
	}
	defer obs.close()
	_, writable, err := memory.MetaValue(ctx, filepath.Join(p.Root, "go", "characters", "lanhe", "memory.db"), memory.MetaWriteEnabledAt)
	if err != nil {
		return err
	}
	if !writable {
		return errors.New("记忆写入没有实际启用，见 backend.log 的只读原因")
	}
	readyCtx, stop := context.WithTimeout(ctx, 20*time.Second)
	err = peer.waitReady(readyCtx)
	stop()
	if err != nil {
		return err
	}
	return play(ctx, o, s, peer, obs, calls, &r, out)
}
func play(ctx context.Context, o options, s scenario, p *fakePeer, d *observations, calls *callRecorder, r *report, out io.Writer) error {
	ids := map[string]string{}
	steps := map[string]step{}
	for i, v := range s.Steps {
		ids[v.ID] = fmt.Sprint(50000 + i)
		steps[v.ID] = v
	}
	offlineLoaded := map[string]bool{}
	for i, v := range s.Steps {
		rr := round{Step: v, Started: time.Now()}
		sendsBefore, _ := p.snapshot()
		callBefore := len(calls.snapshot())
		stepCtx, cancel := context.WithTimeout(ctx, o.timeout)
		var err error
		switch v.Kind {
		case "message":
			err = p.send(stepCtx, ids[v.ID], v, ids)
			if err == nil {
				rr.State, err = d.settle(stepCtx, v, ids[v.ID], rr.Started)
			}
			if err == nil && v.Image {
				err = until(stepCtx, func() (bool, error) {
					var state, why string
					e := d.chat.QueryRowContext(stepCtx, `SELECT f.state,COALESCE(f.last_error,'') FROM media_fetch f JOIN channel_msgs m ON f.event_seq=m.event_seq WHERE m.platform_id=?`, ids[v.ID]).Scan(&state, &why)
					if e == sql.ErrNoRows {
						return false, nil
					}
					if e != nil {
						return false, e
					}
					if state != "pending" && state != "done" {
						return false, fmt.Errorf("收图 %s: %s", state, why)
					}
					return state == "done", nil
				})
			}
		case "recall":
			err = p.recall(stepCtx, ids[v.Target], steps[v.Target])
			if err == nil {
				err = until(stepCtx, func() (bool, error) { return d.recalled(stepCtx, ids[v.Target]) })
			}
			rr.State = "撤回已入库"
		case "disconnect":
			p.disconnect(v.wait())
			// 先把离线消息放到假端历史里，重连核验时有机会被真实补录查询取到；绝不实时重推。
			for _, next := range s.Steps[i+1:] {
				if next.Kind != "offline" {
					break
				}
				if err = p.send(stepCtx, ids[next.ID], next, ids); err != nil {
					break
				}
				offlineLoaded[next.ID] = true
			}
			if err == nil {
				err = p.waitReady(stepCtx)
			}
			rr.State = "WS 已断开并重连"
		case "offline":
			if !offlineLoaded[v.ID] {
				err = p.send(stepCtx, ids[v.ID], v, ids)
			}
			if err == nil {
				err = pause(stepCtx, max(v.wait(), time.Second))
			}
			var found bool
			if err == nil {
				found, err = d.received(stepCtx, ids[v.ID])
			}
			_, actions := p.snapshot()
			queries := 0
			for _, a := range actions {
				if a == "get_group_msg_history" || a == "get_friend_msg_history" {
					queries++
				}
			}
			rr.State = fmt.Sprintf("离线消息入库=%t；历史查询=%d", found, queries)
			if found {
				r.Backfill = "有：只在假端历史中的消息已入库；" + rr.State
			} else if queries == 0 {
				r.Backfill = "没有（本次重连未发起历史查询，离线消息未入库）"
			} else {
				r.Backfill = "有查询但未补入；" + rr.State
			}
		}
		cancel()
		rr.Ended = time.Now()
		if err != nil {
			rr.Error = err.Error()
			rr.State = "失败"
		}
		sends, _ := p.snapshot()
		for _, sent := range sends[len(sendsBefore):] {
			rr.Sent = append(rr.Sent, sent)
		}
		cs := calls.snapshot()
		rr.Calls = cs[callBefore:]
		r.Rounds = append(r.Rounds, rr)
		_, r.Actions = p.snapshot()
		fmt.Fprintf(out, "%s：%s；%d ms；发送 %d 段；调用 %d\n", v.ID, rr.State, rr.Ended.Sub(rr.Started).Milliseconds(), len(rr.Sent), len(rr.Calls))
		if err != nil {
			return fmt.Errorf("步骤 %s: %w", v.ID, err)
		}
		if v.ExpectReply && len(rr.Sent) == 0 {
			return fmt.Errorf("步骤 %s 要求实际回复，结果 %s", v.ID, rr.State)
		}
	}
	return nil
}
func newImages() (*httptest.Server, *mediafetch.Downloader, error) {
	img := image.NewNRGBA(image.Rect(0, 0, 96, 64))
	for y := range 64 {
		for x := range 96 {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 2), G: uint8(y * 3), B: 120, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, nil, err
	}
	pngData := buf.Bytes()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(pngData)
	}))
	addr := server.Listener.Addr().String()
	d := mediafetch.NewDownloader(mediafetch.DownloadOptions{Hosts: mediafetch.HostRule{Exact: []string{"e2e-image.example"}}, LookupIP: func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("8.8.8.8")}, nil }, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}})
	return server, d, nil
}

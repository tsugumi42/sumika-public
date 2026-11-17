// Command ui-tour 巡视自己启动的演示窗口，保存官方 automation 树、PNG、检查器 Issues 和帧日志。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type tour struct {
	c           *client
	log         *journal
	out, prefix string
	start       int
	states      *[]state
}
type runStats struct {
	Variant        string  `json:"variant"`
	PID            int     `json:"pid"`
	Seconds        float64 `json:"seconds"`
	CPUSeconds     float64 `json:"cpuSeconds"`
	PeakWorkingSet uint64  `json:"peakWorkingSetBytes"`
}
type report struct {
	States                []state    `json:"-"`
	Runs                  []runStats `json:"runs"`
	Count                 int        `json:"states"`
	Findings              int        `json:"findings"`
	UniqueIssues          []string   `json:"uniqueIssues"`
	Slowest               frame      `json:"slowestNormalFrame"`
	SlowestInspector      frame      `json:"slowestInspectorFrame"`
	SlowestState          string     `json:"slowestNormalState"`
	SlowestInspectorState string     `json:"slowestInspectorState"`
	Files                 int        `json:"files"`
	Bytes                 int64      `json:"bytes"`
	Seconds               float64    `json:"seconds"`
	Failure               string     `json:"failure,omitempty"`
	Limits                []string   `json:"limits"`
	RunnerCPUSeconds      float64    `json:"runnerCPUSecondsBeforeSummary"`
	RunnerPeakWorkingSet  uint64     `json:"runnerPeakWorkingSetBytesBeforeSummary"`
}

func main() {
	app := flag.String("app", "", "e2e 桌面 exe（只以 -demo 启动）")
	out := flag.String("out", "", "新输出目录")
	sockets := flag.String("socket-dir", "", "当前用户独占的短目录，Windows 可用 subst 映射到 scratchpad")
	flag.BoolVar(&onlyRunPage, "runpage", false, "只看运行页的状态、资源、用量、存储标签")
	flag.Parse()
	if *app == "" || *out == "" || *sockets == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*app, *out, *sockets); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(app, out, sockets string) (ret error) {
	app, err := filepath.Abs(app)
	if err != nil {
		return err
	}
	out, err = filepath.Abs(out)
	if err != nil {
		return err
	}
	if err = os.Mkdir(out, 0700); err != nil {
		return fmt.Errorf("输出目录必须是新目录: %w", err)
	}
	started := time.Now()
	r := report{Limits: []string{
		"AX 只含无障碍节点，布局/Draw 内部内容不在树中；元素数不是全部渲染元素数。",
		"clipped-text 只检查文字框几何裁剪候选；框内字形截断和 SingleLine 实际省略号未验证，必须看 PNG。",
		"AX 同父重复 Key 是候选；布局父级冲突以检查器 Issues 为准，Issues 为当前进程累积值。",
		"family-size 比较同父 tab/radio 高度；其他同族依赖组件语义，未宣称全量普查。",
		"normal 帧含导航和截图工作；inspector 帧单列，均不等同输入到呈现延迟。",
	}}
	defer func() {
		r.Seconds = time.Since(started).Seconds()
		var measureErr error
		r.RunnerCPUSeconds, measureErr = runnerCPU()
		if measureErr == nil {
			r.RunnerPeakWorkingSet, measureErr = peakMemory(os.Getpid())
		}
		if measureErr != nil {
			ret = fmt.Errorf("%v; 巡视进程资源: %w", ret, measureErr)
		}
		if ret != nil {
			r.Failure = ret.Error()
		}
		if err := saveReport(out, &r); err != nil {
			ret = fmt.Errorf("%v; 写报告: %w", ret, err)
		}
	}()
	for _, size := range []struct{ name, value string }{{"wide", "2048x1030"}, {"narrow", "480x720"}} {
		for _, mode := range []struct{ name, value string }{{"light", "light"}, {"dark", "night"}} {
			variant := size.name + "-" + mode.name
			stats, err := session(app, out, sockets, variant, size.value, mode.value, &r.States)
			r.Runs = append(r.Runs, stats)
			if err != nil {
				return fmt.Errorf("%s: %w", variant, err)
			}
		}
	}
	unsettled := 0
	for _, s := range r.States {
		if len(s.IdleErrors) > 0 {
			unsettled++
		}
	}
	if unsettled > 0 {
		return fmt.Errorf("完整巡视已采集；%d 个状态 idle 超时，见各 JSON 的 idleErrors", unsettled)
	}
	return nil
}
func session(app, out, sockets, variant, size, mode string, states *[]state) (stats runStats, ret error) {
	stats.Variant = variant
	sock := filepath.Join(sockets, variant+".sock")
	if len(sock) > 100 {
		return stats, fmt.Errorf("socket 路径 %d 字节过长；Windows 请先 subst 到 scratchpad 下的独占目录", len(sock))
	}
	if _, err := os.Lstat(sock); err == nil {
		return stats, fmt.Errorf("socket 路径已存在: %s", sock)
	} else if !os.IsNotExist(err) {
		return stats, err
	}
	file, err := os.Create(filepath.Join(out, variant+".log"))
	if err != nil {
		return stats, err
	}
	defer file.Close()
	log := &journal{file: file}
	cmd := exec.Command(app, "-demo")
	cmd.Env = append(cleanEnv(), "MYGO_AUTOMATION="+sock, "MYGO_FRAME_STATS=all", "SUMIKA_SIZE="+size, "SUMIKA_AUTOMATION_THEME="+mode)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return stats, err
	}
	cmd.Stdout = file
	if err = cmd.Start(); err != nil {
		return stats, err
	}
	stats.PID = cmd.Process.Pid
	started := time.Now()
	copied := make(chan struct{})
	go func() { log.copy(stderr); close(copied) }()
	defer func() {
		var memErr error
		stats.PeakWorkingSet, memErr = peakMemory(cmd.Process.Pid)
		if memErr != nil && ret == nil {
			ret = memErr
		}
		// 只持有自己启动的 Process；绝不按名字停止日用 sumika.exe。
		if err := cmd.Process.Kill(); err != nil && ret == nil {
			ret = err
		}
		<-copied
		cmd.Wait()
		stats.Seconds = time.Since(started).Seconds()
		if cmd.ProcessState != nil {
			stats.CPUSeconds = (cmd.ProcessState.UserTime() + cmd.ProcessState.SystemTime()).Seconds()
		}
		if err := os.Remove(sock); err != nil && !os.IsNotExist(err) && ret == nil {
			ret = err
		}
		if log.err != nil && ret == nil {
			ret = log.err
		}
	}()
	var c *client
	deadline := time.Now().Add(30 * time.Second)
	for {
		c, err = connect(sock)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return stats, fmt.Errorf("连接自己启动的 PID %d: %w", stats.PID, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	defer c.conn.Close()
	var ws struct {
		Windows []window `json:"windows"`
	}
	if err = c.call("windows", nil, &ws); err != nil {
		return stats, err
	}
	if len(ws.Windows) != 1 || !strings.Contains(ws.Windows[0].Title, "演示数据") {
		return stats, fmt.Errorf("预期唯一演示窗口，得到 %+v", ws.Windows)
	}
	c.win = ws.Windows[0].ID
	t := tour{c: c, log: log, out: out, prefix: variant, start: 0, states: states}
	if err = t.wait("聊天窗口"); err != nil {
		return stats, err
	}
	if onlyRunPage {
		return stats, t.runPageTabs(c)
	}
	if err = t.listPage(); err != nil {
		return stats, err
	}
	if err = c.press("本地"); err != nil {
		return stats, err
	}
	if err = t.listPage(); err != nil {
		return stats, err
	}
	if err = t.take("conversations", ""); err != nil {
		return stats, err
	}
	if err = c.value("搜索会话", "不存在的编造会话"); err != nil {
		return stats, err
	}
	if err = t.wait("没有匹配的会话"); err != nil {
		return stats, err
	}
	if err = t.take("empty-search", "会话列表"); err != nil {
		return stats, err
	}
	if err = c.value("搜索会话", ""); err != nil {
		return stats, err
	}
	for _, cv := range []struct{ nav, label, name string }{{"本地", "小葵", "local-private"}, {"本地", "小屋", "local-group"}, {"QQ", "半糖", "qq-private"}, {"QQ", "测试群", "qq-group-rich"}} {
		if err = t.conversation(cv.nav, cv.label); err != nil {
			return stats, err
		}
		if err = t.take(cv.name, ""); err != nil {
			return stats, err
		}
	}
	for i, label := range []string{"这个会话", "她在想什么", "程序数据"} {
		if i == 0 {
			err = c.pressWithin(`展开右侧栏（Ctrl+\）`, "聊天顶栏")
		} else {
			err = c.press(label)
		}
		if err != nil {
			return stats, err
		}
		if err = t.wait(label); err != nil {
			return stats, err
		}
		if err = t.take(fmt.Sprintf("sidebar-%d", i), ""); err != nil {
			return stats, err
		}
	}
	if err = c.pressWithin(`收起右侧栏（Ctrl+\）`, "右侧栏顶栏"); err != nil {
		return stats, err
	}
	if err = c.idle(); err != nil {
		return stats, err
	}
	if err = t.listPage(); err != nil {
		return stats, err
	}
	for i, label := range []string{"外观", "模型", "记忆和学习", "工具与审批", "平台账号", "通知", "数据", "插件"} {
		if i == 0 {
			if err = c.press("设置"); err != nil {
				return stats, err
			}
		}
		if err = c.press(label); err != nil {
			return stats, err
		}
		if err = t.take(fmt.Sprintf("settings-%02d", i), ""); err != nil {
			return stats, err
		}
		if err = t.bottom(); err != nil {
			return stats, err
		}
		if err = t.take(fmt.Sprintf("settings-%02d-bottom", i), ""); err != nil {
			return stats, err
		}
	}
	for _, label := range []string{"记忆", "运行"} {
		if err = c.press(label); err != nil {
			return stats, err
		}
		if err = t.take(map[string]string{"记忆": "memory", "运行": "runtime"}[label], ""); err != nil {
			return stats, err
		}
	}
	return stats, t.runPageTabs(c)
}

// onlyRunPage 为真时只看运行页的几个标签（-runpage），不走前面的会话、设置各页
var onlyRunPage bool

// runPageTabs 走运行页的状态、资源、用量、存储标签（演示读服务造的六个角色、四十天数据）
func (t *tour) runPageTabs(c *client) error {
	if err := c.press("运行"); err != nil {
		return err
	}
	for _, tab := range []struct{ label, ready, name string }{{"状态", "后台状态", "run-status"}, {"资源", "主程序内部", "resources"}, {"用量", "总输入", "usage"}, {"存储", "数据目录总占用", "storage"}} {
		if tab.label != "状态" {
			if err := c.press(tab.label); err != nil {
				return err
			}
		}
		if tab.label == "资源" || tab.label == "用量" || tab.label == "存储" {
			if err := t.wait(tab.ready); err != nil {
				return err
			}
		}
		if err := t.take(tab.name, ""); err != nil {
			return err
		}
	}
	return nil
}
func cleanEnv() []string {
	var out []string
	for _, s := range os.Environ() {
		k, _, _ := strings.Cut(s, "=")
		if !strings.HasPrefix(strings.ToUpper(k), "SUMIKA_") && !strings.HasPrefix(strings.ToUpper(k), "MYGO_") {
			out = append(out, s)
		}
	}
	return out
}
func (t *tour) wait(label string) error {
	return t.waitFor(label, func(n node) bool { return n.Label == label })
}

func (t *tour) waitFor(label string, ready func(node) bool) error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := t.c.idle(); err != nil {
			return err
		}
		tr, err := t.c.tree()
		if err != nil {
			return err
		}
		for _, n := range tr.Nodes {
			if ready(n) && !has(n.States, "offscreen") {
				return nil
			}
		}
		if time.Now().After(deadline) {
			if err := saveJSON(filepath.Join(t.out, t.prefix+"-failed-tree.json"), tr); err != nil {
				return err
			}
			return fmt.Errorf("等待 %q 超时", label)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
func (t *tour) listPage() error {
	tr, err := t.c.tree()
	if err != nil {
		return err
	}
	for _, n := range tr.Nodes {
		if n.Label == "返回会话列表" && has(n.Actions, "press") {
			return t.c.press(n.Label)
		}
	}
	return nil
}
func (t *tour) conversation(nav, label string) error {
	if err := t.listPage(); err != nil {
		return err
	}
	if err := t.c.press(nav); err != nil {
		return err
	}
	// 虚拟列表不会构建远处的行；用用户可用的搜索定位，宽窄窗口走同一路。
	if err := t.c.value("搜索会话", label); err != nil {
		return err
	}
	if err := t.c.press(label); err != nil {
		return err
	}
	return t.waitFor(label+" 的已加载聊天页", func(n node) bool {
		if string(n.Key) != `"title"` || !strings.HasPrefix(n.Label, label+" ") {
			return false
		}
		var count int
		_, err := fmt.Sscanf(strings.TrimPrefix(n.Label, label+" "), "%d 条", &count)
		return err == nil && count > 0
	})
}
func (t *tour) bottom() error {
	tr, err := t.c.tree()
	if err != nil {
		return err
	}
	// 以当前页面最末一个可滚入的节点为目标，不用固定 wheel 距离猜页高。
	for i := len(tr.Nodes) - 1; i >= 0; i-- {
		n := tr.Nodes[i]
		if has(n.Actions, "scrollIntoView") {
			if err = t.c.act(n, "scrollIntoView", ""); err != nil {
				return err
			}
			return t.c.idle()
		}
	}
	return nil
}
func (t *tour) take(name, empty string) error {
	started := time.Now()
	if err := t.c.idle(); err != nil {
		return err
	}
	var ws struct {
		Windows []window `json:"windows"`
	}
	if err := t.c.call("windows", nil, &ws); err != nil {
		return err
	}
	var w window
	for _, v := range ws.Windows {
		if v.ID == t.c.win {
			w = v
		}
	}
	tr, err := t.c.tree()
	if err != nil {
		return err
	}
	var image struct {
		PNG    []byte `json:"png"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	}
	if err = t.c.call("capture", map[string]any{"window": t.c.win}, &image); err != nil {
		return err
	}
	s := state{Name: t.prefix + "-" + name, EmptyRegion: empty, Window: w, Tree: tr, Width: image.Width, Height: image.Height, Frames: t.log.since(t.start)}
	if err = os.WriteFile(filepath.Join(t.out, s.Name+".png"), image.PNG, 0600); err != nil {
		return err
	}
	// 截完产品状态再打开检查器，避免把检查器挤窄后的布局当成产品截图。
	inspectStart := t.log.position()
	if err = t.c.key("F12"); err != nil {
		return err
	}
	if err = t.c.idle(); err != nil {
		return err
	}
	it, err := t.c.tree()
	if err != nil {
		return err
	}
	found := false
	for _, n := range it.Nodes {
		if strings.HasPrefix(n.Label, "Issues") && has(n.Actions, "press") {
			if err = t.c.click(n); err != nil {
				return err
			}
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%s: 检查器 Issues 入口不在树中", s.Name)
	}
	if err = t.c.idle(); err != nil {
		return err
	}
	s.Inspector, err = t.c.tree()
	if err != nil {
		return err
	}
	noIssues := false
	inspectorRoot := ""
	issuesSelected := false
	for _, n := range s.Inspector.Nodes {
		if n.Label == "Developer tools" {
			inspectorRoot = n.ID
		}
		if strings.HasPrefix(n.Label, "Issues") && n.Role == "tab" && has(n.States, "checked") {
			issuesSelected = true
		}
	}
	if !issuesSelected {
		return fmt.Errorf("%s: Issues 页没有选中，不能把别页文字计作 Issues", s.Name)
	}
	inspectorNodes := index(s.Inspector)
	for _, n := range s.Inspector.Nodes {
		if n.Label == "No issues so far." {
			noIssues = true
		}
		if n.Role == "text" && under(n, inspectorRoot, inspectorNodes) && n.Label != "No issues so far." {
			s.Issues = append(s.Issues, n.Label)
		}
	}
	if !noIssues && len(s.Issues) == 0 {
		return fmt.Errorf("%s: 未取得检查器 Issues 内容", s.Name)
	}
	s.InspectorFrames = t.log.since(inspectStart)
	if err = t.c.key("F12"); err != nil {
		return err
	}
	if err = t.c.idle(); err != nil {
		return err
	}
	t.start = t.log.position()
	s.Millis = float64(time.Since(started).Microseconds()) / 1000
	s.Findings = check(s)
	s.IdleErrors = append([]string{}, t.c.waits...)
	t.c.waits = nil
	if len(s.IdleErrors) > 0 {
		fmt.Printf("%s: idle_timeouts=%d（保留失败）\n", s.Name, len(s.IdleErrors))
	}
	if err = saveJSON(filepath.Join(t.out, s.Name+".json"), s); err != nil {
		return err
	}
	*t.states = append(*t.states, s)
	fmt.Printf("%s: %dx%d px, %.1fx%.1f DIP, %d AX nodes, %d findings, %d issues, %.1f ms\n", s.Name, s.Width, s.Height, w.ContentBounds.Width, w.ContentBounds.Height, len(tr.Nodes), len(s.Findings), len(s.Issues), s.Millis)
	return nil
}
func saveJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0600)
}
func saveReport(out string, r *report) error {
	r.Count = len(r.States)
	unique := map[string]bool{}
	for _, s := range r.States {
		r.Findings += len(s.Findings)
		for _, i := range s.Issues {
			unique[i] = true
		}
		for _, f := range s.Frames {
			if f.Total > r.Slowest.Total {
				r.Slowest = f
				r.SlowestState = s.Name
			}
		}
		for _, f := range s.InspectorFrames {
			if f.Total > r.SlowestInspector.Total {
				r.SlowestInspector = f
				r.SlowestInspectorState = s.Name
			}
		}
	}
	for i := range unique {
		r.UniqueIssues = append(r.UniqueIssues, i)
	}
	sort.Strings(r.UniqueIssues)
	if err := filepath.WalkDir(out, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			r.Files++
			r.Bytes += info.Size()
		}
		return nil
	}); err != nil {
		return err
	}
	if err := saveJSON(filepath.Join(out, "report.json"), r); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(out, "index.html"))
	if err != nil {
		return err
	}
	defer f.Close()
	page := template.Must(template.New("index").Parse(`<!doctype html>
<html lang="zh"><meta charset="utf-8"><title>Sumika 界面巡视</title>
<style>body{font:15px system-ui;margin:24px;background:#eee;color:#222}article{background:white;padding:16px;margin:20px 0}img{max-width:100%;height:auto}pre{white-space:pre-wrap}li{margin:8px 0}.error{color:#a00}</style>
<h1>Sumika 界面巡视</h1>
<p>{{.Count}} 个状态，{{.Findings}} 条规则结果，{{len .UniqueIssues}} 条独立 Issues；正常段最慢帧 {{.Slowest.Total}} ms，检查器段 {{.SlowestInspector.Total}} ms。</p>
{{if .Failure}}<p class="error">验收失败：{{.Failure}}</p>{{end}}
<ul>{{range .Limits}}<li>{{.}}</li>{{end}}</ul>
{{range .States}}<article><h2>{{.Name}}</h2>
<p>{{.Width}}×{{.Height}} px · {{len .Tree.Nodes}} AX 节点 · <a href="{{.Name}}.json">JSON 和逐帧证据</a></p>
{{range .IdleErrors}}<p class="error">{{.}}</p>{{end}}
<a href="{{.Name}}.png"><img loading="lazy" src="{{.Name}}.png"></a>
<ul>{{range .Findings}}<li>{{.Rule}} · {{.Label}} · {{.Detail}}{{if .Candidate}}（候选）{{end}}</li>{{end}}</ul>
<pre>{{range .Issues}}{{.}}
{{end}}</pre></article>{{end}}</html>`))
	if err = page.Execute(f, r); err != nil {
		return err
	}
	fmt.Printf("REPORT states=%d findings=%d issues=%d slowest_ms=%.1f inspector_slowest_ms=%.1f files_before_index=%d bytes_before_index=%d elapsed_s=%.3f\n", r.Count, r.Findings, len(r.UniqueIssues), r.Slowest.Total, r.SlowestInspector.Total, r.Files, r.Bytes, r.Seconds)
	return nil
}

// Package embedtest 是测试程序扮演的"假 sumika-models.exe"：embed.Local 把测试程序当 exe 起起来，
// 它按真服务（internal/localmodels/server）的样子认命令行、回 /health、/v1/models、/v1/embeddings、/v1/rerank。
// embed 和 app 的测试共用这一份；它不引用 embed（embed 的包内测试也要用它），也不引用 localmodels（不链 ORT）。
//
// 用法：测试包的 TestMain 里 `if embedtest.Wanted() { embedtest.Run(); return }`，测试里 t.Setenv(embedtest.EnvOn, "1")
// 再把 os.Executable() 当 exe 交给 Local。下面这些环境变量是各个失败形态的开关，没设就是一个正常的服务。
package embedtest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 环境变量（开关）。
const (
	EnvOn            = "SUMIKA_FAKE_MODELS_EXE"      // 设成 1：这个进程是假的 sumika-models
	EnvEmbedModels   = "SUMIKA_FAKE_EMBED_MODELS"    // /v1/models 里 kind=embed 的 id（逗号分隔），默认 m
	EnvRerankModels  = "SUMIKA_FAKE_RERANK_MODELS"   // kind=rerank 的 id（逗号分隔），默认没有
	EnvKind          = "SUMIKA_FAKE_KIND"            // 把所有 embed id 的 kind 换成这个
	EnvNotReady      = "SUMIKA_FAKE_NOT_READY"       // 设了就把 embed id 标成 ready=false
	EnvNotReadyFile  = "SUMIKA_FAKE_NOT_READY_FILE"  // 这个文件在时，embed id 标成 ready=false；删掉它下一次探测就变就绪：测试用它确定地卡在"没就绪"再放行
	EnvHealthStatus  = "SUMIKA_FAKE_HEALTH_STATUS"   // /health 回这个状态码
	EnvModelsStatus  = "SUMIKA_FAKE_MODELS_STATUS"   // /v1/models 回这个状态码
	EnvEmbedStatus   = "SUMIKA_FAKE_EMBED_STATUS"    // /v1/embeddings 回这个状态码
	EnvTokenFile     = "SUMIKA_FAKE_TOKEN_FILE"      // 启动时把收到的令牌追加一行
	EnvArgsFile      = "SUMIKA_FAKE_ARGS_FILE"       // 启动时把命令行参数（一行 JSON）追加进去
	EnvRequestLog    = "SUMIKA_FAKE_REQUEST_LOG"     // 每个 /v1/embeddings、/v1/rerank 请求追加一行 JSON：{"path","auth","body"}
	EnvStderr        = "SUMIKA_FAKE_STDERR"          // 启动时往 stderr 写这段（{token} 换成收到的令牌）
	EnvExitCode      = "SUMIKA_FAKE_EXIT_CODE"       // 写完 stderr 马上用这个码退出
	EnvExit3Times    = "SUMIKA_FAKE_EXIT3_TIMES"     // 前 N 次启动用退出码 3 退出（要同时给 EnvCounterFile）
	EnvCounterFile   = "SUMIKA_FAKE_COUNTER_FILE"    // 每次启动追加一行，数启动了几次
	EnvHang          = "SUMIKA_FAKE_HANG"            // 设了就写完 stderr 以后一直不监听端口
	EnvDieFile       = "SUMIKA_FAKE_DIE_FILE"        // 这个文件出现时，进程删掉它并用退出码 4（看门狗）退出：就绪以后触发一次"服务没了"
	EnvEmbedFailFile = "SUMIKA_FAKE_EMBED_FAIL_FILE" // 这个文件在时，/v1/embeddings 回 500：服务显示就绪、单次嵌入出错
	EnvStdinEOFFile  = "SUMIKA_FAKE_STDIN_EOF_FILE"  // 开了 --exit-on-stdin-eof 又读到 EOF 时往这个文件写一行再退出（码 0）
	allowedEmbedKeys = "model input encoding_format dimensions user input_type task"
)

// Wanted 说这个进程是不是被当成假 exe 起来的（TestMain 里先问一句）。
func Wanted() bool { return os.Getenv(EnvOn) == "1" }

// WriteModelFiles 把 embed.ModelFiles 给出的路径当成假模型文件造出来：建出目录、写几个字节。
// embed.Local 起子进程前的检查只看文件在不在，假服务也不读内容，所以空文件就够。
// 路径由调用方从 embed.ModelFiles 取：这个包不能 import embed（embed 的包内测试要用它，会成环），也不自己抄一份模型登记表
func WriteModelFiles(paths ...string) error {
	for _, p := range paths {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte("假的模型文件，只给 os.Stat 看"), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Run 就是假服务：认 --port、--models、--lazy-models、--exit-on-stdin-eof，别的参数原样记下、不管。
// 退出码照真服务：2 配置错、3 端口被占。
func Run() {
	args := os.Args[1:]
	token := os.Getenv("SUMIKA_EMBED_TOKEN")
	port := argValue(args, "--port")
	if f := os.Getenv(EnvTokenFile); f != "" {
		appendLine(f, token)
	}
	if f := os.Getenv(EnvArgsFile); f != "" {
		// 命令行和几个环境变量一起记：测试要看 Local 有没有带 Python 专用的那几个（go-models 不该带）
		env := map[string]string{}
		for _, k := range []string{"PYTHONIOENCODING", "SUMIKA_EMBED_SERVER_DIR", "SUMIKA_EMBED_PORT", "LOCAL_MODELS_ROOT"} {
			env[k] = os.Getenv(k)
		}
		line, _ := json.Marshal(map[string]any{"args": args, "env": env})
		appendLine(f, string(line))
	}
	counted := 0
	if f := os.Getenv(EnvCounterFile); f != "" {
		appendLine(f, port)
		b, _ := os.ReadFile(f)
		counted = bytes.Count(b, []byte("\n"))
	}
	if s := os.Getenv(EnvStderr); s != "" {
		fmt.Fprintln(os.Stderr, strings.ReplaceAll(s, "{token}", token))
	}
	if n, _ := strconv.Atoi(os.Getenv(EnvExit3Times)); n > 0 && counted <= n {
		fmt.Fprintln(os.Stderr, "[sumika-models] 错误：127.0.0.1:"+port+" 已被占用（编的）")
		os.Exit(3)
	}
	if c := os.Getenv(EnvExitCode); c != "" {
		n, _ := strconv.Atoi(c)
		os.Exit(n)
	}
	if hasFlag(args, "--exit-on-stdin-eof") {
		go func() {
			_, _ = io.Copy(io.Discard, os.Stdin)
			if f := os.Getenv(EnvStdinEOFFile); f != "" {
				appendLine(f, "stdin EOF")
			}
			os.Exit(0)
		}()
	}
	if os.Getenv(EnvHang) != "" {
		time.Sleep(time.Hour) // 一直不监听端口：测启动超时（不能用 select{}：运行时会判成死锁直接崩）
	}
	if f := os.Getenv(EnvDieFile); f != "" {
		go func() {
			for {
				time.Sleep(100 * time.Millisecond)
				if _, err := os.Stat(f); err == nil {
					os.Remove(f)
					os.Exit(4)
				}
			}
		}()
	}
	embedIDs := splitIDs(envOr(EnvEmbedModels, "m"))
	rerankIDs := splitIDs(os.Getenv(EnvRerankModels))
	mux := http.NewServeMux()
	auth := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, `{"detail":"unauthorized"}`, http.StatusUnauthorized)
			return false
		}
		return true
	}
	forced := func(w http.ResponseWriter, env string) bool {
		if c := os.Getenv(env); c != "" {
			n, _ := strconv.Atoi(c)
			http.Error(w, `{"detail":"编的状态"}`, n)
			return true
		}
		return false
	}
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if auth(w, r) && !forced(w, EnvHealthStatus) {
			w.Write([]byte(`{"status":"ok","engine":"go"}`))
		}
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) || forced(w, EnvModelsStatus) {
			return
		}
		var rows []map[string]any
		for _, id := range embedIDs {
			kind := "embed"
			if k := os.Getenv(EnvKind); k != "" {
				kind = k
			}
			rows = append(rows, map[string]any{"id": id, "object": "model", "owned_by": "local", "kind": kind, "ready": os.Getenv(EnvNotReady) == "" && !fileExists(os.Getenv(EnvNotReadyFile))})
		}
		for _, id := range rerankIDs {
			rows = append(rows, map[string]any{"id": id, "object": "model", "owned_by": "local", "kind": "rerank", "ready": true})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": rows})
	})
	mux.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		raw, _ := io.ReadAll(r.Body)
		logRequest(r, raw)
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			http.Error(w, `{"detail":"不是 JSON"}`, http.StatusUnprocessableEntity)
			return
		}
		for k := range fields {
			if !strings.Contains(" "+allowedEmbedKeys+" ", " "+k+" ") {
				http.Error(w, `{"detail":"有不认识的字段"}`, http.StatusUnprocessableEntity)
				return
			}
		}
		var body struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		json.Unmarshal(raw, &body)
		if !contains(embedIDs, body.Model) {
			http.Error(w, `{"detail":"Unknown embedding model"}`, http.StatusNotFound)
			return
		}
		if forced(w, EnvEmbedStatus) {
			return
		}
		if f := os.Getenv(EnvEmbedFailFile); f != "" {
			if _, err := os.Stat(f); err == nil {
				http.Error(w, `{"detail":"编的嵌入故障"}`, http.StatusInternalServerError)
				return
			}
		}
		// 第 i 句的向量是 [字数, i+1]，故意倒着排 index，看客户端会不会按 index 排回来
		type item struct {
			Object    string    `json:"object"`
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		}
		data := []item{}
		for i := len(body.Input) - 1; i >= 0; i-- {
			data = append(data, item{"embedding", i, []float64{float64(len([]rune(body.Input[i]))), float64(i + 1)}})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data, "model": body.Model, "usage": map[string]int{"prompt_tokens": 1, "total_tokens": 1}})
	})
	// 重排：第 i 条文档的分数是 (i+1)/条数，所以排出来的顺序是倒的（最后一条排第一）
	mux.HandleFunc("/v1/rerank", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		raw, _ := io.ReadAll(r.Body)
		logRequest(r, raw)
		var body struct {
			Model     string   `json:"model"`
			Documents []string `json:"documents"`
		}
		if err := json.Unmarshal(raw, &body); err != nil || !contains(rerankIDs, body.Model) {
			http.Error(w, `{"detail":"Unknown rerank model"}`, http.StatusNotFound)
			return
		}
		type result struct {
			Index          int     `json:"index"`
			RelevanceScore float64 `json:"relevance_score"`
		}
		n := len(body.Documents)
		out := make([]result, 0, n)
		for i := n - 1; i >= 0; i-- {
			out = append(out, result{i, float64(i+1) / float64(n)})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"results": out})
	})
	// 就绪以后子进程写 stderr（运行期的异常回溯可能带到输入文字），再用 code 退出
	mux.HandleFunc("/noise", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(os.Stderr, r.URL.Query().Get("text"))
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("/exit", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("code"))
		os.Exit(n)
	})
	if err := http.ListenAndServe("127.0.0.1:"+port, mux); err != nil {
		fmt.Fprintln(os.Stderr, "[sumika-models] 错误："+err.Error())
		os.Exit(3)
	}
}

func logRequest(r *http.Request, raw []byte) {
	f := os.Getenv(EnvRequestLog)
	if f == "" {
		return
	}
	line, _ := json.Marshal(map[string]any{"path": r.URL.Path, "auth": r.Header.Get("Authorization"), "body": json.RawMessage(raw)})
	appendLine(f, string(line))
}

func argValue(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

func splitIDs(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func contains(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func appendLine(file, line string) {
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, line)
}

// fileExists 路径空算不存在
func fileExists(p string) bool {
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

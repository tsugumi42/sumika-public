package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"

	"sumika-native/internal/localmodels/engine"
	"sumika-native/internal/localmodels/ort"
)

// regEntry 是登记表里一个模型的、两边都有的字段，字段名照 parity_ref.py rules 导出的 ref-registry.json（也就是 Python 的 models.toml）。
type regEntry struct {
	Kind              string `json:"kind"`
	Dir               string `json:"dir"`
	Onnx              string `json:"onnx"`
	Dimension         int    `json:"dimension"`
	Pooling           string `json:"pooling"`
	Normalize         bool   `json:"normalize"`
	MaxTokens         int    `json:"max_tokens"`
	Matryoshka        bool   `json:"matryoshka"`
	MinDimensions     int    `json:"min_dimensions"`
	AllowedDimensions []int  `json:"allowed_dimensions"`
	Truncation        string `json:"truncation"`
	QueryPrefix       string `json:"query_prefix"`
	DocumentPrefix    string `json:"document_prefix"`
	QueryTask         string `json:"query_task"`
}

func regOf(s engine.Spec) regEntry {
	allowed := s.AllowedDimensions
	if allowed == nil {
		allowed = []int{}
	}
	return regEntry{string(s.Kind), s.Dir, s.Onnx, s.Dimension, s.Pooling, s.Normalize, s.MaxTokens, s.Matryoshka, s.MinDimensions,
		allowed, s.Truncation, s.QueryPrefix, s.DocumentPrefix, s.QueryTask}
}

// compareRegistry 逐模型逐字段比 Go 的登记和 Python 的，返回每个模型的字段数和对不上的字段（"模型.字段"）。
func compareRegistry(goSpecs []engine.Spec, py map[string]regEntry) (fields int, mismatches []string) {
	for _, s := range goSpecs {
		want, ok := py[s.ID]
		if !ok {
			mismatches = append(mismatches, s.ID+"：Python 的表里没有")
			continue
		}
		g, w := reflect.ValueOf(regOf(s)), reflect.ValueOf(want)
		for i := 0; i < g.NumField(); i++ {
			fields++
			if !reflect.DeepEqual(g.Field(i).Interface(), w.Field(i).Interface()) {
				mismatches = append(mismatches, fmt.Sprintf("%s.%s", s.ID, g.Type().Field(i).Tag.Get("json")))
			}
		}
	}
	for id := range py {
		found := false
		for _, s := range goSpecs {
			found = found || s.ID == id
		}
		if !found && (id == gteID || id == bgeID) {
			mismatches = append(mismatches, id+"：Go 的表里没有")
		}
	}
	slices.Sort(mismatches)
	return fields, mismatches
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func runRegistry(ctx context.Context, o *options) int {
	rep := newReport("registry", map[string]any{"python": o.python != ""})
	row := func(r Row) { rep.Rows = append(rep.Rows, r) }

	// 1. 登记表：Go 的 BuiltinSpecs 对 Python 的 load_specs（现场导出）
	if o.python == "" || o.parityRef == "" {
		row(Row{Stage: "登记表", What: "gte、bge 的字段、dir、前缀逐项一致", Value: "没给 -python / -server，没跑", Verdict: vNotRun})
	} else {
		work, err := os.MkdirTemp("", "models-parity-reg-")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		defer os.RemoveAll(work)
		var out bytes.Buffer
		cmd := exec.Command(o.python, "-I", "-B", "-X", "utf8", o.parityRef, "rules", "--models-root", o.modelsRoot, "--out", work)
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "parity_ref.py rules 失败：%v\n%s\n", err, tailString(out.String(), 600))
			return 2
		}
		var ref struct {
			Models map[string]regEntry `json:"models"`
		}
		if err := readJSON(filepath.Join(work, "ref-registry.json"), &ref); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		n, bad := compareRegistry(engine.BuiltinSpecs(), ref.Models)
		row(Row{Stage: "登记表", What: "gte、bge 的字段、dir、前缀逐项一致（Go 的 BuiltinSpecs 对 Python 现场导出的 load_specs）", Gate: "全部一致",
			Value: fmt.Sprintf("%d/%d 个字段一致%s", n-len(bad), n, listOrEmpty(bad)), Verdict: verdictOf(len(bad) == 0 && n > 0)})
	}

	// 2. 模型文件的哈希：sumika-models --verify（不起服务、不要令牌、不开 dll）
	var vout bytes.Buffer
	vcmd := exec.CommandContext(ctx, o.goExe, "--verify", "--models-root", o.modelsRoot)
	vcmd.Stdout, vcmd.Stderr = &vout, &vout
	verr := vcmd.Run()
	oks := 0
	sc := bufio.NewScanner(&vout)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "OK ") {
			oks++
		}
	}
	row(Row{Stage: "哈希", What: "模型文件（onnx 和 tokenizer.json）的 SHA-256 对登记表（sumika-models --verify）", Gate: "退出码 0，每个登记的模型一行 OK",
		Value: fmt.Sprintf("退出码 %s，%d 个模型 OK", exitText(verr), oks), Verdict: verdictOf(verr == nil && oks == len(engine.BuiltinSpecs()))})

	// 3. dll 的哈希在不在已知好的表里
	if o.ortLibrary == "" {
		row(Row{Stage: "哈希", What: "onnxruntime.dll 的 SHA-256 在已知好的表里", Value: "没给 -ort-library", Verdict: vNotRun})
	} else if sum, err := sha256File(o.ortLibrary); err != nil {
		row(Row{Stage: "哈希", What: "onnxruntime.dll 的 SHA-256 在已知好的表里", Value: err.Error(), Verdict: vFail})
	} else {
		known := ""
		for _, k := range ort.KnownDLLs {
			if k.GOOS == runtime.GOOS && k.GOARCH == runtime.GOARCH && strings.EqualFold(k.SHA256, sum) {
				known = k.Version + "（" + k.Source + "）"
			}
		}
		row(Row{Stage: "哈希", What: "onnxruntime.dll 的 SHA-256 在已知好的表里（ort.KnownDLLs，这一版只有官方发布包里的那份）", Gate: "在表里",
			Value: fmt.Sprintf("%s…%s", sum[:12], map[bool]string{true: " 在表里：" + known, false: " 不在表里"}[known != ""]), Verdict: verdictOf(known != "")})
	}
	rep.finish()
	rep.print(os.Stdout)
	if o.jsonOut != "" {
		if err := rep.writeJSON(o.jsonOut); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
	}
	return rep.exitCode()
}

func exitText(err error) string {
	if err == nil {
		return "0"
	}
	var b strings.Builder
	if ee, ok := err.(*exec.ExitError); ok {
		fmt.Fprintf(&b, "%d", ee.ExitCode())
	} else {
		b.WriteString(err.Error())
	}
	return b.String()
}

var _ = json.Marshal

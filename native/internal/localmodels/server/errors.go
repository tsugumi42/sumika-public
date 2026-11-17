package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// apiError 是要回给调用方的错误：状态码、一句原因（不带输入文字），和少数要带的响应头。
type apiError struct {
	status int
	detail string
	header http.Header
}

func newAPIError(status int, detail string) *apiError {
	return &apiError{status: status, detail: detail}
}

// unprocessable 是请求体格式不对（非 JSON、字段类型不对、有未知字段、缺必填字段）：422，正文 {"detail": "…"}。
func unprocessable(detail string) *apiError {
	return newAPIError(http.StatusUnprocessableEntity, detail)
}

// writeJSON 把 v 写成 JSON 响应。不转义 < > &（401 的提示里有 'Bearer <SUMIKA_EMBED_TOKEN>'），也不带结尾换行。
// 先编码再写：编码失败时还来得及回 500，不会写出半截。
func writeJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"detail":"Internal server error"}`))
		return
	}
	b := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

// writeError 回 {"detail": "…"}，和 server.py 的 HTTPException 同一个形状。
func writeError(w http.ResponseWriter, e *apiError) {
	for k, vs := range e.header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	writeJSON(w, e.status, map[string]string{"detail": e.detail})
}

func joinList(ss []string) string { return strings.Join(ss, ", ") }

// clip 把调用方给的串截短再放进响应和日志的文字里（模型 id 是调用方随便写的，可能很长）。
func clip(s string) string {
	const max = 80
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

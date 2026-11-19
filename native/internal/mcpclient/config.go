package mcpclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
)

type Configuration struct {
	Sumika     struct{ Enabled bool }
	MCPServers map[string]ServerConfig
}

var reference = regexp.MustCompile(`\$\{(secret|env):([^}]+)\}`)

// plainVar 是 Claude Desktop、Claude Code 片段里的 ${VAR} 和 ${VAR:-默认值}：按系统环境变量展开，和 ${env:VAR} 一样不进日志。
var plainVar = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

// ParseConfiguration 不启动进程；半写或重复键拒绝整次发布，凭据缺失不回退为空。
func ParseConfiguration(data []byte, file string, lookup func(string, string) (string, error)) (Configuration, error) {
	var c Configuration
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := uniqueJSON(dec); err != nil {
		return c, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return c, fmt.Errorf("MCP 配置有尾随内容")
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, err
	}
	// 社区片段（Claude Desktop、Claude Code 的 mcpServers）没有 sumika 段：总开关和每个服务的 enabled 不写就是开，
	// include 不写就是全部工具（工具还要先 mcp_search、调用还要审批）；写了 false 照旧是关。
	var explicit struct {
		Sumika     struct{ Enabled *bool }
		MCPServers map[string]struct{ Sumika struct{ Enabled *bool } }
	}
	if err := json.Unmarshal(data, &explicit); err != nil {
		return c, err
	}
	c.Sumika.Enabled = len(c.MCPServers) > 0
	if explicit.Sumika.Enabled != nil {
		c.Sumika.Enabled = *explicit.Sumika.Enabled
	}
	if !c.Sumika.Enabled {
		return c, nil
	}
	for id, s := range c.MCPServers {
		s.ID = id
		s.Sumika.Enabled = explicit.MCPServers[id].Sumika.Enabled == nil || *explicit.MCPServers[id].Sumika.Enabled
		expand := func(raw string) (string, error) {
			var foundErr error
			raw = plainVar.ReplaceAllStringFunc(raw, func(ref string) string {
				parts := plainVar.FindStringSubmatch(ref)
				if lookup != nil {
					if value, err := lookup("env", parts[1]); err == nil {
						return value
					}
				}
				if strings.Contains(ref, ":-") {
					return parts[2]
				}
				foundErr = fmt.Errorf("MCP 环境变量 %s 没设置（写成 ${%s:-默认值} 可以给默认）", parts[1], parts[1])
				return ""
			})
			if foundErr != nil {
				return "", foundErr
			}
			out := reference.ReplaceAllStringFunc(raw, func(ref string) string {
				parts := reference.FindStringSubmatch(ref)
				if lookup == nil {
					foundErr = fmt.Errorf("MCP 无凭据解析器")
					return ""
				}
				value, err := lookup(parts[1], parts[2])
				if err != nil {
					foundErr = fmt.Errorf("MCP 凭据不可用")
				}
				return value
			})
			if strings.Contains(out, "${") || strings.Contains(out, "$(") || strings.Contains(out, "`") {
				return "", fmt.Errorf("MCP 不支持动态展开")
			}
			return out, foundErr
		}
		for k, v := range s.Env {
			out, err := expand(v)
			if err != nil {
				return c, err
			}
			s.Env[k] = out
		}
		for k, v := range s.Headers {
			out, err := expand(v)
			if err != nil {
				return c, err
			}
			s.Headers[k] = out
		}
		for i, a := range s.Args {
			out, err := expand(a)
			if err != nil {
				return c, err
			}
			s.Args[i] = out
		}
		var err error
		if s.Command, err = expand(s.Command); err != nil {
			return c, err
		}
		if s.URL, err = expand(s.URL); err != nil {
			return c, err
		}
		if s.CWD != "" && !filepath.IsAbs(s.CWD) {
			s.CWD = filepath.Join(filepath.Dir(file), s.CWD)
		}
		// 光写程序名（npx、uvx、node）按 PATH 找，启动时才解析；带路径分隔符的相对路径才相对配置文件
		if s.Command != "" && !filepath.IsAbs(s.Command) && strings.ContainsAny(s.Command, `/\`) {
			s.Command = filepath.Join(filepath.Dir(file), s.Command)
		}
		if err := normalizeTransport(id, &s); err != nil && s.Sumika.Enabled { // 关着的占位条目不因为写得不全拖垮整份
			return c, err
		}
		if err := defaults(&s); err != nil {
			return c, err
		}
		c.MCPServers[id] = s
	}
	return c, nil
}
func uniqueJSON(d *json.Decoder) error {
	tok, err := d.Token()
	if err != nil {
		return err
	}
	delimiter, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			tok, err := d.Token()
			if err != nil {
				return err
			}
			key := tok.(string)
			if seen[key] {
				return fmt.Errorf("MCP 重复配置键")
			}
			seen[key] = true
			if err := uniqueJSON(d); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := uniqueJSON(d); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("MCP JSON 非法")
	}
	_, err = d.Token()
	return err
}

// normalizeTransport 把社区写法认成 Sumika 的两种传输：stdio（写了 command）和 Streamable HTTP（写了 url）。
// type 可以不写：有 url 没 command 当 http。sse 没有支持，写明白；两样都没写也写明白，不等到启动才报“必须为绝对路径”。
func normalizeTransport(id string, s *ServerConfig) error {
	switch strings.ToLower(strings.TrimSpace(s.Type)) {
	case "":
		if s.URL != "" && s.Command == "" {
			s.Type = "http"
		}
	case "stdio":
		s.Type = ""
	case "http", "streamable-http", "streamablehttp", "streamable_http":
		s.Type = "http"
	case "sse":
		return fmt.Errorf("MCP 服务 %s 用的是 sse 传输；Sumika 只连 stdio 和 Streamable HTTP。服务支持的话把 type 改成 http，不支持就用它的 stdio 启动方式", id)
	default:
		return fmt.Errorf("MCP 服务 %s 的 type 是 %q；只认 stdio 或 http（可以不写，写了 url 就是 http）", id, s.Type)
	}
	if s.Type == "http" && s.URL == "" {
		return fmt.Errorf("MCP 服务 %s 是 http 却没写 url", id)
	}
	if s.Type != "http" && s.Command == "" {
		return fmt.Errorf("MCP 服务 %s 既没写 command（本机程序）也没写 url（远程服务）", id)
	}
	return nil
}

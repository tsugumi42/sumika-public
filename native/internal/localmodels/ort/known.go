package ort

import (
	"runtime"
	"strings"
)

// KnownDLL 是一份已知好的 onnxruntime 动态库：版本、系统、架构、整个文件的 SHA-256。清单里没有的文件一律不加载。
// 清单只放哈希，不放路径：dll 放在哪由配置（embed.ortLibrary）或"和 sumika-models.exe 同一个目录"决定（规格 2.3）。
type KnownDLL struct {
	Version string
	GOOS    string
	GOARCH  string
	SHA256  string // 小写十六进制
	Source  string // 这份文件从哪来，给人看
}

// KnownDLLs 是编进程序的清单。新增一行 = 重新编译、提交；没有环境变量能放行别的文件。
var KnownDLLs = []KnownDLL{
	{
		Version: "1.24.4",
		GOOS:    "windows",
		GOARCH:  "amd64",
		SHA256:  "b95efb2113b603bbbf3f191061c5516a871ed546893c820e4f3b7b6c358dbf2a",
		Source:  "GitHub 发布页 onnxruntime-win-x64-1.24.4.zip（压缩包 SHA-256 d2319fddfb6ea4db99ccc4b60c85c517bcd855721f5daa6a06d40d7cb2ee2357）里的 lib/onnxruntime.dll，13.5 MB；scripts/fetch-ort.ts 取回，2026-11-02 在本机算的哈希",
	},
	// 清单里只放官方发布包里的那份。Python wheel 里的 onnxruntime.dll（MaiBot 环境里的就是它）不登记：
	// 开发和对拍都用官方包（ADR-233），借来的文件不进清单。
}

// findKnown 按当前系统和架构在清单里找这个哈希。
func findKnown(list []KnownDLL, sha256hex string) (KnownDLL, bool) {
	sum := strings.ToLower(sha256hex)
	for _, k := range list {
		if k.GOOS == runtime.GOOS && k.GOARCH == runtime.GOARCH && strings.ToLower(k.SHA256) == sum {
			return k, true
		}
	}
	return KnownDLL{}, false
}

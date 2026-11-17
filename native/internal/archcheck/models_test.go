package archcheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 规格-Go本地模型 4.2 / GM5 验收 12：sumika-models 是个小程序，又是个能看到所有送去嵌入的文本的进程。

const modelsCmd = "./cmd/sumika-models"

// sumika-models 的依赖里不许有这些：保持它小（exe ≤ 15 MB），也别让它顺手接上整个后台。
var modelsBannedDeps = []string{
	"sumika-native/internal/app",
	"sumika-native/internal/store",
	"sumika-native/internal/memory",
}

func TestSumikaModelsStaysSmall(t *testing.T) {
	deps := depsOf(t, modelsCmd)
	sawServer := false
	for _, d := range deps {
		if d == "sumika-native/internal/localmodels/server" {
			sawServer = true
		}
		for _, banned := range modelsBannedDeps {
			if d == banned || strings.HasPrefix(d, banned+"/") {
				t.Errorf("%s 链接了 %s：sumika-models 要保持小，不接整个后台（规格 4.2）", modelsCmd, d)
			}
		}
	}
	if !sawServer {
		t.Fatalf("%s 的依赖里没有 internal/localmodels/server：包改名、搬家了就把这里一起改，不然这个测试什么也没查", modelsCmd)
	}
}

// 依赖白名单（规格 4.2，复核方提的）：这个进程能看到所有送去嵌入的文本，所以算的那几层不许有往外连或起别的程序的能力。
// 依赖闭包（-deps，只算正式代码，测试文件的 import 不算）里：
//   - tokenizer、ort、engine、pool 和两个第三方库（ONNX Runtime 的绑定 younland 是 ort 的依赖，一并算）：
//     不许有 net、net/http、os/exec；
//   - server 是唯一可以 net/http 的，也不许 os/exec。
var modelsNoNetwork = []string{
	"./internal/localmodels/tokenizer",
	"./internal/localmodels/ort",
	"./internal/localmodels/engine",
	"./internal/localmodels/pool",
	"./third_party/goSentencePiece",
}

const modelsServer = "./internal/localmodels/server"

func TestLocalModelsImportWhitelist(t *testing.T) {
	have := allPackages(t)
	check := func(pkg string, banned ...string) {
		t.Helper()
		if !have["sumika-native/"+strings.TrimPrefix(pkg, "./")] {
			t.Fatalf("go list ./... 里没有 %s：包改名、搬家了就把这里一起改，不然这个测试什么也没查", pkg)
		}
		deps := depsOf(t, pkg)
		if len(deps) < 3 {
			t.Fatalf("%s 的依赖只列出 %d 个，go list 不对", pkg, len(deps))
		}
		for _, d := range deps {
			for _, b := range banned {
				if d == b {
					t.Errorf("%s 的依赖闭包里有 %s：这几层不许有（规格 4.2），算向量的代码不该有往外连或起别的程序的能力", pkg, d)
				}
			}
		}
	}
	for _, pkg := range modelsNoNetwork {
		check(pkg, "net", "net/http", "os/exec")
	}
	check(modelsServer, "os/exec") // 这一层要 net/http，所以只禁起程序
	check(modelsCmd, "os/exec")
}

// exe 大小 ≤ 15 MB（规格 4.2；用默认的 go build，不带任何 tag）。调研时 4.7 MB 是没有 HTTP 的试验程序，
// 加上 net/http 以后是 10.6 MB（2026-11-02）；涨到 15 MB 说明依赖里混进了不该有的东西。
func TestSumikaModelsExeSize(t *testing.T) {
	if testing.Short() {
		t.Skip("要真的编译一个 exe，放在每日整套里查")
	}
	out := filepath.Join(t.TempDir(), "sumika-models.exe")
	cmd := exec.Command("go", "build", "-o", out, modelsCmd)
	cmd.Dir = moduleRoot(t)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s 失败：%v\n%s", modelsCmd, err, b)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	const limit = 15 << 20
	t.Logf("sumika-models.exe：%d 字节（%.1f MiB），上限 %d MiB", info.Size(), float64(info.Size())/(1<<20), limit>>20)
	if info.Size() > limit {
		t.Errorf("sumika-models.exe %d 字节，超过 15 MiB：看依赖里混进了什么", info.Size())
	}
}

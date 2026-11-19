package checkpoint

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCheckpointFileBoundaries(t *testing.T) {
	for _, kind := range []string{"escape", "ADS", "device", "git", "hardlink", "junction"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t, nil)
			outside := t.TempDir()
			secret := filepath.Join(outside, "outside.txt")
			require(t, os.WriteFile(secret, []byte("untouched"), 0600))
			target := filepath.Join(f.work, "a")
			switch kind {
			case "escape":
				target = secret
			case "ADS":
				target = filepath.Join(f.work, "a:stream")
			case "device":
				target = filepath.Join(f.work, "CON")
			case "git":
				target = filepath.Join(f.work, ".git")
			case "hardlink":
				require(t, os.Link(secret, target))
			case "junction":
				link := filepath.Join(f.work, "linked")
				if runtime.GOOS == "windows" {
					if output, err := exec.Command("cmd.exe", "/c", "mklink", "/J", link, outside).CombinedOutput(); err != nil {
						t.Fatalf("创建临时联接：%v %s", err, output)
					}
				} else {
					require(t, os.Symlink(outside, link))
				}
				target = filepath.Join(link, "outside.txt")
			}
			ran := false
			_, err := f.s.Run(t.Context(), Capture{Invocation: f.in(), Owner: f.owner, CWD: f.work, Tool: "write", Paths: []string{target}}, func() error { ran = true; return os.WriteFile(target, []byte("changed"), 0600) })
			if err == nil || ran {
				t.Fatalf("危险路径进入执行：%s ran=%v err=%v", target, ran, err)
			}
			checkFile(t, secret, []byte("untouched"))
		})
	}
}

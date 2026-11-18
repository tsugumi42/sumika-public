package checkpoint

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCheckpointMaterialIdentityReplacement(t *testing.T) {
	for _, stage := range []string{"file", "directory", "after-check", "missing-registration", "torn-registration"} {
		t.Run(stage, func(t *testing.T) {
			f := newFixture(t, nil)
			nested := filepath.Join(f.work, "nested")
			require(t, os.Mkdir(nested, 0700))
			require(t, os.WriteFile(filepath.Join(nested, "a"), []byte("original"), 0600))
			rec := f.capture(t, filepath.Join("nested", "a"), "agent")
			cmd := exec.Command(os.Args[0], "-test.run=^TestCheckpointRestoreTempCrash$")
			cmd.Env = append(os.Environ(), "CKPT_TEMP_RECORD="+filepath.Join(f.data, rec.ID, "record.json"), "CKPT_TEMP_STAGE=synced")
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 75 {
				t.Fatalf("未硬退出：%v %s", err, out)
			}
			records, err := f.s.List(t.Context(), f.owner)
			require(t, err)
			inverse := records[0]
			name := materialName(inverse.ID+"-0", true)
			owned := filepath.Join(nested, name)
			checkFile(t, owned, []byte("original"))
			moved := filepath.Join(nested, "retained-original")
			if stage == "directory" {
				require(t, os.Rename(nested, filepath.Join(f.work, "moved")))
				require(t, os.Mkdir(nested, 0700))
				require(t, os.WriteFile(owned, []byte("user-file"), 0600))
			} else if stage == "file" {
				require(t, os.Rename(owned, moved))
				require(t, os.WriteFile(owned, []byte("user-file"), 0600))
			} else if stage == "after-check" {
				original := deleteRestoreMaterial
				t.Cleanup(func() { deleteRestoreMaterial = original })
				deleteRestoreMaterial = func(file *os.File) error {
					require(t, os.Rename(owned, moved))
					require(t, os.WriteFile(owned, []byte("user-file"), 0600))
					return original(file)
				}
			} else if stage == "missing-registration" {
				require(t, os.Remove(filepath.Join(f.data, inverse.ID, "materials.jsonl")))
			} else {
				require(t, os.WriteFile(filepath.Join(f.data, inverse.ID, "materials.jsonl"), []byte(`{"Token":`), 0600))
			}
			_, err = New(f.data, f.s.opts)
			require(t, err)
			if stage == "missing-registration" || stage == "torn-registration" {
				checkFile(t, owned, []byte("original"))
			} else {
				checkFile(t, owned, []byte("user-file"))
			}
			if stage == "file" {
				checkFile(t, moved, []byte("original"))
			}
			if stage == "directory" {
				checkFile(t, filepath.Join(f.work, "moved", name), []byte("original"))
			}
			if stage == "after-check" {
				if _, err := os.Stat(moved); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("未按句柄回收原材料：%v", err)
				}
			}
		})
	}
}

package checkpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/kernel"
)

func TestCheckpointRestoreTempCrash(t *testing.T) {
	if path := os.Getenv("CKPT_TEMP_RECORD"); path != "" {
		body, err := os.ReadFile(path)
		require(t, err)
		var rec Record
		require(t, json.Unmarshal(body, &rec))
		s, err := New(filepath.Dir(filepath.Dir(path)), Options{Authorize: func(context.Context, kernel.Scope, string) error { return nil }})
		require(t, err)
		published := make(chan struct{})
		if os.Getenv("CKPT_TEMP_STAGE") == "second" {
			original := publishReplacement
			publishReplacement = func(root *os.Root, from, to string, mode os.FileMode) error {
				err := original(root, from, to, mode)
				if to == "a" && err == nil {
					close(published)
				}
				return err
			}
		}
		syncRestoreTemp = func(file *os.File) error {
			if os.Getenv("CKPT_TEMP_STAGE") == "second" {
				if strings.HasSuffix(file.Name(), "-0.tmp") {
					return file.Sync()
				}
				<-published
				require(t, file.Sync())
			}
			if os.Getenv("CKPT_TEMP_STAGE") == "synced" {
				require(t, file.Sync())
			}
			os.Exit(75)
			return nil
		}
		plan, err := s.Preview(t.Context(), rec.Owner, rec.ID, "step")
		require(t, err)
		in := rec.Invocation
		in.RunID, in.ToolID = "fixture-restore", "fixture-temp"
		_, err = s.Restore(t.Context(), rec.Owner, in, rec.ID, "step", plan.Version)
		t.Fatalf("未到达临时文件中断点：%v", err)
	}
	for _, stage := range []string{"written", "synced"} {
		t.Run(stage, func(t *testing.T) {
			f := newFixture(t, nil)
			require(t, os.Mkdir(filepath.Join(f.work, "nested"), 0700))
			target := filepath.Join("nested", "a")
			require(t, os.WriteFile(filepath.Join(f.work, target), []byte("original\r\n"), 0600))
			rec := f.capture(t, target, "agent")
			cmd := exec.Command(os.Args[0], "-test.run=^TestCheckpointRestoreTempCrash$")
			cmd.Env = append(os.Environ(), "CKPT_TEMP_RECORD="+filepath.Join(f.data, rec.ID, "record.json"), "CKPT_TEMP_STAGE="+stage)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 75 {
				t.Fatalf("未硬退出：%v %s", err, output)
			}
			records, err := f.s.List(t.Context(), f.owner)
			require(t, err)
			inverse := records[0]
			if inverse.InverseOf != rec.ID || inverse.Status != "interrupted" {
				t.Fatalf("缺少待恢复逆记录：%+v", inverse)
			}
			owned := filepath.Join(f.work, "nested", materialName(inverse.ID+"-0", true))
			checkFile(t, owned, []byte("original\r\n"))
			neighbors := []string{
				filepath.Join(f.work, materialName(inverse.ID+"-0", true)),
				filepath.Join(f.work, "nested", materialName(inverse.ID+"-1", true)),
				filepath.Join(f.work, "nested", materialName(inverse.ID+"-0", false)),
				filepath.Join(f.work, "nested", ".sumika-checkpoint-user.tmp"),
			}
			for i, path := range neighbors {
				require(t, os.WriteFile(path, []byte(fmt.Sprintf("neighbor-%d", i)), 0600))
			}
			cold, err := New(f.data, f.s.opts)
			require(t, err)
			if _, err := os.Stat(owned); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("冷开遗留原文临时文件：%v", err)
			}
			checkFile(t, filepath.Join(f.work, target), []byte("agent"))
			for i, path := range neighbors {
				checkFile(t, path, []byte(fmt.Sprintf("neighbor-%d", i)))
			}
			plan, err := cold.Preview(t.Context(), f.owner, inverse.ID, "recovery")
			require(t, err)
			_, err = cold.Restore(t.Context(), f.owner, f.in(), inverse.ID, "recovery", plan.Version)
			require(t, err)
			checkFile(t, filepath.Join(f.work, target), []byte("agent"))
		})
	}
}

func TestCheckpointMaterialRegistrationValidation(t *testing.T) {
	f := newFixture(t, nil)
	rec := f.capture(t, "a", "agent")
	neighbor := filepath.Join(f.work, materialName(rec.ID+"-0", true))
	require(t, os.WriteFile(neighbor, []byte("neighbor"), 0600))
	root, err := os.OpenRoot(filepath.Join(f.data, rec.ID))
	require(t, err)
	rec.InverseOf = rec.ID
	require(t, saveRecord(root, rec))
	require(t, root.Close())
	if _, err := New(f.data, f.s.opts); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("普通工具记录伪装成恢复材料登记：%v", err)
	}
	checkFile(t, neighbor, []byte("neighbor"))
}

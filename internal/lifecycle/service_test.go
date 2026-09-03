package lifecycle

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CapedHero/brigsby/internal/recovery"
)

func TestApplyPartialBatchRestoresEveryTarget(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	if err := os.WriteFile(first, []byte("before-first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("before-second"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := New(root)
	batch, err := service.Apply([]Target{{Path: first}, {Path: second}}, func() error {
		if err := os.WriteFile(first, []byte("after-first"), 0o600); err != nil {
			return err
		}
		return errors.New("second target failed")
	})
	if err == nil || batch.ID == "" {
		t.Fatalf("Apply = (%+v, %v), want persisted partial batch", batch, err)
	}
	if err := service.Restore(batch.ID); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	for _, target := range []string{first, second} {
		data, err := os.ReadFile(target)
		if err != nil || string(data) != "before-"+filepath.Base(target) {
			t.Fatalf("restored %s = %q, err=%v", target, data, err)
		}
	}
}

func TestRestoreResumesAfterAnInterruptedTarget(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	for _, target := range []string{first, second} {
		if err := os.WriteFile(target, []byte("before-"+filepath.Base(target)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	service := New(root)
	batch, err := service.Apply([]Target{{Path: first}, {Path: second}}, func() error {
		for _, target := range []string{first, second} {
			if err := os.WriteFile(target, []byte("after-"+filepath.Base(target)), 0o600); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "lifecycle", batch.ID)
	m, err := readManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a process failure after target one was restored but before its
	// progress marker was persisted. The next restore must complete the batch.
	if err := os.WriteFile(first, []byte("before-first"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.State = "restoring"
	if err := writeManifest(dir, m); err != nil {
		t.Fatal(err)
	}
	if err := service.Restore(batch.ID); err != nil {
		t.Fatalf("resume Restore: %v", err)
	}
	for _, target := range []string{first, second} {
		data, err := os.ReadFile(target)
		if err != nil || string(data) != "before-"+filepath.Base(target) {
			t.Fatalf("resumed restore %s = %q, err=%v", target, data, err)
		}
	}
}

func TestRestoreApplyingBatchUsesPreimages(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := New(root)
	batch, err := service.Apply([]Target{{Path: target}}, func() error {
		return os.WriteFile(target, []byte("after"), 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "lifecycle", batch.ID)
	m, err := readManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	// This is the durable state left if the post-mutation manifest write fails.
	m.State = "applying"
	m.Target[0].After = ""
	if err := writeManifest(dir, m); err != nil {
		t.Fatal(err)
	}
	if err := service.Restore(batch.ID); err != nil {
		t.Fatalf("Restore applying batch: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "before" {
		t.Fatalf("restored target = %q, err=%v", data, err)
	}
}

func TestRestoreRejectsPathTraversalID(t *testing.T) {
	if err := New(t.TempDir()).Restore("../../outside"); err == nil {
		t.Fatal("Restore accepted a path traversal ID")
	}
}

func TestPruneHonorsByteBudget(t *testing.T) {
	root := t.TempDir()
	service := New(root)
	for _, name := range []string{"one", "two"} {
		target := filepath.Join(root, name)
		if err := os.WriteFile(target, make([]byte, 128), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := service.Apply([]Target{{Path: target}}, func() error { return os.WriteFile(target, make([]byte, 128), 0o600) }); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	result, err := service.Prune(recovery.Retention{MaxBytes: 1}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Removed) != 2 || result.Exceeded || result.ReclaimedBytes == 0 {
		t.Fatalf("Prune = %+v, want both eligible batches reclaimed to meet the hard limit", result)
	}
}

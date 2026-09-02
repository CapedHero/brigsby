package recovery_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/CapedHero/brigsby/internal/recovery"
)

func BenchmarkPlanSingleTextFile(b *testing.B) {
	root := b.TempDir()
	target := benchmarkWriteFile(b, root, "harness/skills/release-notes/SKILL.md", "old revision\n")
	replacement := benchmarkWriteFile(b, root, "canonical/release-notes/SKILL.md", "new revision\n")
	service := recovery.New(filepath.Join(root, ".brigsby"))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := service.Plan(target, replacement); err != nil {
			b.Fatalf("plan: %v", err)
		}
	}
}

func benchmarkWriteFile(b *testing.B, root, relativePath, contents string) string {
	b.Helper()
	path := filepath.Join(root, relativePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		b.Fatalf("create parent: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		b.Fatalf("write fixture: %v", err)
	}
	return path
}

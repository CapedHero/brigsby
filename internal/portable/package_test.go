package portable

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestInspectRejectsExecutableAndBinaryMembers(t *testing.T) {
	for _, fixture := range []struct {
		name string
		mode int64
		data []byte
	}{
		{name: "executable", mode: 0o755, data: []byte("echo nope\n")},
		{name: "binary", mode: 0o600, data: []byte{0xff, 0x00}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "unsafe.tar.gz")
			file, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			gzipWriter := gzip.NewWriter(file)
			tarWriter := tar.NewWriter(gzipWriter)
			if err := tarWriter.WriteHeader(&tar.Header{Name: "unsafe", Mode: fixture.mode, Size: int64(len(fixture.data))}); err != nil {
				t.Fatal(err)
			}
			if _, err := tarWriter.Write(fixture.data); err != nil {
				t.Fatal(err)
			}
			if err := tarWriter.Close(); err != nil {
				t.Fatal(err)
			}
			if err := gzipWriter.Close(); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := Inspect(path, ""); err == nil {
				t.Fatal("Inspect accepted unsafe Package member")
			}
		})
	}
}

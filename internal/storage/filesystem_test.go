package storage

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestFilesystemAdapter_UploadAndDownload(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "fs-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	adapter, err := NewFilesystemAdapter(tmpDir, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	data := []byte("hello world")
	objectName := "test/file.txt"

	// Upload
	err = adapter.UploadStream(ctx, objectName, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("upload failed: %v", err)
	}

	// Verify file exists
	fullPath := filepath.Join(tmpDir, objectName)
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		t.Fatal("uploaded file doesn't exist")
	}

	// Download
	reader, err := adapter.CreateDownloadStream(ctx, objectName)
	if err != nil {
		t.Fatalf("download failed: %v", err)
	}
	defer reader.Close()

	downloaded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("failed to read downloaded data: %v", err)
	}

	if !bytes.Equal(data, downloaded) {
		t.Errorf("downloaded data doesn't match: expected %q, got %q", data, downloaded)
	}
}

func TestFilesystemAdapter_DownloadNotFound(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "fs-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	adapter, err := NewFilesystemAdapter(tmpDir, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	_, err = adapter.CreateDownloadStream(ctx, "nonexistent.txt")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}

	var notFoundErr *ObjectNotFoundError
	if _, ok := err.(*ObjectNotFoundError); !ok {
		t.Errorf("expected ObjectNotFoundError, got %T", notFoundErr)
	}
}

func TestFilesystemAdapter_DeleteFolder(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "fs-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	adapter, err := NewFilesystemAdapter(tmpDir, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// Create some files in a folder
	for i := range 3 {
		objectName := filepath.Join("folder", "file"+string(rune('0'+i))+".txt")
		err := adapter.UploadStream(ctx, objectName, bytes.NewReader([]byte("test")))
		if err != nil {
			t.Fatalf("failed to upload: %v", err)
		}
	}

	// Verify folder exists
	folderPath := filepath.Join(tmpDir, "folder")
	if _, err := os.Stat(folderPath); os.IsNotExist(err) {
		t.Fatal("folder doesn't exist before deletion")
	}

	// Delete folder
	err = adapter.DeleteFolder(ctx, "folder")
	if err != nil {
		t.Fatalf("delete folder failed: %v", err)
	}

	// Verify folder is gone
	if _, err := os.Stat(folderPath); !os.IsNotExist(err) {
		t.Error("folder still exists after deletion")
	}
}

func TestFilesystemAdapter_CountFilesInFolder(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "fs-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	adapter, err := NewFilesystemAdapter(tmpDir, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// Create some files in a folder
	for i := range 5 {
		objectName := filepath.Join("parts", "part"+string(rune('0'+i)))
		err := adapter.UploadStream(ctx, objectName, bytes.NewReader([]byte("test")))
		if err != nil {
			t.Fatalf("failed to upload: %v", err)
		}
	}

	count, err := adapter.CountFilesInFolder(ctx, "parts")
	if err != nil {
		t.Fatalf("count files failed: %v", err)
	}

	if count != 5 {
		t.Errorf("expected 5 files, got %d", count)
	}
}

func TestFilesystemAdapter_CountFilesInNonexistentFolder(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "fs-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	adapter, err := NewFilesystemAdapter(tmpDir, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	count, err := adapter.CountFilesInFolder(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("count files failed: %v", err)
	}

	if count != 0 {
		t.Errorf("expected 0 files, got %d", count)
	}
}

func TestFilesystemAdapter_ListFilesInFolder(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "fs-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	adapter, err := NewFilesystemAdapter(tmpDir, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// Create some files in a folder with numeric names (like parts)
	expectedFiles := []string{"0", "1", "2", "3", "4"}
	for _, name := range expectedFiles {
		objectName := filepath.Join("parts", name)
		err := adapter.UploadStream(ctx, objectName, bytes.NewReader([]byte("test")))
		if err != nil {
			t.Fatalf("failed to upload: %v", err)
		}
	}

	files, err := adapter.ListFilesInFolder(ctx, "parts")
	if err != nil {
		t.Fatalf("list files failed: %v", err)
	}

	if len(files) != 5 {
		t.Errorf("expected 5 files, got %d", len(files))
	}

	// Check that all expected files are present (order may vary)
	fileSet := make(map[string]bool)
	for _, f := range files {
		fileSet[f] = true
	}
	for _, expected := range expectedFiles {
		if !fileSet[expected] {
			t.Errorf("expected file %s not found", expected)
		}
	}
}

func TestFilesystemAdapter_ListFilesInFolder_Empty(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "fs-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	adapter, err := NewFilesystemAdapter(tmpDir, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// Create empty folder
	emptyDir := filepath.Join(tmpDir, "emptyparts")
	if err := os.MkdirAll(emptyDir, 0755); err != nil {
		t.Fatal(err)
	}

	files, err := adapter.ListFilesInFolder(ctx, "emptyparts")
	if err != nil {
		t.Fatalf("list files failed: %v", err)
	}

	if len(files) != 0 {
		t.Errorf("expected 0 files, got %d", len(files))
	}
}

func TestFilesystemAdapter_ListFilesInFolder_Nonexistent(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "fs-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	adapter, err := NewFilesystemAdapter(tmpDir, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	files, err := adapter.ListFilesInFolder(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("list files failed: %v", err)
	}

	if files != nil {
		t.Errorf("expected nil for nonexistent folder, got %v", files)
	}
}

func TestFilesystemAdapter_ListFilesInFolder_NonSequential(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "fs-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	adapter, err := NewFilesystemAdapter(tmpDir, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// Create files with non-sequential indices (1-based or with gaps)
	nonSequentialFiles := []string{"1", "2", "3", "5", "10"}
	for _, name := range nonSequentialFiles {
		objectName := filepath.Join("parts", name)
		err := adapter.UploadStream(ctx, objectName, bytes.NewReader([]byte("test")))
		if err != nil {
			t.Fatalf("failed to upload: %v", err)
		}
	}

	files, err := adapter.ListFilesInFolder(ctx, "parts")
	if err != nil {
		t.Fatalf("list files failed: %v", err)
	}

	if len(files) != 5 {
		t.Errorf("expected 5 files, got %d", len(files))
	}

	// Check all expected files are present
	fileSet := make(map[string]bool)
	for _, f := range files {
		fileSet[f] = true
	}
	for _, expected := range nonSequentialFiles {
		if !fileSet[expected] {
			t.Errorf("expected file %s not found", expected)
		}
	}
}

func TestFilesystemAdapter_SignedURL(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "fs-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	adapter, err := NewFilesystemAdapter(tmpDir, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// Filesystem adapter doesn't support signed URLs
	url, err := adapter.CreateDownloadURL(ctx, "test.txt", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url != "" {
		t.Error("expected empty URL for filesystem adapter")
	}
}

func BenchmarkFilesystemUpload(b *testing.B) {
	tmpDir, err := os.MkdirTemp("", "fs-bench-*")
	if err != nil {
		b.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	adapter, err := NewFilesystemAdapter(tmpDir, 1024*1024)
	if err != nil {
		b.Fatal(err)
	}

	ctx := context.Background()
	data := make([]byte, 100*1024*1024) // 100MB

	b.ResetTimer()
	b.SetBytes(int64(len(data)))

	for i := 0; i < b.N; i++ {
		objectName := filepath.Join("bench", "file"+string(rune('0'+i%10)))
		err := adapter.UploadStream(ctx, objectName, bytes.NewReader(data))
		if err != nil {
			b.Fatalf("upload failed: %v", err)
		}
	}
}

func BenchmarkFilesystemDownload(b *testing.B) {
	tmpDir, err := os.MkdirTemp("", "fs-bench-*")
	if err != nil {
		b.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	adapter, err := NewFilesystemAdapter(tmpDir, 1024*1024)
	if err != nil {
		b.Fatal(err)
	}

	ctx := context.Background()
	data := make([]byte, 100*1024*1024) // 100MB
	objectName := "bench/testfile"

	err = adapter.UploadStream(ctx, objectName, bytes.NewReader(data))
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.SetBytes(int64(len(data)))

	for i := 0; i < b.N; i++ {
		reader, err := adapter.CreateDownloadStream(ctx, objectName)
		if err != nil {
			b.Fatalf("download failed: %v", err)
		}
		_, err = io.Copy(io.Discard, reader)
		reader.Close()
		if err != nil {
			b.Fatalf("read failed: %v", err)
		}
	}
}

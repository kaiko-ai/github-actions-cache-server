package handlers

import (
	"encoding/base64"
	"encoding/binary"
	"testing"
)

func TestGetChunkIndexFromBlockID_64Byte(t *testing.T) {
	// Create a 64-byte block ID similar to Docker BuildX format
	// The chunk index is a UInt32BE at offset 16
	data := make([]byte, 64)
	binary.BigEndian.PutUint32(data[16:20], 42)
	blockID := base64.StdEncoding.EncodeToString(data)

	index, err := getChunkIndexFromBlockID(blockID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if index != 42 {
		t.Errorf("expected index 42, got %d", index)
	}
}

func TestGetChunkIndexFromBlockID_48Byte(t *testing.T) {
	// Create a 48-byte block ID with UUID prefix (36 chars) + index
	// UUID format: xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx (36 chars)
	uuid := "12345678-1234-1234-1234-123456789012"
	indexStr := "000000000123" // padded to fill 48 bytes total
	data := uuid + indexStr
	if len(data) != 48 {
		t.Fatalf("test data length should be 48, got %d", len(data))
	}
	blockID := base64.StdEncoding.EncodeToString([]byte(data))

	index, err := getChunkIndexFromBlockID(blockID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if index != 123 {
		t.Errorf("expected index 123, got %d", index)
	}
}

func TestGetChunkIndexFromBlockID_48Byte_FirstChunk(t *testing.T) {
	// Test first chunk (index 0)
	uuid := "12345678-1234-1234-1234-123456789012"
	indexStr := "000000000000" // index 0
	data := uuid + indexStr
	blockID := base64.StdEncoding.EncodeToString([]byte(data))

	index, err := getChunkIndexFromBlockID(blockID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if index != 0 {
		t.Errorf("expected index 0, got %d", index)
	}
}

func TestGetChunkIndexFromBlockID_InvalidBase64(t *testing.T) {
	_, err := getChunkIndexFromBlockID("not-valid-base64!!!")
	if err == nil {
		t.Error("expected error for invalid base64")
	}
}

func TestGetChunkIndexFromBlockID_UnsupportedLength(t *testing.T) {
	// Create data with unsupported length (not 48 or 64)
	data := make([]byte, 32)
	blockID := base64.StdEncoding.EncodeToString(data)

	_, err := getChunkIndexFromBlockID(blockID)
	if err == nil {
		t.Error("expected error for unsupported length")
	}
}

func TestGenerateNumberID(t *testing.T) {
	// Test that generated IDs are within expected range
	for i := 0; i < 100; i++ {
		id := generateNumberID()
		if id < 1000000000 || id > 9999999999 {
			t.Errorf("ID %d is outside valid range [1000000000, 9999999999]", id)
		}
	}
}

func TestGenerateNumberID_Uniqueness(t *testing.T) {
	// Generate many IDs and check for duplicates
	ids := make(map[int64]bool)
	for i := 0; i < 1000; i++ {
		id := generateNumberID()
		if ids[id] {
			t.Errorf("duplicate ID generated: %d", id)
		}
		ids[id] = true
	}
}

func TestCountingReader(t *testing.T) {
	data := []byte("hello world")
	r := &countingReader{r: &mockReader{data: data}}

	buf := make([]byte, 5)
	n, _ := r.Read(buf)
	if n != 5 {
		t.Errorf("expected 5 bytes read, got %d", n)
	}
	if r.count != 5 {
		t.Errorf("expected count 5, got %d", r.count)
	}

	n, _ = r.Read(buf)
	if r.count != 10 {
		t.Errorf("expected count 10, got %d", r.count)
	}
}

type mockReader struct {
	data   []byte
	offset int
}

func (m *mockReader) Read(p []byte) (int, error) {
	if m.offset >= len(m.data) {
		return 0, nil
	}
	n := copy(p, m.data[m.offset:])
	m.offset += n
	return n, nil
}

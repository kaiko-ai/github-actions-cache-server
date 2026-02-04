package main

import (
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
	"unsafe"
)

// DiskBenchmark holds results for disk I/O benchmarks
type DiskBenchmark struct {
	Operation string
	FileSize  int64
	Stats     Stats
}

// RunDiskBenchmarks performs raw disk I/O benchmarks for baseline comparison
func RunDiskBenchmarks(tempDir string, fileSizes []int64, iterations int) ([]DiskBenchmark, error) {
	var results []DiskBenchmark

	for _, size := range fileSizes {
		// Generate random data once
		data := make([]byte, size)
		if _, err := rand.Read(data); err != nil {
			return nil, fmt.Errorf("failed to generate random data: %w", err)
		}

		// Write benchmark
		writeDurations := make([]float64, iterations)
		for i := 0; i < iterations; i++ {
			filePath := filepath.Join(tempDir, fmt.Sprintf("disk-bench-write-%d-%d.bin", size, i))

			start := time.Now()
			f, err := os.Create(filePath)
			if err != nil {
				return nil, fmt.Errorf("failed to create file: %w", err)
			}

			_, err = f.Write(data)
			if err != nil {
				f.Close()
				return nil, fmt.Errorf("failed to write file: %w", err)
			}

			err = f.Sync()
			if err != nil {
				f.Close()
				return nil, fmt.Errorf("failed to sync file: %w", err)
			}

			f.Close()
			writeDurations[i] = time.Since(start).Seconds()

			os.Remove(filePath)
		}

		writeStats, err := ThroughputStats(size, writeDurations)
		if err != nil {
			return nil, err
		}

		results = append(results, DiskBenchmark{
			Operation: "disk-write",
			FileSize:  size,
			Stats:     writeStats,
		})

		// Read benchmark - For each iteration, create a new file with cache bypass,
		// sync to disk, then read it back with cache bypass.
		// This ensures we're measuring actual disk I/O, not page cache.
		readDurations := make([]float64, iterations)
		for i := 0; i < iterations; i++ {
			readFilePath := filepath.Join(tempDir, fmt.Sprintf("disk-bench-read-%d-%d.bin", size, i))

			// Create file with cache bypass to avoid polluting page cache
			if err := createFileWithNoCache(readFilePath, data); err != nil {
				return nil, fmt.Errorf("failed to create read test file: %w", err)
			}

			// Read the file with cache bypass
			duration, err := benchmarkReadWithNoCache(readFilePath, size)
			if err != nil {
				os.Remove(readFilePath)
				return nil, fmt.Errorf("failed to read file: %w", err)
			}
			readDurations[i] = duration

			os.Remove(readFilePath)
		}

		readStats, err := ThroughputStats(size, readDurations)
		if err != nil {
			return nil, err
		}

		results = append(results, DiskBenchmark{
			Operation: "disk-read",
			FileSize:  size,
			Stats:     readStats,
		})
	}

	return results, nil
}

// benchmarkReadWithNoCache reads a file while bypassing the OS page cache
func benchmarkReadWithNoCache(filePath string, size int64) (float64, error) {
	f, err := openWithNoCache(filePath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	// Use aligned buffer for O_DIRECT compatibility on Linux
	// 4KB alignment is required for most filesystems
	const alignment = 4096
	bufSize := alignUp(int(size), alignment)
	buf := makeAlignedBuffer(bufSize, alignment)

	start := time.Now()

	totalRead := 0
	for totalRead < int(size) {
		n, err := f.Read(buf[totalRead:])
		if err != nil {
			// EOF is expected when we've read all data
			if err == io.EOF {
				break
			}
			return 0, fmt.Errorf("read error: %w", err)
		}
		if n == 0 {
			break
		}
		totalRead += n
	}

	return time.Since(start).Seconds(), nil
}

// alignUp rounds n up to the nearest multiple of alignment
func alignUp(n, alignment int) int {
	return (n + alignment - 1) &^ (alignment - 1)
}

// makeAlignedBuffer creates a byte slice with the specified alignment
// This is needed for O_DIRECT on Linux
func makeAlignedBuffer(size, alignment int) []byte {
	// Allocate extra space to ensure we can align
	buf := make([]byte, size+alignment)
	// Find the aligned offset
	offset := alignment - int(uintptr(unsafe.Pointer(&buf[0])) % uintptr(alignment))
	if offset == alignment {
		offset = 0
	}
	return buf[offset : offset+size]
}

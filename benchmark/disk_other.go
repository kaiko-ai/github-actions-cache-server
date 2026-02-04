//go:build !darwin && !linux

package main

import (
	"os"
)

// openWithNoCache opens a file on unsupported platforms (no cache bypass available)
func openWithNoCache(filePath string) (*os.File, error) {
	// On unsupported platforms, just open the file normally
	// This will use cached reads, but the benchmark will still work
	return os.Open(filePath)
}

// createFileWithNoCache creates a file and writes data on unsupported platforms
// No cache bypass is available, so this just writes the file normally
func createFileWithNoCache(filePath string, data []byte) error {
	return os.WriteFile(filePath, data, 0644)
}

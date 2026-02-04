//go:build darwin

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// openWithNoCache opens a file with caching disabled on macOS
func openWithNoCache(filePath string) (*os.File, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}

	// macOS: Use F_NOCACHE to bypass the unified buffer cache
	_, err = unix.FcntlInt(f.Fd(), unix.F_NOCACHE, 1)
	if err != nil {
		f.Close()
		return nil, err
	}

	return f, nil
}

// createFileWithNoCache creates a file and writes data with caching disabled on macOS
// This ensures the data is written directly to disk and not kept in page cache
func createFileWithNoCache(filePath string, data []byte) error {
	f, err := os.Create(filePath)
	if err != nil {
		return err
	}

	// Set F_NOCACHE before writing to prevent data from entering page cache
	_, err = unix.FcntlInt(f.Fd(), unix.F_NOCACHE, 1)
	if err != nil {
		f.Close()
		return err
	}

	_, err = f.Write(data)
	if err != nil {
		f.Close()
		return err
	}

	// Sync to ensure data is on disk
	err = f.Sync()
	if err != nil {
		f.Close()
		return err
	}

	return f.Close()
}

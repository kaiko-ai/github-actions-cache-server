//go:build linux

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// openWithNoCache opens a file with caching disabled on Linux
func openWithNoCache(filePath string) (*os.File, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}

	// Linux: Use posix_fadvise to tell the kernel we don't need the data cached
	// Note: For true O_DIRECT, the file needs to be opened with that flag
	// and buffers must be aligned. FADV_DONTNEED is a best-effort approach.
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}

	err = unix.Fadvise(int(f.Fd()), 0, fi.Size(), unix.FADV_DONTNEED)
	if err != nil {
		f.Close()
		return nil, err
	}

	return f, nil
}

// createFileWithNoCache creates a file and writes data, then invalidates the page cache on Linux
// This ensures subsequent reads will come from disk, not page cache
func createFileWithNoCache(filePath string, data []byte) error {
	f, err := os.Create(filePath)
	if err != nil {
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

	// Tell kernel to drop this file from page cache
	// FADV_DONTNEED: The specified data will not be accessed in the near future
	err = unix.Fadvise(int(f.Fd()), 0, int64(len(data)), unix.FADV_DONTNEED)
	if err != nil {
		f.Close()
		return err
	}

	return f.Close()
}

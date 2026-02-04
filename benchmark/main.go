package main

import (
	"crypto/rand"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func main() {
	// CLI flags
	projectRoot := flag.String("project", "..", "Path to project root")
	fileSizesStr := flag.String("file-sizes", "100,500", "File sizes in MB (comma-separated)")
	bufferSizesStr := flag.String("buffer-sizes", "64,128,256,512,1024", "Buffer sizes in KB (comma-separated)")
	iterations := flag.Int("iterations", 3, "Iterations per benchmark")
	format := flag.String("format", "table", "Output format: table or csv")
	output := flag.String("output", "", "Output file (default: stdout)")

	flag.Parse()

	// Parse file sizes
	fileSizes, err := parseIntList(*fileSizesStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing file sizes: %v\n", err)
		os.Exit(1)
	}
	for i := range fileSizes {
		fileSizes[i] *= 1024 * 1024 // Convert MB to bytes
	}

	// Parse buffer sizes
	bufferSizes, err := parseIntList(*bufferSizesStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing buffer sizes: %v\n", err)
		os.Exit(1)
	}
	for i := range bufferSizes {
		bufferSizes[i] *= 1024 // Convert KB to bytes
	}

	// Resolve project root
	absProjectRoot, err := filepath.Abs(*projectRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving project root: %v\n", err)
		os.Exit(1)
	}

	// Create temp directory
	tempDir, err := os.MkdirTemp("", "cache-benchmark-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating temp directory: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tempDir)

	fmt.Fprintf(os.Stderr, "Benchmark configuration:\n")
	fmt.Fprintf(os.Stderr, "  Project root: %s\n", absProjectRoot)
	fmt.Fprintf(os.Stderr, "  Temp directory: %s\n", tempDir)
	fmt.Fprintf(os.Stderr, "  File sizes: %v MB\n", *fileSizesStr)
	fmt.Fprintf(os.Stderr, "  Buffer sizes: %v KB\n", *bufferSizesStr)
	fmt.Fprintf(os.Stderr, "  Iterations: %d\n", *iterations)
	fmt.Fprintf(os.Stderr, "\n")

	report := &Report{}

	// Step 1: Run disk baselines
	fmt.Fprintf(os.Stderr, "Running disk baselines...\n")
	int64FileSizes := make([]int64, len(fileSizes))
	for i, s := range fileSizes {
		int64FileSizes[i] = int64(s)
	}

	diskResults, err := RunDiskBenchmarks(tempDir, int64FileSizes, *iterations)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error running disk benchmarks: %v\n", err)
		os.Exit(1)
	}
	report.DiskBaselines = diskResults

	for _, r := range diskResults {
		fmt.Fprintf(os.Stderr, "  %s %s: %.2f MB/s\n", r.Operation, formatSize(r.FileSize), r.Stats.Mean)
	}
	fmt.Fprintf(os.Stderr, "\n")

	// Step 2: Build server
	fmt.Fprintf(os.Stderr, "Building server...\n")
	server := NewServerManager(absProjectRoot, tempDir)
	if err := server.Build(); err != nil {
		fmt.Fprintf(os.Stderr, "Error building server: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "Server built successfully.\n\n")

	// Step 3: Run server benchmarks for each buffer size
	client := NewCacheClient(server.BaseURL())

	for _, bufferSize := range bufferSizes {
		fmt.Fprintf(os.Stderr, "Testing buffer size: %s\n", formatSize(int64(bufferSize)))

		// Start server with this buffer size
		if err := server.Start(bufferSize); err != nil {
			fmt.Fprintf(os.Stderr, "Error starting server: %v\n", err)
			os.Exit(1)
		}

		// Run benchmarks for each file size
		for _, fileSize := range fileSizes {
			fmt.Fprintf(os.Stderr, "  File size: %s\n", formatSize(int64(fileSize)))

			// Generate test data
			data := make([]byte, fileSize)
			if _, err := rand.Read(data); err != nil {
				fmt.Fprintf(os.Stderr, "Error generating test data: %v\n", err)
				server.Stop()
				os.Exit(1)
			}

			// Upload benchmarks
			uploadDurations := make([]float64, *iterations)
			for i := 0; i < *iterations; i++ {
				key := fmt.Sprintf("bench-%d-%d-%d-up", bufferSize, fileSize, i)
				version := "v1"

				duration, err := client.UploadCache(key, version, data)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error uploading: %v\n", err)
					server.Stop()
					os.Exit(1)
				}
				uploadDurations[i] = duration
			}

			uploadStats, err := ThroughputStats(int64(fileSize), uploadDurations)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error calculating upload stats: %v\n", err)
				server.Stop()
				os.Exit(1)
			}

			report.ServerResults = append(report.ServerResults, BenchmarkResult{
				Operation:  "upload",
				FileSize:   int64(fileSize),
				BufferSize: bufferSize,
				Stats:      uploadStats,
			})
			fmt.Fprintf(os.Stderr, "    Upload: %.2f MB/s\n", uploadStats.Mean)

			// Download benchmarks - use the last uploaded cache
			downloadDurations := make([]float64, *iterations)
			lastKey := fmt.Sprintf("bench-%d-%d-%d-up", bufferSize, fileSize, *iterations-1)

			for i := 0; i < *iterations; i++ {
				downloaded, duration, err := client.DownloadCache(lastKey, "v1")
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error downloading: %v\n", err)
					server.Stop()
					os.Exit(1)
				}
				if len(downloaded) != fileSize {
					fmt.Fprintf(os.Stderr, "Downloaded size mismatch: got %d, expected %d\n", len(downloaded), fileSize)
					server.Stop()
					os.Exit(1)
				}
				downloadDurations[i] = duration
			}

			downloadStats, err := ThroughputStats(int64(fileSize), downloadDurations)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error calculating download stats: %v\n", err)
				server.Stop()
				os.Exit(1)
			}

			report.ServerResults = append(report.ServerResults, BenchmarkResult{
				Operation:  "download",
				FileSize:   int64(fileSize),
				BufferSize: bufferSize,
				Stats:      downloadStats,
			})
			fmt.Fprintf(os.Stderr, "    Download: %.2f MB/s\n", downloadStats.Mean)

			// Clean up storage between file sizes
			if err := server.CleanupStorage(); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to cleanup storage: %v\n", err)
			}
		}

		// Stop server
		if err := server.Stop(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: error stopping server: %v\n", err)
		}
		fmt.Fprintf(os.Stderr, "\n")
	}

	// Step 4: Calculate overhead and generate report
	report.CalculateOverhead()
	report.SortResults()

	// Output results
	var out *os.File
	if *output != "" {
		out, err = os.Create(*output)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating output file: %v\n", err)
			os.Exit(1)
		}
		defer out.Close()
	} else {
		out = os.Stdout
	}

	fmt.Fprintf(os.Stderr, "\n")
	switch *format {
	case "csv":
		report.WriteCSV(out)
	default:
		report.WriteTable(out)
	}
}

func parseIntList(s string) ([]int, error) {
	parts := strings.Split(s, ",")
	result := make([]int, 0, len(parts))

	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("invalid number: %s", p)
		}
		result = append(result, n)
	}

	return result, nil
}

package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// BenchmarkResult holds results for a server benchmark run
type BenchmarkResult struct {
	Operation  string
	FileSize   int64
	BufferSize int
	Stats      Stats
	Overhead   float64 // Percentage overhead compared to disk baseline
}

// Report generates benchmark reports
type Report struct {
	DiskBaselines []DiskBenchmark
	ServerResults []BenchmarkResult
}

// WriteTable writes the report in table format
func (r *Report) WriteTable(w io.Writer) {
	r.writeDiskBaselines(w)
	fmt.Fprintln(w)
	r.writeServerResults(w)
	fmt.Fprintln(w)
	r.writeAnalysis(w)
}

// WriteCSV writes the report in CSV format
func (r *Report) WriteCSV(w io.Writer) {
	// Header
	fmt.Fprintln(w, "type,operation,file_size_mb,buffer_size_kb,mean_mbps,stddev,min_mbps,max_mbps,p50_mbps,p95_mbps,p99_mbps,overhead_pct")

	// Disk baselines
	for _, b := range r.DiskBaselines {
		fmt.Fprintf(w, "disk,%s,%d,,%0.2f,%0.2f,%0.2f,%0.2f,%0.2f,%0.2f,%0.2f,\n",
			b.Operation,
			b.FileSize/(1024*1024),
			b.Stats.Mean,
			b.Stats.StdDev,
			b.Stats.Min,
			b.Stats.Max,
			b.Stats.P50,
			b.Stats.P95,
			b.Stats.P99,
		)
	}

	// Server results
	for _, r := range r.ServerResults {
		fmt.Fprintf(w, "server,%s,%d,%d,%0.2f,%0.2f,%0.2f,%0.2f,%0.2f,%0.2f,%0.2f,%0.1f\n",
			r.Operation,
			r.FileSize/(1024*1024),
			r.BufferSize/1024,
			r.Stats.Mean,
			r.Stats.StdDev,
			r.Stats.Min,
			r.Stats.Max,
			r.Stats.P50,
			r.Stats.P95,
			r.Stats.P99,
			r.Overhead,
		)
	}
}

func (r *Report) writeDiskBaselines(w io.Writer) {
	fmt.Fprintln(w, "=== Disk Baselines ===")
	fmt.Fprintf(w, "%-12s %-10s %-12s %-12s %s\n",
		"Operation", "File Size", "Mean (MB/s)", "StdDev", "Min-Max")
	fmt.Fprintln(w, strings.Repeat("-", 70))

	for _, b := range r.DiskBaselines {
		fmt.Fprintf(w, "%-12s %-10s %-12.2f %-12.2f %.2f - %.2f\n",
			b.Operation,
			formatSize(b.FileSize),
			b.Stats.Mean,
			b.Stats.StdDev,
			b.Stats.Min,
			b.Stats.Max,
		)
	}
}

func (r *Report) writeServerResults(w io.Writer) {
	fmt.Fprintln(w, "=== Server Throughput ===")
	fmt.Fprintf(w, "%-10s %-10s %-10s %-12s %-12s %s\n",
		"Operation", "File Size", "Buffer", "Mean (MB/s)", "StdDev", "Overhead")
	fmt.Fprintln(w, strings.Repeat("-", 75))

	for _, res := range r.ServerResults {
		overheadStr := fmt.Sprintf("%+.1f%%", res.Overhead)
		fmt.Fprintf(w, "%-10s %-10s %-10s %-12.2f %-12.2f %s\n",
			res.Operation,
			formatSize(res.FileSize),
			formatSize(int64(res.BufferSize)),
			res.Stats.Mean,
			res.Stats.StdDev,
			overheadStr,
		)
	}
}

func (r *Report) writeAnalysis(w io.Writer) {
	fmt.Fprintln(w, "=== Analysis ===")

	// Find best throughput for each operation
	bestUpload := r.findBest("upload")
	bestDownload := r.findBest("download")

	if bestUpload != nil {
		fmt.Fprintf(w, "Best upload throughput: %.2f MB/s with %s buffer\n",
			bestUpload.Stats.Mean, formatSize(int64(bestUpload.BufferSize)))
	}
	if bestDownload != nil {
		fmt.Fprintf(w, "Best download throughput: %.2f MB/s with %s buffer\n",
			bestDownload.Stats.Mean, formatSize(int64(bestDownload.BufferSize)))
	}

	// Find high overhead cases
	var warnings []BenchmarkResult
	for _, res := range r.ServerResults {
		if res.Overhead > 20 {
			warnings = append(warnings, res)
		}
	}

	if len(warnings) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "WARNING: High overhead detected (>20% slower than raw disk):")
		for _, warn := range warnings {
			fmt.Fprintf(w, "  - %s (%s, %s buffer): %.1f%% overhead\n",
				warn.Operation,
				formatSize(warn.FileSize),
				formatSize(int64(warn.BufferSize)),
				warn.Overhead,
			)
		}

		fmt.Fprintln(w)
		fmt.Fprintln(w, "Optimization suggestions:")
		fmt.Fprintln(w, "  1. Increase STORAGE_HIGH_WATER_MARK (try 1MB or higher)")
		fmt.Fprintln(w, "  2. Check if disk is SSD vs HDD")
		fmt.Fprintln(w, "  3. Review TCP buffer sizes")
		fmt.Fprintln(w, "  4. Consider ENABLE_DIRECT_DOWNLOADS=true")
	} else {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "All benchmarks show acceptable overhead (<= 20%).")
	}
}

func (r *Report) findBest(operation string) *BenchmarkResult {
	var best *BenchmarkResult
	for i := range r.ServerResults {
		res := &r.ServerResults[i]
		if res.Operation == operation {
			if best == nil || res.Stats.Mean > best.Stats.Mean {
				best = res
			}
		}
	}
	return best
}

// CalculateOverhead computes overhead percentages by comparing server results to disk baselines
func (r *Report) CalculateOverhead() {
	// Build lookup map for disk baselines
	diskWrite := make(map[int64]float64)
	diskRead := make(map[int64]float64)

	for _, b := range r.DiskBaselines {
		if b.Operation == "disk-write" {
			diskWrite[b.FileSize] = b.Stats.Mean
		} else if b.Operation == "disk-read" {
			diskRead[b.FileSize] = b.Stats.Mean
		}
	}

	// Calculate overhead for each server result
	for i := range r.ServerResults {
		res := &r.ServerResults[i]
		var baseline float64

		if res.Operation == "upload" {
			baseline = diskWrite[res.FileSize]
		} else if res.Operation == "download" {
			baseline = diskRead[res.FileSize]
		}

		if baseline > 0 {
			// Overhead = (baseline - actual) / baseline * 100
			// Positive overhead means slower than baseline
			res.Overhead = (baseline - res.Stats.Mean) / baseline * 100
		}
	}
}

// SortResults sorts server results by operation, file size, then buffer size
func (r *Report) SortResults() {
	sort.Slice(r.ServerResults, func(i, j int) bool {
		a, b := r.ServerResults[i], r.ServerResults[j]
		if a.Operation != b.Operation {
			return a.Operation < b.Operation
		}
		if a.FileSize != b.FileSize {
			return a.FileSize < b.FileSize
		}
		return a.BufferSize < b.BufferSize
	})
}

func formatSize(bytes int64) string {
	if bytes >= 1024*1024 {
		return fmt.Sprintf("%dMB", bytes/(1024*1024))
	}
	return fmt.Sprintf("%dKB", bytes/1024)
}

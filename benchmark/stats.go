package main

import (
	"github.com/montanaflynn/stats"
)

// Stats holds statistical measurements for a set of samples
type Stats struct {
	Min    float64
	Max    float64
	Mean   float64
	Median float64
	StdDev float64
	P50    float64
	P95    float64
	P99    float64
}

// CalculateStats computes statistical measures from a slice of float64 values
func CalculateStats(data []float64) (Stats, error) {
	if len(data) == 0 {
		return Stats{}, nil
	}

	s := Stats{}
	var err error

	s.Min, err = stats.Min(data)
	if err != nil {
		return s, err
	}

	s.Max, err = stats.Max(data)
	if err != nil {
		return s, err
	}

	s.Mean, err = stats.Mean(data)
	if err != nil {
		return s, err
	}

	s.Median, err = stats.Median(data)
	if err != nil {
		return s, err
	}

	s.StdDev, err = stats.StandardDeviation(data)
	if err != nil {
		return s, err
	}

	s.P50, err = stats.Percentile(data, 50)
	if err != nil {
		return s, err
	}

	s.P95, err = stats.Percentile(data, 95)
	if err != nil {
		return s, err
	}

	s.P99, err = stats.Percentile(data, 99)
	if err != nil {
		return s, err
	}

	return s, nil
}

// ThroughputStats calculates throughput statistics from duration samples
// sizeBytes is the data size, durations are in seconds
func ThroughputStats(sizeBytes int64, durations []float64) (Stats, error) {
	throughputs := make([]float64, len(durations))
	sizeMB := float64(sizeBytes) / (1024 * 1024)

	for i, d := range durations {
		if d > 0 {
			throughputs[i] = sizeMB / d // MB/s
		}
	}

	return CalculateStats(throughputs)
}

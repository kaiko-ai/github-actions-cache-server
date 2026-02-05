// Package metrics provides OpenTelemetry metrics for the cache server.
package metrics

import (
	"context"
	"net/http"
	"strings"
	"time"

	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Metrics holds all the metrics instruments.
type Metrics struct {
	// HTTP metrics
	HTTPRequestDuration metric.Float64Histogram
	HTTPRequestsTotal   metric.Int64Counter

	// Cache metrics
	CacheOperationsTotal metric.Int64Counter

	// Storage metrics
	StorageOperationsTotal    metric.Int64Counter
	StorageOperationDuration  metric.Float64Histogram
	CacheBytesUploadedTotal   metric.Int64Counter
	CacheBytesDownloadedTotal metric.Int64Counter

	// Database metrics
	DBQueryDuration metric.Float64Histogram
	DBQueriesTotal  metric.Int64Counter

	meter  metric.Meter
	tracer trace.Tracer
}

// Config holds metrics configuration.
type Config struct {
	Enabled bool
}

var globalMetrics *Metrics
var prometheusRegistry *promclient.Registry

// Init initializes OpenTelemetry with autoexport and Prometheus.
func Init(ctx context.Context, cfg Config) (shutdown func(context.Context) error, err error) {
	if !cfg.Enabled {
		// Return a no-op shutdown function
		return func(context.Context) error { return nil }, nil
	}

	var shutdownFuncs []func(context.Context) error

	// Create Prometheus registry for /metrics endpoint
	prometheusRegistry = promclient.NewRegistry()
	prometheusExporter, err := otelprom.New(otelprom.WithRegisterer(prometheusRegistry))
	if err != nil {
		return nil, err
	}

	// Metrics with both autoexport and Prometheus
	metricReader, err := autoexport.NewMetricReader(ctx)
	if err != nil {
		// If autoexport fails (e.g., no OTEL_* env vars), just use Prometheus
		meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(prometheusExporter))
		otel.SetMeterProvider(meterProvider)
		shutdownFuncs = append(shutdownFuncs, meterProvider.Shutdown)
	} else {
		// Use both autoexport and Prometheus
		meterProvider := sdkmetric.NewMeterProvider(
			sdkmetric.WithReader(metricReader),
			sdkmetric.WithReader(prometheusExporter),
		)
		otel.SetMeterProvider(meterProvider)
		shutdownFuncs = append(shutdownFuncs, meterProvider.Shutdown)
	}

	// Traces
	spanExporter, err := autoexport.NewSpanExporter(ctx)
	if err != nil {
		// If autoexport fails, skip tracing
	} else {
		tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(spanExporter))
		otel.SetTracerProvider(tracerProvider)
		shutdownFuncs = append(shutdownFuncs, tracerProvider.Shutdown)
	}

	// Initialize metrics instruments
	globalMetrics, err = newMetrics()
	if err != nil {
		return nil, err
	}

	return func(ctx context.Context) error {
		for _, fn := range shutdownFuncs {
			if err := fn(ctx); err != nil {
				return err
			}
		}
		return nil
	}, nil
}

func newMetrics() (*Metrics, error) {
	meter := otel.Meter("github-actions-cache-server")
	tracer := otel.Tracer("github-actions-cache-server")

	m := &Metrics{
		meter:  meter,
		tracer: tracer,
	}

	var err error

	// HTTP metrics
	m.HTTPRequestDuration, err = meter.Float64Histogram(
		"http_request_duration_seconds",
		metric.WithDescription("HTTP request duration in seconds"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}

	m.HTTPRequestsTotal, err = meter.Int64Counter(
		"http_requests_total",
		metric.WithDescription("Total HTTP requests"),
	)
	if err != nil {
		return nil, err
	}

	// Cache metrics
	m.CacheOperationsTotal, err = meter.Int64Counter(
		"cache_operations_total",
		metric.WithDescription("Total cache operations"),
	)
	if err != nil {
		return nil, err
	}

	// Storage metrics
	m.StorageOperationsTotal, err = meter.Int64Counter(
		"storage_operations_total",
		metric.WithDescription("Total storage operations"),
	)
	if err != nil {
		return nil, err
	}

	m.StorageOperationDuration, err = meter.Float64Histogram(
		"storage_operation_duration_seconds",
		metric.WithDescription("Storage operation duration in seconds"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}

	m.CacheBytesUploadedTotal, err = meter.Int64Counter(
		"cache_bytes_uploaded_total",
		metric.WithDescription("Total bytes uploaded to cache"),
		metric.WithUnit("By"),
	)
	if err != nil {
		return nil, err
	}

	m.CacheBytesDownloadedTotal, err = meter.Int64Counter(
		"cache_bytes_downloaded_total",
		metric.WithDescription("Total bytes downloaded from cache"),
		metric.WithUnit("By"),
	)
	if err != nil {
		return nil, err
	}

	// Database metrics
	m.DBQueryDuration, err = meter.Float64Histogram(
		"db_query_duration_seconds",
		metric.WithDescription("Database query duration in seconds"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}

	m.DBQueriesTotal, err = meter.Int64Counter(
		"db_queries_total",
		metric.WithDescription("Total database queries"),
	)
	if err != nil {
		return nil, err
	}

	return m, nil
}

// Get returns the global metrics instance.
func Get() *Metrics {
	return globalMetrics
}

// PrometheusHandler returns an HTTP handler for Prometheus metrics.
func PrometheusHandler() http.Handler {
	if prometheusRegistry == nil {
		return nil
	}
	return promhttp.HandlerFor(prometheusRegistry, promhttp.HandlerOpts{})
}

// RecordHTTPRequest records an HTTP request.
func (m *Metrics) RecordHTTPRequest(ctx context.Context, method, route string, status int, duration time.Duration) {
	if m == nil {
		return
	}

	attrs := []attribute.KeyValue{
		attribute.String("method", method),
		attribute.String("route", normalizeRoute(route)),
		attribute.Int("status", status),
	}

	m.HTTPRequestDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(attrs...))

	statusClass := getStatusClass(status)
	m.HTTPRequestsTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("method", method),
		attribute.String("route", normalizeRoute(route)),
		attribute.String("status_class", statusClass),
	))
}

// RecordCacheOperation records a cache operation.
func (m *Metrics) RecordCacheOperation(ctx context.Context, operation, result string) {
	if m == nil {
		return
	}

	m.CacheOperationsTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("operation", operation),
		attribute.String("result", result),
	))
}

// RecordStorageOperation records a storage operation.
func (m *Metrics) RecordStorageOperation(ctx context.Context, operation, adapter string, duration time.Duration) {
	if m == nil {
		return
	}

	attrs := []attribute.KeyValue{
		attribute.String("operation", operation),
		attribute.String("adapter", adapter),
	}

	m.StorageOperationsTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
	m.StorageOperationDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(attrs...))
}

// RecordBytesUploaded records bytes uploaded.
func (m *Metrics) RecordBytesUploaded(ctx context.Context, bytes int64, operation, adapter, route string) {
	if m == nil {
		return
	}

	m.CacheBytesUploadedTotal.Add(ctx, bytes, metric.WithAttributes(
		attribute.String("operation", operation),
		attribute.String("adapter", adapter),
		attribute.String("route", normalizeRoute(route)),
	))
}

// RecordBytesDownloaded records bytes downloaded.
func (m *Metrics) RecordBytesDownloaded(ctx context.Context, bytes int64, operation, adapter, route string) {
	if m == nil {
		return
	}

	m.CacheBytesDownloadedTotal.Add(ctx, bytes, metric.WithAttributes(
		attribute.String("operation", operation),
		attribute.String("adapter", adapter),
		attribute.String("route", normalizeRoute(route)),
	))
}

// RecordDBQuery records a database query.
func (m *Metrics) RecordDBQuery(ctx context.Context, table string, duration time.Duration) {
	if m == nil {
		return
	}

	attrs := []attribute.KeyValue{
		attribute.String("table", table),
	}

	m.DBQueriesTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
	m.DBQueryDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(attrs...))
}

// StartSpan starts a new trace span.
func (m *Metrics) StartSpan(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	if m == nil {
		return ctx, trace.SpanFromContext(ctx)
	}
	return m.tracer.Start(ctx, name, opts...)
}

// HTTPMiddleware returns an HTTP middleware for request tracing.
func HTTPMiddleware(handler http.Handler) http.Handler {
	otelHandler := otelhttp.NewHandler(handler, "http.request")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip metrics endpoint to avoid recursion
		if strings.HasPrefix(r.URL.Path, "/metrics") {
			otelHandler.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		otelHandler.ServeHTTP(rec, r)

		if m := Get(); m != nil {
			m.RecordHTTPRequest(r.Context(), r.Method, r.URL.Path, rec.status, time.Since(start))
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// normalizeRoute normalizes route paths for metrics.
func normalizeRoute(route string) string {
	// Normalize /download/{id} and /upload/{id}
	if strings.HasPrefix(route, "/download/") {
		return "/download/:cacheEntryId"
	}
	if strings.HasPrefix(route, "/upload/") {
		return "/upload/:uploadId"
	}
	return route
}

// getStatusClass returns the status class (2xx, 3xx, 4xx, 5xx).
func getStatusClass(status int) string {
	switch {
	case status >= 500:
		return "5xx"
	case status >= 400:
		return "4xx"
	case status >= 300:
		return "3xx"
	case status >= 200:
		return "2xx"
	default:
		return "1xx"
	}
}

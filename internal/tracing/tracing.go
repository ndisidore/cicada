// Package tracing initialises an OpenTelemetry TracerProvider for cicada and
// provides a BuildKit vertex span observer.
package tracing

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// _serviceName is the OTEL service.name attribute value.
const _serviceName = "cicada"

// _version is set at link time via -ldflags "-X ...".
var _version = "dev"

// Config holds tracing configuration.
type Config struct {
	// Endpoint is the OTLP gRPC endpoint (e.g. "localhost:4317").
	// Also honoured via OTEL_EXPORTER_OTLP_ENDPOINT.
	Endpoint string
	// File is a path to write JSON spans to; empty disables this exporter.
	File string
	// Stdout writes JSON spans to stderr when true (debug mode).
	Stdout bool
}

// Setup initialises the global TracerProvider from cfg.
// Returns the noop tracer and a no-op shutdown when no exporters are configured.
// The returned shutdown must be called before process exit to flush spans.
func Setup(ctx context.Context, cfg Config) (trace.Tracer, func(context.Context) error, error) {
	exporters, closeFiles, err := buildExporters(ctx, cfg)
	if err != nil {
		return noop.NewTracerProvider().Tracer(_serviceName), func(context.Context) error { return nil }, fmt.Errorf("Setup: %w", err)
	}
	if len(exporters) == 0 {
		return noop.NewTracerProvider().Tracer(_serviceName), func(context.Context) error { return nil }, nil
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(_serviceName),
			semconv.ServiceVersion(_version),
		),
	)
	if err != nil {
		for _, exp := range exporters {
			_ = exp.Shutdown(ctx)
		}
		closeFiles()
		return nil, nil, fmt.Errorf("building otel resource: %w", err)
	}

	opts := make([]sdktrace.TracerProviderOption, 0, len(exporters)+1)
	opts = append(opts, sdktrace.WithResource(res))
	for _, exp := range exporters {
		opts = append(opts, sdktrace.WithBatcher(exp))
	}
	tp := sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(tp)

	shutdown := func(sctx context.Context) error {
		shutdownErr := tp.Shutdown(sctx)
		closeFiles()
		return shutdownErr
	}
	return tp.Tracer(_serviceName), shutdown, nil
}

// buildExporters constructs all configured span exporters and returns a cleanup
// func that closes any opened files.
//
//revive:disable-next-line:cognitive-complexity buildExporters is a linear exporter-init sequence with per-exporter error cleanup; splitting it hurts readability.
func buildExporters(ctx context.Context, cfg Config) ([]sdktrace.SpanExporter, func(), error) {
	var exporters []sdktrace.SpanExporter
	var files []*os.File

	closeFiles := func() {
		for _, f := range files {
			_ = f.Close()
		}
	}

	// failWith shuts down any already-started exporters and closes open files
	// before returning an error, preventing resource leaks on partial init.
	failWith := func(err error) ([]sdktrace.SpanExporter, func(), error) {
		for _, exp := range exporters {
			_ = exp.Shutdown(ctx)
		}
		closeFiles()
		return nil, nil, err
	}

	if cfg.Endpoint != "" {
		// Full URLs (with path components) require WithEndpointURL; bare host:port uses WithEndpoint.
		// Insecure transport applies only when the scheme is explicitly "http://".
		var grpcOpts []otlptracegrpc.Option
		switch {
		case strings.HasPrefix(cfg.Endpoint, "http://"):
			grpcOpts = []otlptracegrpc.Option{
				otlptracegrpc.WithEndpointURL(cfg.Endpoint),
				otlptracegrpc.WithInsecure(),
			}
		case strings.HasPrefix(cfg.Endpoint, "https://"):
			grpcOpts = []otlptracegrpc.Option{otlptracegrpc.WithEndpointURL(cfg.Endpoint)}
		default:
			grpcOpts = []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(cfg.Endpoint)}
		}
		grpcOpts = append(grpcOpts, otlptracegrpc.WithTimeout(10*time.Second))
		exp, err := otlptracegrpc.New(ctx, grpcOpts...)
		if err != nil {
			return failWith(fmt.Errorf("creating otlp grpc exporter: %w", err))
		}
		exporters = append(exporters, exp)
	}

	if cfg.File != "" {
		f, err := os.OpenFile(cfg.File, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // G304: path is user-supplied CLI flag, not a taint source
		if err != nil {
			return failWith(fmt.Errorf("opening trace file %s: %w", cfg.File, err))
		}
		files = append(files, f)
		exp, err := stdouttrace.New(stdouttrace.WithWriter(f))
		if err != nil {
			return failWith(fmt.Errorf("creating file trace exporter: %w", err))
		}
		exporters = append(exporters, exp)
	}

	if cfg.Stdout {
		exp, err := stdouttrace.New(stdouttrace.WithWriter(os.Stderr))
		if err != nil {
			return failWith(fmt.Errorf("creating stderr trace exporter: %w", err))
		}
		exporters = append(exporters, exp)
	}

	return exporters, closeFiles, nil
}

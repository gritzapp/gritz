package otelx

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Provider manages the OpenTelemetry trace and metric providers.
type Provider struct {
	tp *sdktrace.TracerProvider
	mp *sdkmetric.MeterProvider
}

// Shutdown gracefully shuts down the trace and metric providers.
func (p *Provider) Shutdown(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var errs []error
	if p.tp != nil {
		if err := p.tp.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if p.mp != nil {
		if err := p.mp.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Setup initializes OpenTelemetry tracing and metrics.
// The OTLP exporters are configured via standard OTel environment variables
// (OTEL_EXPORTER_OTLP_ENDPOINT, OTEL_EXPORTER_OTLP_INSECURE, etc.).
// If OTEL_EXPORTER_OTLP_ENDPOINT is not set, Setup is a no-op.
func Setup(ctx context.Context) (*Provider, error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return &Provider{}, nil
	}
	traceExporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	metricExporter, err := otlpmetrichttp.New(ctx)
	if err != nil {
		return nil, err
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		// Drop the peer attributes from otelconnect's RPC metrics. On the server,
		// net.peer.port is the client's ephemeral port, so leaving it in mints a new
		// metric stream per connection, and cumulative temporality re-exports every
		// stream forever. Views only apply to metrics, so spans keep the attributes.
		sdkmetric.WithView(sdkmetric.NewView(
			sdkmetric.Instrument{Scope: instrumentation.Scope{Name: "connectrpc.com/otelconnect"}},
			sdkmetric.Stream{
				AttributeFilter: attribute.NewDenyKeysFilter("net.peer.name", "net.peer.port"),
			},
		)),
	)
	otel.SetMeterProvider(mp)

	return &Provider{tp: tp, mp: mp}, nil
}

// TraceResponseHeader is middleware that writes the OTel trace ID to a Traceresponse header.
// It must be wrapped by otelhttp.NewHandler so the span context is available.
func TraceResponseHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sc := trace.SpanFromContext(r.Context()).SpanContext()
		if sc.HasTraceID() {
			w.Header().Set("Traceresponse", fmt.Sprintf("00-%s-%s-%s", sc.TraceID(), sc.SpanID(), sc.TraceFlags()))
		}
		next.ServeHTTP(w, r)
	})
}

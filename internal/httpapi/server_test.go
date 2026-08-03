package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

const serverTestIDBody = "aaaaaaaaaaaaaaaaaaaaaaaaaa"

type testBackend struct {
	models      []core.Model
	modelsCalls atomic.Int32
	respond     func(context.Context, core.Request) (core.Result, error)
}

func (b *testBackend) Models() []core.Model {
	b.modelsCalls.Add(1)
	return b.models
}

func (b *testBackend) Respond(ctx context.Context, request core.Request) (core.Result, error) {
	if b.respond == nil {
		return core.Result{Text: "ok"}, nil
	}
	return b.respond(ctx, request)
}

type fixedIDs struct {
	unsafe bool
	suffix string
}

func (s *fixedIDs) Next(prefix string) string {
	if s.unsafe {
		return prefix + "_unsafe\nheader"
	}
	if s.suffix != "" {
		return prefix + "_" + s.suffix
	}
	return prefix + "_" + serverTestIDBody
}

type testCounters struct {
	count atomic.Int32
}

func (c *testCounters) ClientCanceled() {
	c.count.Add(1)
}

func validServerConfig() Config {
	return Config{
		Listen:            "127.0.0.1:8080",
		HTTPBodyBytes:     1 << 20,
		RequestLimits:     DefaultRequestLimits(),
		HandlerLimit:      4,
		BodyReaderLimit:   2,
		MaxHeaderBytes:    16 << 10,
		ReadHeaderTimeout: time.Second,
		BodyReadTimeout:   time.Second,
		IdleTimeout:       time.Second,
		FinalBytes:        1 << 20,
		MaxModels:         16,
	}
}

func validServerBackend() *testBackend {
	return &testBackend{models: []core.Model{{
		ID:            "codex-default",
		Provider:      core.ProviderCodex,
		ProviderModel: "gpt-test",
		Created:       7,
	}}}
}

func validServerDependencies() (Dependencies, *testCounters) {
	counters := &testCounters{}
	return Dependencies{
		Now:       func() time.Time { return time.Unix(1_785_369_600, 0) },
		LookupEnv: func(string) (string, bool) { return "", false },
		IDs:       &fixedIDs{},
		Counters:  counters,
	}, counters
}

func newTestServer(
	t *testing.T,
	cfg Config,
	backend Backend,
	deps Dependencies,
	logBuffer *bytes.Buffer,
) (*http.Server, http.Handler) {
	t.Helper()
	if logBuffer == nil {
		logBuffer = &bytes.Buffer{}
	}
	logger := slog.New(slog.NewTextHandler(logBuffer, nil))
	server, handler, err := New(cfg, deps, backend, logger)
	if err != nil {
		t.Fatal(err)
	}
	return server, handler
}

func TestNewServerValidatesConfigurationDependenciesAndModels(t *testing.T) {
	t.Run("constructs exact bounded server and snapshots models once", func(t *testing.T) {
		cfg := validServerConfig()
		deps, _ := validServerDependencies()
		backend := validServerBackend()
		server, handler := newTestServer(t, cfg, backend, deps, nil)

		if backend.modelsCalls.Load() != 1 {
			t.Fatalf("models calls=%d", backend.modelsCalls.Load())
		}
		if server.Handler != handler || server.Addr != cfg.Listen ||
			server.ReadHeaderTimeout != cfg.ReadHeaderTimeout ||
			server.IdleTimeout != cfg.IdleTimeout ||
			server.MaxHeaderBytes != cfg.MaxHeaderBytes ||
			server.ReadTimeout != 0 || server.WriteTimeout != 0 ||
			!server.DisableGeneralOptionsHandler || server.ErrorLog == nil {
			t.Fatalf("server=%+v handler=%T", server, handler)
		}

		backend.models[0].ID = "mutated"
		backend.models = append(backend.models, core.Model{
			ID: "later", Provider: core.ProviderCodex, ProviderModel: "later",
		})
		request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:8080/v1/models", nil)
		request.Host = cfg.Listen
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "mutated") ||
			strings.Contains(response.Body.String(), "later") ||
			!strings.Contains(response.Body.String(), "codex-default") {
			t.Fatalf("body=%s", response.Body.String())
		}
		if backend.modelsCalls.Load() != 1 {
			t.Fatalf("models calls after GET=%d", backend.modelsCalls.Load())
		}
	})

	t.Run("rejects invalid values without panicking", func(t *testing.T) {
		base := validServerConfig()
		deps, _ := validServerDependencies()
		backend := validServerBackend()
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))

		invalid := []struct {
			name   string
			mutate func(*Config)
		}{
			{"hostname listener", func(cfg *Config) { cfg.Listen = "localhost:8080" }},
			{"wildcard listener", func(cfg *Config) { cfg.Listen = "0.0.0.0:8080" }},
			{"zero port", func(cfg *Config) { cfg.Listen = "127.0.0.1:0" }},
			{"bad environment name", func(cfg *Config) { cfg.APIKeyEnv = "bad-key" }},
			{"zero body", func(cfg *Config) { cfg.HTTPBodyBytes = 0 }},
			{"large body", func(cfg *Config) { cfg.HTTPBodyBytes = 16<<20 + 1 }},
			{"input above body", func(cfg *Config) { cfg.RequestLimits.InputBytes = int(cfg.HTTPBodyBytes) + 1 }},
			{"instruction above body", func(cfg *Config) { cfg.RequestLimits.InstructionsBytes = int(cfg.HTTPBodyBytes) + 1 }},
			{"schema above ceiling", func(cfg *Config) { cfg.RequestLimits.SchemaBytes = 1<<20 + 1 }},
			{"schema above body", func(cfg *Config) { cfg.HTTPBodyBytes = 100; cfg.RequestLimits.SchemaBytes = 101 }},
			{"nonfixed depth", func(cfg *Config) { cfg.RequestLimits.MaxDepth = 63 }},
			{"nonfixed number", func(cfg *Config) { cfg.RequestLimits.MaxNumberBytes = 127 }},
			{"zero handler", func(cfg *Config) { cfg.HandlerLimit = 0 }},
			{"large handler", func(cfg *Config) { cfg.HandlerLimit = 4097 }},
			{"zero body gate", func(cfg *Config) { cfg.BodyReaderLimit = 0 }},
			{"body gate above handler", func(cfg *Config) { cfg.BodyReaderLimit = cfg.HandlerLimit + 1 }},
			{"large body gate", func(cfg *Config) { cfg.HandlerLimit = 300; cfg.BodyReaderLimit = 257 }},
			{"zero header", func(cfg *Config) { cfg.MaxHeaderBytes = 0 }},
			{"large header", func(cfg *Config) { cfg.MaxHeaderBytes = 1<<20 + 1 }},
			{"zero header timeout", func(cfg *Config) { cfg.ReadHeaderTimeout = 0 }},
			{"large body timeout", func(cfg *Config) { cfg.BodyReadTimeout = 24*time.Hour + 1 }},
			{"zero idle timeout", func(cfg *Config) { cfg.IdleTimeout = 0 }},
			{"zero final", func(cfg *Config) { cfg.FinalBytes = 0 }},
			{"large final", func(cfg *Config) { cfg.FinalBytes = 16<<20 + 1 }},
			{"zero models", func(cfg *Config) { cfg.MaxModels = 0 }},
			{"large models", func(cfg *Config) { cfg.MaxModels = 1025 }},
		}
		for _, test := range invalid {
			t.Run(test.name, func(t *testing.T) {
				cfg := base
				test.mutate(&cfg)
				if _, _, err := New(cfg, deps, backend, logger); !errors.Is(err, errServerConfiguration) {
					t.Fatalf("err=%v", err)
				}
			})
		}

		var nilBackend *testBackend
		if _, _, err := New(base, deps, nilBackend, logger); !errors.Is(err, errServerConfiguration) {
			t.Fatalf("typed nil backend err=%v", err)
		}
		var nilIDs *fixedIDs
		badDeps := deps
		badDeps.IDs = nilIDs
		if _, _, err := New(base, badDeps, backend, logger); !errors.Is(err, errServerConfiguration) {
			t.Fatalf("typed nil IDs err=%v", err)
		}
		var nilCounters *testCounters
		badDeps = deps
		badDeps.Counters = nilCounters
		if _, _, err := New(base, badDeps, backend, logger); !errors.Is(err, errServerConfiguration) {
			t.Fatalf("typed nil counters err=%v", err)
		}
		badDeps = deps
		badDeps.Now = nil
		if _, _, err := New(base, badDeps, backend, logger); !errors.Is(err, errServerConfiguration) {
			t.Fatalf("nil clock err=%v", err)
		}
		if _, _, err := New(base, deps, backend, nil); !errors.Is(err, errServerConfiguration) {
			t.Fatalf("nil logger err=%v", err)
		}
	})

	t.Run("authentication lookup and model validation fail closed", func(t *testing.T) {
		cfg := validServerConfig()
		cfg.APIKeyEnv = "AI_CLI_GATEWAY_API_KEY"
		deps, _ := validServerDependencies()
		var lookups atomic.Int32
		deps.LookupEnv = func(name string) (string, bool) {
			lookups.Add(1)
			if name != cfg.APIKeyEnv {
				t.Fatalf("name=%q", name)
			}
			return "", false
		}
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		if _, _, err := New(cfg, deps, validServerBackend(), logger); !errors.Is(err, errServerConfiguration) {
			t.Fatalf("auth err=%v", err)
		}
		if lookups.Load() != 1 {
			t.Fatalf("lookups=%d", lookups.Load())
		}

		cfg.APIKeyEnv = ""
		deps.LookupEnv = func(string) (string, bool) {
			t.Fatal("lookup called while authentication disabled")
			return "", false
		}
		badModels := []struct {
			name   string
			models []core.Model
		}{
			{"empty", nil},
			{"duplicate", []core.Model{
				{ID: "same", Provider: core.ProviderCodex, ProviderModel: "one"},
				{ID: "same", Provider: core.ProviderClaude, ProviderModel: "two"},
			}},
			{"unknown provider", []core.Model{{ID: "bad", Provider: "other", ProviderModel: "x"}}},
			{"negative created", []core.Model{{ID: "bad", Provider: core.ProviderCodex, ProviderModel: "x", Created: -1}}},
		}
		for _, test := range badModels {
			t.Run(test.name, func(t *testing.T) {
				backend := &testBackend{models: test.models}
				if _, _, err := New(cfg, deps, backend, logger); !errors.Is(err, errServerConfiguration) {
					t.Fatalf("err=%v", err)
				}
				if backend.modelsCalls.Load() != 1 {
					t.Fatalf("calls=%d", backend.modelsCalls.Load())
				}
			})
		}
	})
}

func TestServerRouteAuthHostQueryAndMediaPrecedence(t *testing.T) {
	cfg := validServerConfig()
	cfg.APIKeyEnv = "AI_CLI_GATEWAY_API_KEY"
	deps, _ := validServerDependencies()
	deps.LookupEnv = func(name string) (string, bool) {
		return "gateway-secret", name == cfg.APIKeyEnv
	}
	_, handler := newTestServer(t, cfg, validServerBackend(), deps, nil)

	validBody := `{"model":"codex-default","input":"hello"}`
	tests := []struct {
		name        string
		method      string
		target      string
		host        string
		authorize   bool
		contentType string
		encoding    string
		wantStatus  int
		wantCode    string
		wantParam   *string
	}{
		{"host before query", http.MethodPost, "/v1/responses?secret=x", "evil.invalid:8080", false, "application/json", "", 400, core.CodeInvalidRequest, nil},
		{"userinfo Host", http.MethodGet, "/v1/models", "user@" + cfg.Listen, false, "", "", 400, core.CodeInvalidRequest, nil},
		{"other hostname", http.MethodGet, "/v1/models", "other.invalid:8080", false, "", "", 400, core.CodeInvalidRequest, nil},
		{"query before auth", http.MethodPost, "/v1/responses?secret=x", cfg.Listen, false, "application/json", "", 400, core.CodeUnsupportedParameter, stringPointer("query")},
		{"forced empty query", http.MethodPost, "/v1/responses?", cfg.Listen, true, "application/json", "", 400, core.CodeUnsupportedParameter, stringPointer("query")},
		{"auth before route", http.MethodGet, "/private", cfg.Listen, false, "", "", 401, core.CodeInvalidBearerKey, nil},
		{"unknown route", http.MethodGet, "/private", cfg.Listen, true, "", "", 404, core.CodeNotFound, nil},
		{"trailing slash", http.MethodPost, "/v1/responses/", cfg.Listen, true, "application/json", "", 404, core.CodeNotFound, nil},
		{"wrong response method", http.MethodGet, "/v1/responses", cfg.Listen, true, "", "", 405, core.CodeMethodNotAllowed, nil},
		{"wrong models method", http.MethodPost, "/v1/models", cfg.Listen, true, "application/json", "", 405, core.CodeMethodNotAllowed, nil},
		{"missing content type", http.MethodPost, "/v1/responses", cfg.Listen, true, "", "", 415, core.CodeUnsupportedMediaType, nil},
		{"wrong content type", http.MethodPost, "/v1/responses", cfg.Listen, true, "text/plain", "", 415, core.CodeUnsupportedMediaType, nil},
		{"extra content parameter", http.MethodPost, "/v1/responses", cfg.Listen, true, "application/json; charset=utf-8; version=1", "", 415, core.CodeUnsupportedMediaType, nil},
		{"wrong charset", http.MethodPost, "/v1/responses", cfg.Listen, true, "application/json; charset=latin1", "", 415, core.CodeUnsupportedMediaType, nil},
		{"unsupported encoding", http.MethodPost, "/v1/responses", cfg.Listen, true, "application/json", "gzip", 415, core.CodeUnsupportedMediaType, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequestWithContext(context.Background(), test.method, "http://127.0.0.1:8080"+test.target, strings.NewReader(validBody))
			request.Host = test.host
			if test.authorize {
				request.Header.Set("Authorization", "Bearer gateway-secret")
			}
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			if test.encoding != "" {
				request.Header.Set("Content-Encoding", test.encoding)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			assertServerError(t, response, test.wantStatus, test.wantCode, test.wantParam)
			assertApplicationHeaders(t, response.Header())
		})
	}

	for _, contentType := range []string{
		"application/json",
		"Application/JSON",
		"application/json; charset=utf-8",
		"application/json; charset=\"UTF-8\"",
	} {
		t.Run("accept "+contentType, func(t *testing.T) {
			request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "http://127.0.0.1:8080/v1/responses", strings.NewReader(validBody))
			request.Host = cfg.Listen
			request.Header.Set("Authorization", "Bearer gateway-secret")
			request.Header.Set("Content-Type", contentType)
			request.Header.Set("Content-Encoding", "identity")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			assertApplicationHeaders(t, response.Header())
		})
	}
	if got := httptest.NewRecorder().Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("unexpected CORS=%q", got)
	}
}

func TestServerModelsContentEncodingContract(t *testing.T) {
	cfg := validServerConfig()
	deps, _ := validServerDependencies()
	_, handler := newTestServer(t, cfg, validServerBackend(), deps, nil)

	tests := []struct {
		name       string
		method     string
		path       string
		encodings  []string
		wantStatus int
		wantCode   string
	}{
		{name: "absent", method: http.MethodGet, path: modelsEndpoint, wantStatus: http.StatusOK},
		{name: "identity", method: http.MethodGet, path: modelsEndpoint, encodings: []string{"identity"}, wantStatus: http.StatusOK},
		{name: "identity case and whitespace", method: http.MethodGet, path: modelsEndpoint, encodings: []string{" Identity \t"}, wantStatus: http.StatusOK},
		{name: "gzip", method: http.MethodGet, path: modelsEndpoint, encodings: []string{"gzip"}, wantStatus: http.StatusUnsupportedMediaType, wantCode: core.CodeUnsupportedMediaType},
		{name: "comma separated values", method: http.MethodGet, path: modelsEndpoint, encodings: []string{"identity, gzip"}, wantStatus: http.StatusUnsupportedMediaType, wantCode: core.CodeUnsupportedMediaType},
		{name: "multiple header values", method: http.MethodGet, path: modelsEndpoint, encodings: []string{"identity", "identity"}, wantStatus: http.StatusUnsupportedMediaType, wantCode: core.CodeUnsupportedMediaType},
		{name: "method takes precedence", method: http.MethodPost, path: modelsEndpoint, encodings: []string{"gzip"}, wantStatus: http.StatusMethodNotAllowed, wantCode: core.CodeMethodNotAllowed},
		{name: "route takes precedence", method: http.MethodGet, path: "/private", encodings: []string{"gzip"}, wantStatus: http.StatusNotFound, wantCode: core.CodeNotFound},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequestWithContext(context.Background(), test.method, "http://127.0.0.1:8080"+test.path, nil)
			request.Host = cfg.Listen
			for _, encoding := range test.encodings {
				request.Header.Add("Content-Encoding", encoding)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if test.wantCode == "" {
				if response.Code != test.wantStatus {
					t.Fatalf("status=%d want=%d body=%s", response.Code, test.wantStatus, response.Body.String())
				}
				assertApplicationHeaders(t, response.Header())
				return
			}
			assertServerError(t, response, test.wantStatus, test.wantCode, nil)
			assertApplicationHeaders(t, response.Header())
		})
	}
}

func TestServerEncodedPathsNeverMatchLiteralRoutes(t *testing.T) {
	cfg := validServerConfig()
	cfg.APIKeyEnv = "AI_CLI_GATEWAY_API_KEY"
	deps, _ := validServerDependencies()
	deps.LookupEnv = func(string) (string, bool) { return "gateway-secret", true }
	var logs bytes.Buffer
	_, handler := newTestServer(t, cfg, validServerBackend(), deps, &logs)

	for _, target := range []string{
		"/v1%2Fmodels",
		"/v1%2fmodels",
		"/v1/%6Dodels",
		"/v1/%6dodels",
		"/v1%2Fresponses",
		"/v1%2fresponses",
		"/v1/%72esponses",
	} {
		t.Run(target, func(t *testing.T) {
			logs.Reset()
			request := httptest.NewRequestWithContext(
				context.Background(),
				http.MethodGet,
				"http://127.0.0.1:8080"+target,
				nil,
			)
			request.Host = cfg.Listen
			request.Header.Set("Authorization", "Bearer gateway-secret")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			assertServerError(t, response, http.StatusNotFound, core.CodeNotFound, nil)
			if !strings.Contains(logs.String(), "endpoint=unmatched") ||
				strings.Contains(logs.String(), target) {
				t.Fatalf("target=%q logs=%q", target, logs.String())
			}
		})
	}
}

func TestServerUnreadBodyDispositionRunsOnlyAfterCancellationCheck(t *testing.T) {
	cfg := validServerConfig()
	deps, counters := validServerDependencies()
	var logs bytes.Buffer
	_, handler := newTestServer(t, cfg, validServerBackend(), deps, &logs)

	t.Run("active request retires connection before publishing", func(t *testing.T) {
		body := &countingReadCloser{}
		request := httptest.NewRequestWithContext(
			context.Background(),
			http.MethodPost,
			"http://127.0.0.1:8080/v1/responses",
			nil,
		)
		request.Host = "wrong.invalid:8080"
		request.Body = body
		writer := newDeadlineTrackingWriter(nil)
		handler.ServeHTTP(writer, request)

		assertTrackingError(t, writer.trackingResponseWriter, http.StatusBadRequest, core.CodeInvalidRequest)
		if writer.header.Get("Connection") != "close" || writer.installCalls.Load() != 1 ||
			writer.clearCalls.Load() != 0 || body.reads.Load() != 0 || body.closes.Load() != 1 {
			t.Fatalf("headers=%v install=%d clear=%d reads=%d closes=%d",
				writer.header, writer.installCalls.Load(), writer.clearCalls.Load(),
				body.reads.Load(), body.closes.Load())
		}
	})

	t.Run("already canceled request has no disposition or publication mutation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		body := &countingReadCloser{}
		request := httptest.NewRequestWithContext(
			ctx,
			http.MethodPost,
			"http://127.0.0.1:8080/v1/responses",
			nil,
		)
		request.Host = "wrong.invalid:8080"
		request.Body = body
		writer := newDeadlineTrackingWriter(nil)
		handler.ServeHTTP(writer, request)

		assertNoResponseMutation(t, writer.trackingResponseWriter)
		if writer.installCalls.Load() != 0 || writer.clearCalls.Load() != 0 ||
			body.reads.Load() != 0 || body.closes.Load() != 0 ||
			counters.count.Load() != 1 {
			t.Fatalf("install=%d clear=%d reads=%d closes=%d cancellations=%d",
				writer.installCalls.Load(), writer.clearCalls.Load(), body.reads.Load(),
				body.closes.Load(), counters.count.Load())
		}
	})
}

func TestServerSuccessSnapshotLoggingAndErrors(t *testing.T) {
	t.Run("success snapshots echo data and logs only trusted metadata", func(t *testing.T) {
		cfg := validServerConfig()
		deps, _ := validServerDependencies()
		var logs bytes.Buffer
		backend := validServerBackend()
		backend.respond = func(_ context.Context, request core.Request) (core.Result, error) {
			*request.Instructions = "mutated instruction"
			*request.Format.Description = "mutated description"
			request.Format.Schema[0] = '['
			return core.Result{
				Text: `{"answer":"ok"}`,
				Meta: core.ResultMeta{
					Provider:        core.ProviderCodex,
					StdoutBytes:     17,
					StderrBytes:     3,
					QueueDepth:      1,
					RunningCount:    1,
					QueueDuration:   2 * time.Millisecond,
					ExecutionTime:   3 * time.Millisecond,
					ProviderVersion: "0.146.0",
					ExitCategory:    "completed",
					StopReason:      "completed",
					StopAction:      "none",
				},
			}, nil
		}
		_, handler := newTestServer(t, cfg, backend, deps, &logs)
		body := `{"model":"codex-default","instructions":"original instruction","input":"secret input","text":{"format":{"type":"json_schema","name":"result","description":"original description","strict":true,"schema":{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}}}}`
		response := servePOST(handler, cfg.Listen, body)
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		var decoded map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded["model"] != "codex-default" || decoded["instructions"] != "original instruction" {
			t.Fatalf("decoded=%v", decoded)
		}
		text := decoded["text"].(map[string]any)
		format := text["format"].(map[string]any)
		if format["description"] != "original description" {
			t.Fatalf("format=%v", format)
		}
		logText := logs.String()
		for _, want := range []string{"request_completed", "codex-default", "codex", "0.146.0"} {
			if !strings.Contains(logText, want) {
				t.Fatalf("logs missing %q: %s", want, logText)
			}
		}
		for _, forbidden := range []string{"secret input", "original instruction", "original description", `{"answer":"ok"}`} {
			if strings.Contains(logText, forbidden) {
				t.Fatalf("logs leaked %q: %s", forbidden, logText)
			}
		}
	})

	t.Run("unknown alias and raw error never enter public response or logs", func(t *testing.T) {
		cfg := validServerConfig()
		deps, _ := validServerDependencies()
		var logs bytes.Buffer
		backend := validServerBackend()
		backend.respond = func(context.Context, core.Request) (core.Result, error) {
			return core.Result{}, errors.New("PLANTED_RAW_BACKEND_SECRET")
		}
		_, handler := newTestServer(t, cfg, backend, deps, &logs)
		response := servePOST(handler, cfg.Listen, `{"model":"attacker-alias","input":"hello"}`)
		assertServerError(t, response, 500, core.CodeInternalError, nil)
		if strings.Contains(logs.String(), "attacker-alias") ||
			strings.Contains(logs.String(), "PLANTED_RAW_BACKEND_SECRET") {
			t.Fatalf("logs=%s", logs.String())
		}
	})

	t.Run("forged oversized success becomes fixed internal error", func(t *testing.T) {
		cfg := validServerConfig()
		cfg.FinalBytes = 4
		deps, _ := validServerDependencies()
		backend := validServerBackend()
		backend.respond = func(context.Context, core.Request) (core.Result, error) {
			return core.Result{Text: "12345"}, nil
		}
		_, handler := newTestServer(t, cfg, backend, deps, nil)
		response := servePOST(handler, cfg.Listen, `{"model":"codex-default","input":"hello"}`)
		assertServerError(t, response, 500, core.CodeInternalError, nil)
	})
}

func TestServerBodyBoundsDeadlinesAndConfiguredDecodeLimits(t *testing.T) {
	t.Run("body and field limits classify exactly", func(t *testing.T) {
		cfg := validServerConfig()
		cfg.HTTPBodyBytes = 96
		cfg.RequestLimits.InputBytes = 4
		cfg.RequestLimits.InstructionsBytes = 3
		cfg.RequestLimits.SchemaBytes = 32
		deps, _ := validServerDependencies()
		_, handler := newTestServer(t, cfg, validServerBackend(), deps, nil)

		cases := []struct {
			name       string
			body       string
			wantStatus int
			wantCode   string
			wantParam  *string
		}{
			{"input limit", `{"model":"codex-default","input":"12345"}`, 413, core.CodeRequestTooLarge, stringPointer("input")},
			{"instructions limit", `{"model":"codex-default","input":"1234","instructions":"1234"}`, 413, core.CodeRequestTooLarge, stringPointer("instructions")},
			{"invalid JSON", `{"model":`, 400, core.CodeInvalidJSON, nil},
			{"oversized body", strings.Repeat("x", 97), 413, core.CodeRequestTooLarge, nil},
		}
		for _, test := range cases {
			t.Run(test.name, func(t *testing.T) {
				response := servePOST(handler, cfg.Listen, test.body)
				assertServerError(t, response, test.wantStatus, test.wantCode, test.wantParam)
			})
		}
	})

	t.Run("timeout and arbitrary read failures are closed", func(t *testing.T) {
		cfg := validServerConfig()
		deps, _ := validServerDependencies()
		_, handler := newTestServer(t, cfg, validServerBackend(), deps, nil)

		request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "http://127.0.0.1:8080/v1/responses", nil)
		request.Host = cfg.Listen
		request.Header.Set("Content-Type", "application/json")
		request.Body = io.NopCloser(timeoutReader{})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		assertServerError(t, response, 408, core.CodeRequestTimeout, nil)

		request = httptest.NewRequestWithContext(context.Background(), http.MethodPost, "http://127.0.0.1:8080/v1/responses", nil)
		request.Host = cfg.Listen
		request.Header.Set("Content-Type", "application/json")
		request.Body = io.NopCloser(errorBodyReader{})
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		assertServerError(t, response, 400, core.CodeInvalidJSON, nil)
	})
}

func TestServerBodyDeadlineRequiresInstalledOwnedProvenance(t *testing.T) {
	t.Run("arbitrary timeout plus cancellation is client cancellation", func(t *testing.T) {
		cfg := validServerConfig()
		deps, counters := validServerDependencies()
		var logs bytes.Buffer
		backend := validServerBackend()
		var backendCalls atomic.Int32
		backend.respond = func(context.Context, core.Request) (core.Result, error) {
			backendCalls.Add(1)
			return core.Result{Text: "unexpected"}, nil
		}
		_, handler := newTestServer(t, cfg, backend, deps, &logs)

		ctx, cancel := context.WithCancel(context.Background())
		request := httptest.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:8080/v1/responses", nil)
		request.Host = cfg.Listen
		request.Header.Set("Content-Type", "application/json")
		request.Body = &callbackErrorReadCloser{
			callback: cancel,
			err:      timeoutReadError{},
		}
		writer := newDeadlineTrackingWriter(nil)
		handler.ServeHTTP(writer, request)

		assertNoResponseMutation(t, writer.trackingResponseWriter)
		if counters.count.Load() != 1 || backendCalls.Load() != 0 || logs.Len() != 0 {
			t.Fatalf("counters=%d backend=%d logs=%q", counters.count.Load(), backendCalls.Load(), logs.String())
		}
		if writer.installCalls.Load() != 1 || writer.clearCalls.Load() != 1 {
			t.Fatalf("install=%d clear=%d", writer.installCalls.Load(), writer.clearCalls.Load())
		}
	})

	t.Run("failed deadline installation preserves timeout classification without ownership", func(t *testing.T) {
		for _, readErr := range []error{timeoutReadError{}, os.ErrDeadlineExceeded} {
			cfg := validServerConfig()
			deps, counters := validServerDependencies()
			var logs bytes.Buffer
			_, handler := newTestServer(t, cfg, validServerBackend(), deps, &logs)
			request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "http://127.0.0.1:8080/v1/responses", nil)
			request.Host = cfg.Listen
			request.Header.Set("Content-Type", "application/json")
			request.Body = &callbackErrorReadCloser{err: readErr}
			writer := newDeadlineTrackingWriter(errors.New("PLANTED_DEADLINE_INSTALL_SECRET"))

			handler.ServeHTTP(writer, request)

			assertTrackingError(t, writer.trackingResponseWriter, 408, core.CodeRequestTimeout)
			if counters.count.Load() != 0 || writer.installCalls.Load() != 1 || writer.clearCalls.Load() != 0 {
				t.Fatalf("counters=%d install=%d clear=%d", counters.count.Load(), writer.installCalls.Load(), writer.clearCalls.Load())
			}
			if strings.Contains(logs.String(), "PLANTED_DEADLINE_INSTALL_SECRET") {
				t.Fatalf("logs=%q", logs.String())
			}
		}
	})

	t.Run("failed deadline installation cannot override cancellation", func(t *testing.T) {
		for _, readErr := range []error{timeoutReadError{}, os.ErrDeadlineExceeded} {
			cfg := validServerConfig()
			deps, counters := validServerDependencies()
			var logs bytes.Buffer
			_, handler := newTestServer(t, cfg, validServerBackend(), deps, &logs)
			ctx, cancel := context.WithCancel(context.Background())
			request := httptest.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:8080/v1/responses", nil)
			request.Host = cfg.Listen
			request.Header.Set("Content-Type", "application/json")
			request.Body = &callbackErrorReadCloser{callback: cancel, err: readErr}
			writer := newDeadlineTrackingWriter(errors.New("PLANTED_DEADLINE_INSTALL_SECRET"))

			handler.ServeHTTP(writer, request)

			assertNoResponseMutation(t, writer.trackingResponseWriter)
			if counters.count.Load() != 1 || writer.installCalls.Load() != 1 ||
				writer.clearCalls.Load() != 0 || logs.Len() != 0 {
				t.Fatalf("counters=%d install=%d clear=%d logs=%q",
					counters.count.Load(), writer.installCalls.Load(), writer.clearCalls.Load(), logs.String())
			}
		}
	})

	t.Run("installed OS deadline sentinel is server timeout", func(t *testing.T) {
		cfg := validServerConfig()
		deps, counters := validServerDependencies()
		var logs bytes.Buffer
		_, handler := newTestServer(t, cfg, validServerBackend(), deps, &logs)
		ctx, cancel := context.WithCancel(context.Background())
		request := httptest.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:8080/v1/responses", nil)
		request.Host = cfg.Listen
		request.Header.Set("Content-Type", "application/json")
		request.Body = &callbackErrorReadCloser{callback: cancel, err: os.ErrDeadlineExceeded}
		writer := newDeadlineTrackingWriter(nil)

		handler.ServeHTTP(writer, request)

		assertTrackingError(t, writer.trackingResponseWriter, 408, core.CodeRequestTimeout)
		if counters.count.Load() != 0 || writer.installCalls.Load() != 1 || writer.clearCalls.Load() != 1 {
			t.Fatalf("counters=%d install=%d clear=%d", counters.count.Load(), writer.installCalls.Load(), writer.clearCalls.Load())
		}
		if !strings.Contains(logs.String(), "error_code=request_timeout") {
			t.Fatalf("logs=%q", logs.String())
		}
	})
}

func TestServerPreRouteFailuresUseOnlyUnmatchedEndpoint(t *testing.T) {
	t.Run("saturated handler never classifies a path", func(t *testing.T) {
		cfg := validServerConfig()
		cfg.HandlerLimit = 1
		cfg.BodyReaderLimit = 1
		deps, _ := validServerDependencies()
		barrierContext, cancelBarrier := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancelBarrier()
		entered := make(chan struct{})
		release := make(chan struct{})
		backend := validServerBackend()
		backend.respond = func(context.Context, core.Request) (core.Result, error) {
			close(entered)
			if err := waitForRelease(barrierContext, release); err != nil {
				return core.Result{}, err
			}
			return core.Result{Text: "ok"}, nil
		}
		var logs bytes.Buffer
		_, handler := newTestServer(t, cfg, backend, deps, &logs)
		firstDone := make(chan *httptest.ResponseRecorder, 1)
		go func() { firstDone <- servePOST(handler, cfg.Listen, `{"model":"codex-default","input":"one"}`) }()
		awaitSignal(t, entered, "saturating Backend did not enter")

		for _, path := range []string{"/v1/responses", "/v1/models", "/PLANTED_PATH_SECRET"} {
			request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:8080"+path, nil)
			request.Host = "PLANTED_HOST_SECRET"
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			assertServerError(t, response, 429, core.CodeServerBusy, nil)
		}
		logText := logs.String()
		if strings.Count(logText, "endpoint=unmatched") != 3 ||
			strings.Contains(logText, responsesEndpoint) ||
			strings.Contains(logText, modelsEndpoint) ||
			strings.Contains(logText, "PLANTED_PATH_SECRET") ||
			strings.Contains(logText, "PLANTED_HOST_SECRET") {
			t.Fatalf("logs=%q", logText)
		}
		close(release)
		if first := awaitValue(t, firstDone, "saturating request did not finish"); first.Code != http.StatusOK {
			t.Fatalf("first status=%d", first.Code)
		}
	})

	t.Run("Host query and auth failures stay unmatched", func(t *testing.T) {
		cfg := validServerConfig()
		cfg.APIKeyEnv = "AI_CLI_GATEWAY_API_KEY"
		deps, _ := validServerDependencies()
		deps.LookupEnv = func(string) (string, bool) { return "gateway-secret", true }
		var logs bytes.Buffer
		_, handler := newTestServer(t, cfg, validServerBackend(), deps, &logs)
		tests := []struct {
			name   string
			target string
			host   string
			auth   string
		}{
			{"Host", "/v1/models", "wrong.invalid:8080", ""},
			{"query", "/v1/models?PLANTED_QUERY_SECRET=x", cfg.Listen, ""},
			{"auth", "/v1/models", cfg.Listen, ""},
		}
		for _, test := range tests {
			logs.Reset()
			request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:8080"+test.target, nil)
			request.Host = test.host
			if test.auth != "" {
				request.Header.Set("Authorization", test.auth)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if !strings.Contains(logs.String(), "endpoint=unmatched") ||
				strings.Contains(logs.String(), modelsEndpoint) ||
				strings.Contains(logs.String(), "PLANTED_QUERY_SECRET") {
				t.Fatalf("%s logs=%q", test.name, logs.String())
			}
		}
	})
}

func TestServerHandlerAndBodyReaderGatesRelease(t *testing.T) {
	t.Run("handler gate rejects overflow before Host", func(t *testing.T) {
		cfg := validServerConfig()
		cfg.HandlerLimit = 1
		cfg.BodyReaderLimit = 1
		deps, _ := validServerDependencies()
		barrierContext, cancelBarrier := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancelBarrier()
		entered := make(chan struct{})
		release := make(chan struct{})
		var backendOnce sync.Once
		backend := validServerBackend()
		backend.respond = func(context.Context, core.Request) (core.Result, error) {
			backendOnce.Do(func() {
				close(entered)
				_ = waitForRelease(barrierContext, release)
			})
			return core.Result{Text: "ok"}, nil
		}
		_, handler := newTestServer(t, cfg, backend, deps, nil)

		firstDone := make(chan *httptest.ResponseRecorder, 1)
		go func() { firstDone <- servePOST(handler, cfg.Listen, `{"model":"codex-default","input":"one"}`) }()
		awaitSignal(t, entered, "handler-gate Backend did not enter")
		second := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:8080/other", nil)
		second.Host = "wrong.invalid"
		secondResponse := httptest.NewRecorder()
		handler.ServeHTTP(secondResponse, second)
		assertServerError(t, secondResponse, 429, core.CodeServerBusy, nil)
		close(release)
		if first := awaitValue(t, firstDone, "handler-gate request did not finish"); first.Code != 200 {
			t.Fatalf("first status=%d", first.Code)
		}

		third := servePOST(handler, cfg.Listen, `{"model":"codex-default","input":"three"}`)
		if third.Code != 200 {
			t.Fatalf("third status=%d", third.Code)
		}
	})

	t.Run("body gate releases before backend", func(t *testing.T) {
		cfg := validServerConfig()
		cfg.HandlerLimit = 3
		cfg.BodyReaderLimit = 1
		deps, _ := validServerDependencies()
		barrierContext, cancelBarrier := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancelBarrier()
		bodyEntered := make(chan struct{})
		bodyRelease := make(chan struct{})
		backendEntered := make(chan struct{}, 1)
		backend := validServerBackend()
		backend.respond = func(context.Context, core.Request) (core.Result, error) {
			backendEntered <- struct{}{}
			return core.Result{Text: "ok"}, nil
		}
		_, handler := newTestServer(t, cfg, backend, deps, nil)

		firstRequest := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "http://127.0.0.1:8080/v1/responses", nil)
		firstRequest.Host = cfg.Listen
		firstRequest.Header.Set("Content-Type", "application/json")
		firstRequest.Body = &blockingReadCloser{
			context: barrierContext,
			entered: bodyEntered,
			release: bodyRelease,
			payload: []byte(`{"model":"codex-default","input":"one"}`),
		}
		firstDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, firstRequest)
			firstDone <- response
		}()
		awaitSignal(t, bodyEntered, "body-gate reader did not enter")

		second := servePOST(handler, cfg.Listen, `{"model":"codex-default","input":"two"}`)
		assertServerError(t, second, 429, core.CodeServerBusy, nil)
		close(bodyRelease)
		awaitSignal(t, backendEntered, "Backend did not enter after body permit release")
		if first := awaitValue(t, firstDone, "body-gate request did not finish"); first.Code != 200 {
			t.Fatalf("first status=%d", first.Code)
		}
		third := servePOST(handler, cfg.Listen, `{"model":"codex-default","input":"three"}`)
		if third.Code != 200 {
			t.Fatalf("third status=%d", third.Code)
		}
	})
}

func TestServerCancellationSuppressesAllWritesAndCountsOnce(t *testing.T) {
	cfg := validServerConfig()
	deps, counters := validServerDependencies()
	var logs bytes.Buffer
	entered := make(chan struct{})
	backend := validServerBackend()
	backend.respond = func(ctx context.Context, _ core.Request) (core.Result, error) {
		close(entered)
		<-ctx.Done()
		return core.Result{}, ctx.Err()
	}
	_, handler := newTestServer(t, cfg, backend, deps, &logs)

	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:8080/v1/responses", strings.NewReader(`{"model":"codex-default","input":"hello"}`))
	request.Host = cfg.Listen
	request.Header.Set("Content-Type", "application/json")
	writer := &trackingResponseWriter{header: make(http.Header)}
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(writer, request)
		close(done)
	}()
	awaitSignal(t, entered, "cancellation Backend did not enter")
	cancel()
	awaitSignal(t, done, "canceled handler did not return")
	if counters.count.Load() != 1 {
		t.Fatalf("cancellations=%d", counters.count.Load())
	}
	if writer.touched.Load() || writer.status.Load() != 0 || writer.body.Len() != 0 {
		t.Fatalf("writer touched=%v status=%d body=%q", writer.touched.Load(), writer.status.Load(), writer.body.String())
	}
	if logs.Len() != 0 {
		t.Fatalf("logs=%s", logs.String())
	}
}

func TestServerHundredConcurrentBodiesRemainWithinConfiguredBounds(t *testing.T) {
	cfg := validServerConfig()
	cfg.HTTPBodyBytes = 128
	cfg.RequestLimits.InputBytes = 128
	cfg.RequestLimits.InstructionsBytes = 128
	cfg.RequestLimits.SchemaBytes = 128
	cfg.HandlerLimit = 100
	cfg.BodyReaderLimit = 4
	cfg.FinalBytes = 16
	deps, counters := validServerDependencies()
	matrixContext, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	eofRelease := make(chan struct{})
	backendRelease := make(chan struct{})
	publicationRelease := make(chan struct{})
	readTracker := &logicalBodyReadTracker{
		context:    matrixContext,
		eofEntered: make(chan struct{}, cfg.BodyReaderLimit),
		eofRelease: eofRelease,
	}
	backendTracker := &logicalBackendTracker{
		context:        matrixContext,
		entered:        make(chan struct{}, cfg.BodyReaderLimit),
		release:        backendRelease,
		expectedInputs: make(map[string]struct{}, cfg.BodyReaderLimit),
		seenInputs:     make(map[string]struct{}, cfg.BodyReaderLimit),
	}
	publicationTracker := &logicalPublicationTracker{
		context: matrixContext,
		entered: make(chan struct{}, cfg.HandlerLimit),
		release: publicationRelease,
	}
	backend := validServerBackend()
	backend.respond = func(ctx context.Context, request core.Request) (core.Result, error) {
		return backendTracker.respond(ctx, request, cfg.FinalBytes)
	}
	_, genericHandler := newTestServer(t, cfg, backend, deps, nil)
	handler := genericHandler.(*applicationHandler)

	admittedBodies := make([]string, cfg.BodyReaderLimit)
	var expectedNormalizedWeight int64
	for index := range cfg.BodyReaderLimit {
		admittedBodies[index] = exactSizedRequestBodyWithMarker(
			t,
			int(cfg.HTTPBodyBytes),
			fmt.Sprintf("admitted-%02d-", index),
		)
		normalized, apiErr := DecodeRequest([]byte(admittedBodies[index]), cfg.RequestLimits)
		if apiErr != nil {
			t.Fatalf("decode admitted fixture %d: %v", index, apiErr)
		}
		expectedNormalizedWeight += logicalRequestWeight(normalized)
		backendTracker.expectedInputs[normalized.Input] = struct{}{}
	}

	firstResults := make(chan *httptest.ResponseRecorder, cfg.BodyReaderLimit)
	for index := range cfg.BodyReaderLimit {
		go func(body string, weight int64) {
			request := httptest.NewRequestWithContext(matrixContext, http.MethodPost, "http://127.0.0.1:8080/v1/responses", nil)
			request.Host = cfg.Listen
			request.Header.Set("Content-Type", "application/json")
			request.ContentLength = int64(len(body))
			request.Body = &logicalExactBody{
				payload: []byte(body),
				tracker: readTracker,
			}
			response := newPublicationBlockingWriter(publicationTracker)
			handler.ServeHTTP(response, request)
			backendTracker.finishRequest(weight, cfg.FinalBytes)
			firstResults <- response.ResponseRecorder
		}(admittedBodies[index], logicalRequestWeightMust(t, admittedBodies[index], cfg.RequestLimits))
	}
	for range cfg.BodyReaderLimit {
		awaitSignalBefore(matrixContext, t, readTracker.eofEntered, "exact-limit reader did not reach EOF barrier")
	}

	const overflowRequests = 96
	start := make(chan struct{})
	overflowResults := make(chan *httptest.ResponseRecorder, overflowRequests)
	var rejectedReads atomic.Int64
	var rejectedCloses atomic.Int64
	for index := range overflowRequests {
		body := exactSizedRequestBodyWithMarker(
			t,
			int(cfg.HTTPBodyBytes),
			fmt.Sprintf("rejected-%02d-", index),
		)
		go func(payload string) {
			select {
			case <-start:
			case <-matrixContext.Done():
				return
			}
			request := httptest.NewRequestWithContext(matrixContext, http.MethodPost, "http://127.0.0.1:8080/v1/responses", nil)
			request.Host = cfg.Listen
			request.Header.Set("Content-Type", "application/json")
			request.ContentLength = int64(len(payload))
			request.Body = &unreadLogicalBody{
				payload: []byte(payload), reads: &rejectedReads, closes: &rejectedCloses,
			}
			response := newPublicationBlockingWriter(publicationTracker)
			handler.ServeHTTP(response, request)
			overflowResults <- response.ResponseRecorder
		}(body)
	}
	close(start)
	for range overflowRequests {
		awaitSignalBefore(matrixContext, t, publicationTracker.entered, "rejected request did not reach publication barrier")
	}
	if rejectedReads.Load() != 0 || rejectedCloses.Load() != overflowRequests ||
		readTracker.deliveredBytes.Load() != int64(cfg.BodyReaderLimit)*cfg.HTTPBodyBytes ||
		readTracker.activeReaders.Load() != int64(cfg.BodyReaderLimit) ||
		readTracker.peakReaders.Load() != int64(cfg.BodyReaderLimit) ||
		backendTracker.calls.Load() != 0 ||
		publicationTracker.active.Load() != int64(overflowRequests) ||
		publicationTracker.activeBytes.Load() > int64(overflowRequests*handler.successLimit) ||
		len(handler.handlerGate) != cfg.HandlerLimit ||
		len(handler.bodyReaderGate) != cfg.BodyReaderLimit {
		t.Fatalf("rejected_reads=%d rejected_closes=%d delivered=%d readers=%d peak_readers=%d backend=%d publications=%d publication_bytes=%d handler_gate=%d body_gate=%d",
			rejectedReads.Load(), rejectedCloses.Load(), readTracker.deliveredBytes.Load(),
			readTracker.activeReaders.Load(), readTracker.peakReaders.Load(), backendTracker.calls.Load(),
			publicationTracker.active.Load(), publicationTracker.activeBytes.Load(),
			len(handler.handlerGate), len(handler.bodyReaderGate))
	}

	close(eofRelease)
	for range cfg.BodyReaderLimit {
		awaitSignalBefore(matrixContext, t, backendTracker.entered, "normalized request did not reach Backend barrier")
	}
	if readTracker.activeReaders.Load() != 0 || len(handler.bodyReaderGate) != 0 ||
		backendTracker.active.Load() != int64(cfg.BodyReaderLimit) ||
		backendTracker.logicalWeight.Load() != expectedNormalizedWeight ||
		len(handler.handlerGate) != cfg.HandlerLimit {
		t.Fatalf("readers=%d body_gate=%d backend_active=%d logical_weight=%d want=%d handler_gate=%d",
			readTracker.activeReaders.Load(), len(handler.bodyReaderGate), backendTracker.active.Load(),
			backendTracker.logicalWeight.Load(), expectedNormalizedWeight, len(handler.handlerGate))
	}

	close(backendRelease)
	for range cfg.BodyReaderLimit {
		awaitSignalBefore(matrixContext, t, publicationTracker.entered, "successful response did not reach publication barrier")
	}
	if publicationTracker.active.Load() != int64(cfg.HandlerLimit) ||
		publicationTracker.peak.Load() != int64(cfg.HandlerLimit) ||
		publicationTracker.activeBytes.Load() > int64(cfg.HandlerLimit*handler.successLimit) ||
		publicationTracker.peakBytes.Load() > int64(cfg.HandlerLimit*handler.successLimit) ||
		backendTracker.active.Load() != 0 ||
		backendTracker.normalizedActive.Load() != int64(cfg.BodyReaderLimit) ||
		backendTracker.logicalWeight.Load() != expectedNormalizedWeight ||
		backendTracker.finalBytes.Load() != int64(cfg.BodyReaderLimit*cfg.FinalBytes) ||
		len(handler.handlerGate) != cfg.HandlerLimit {
		t.Fatalf("publications=%d peak_publications=%d publication_bytes=%d peak_bytes=%d backend_active=%d normalized_active=%d logical_weight=%d final_bytes=%d handler_gate=%d",
			publicationTracker.active.Load(), publicationTracker.peak.Load(),
			publicationTracker.activeBytes.Load(), publicationTracker.peakBytes.Load(), backendTracker.active.Load(),
			backendTracker.normalizedActive.Load(), backendTracker.logicalWeight.Load(),
			backendTracker.finalBytes.Load(), len(handler.handlerGate))
	}

	close(publicationRelease)
	for range cfg.BodyReaderLimit {
		response := awaitValueBefore(matrixContext, t, firstResults, "admitted request did not finish")
		if response.Code != http.StatusOK {
			t.Fatalf("admitted status=%d body=%q", response.Code, response.Body.String())
		}
	}
	for range overflowRequests {
		response := awaitValueBefore(matrixContext, t, overflowResults, "rejected request did not finish")
		assertServerError(t, response, http.StatusTooManyRequests, core.CodeServerBusy, nil)
	}
	if readTracker.activeReaders.Load() != 0 || backendTracker.active.Load() != 0 ||
		backendTracker.normalizedActive.Load() != 0 || backendTracker.logicalWeight.Load() != 0 ||
		backendTracker.finalBytes.Load() != 0 || publicationTracker.active.Load() != 0 ||
		publicationTracker.activeBytes.Load() != 0 ||
		len(handler.handlerGate) != 0 || len(handler.bodyReaderGate) != 0 ||
		counters.count.Load() != 0 || backendTracker.calls.Load() != int64(cfg.BodyReaderLimit) ||
		backendTracker.unexpected.Load() != 0 || backendTracker.duplicates.Load() != 0 ||
		publicationTracker.writes.Load() != int64(cfg.HandlerLimit) {
		t.Fatalf("readers=%d backend=%d normalized=%d weight=%d final=%d publications=%d publication_bytes=%d handler_gate=%d body_gate=%d cancellations=%d calls=%d unexpected=%d duplicates=%d writes=%d",
			readTracker.activeReaders.Load(), backendTracker.active.Load(), backendTracker.normalizedActive.Load(),
			backendTracker.logicalWeight.Load(), backendTracker.finalBytes.Load(), publicationTracker.active.Load(),
			publicationTracker.activeBytes.Load(),
			len(handler.handlerGate), len(handler.bodyReaderGate), counters.count.Load(), backendTracker.calls.Load(),
			backendTracker.unexpected.Load(), backendTracker.duplicates.Load(), publicationTracker.writes.Load())
	}
}

func TestServerReleasesHandlerAndBodyPermitsOnEveryTerminalBodyPath(t *testing.T) {
	newHandler := func(t *testing.T) (Config, http.Handler, *testCounters) {
		t.Helper()
		cfg := validServerConfig()
		cfg.HTTPBodyBytes = 64
		cfg.RequestLimits.InputBytes = 64
		cfg.RequestLimits.InstructionsBytes = 64
		cfg.RequestLimits.SchemaBytes = 64
		cfg.HandlerLimit = 1
		cfg.BodyReaderLimit = 1
		deps, counters := validServerDependencies()
		_, handler := newTestServer(t, cfg, validServerBackend(), deps, nil)
		return cfg, handler, counters
	}
	assertReleased := func(t *testing.T, cfg Config, handler http.Handler) {
		t.Helper()
		response := servePOST(handler, cfg.Listen, `{"model":"codex-default","input":"ok"}`)
		if response.Code != http.StatusOK {
			t.Fatalf("permit not released: status=%d body=%q", response.Code, response.Body.String())
		}
	}

	t.Run("decode failure", func(t *testing.T) {
		cfg, handler, _ := newHandler(t)
		response := servePOST(handler, cfg.Listen, `{"model":`)
		assertServerError(t, response, 400, core.CodeInvalidJSON, nil)
		assertReleased(t, cfg, handler)
	})

	t.Run("arbitrary read failure", func(t *testing.T) {
		cfg, handler, _ := newHandler(t)
		request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "http://127.0.0.1:8080/v1/responses", nil)
		request.Host = cfg.Listen
		request.Header.Set("Content-Type", "application/json")
		request.Body = io.NopCloser(errorBodyReader{})
		writer := newDeadlineTrackingWriter(nil)
		handler.ServeHTTP(writer, request)
		assertTrackingError(t, writer.trackingResponseWriter, http.StatusBadRequest, core.CodeInvalidJSON)
		assertReleased(t, cfg, handler)
	})

	t.Run("oversized body", func(t *testing.T) {
		cfg, handler, _ := newHandler(t)
		response := servePOST(handler, cfg.Listen, strings.Repeat("x", 65))
		assertServerError(t, response, 413, core.CodeRequestTooLarge, nil)
		assertReleased(t, cfg, handler)
	})

	t.Run("body deadline", func(t *testing.T) {
		cfg, handler, _ := newHandler(t)
		request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "http://127.0.0.1:8080/v1/responses", nil)
		request.Host = cfg.Listen
		request.Header.Set("Content-Type", "application/json")
		request.Body = &callbackErrorReadCloser{err: os.ErrDeadlineExceeded}
		writer := newDeadlineTrackingWriter(nil)
		handler.ServeHTTP(writer, request)
		assertTrackingError(t, writer.trackingResponseWriter, 408, core.CodeRequestTimeout)
		assertReleased(t, cfg, handler)
	})

	t.Run("client cancellation", func(t *testing.T) {
		cfg, handler, counters := newHandler(t)
		ctx, cancel := context.WithCancel(context.Background())
		request := httptest.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:8080/v1/responses", nil)
		request.Host = cfg.Listen
		request.Header.Set("Content-Type", "application/json")
		request.Body = &callbackErrorReadCloser{callback: cancel, err: errors.New("PLANTED_CANCEL_READ_SECRET")}
		writer := newDeadlineTrackingWriter(nil)
		handler.ServeHTTP(writer, request)
		assertNoResponseMutation(t, writer.trackingResponseWriter)
		if counters.count.Load() != 1 {
			t.Fatalf("cancellations=%d", counters.count.Load())
		}
		assertReleased(t, cfg, handler)
	})
}

func TestServerCancellationMatrixSuppressesPublicationExactlyOnce(t *testing.T) {
	t.Run("canceled before Backend", func(t *testing.T) {
		cfg := validServerConfig()
		deps, counters := validServerDependencies()
		var logs bytes.Buffer
		var backendCalls atomic.Int32
		backend := validServerBackend()
		backend.respond = func(context.Context, core.Request) (core.Result, error) {
			backendCalls.Add(1)
			return core.Result{Text: "unexpected"}, nil
		}
		_, handler := newTestServer(t, cfg, backend, deps, &logs)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		request := httptest.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:8080/v1/responses", strings.NewReader(`{"model":"codex-default","input":"hello"}`))
		request.Host = cfg.Listen
		request.Header.Set("Content-Type", "application/json")
		writer := &trackingResponseWriter{header: make(http.Header)}
		handler.ServeHTTP(writer, request)
		assertCanceledOutcome(t, writer, counters, &logs)
		if backendCalls.Load() != 0 {
			t.Fatalf("backend calls=%d", backendCalls.Load())
		}
	})

	t.Run("canceled during body read", func(t *testing.T) {
		cfg := validServerConfig()
		deps, counters := validServerDependencies()
		var logs bytes.Buffer
		_, handler := newTestServer(t, cfg, validServerBackend(), deps, &logs)
		ctx, cancel := context.WithCancel(context.Background())
		entered := make(chan struct{})
		request := httptest.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:8080/v1/responses", nil)
		request.Host = cfg.Listen
		request.Header.Set("Content-Type", "application/json")
		request.Body = &contextReadCloser{ctx: ctx, entered: entered}
		writer := newDeadlineTrackingWriter(nil)
		done := make(chan struct{})
		go func() {
			handler.ServeHTTP(writer, request)
			close(done)
		}()
		awaitSignal(t, entered, "canceling body reader did not enter")
		cancel()
		awaitSignal(t, done, "body-canceled handler did not return")
		assertCanceledOutcome(t, writer.trackingResponseWriter, counters, &logs)
	})

	t.Run("canceled after Backend result before publication", func(t *testing.T) {
		cfg := validServerConfig()
		deps, counters := validServerDependencies()
		barrierContext, cancelBarrier := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancelBarrier()
		publicationReached := make(chan struct{})
		publicationRelease := make(chan struct{})
		deps.IDs = &publicationGateIDs{
			context: barrierContext,
			reached: publicationReached,
			release: publicationRelease,
		}
		var logs bytes.Buffer
		_, handler := newTestServer(t, cfg, validServerBackend(), deps, &logs)
		ctx, cancel := context.WithCancel(context.Background())
		request := httptest.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:8080/v1/responses", strings.NewReader(`{"model":"codex-default","input":"hello"}`))
		request.Host = cfg.Listen
		request.Header.Set("Content-Type", "application/json")
		writer := &trackingResponseWriter{header: make(http.Header)}
		done := make(chan struct{})
		go func() {
			handler.ServeHTTP(writer, request)
			close(done)
		}()
		awaitSignal(t, publicationReached, "response publication barrier was not reached")
		cancel()
		close(publicationRelease)
		awaitSignal(t, done, "publication-canceled handler did not return")
		assertCanceledOutcome(t, writer, counters, &logs)
	})

	t.Run("cancellation and successful Backend return become observable together", func(t *testing.T) {
		cfg := validServerConfig()
		deps, counters := validServerDependencies()
		entered := make(chan struct{})
		var logs bytes.Buffer
		backend := validServerBackend()
		backend.respond = func(ctx context.Context, _ core.Request) (core.Result, error) {
			close(entered)
			<-ctx.Done()
			return core.Result{Text: "ok"}, nil
		}
		_, handler := newTestServer(t, cfg, backend, deps, &logs)
		ctx, cancel := context.WithCancel(context.Background())
		request := httptest.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:8080/v1/responses", strings.NewReader(`{"model":"codex-default","input":"hello"}`))
		request.Host = cfg.Listen
		request.Header.Set("Content-Type", "application/json")
		writer := &trackingResponseWriter{header: make(http.Header)}
		done := make(chan struct{})
		go func() {
			handler.ServeHTTP(writer, request)
			close(done)
		}()
		awaitSignal(t, entered, "simultaneous-return Backend did not enter")
		cancel()
		awaitSignal(t, done, "simultaneous-return handler did not return")
		assertCanceledOutcome(t, writer, counters, &logs)
	})
}

func TestServerRejectsUnsafeInjectedRequestIDWithoutMutation(t *testing.T) {
	for _, source := range []*fixedIDs{
		{unsafe: true},
		{suffix: "short"},
		{suffix: strings.Repeat("1", 26)},
	} {
		cfg := validServerConfig()
		deps, counters := validServerDependencies()
		deps.IDs = source
		var logs bytes.Buffer
		_, handler := newTestServer(t, cfg, validServerBackend(), deps, &logs)
		request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:8080/v1/models", nil)
		request.Host = cfg.Listen
		writer := &trackingResponseWriter{header: make(http.Header)}
		handler.ServeHTTP(writer, request)
		if writer.touched.Load() || writer.status.Load() != 0 || writer.body.Len() != 0 ||
			logs.Len() != 0 || counters.count.Load() != 0 {
			t.Fatalf("unsafe ID escaped: touched=%v status=%d body=%q logs=%q counters=%d",
				writer.touched.Load(), writer.status.Load(), writer.body.String(), logs.String(), counters.count.Load())
		}
	}
}

func servePOST(handler http.Handler, host string, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+host+"/v1/responses", strings.NewReader(body))
	request.Host = host
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func awaitSignal(t *testing.T, signal <-chan struct{}, failure string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	awaitSignalBefore(ctx, t, signal, failure)
}

func awaitValue[T any](t *testing.T, values <-chan T, failure string) T {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	return awaitValueBefore(ctx, t, values, failure)
}

func awaitSignalBefore(
	ctx context.Context,
	t *testing.T,
	signal <-chan struct{},
	failure string,
) {
	t.Helper()
	select {
	case <-signal:
		return
	case <-ctx.Done():
		t.Fatalf("%s: %v", failure, ctx.Err())
	}
}

func awaitValueBefore[T any](
	ctx context.Context,
	t *testing.T,
	values <-chan T,
	failure string,
) T {
	t.Helper()
	select {
	case value, ok := <-values:
		if !ok {
			t.Fatalf("%s: channel closed", failure)
		}
		return value
	case <-ctx.Done():
		t.Fatalf("%s: %v", failure, ctx.Err())
		var zero T
		return zero
	}
}

func waitForRelease(ctx context.Context, release <-chan struct{}) error {
	select {
	case <-release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func assertApplicationHeaders(t *testing.T, header http.Header) {
	t.Helper()
	if header.Get("Content-Type") != "application/json" {
		t.Fatalf("content type=%q", header.Get("Content-Type"))
	}
	if !validGeneratedRequestID(header.Get("X-Request-ID")) {
		t.Fatalf("request ID=%q", header.Get("X-Request-ID"))
	}
	for name := range header {
		if strings.HasPrefix(strings.ToLower(name), "access-control-") {
			t.Fatalf("unexpected CORS header %q", name)
		}
	}
}

func assertServerError(
	t *testing.T,
	response *httptest.ResponseRecorder,
	wantStatus int,
	wantCode string,
	wantParam *string,
) {
	t.Helper()
	if response.Code != wantStatus {
		t.Fatalf("status=%d want=%d body=%s", response.Code, wantStatus, response.Body.String())
	}
	var envelope struct {
		Error struct {
			Message string  `json:"message"`
			Type    string  `json:"type"`
			Param   *string `json:"param"`
			Code    string  `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode body=%q: %v", response.Body.String(), err)
	}
	if envelope.Error.Code != wantCode || !equalStringPointers(envelope.Error.Param, wantParam) {
		t.Fatalf("error=%+v want code=%q param=%v", envelope.Error, wantCode, wantParam)
	}
	canonical := core.Error(wantCode, wantParam)
	if envelope.Error.Message != canonical.MessageText() || envelope.Error.Type != canonical.TypeName() {
		t.Fatalf("error=%+v canonical=%v", envelope.Error, canonical)
	}
}

type timeoutReader struct{}

func (timeoutReader) Read([]byte) (int, error) { return 0, timeoutReadError{} }

type timeoutReadError struct{}

func (timeoutReadError) Error() string   { return "PLANTED_TIMEOUT_SECRET" }
func (timeoutReadError) Timeout() bool   { return true }
func (timeoutReadError) Temporary() bool { return true }

type errorBodyReader struct{}

func (errorBodyReader) Read([]byte) (int, error) {
	return 0, errors.New("PLANTED_BODY_READER_SECRET")
}

type countingReadCloser struct {
	reads  atomic.Int64
	closes atomic.Int64
}

func (r *countingReadCloser) Read([]byte) (int, error) {
	r.reads.Add(1)
	return 0, io.EOF
}

func (r *countingReadCloser) Close() error {
	r.closes.Add(1)
	return nil
}

type blockingReadCloser struct {
	context context.Context
	entered chan struct{}
	release chan struct{}
	payload []byte
	once    sync.Once
	done    bool
	waitErr error
}

func (r *blockingReadCloser) Read(buffer []byte) (int, error) {
	r.once.Do(func() {
		close(r.entered)
		r.waitErr = waitForRelease(r.context, r.release)
	})
	if r.waitErr != nil {
		return 0, r.waitErr
	}
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	return copy(buffer, r.payload), nil
}

func (*blockingReadCloser) Close() error { return nil }

type trackingResponseWriter struct {
	header  http.Header
	touched atomic.Bool
	status  atomic.Int64
	body    bytes.Buffer
}

func (w *trackingResponseWriter) Header() http.Header {
	w.touched.Store(true)
	return w.header
}

func (w *trackingResponseWriter) WriteHeader(status int) {
	w.touched.Store(true)
	w.status.Store(int64(status))
}

func (w *trackingResponseWriter) Write(data []byte) (int, error) {
	w.touched.Store(true)
	return w.body.Write(data)
}

type deadlineTrackingWriter struct {
	*trackingResponseWriter
	installErr   error
	installCalls atomic.Int32
	clearCalls   atomic.Int32
}

func newDeadlineTrackingWriter(installErr error) *deadlineTrackingWriter {
	return &deadlineTrackingWriter{
		trackingResponseWriter: &trackingResponseWriter{header: make(http.Header)},
		installErr:             installErr,
	}
}

func (w *deadlineTrackingWriter) SetReadDeadline(deadline time.Time) error {
	if deadline.IsZero() {
		w.clearCalls.Add(1)
		return nil
	}
	w.installCalls.Add(1)
	return w.installErr
}

type callbackErrorReadCloser struct {
	callback func()
	err      error
	once     sync.Once
}

func (r *callbackErrorReadCloser) Read([]byte) (int, error) {
	r.once.Do(func() {
		if r.callback != nil {
			r.callback()
		}
	})
	return 0, r.err
}

func (*callbackErrorReadCloser) Close() error { return nil }

func exactSizedRequestBodyWithMarker(t *testing.T, size int, marker string) string {
	t.Helper()
	prefix := `{"model":"codex-default","input":"`
	suffix := `"}`
	if size < len(prefix)+len(marker)+len(suffix)+1 {
		t.Fatalf("body size=%d is too small", size)
	}
	body := prefix + marker + strings.Repeat("x", size-len(prefix)-len(marker)-len(suffix)) + suffix
	if len(body) != size {
		t.Fatalf("body size=%d want=%d", len(body), size)
	}
	return body
}

func logicalRequestWeight(request core.Request) int64 {
	return request.Weight()
}

func logicalRequestWeightMust(t *testing.T, body string, limits RequestLimits) int64 {
	t.Helper()
	request, apiErr := DecodeRequest([]byte(body), limits)
	if apiErr != nil {
		t.Fatalf("decode logical request weight: %v", apiErr)
	}
	return logicalRequestWeight(request)
}

type logicalBodyReadTracker struct {
	context        context.Context
	eofEntered     chan struct{}
	eofRelease     <-chan struct{}
	activeReaders  atomic.Int64
	peakReaders    atomic.Int64
	deliveredBytes atomic.Int64
}

type logicalExactBody struct {
	payload    []byte
	tracker    *logicalBodyReadTracker
	enterOnce  sync.Once
	eofOnce    sync.Once
	finishOnce sync.Once
	started    atomic.Bool
	offset     int
	eofErr     error
}

func (r *logicalExactBody) Read(buffer []byte) (int, error) {
	r.enterOnce.Do(func() {
		r.started.Store(true)
		active := r.tracker.activeReaders.Add(1)
		updateAtomicPeak(&r.tracker.peakReaders, active)
	})
	if r.offset < len(r.payload) {
		n := copy(buffer, r.payload[r.offset:])
		r.offset += n
		r.tracker.deliveredBytes.Add(int64(n))
		return n, nil
	}
	r.eofOnce.Do(func() {
		select {
		case r.tracker.eofEntered <- struct{}{}:
		case <-r.tracker.context.Done():
			r.eofErr = r.tracker.context.Err()
			r.finish()
			return
		}
		select {
		case <-r.tracker.eofRelease:
		case <-r.tracker.context.Done():
			r.eofErr = r.tracker.context.Err()
		}
		r.finish()
	})
	if r.eofErr != nil {
		return 0, r.eofErr
	}
	return 0, io.EOF
}

func (r *logicalExactBody) Close() error {
	r.finish()
	return nil
}

func (r *logicalExactBody) finish() {
	r.finishOnce.Do(func() {
		if r.started.Load() {
			r.tracker.activeReaders.Add(-1)
		}
	})
}

type unreadLogicalBody struct {
	payload []byte
	reads   *atomic.Int64
	closes  *atomic.Int64
	offset  int
}

func (r *unreadLogicalBody) Read(buffer []byte) (int, error) {
	r.reads.Add(1)
	if r.offset >= len(r.payload) {
		return 0, io.EOF
	}
	n := copy(buffer, r.payload[r.offset:])
	r.offset += n
	return n, nil
}

func (r *unreadLogicalBody) Close() error {
	r.closes.Add(1)
	return nil
}

type logicalBackendTracker struct {
	context          context.Context
	entered          chan struct{}
	release          <-chan struct{}
	mu               sync.Mutex
	expectedInputs   map[string]struct{}
	seenInputs       map[string]struct{}
	calls            atomic.Int64
	active           atomic.Int64
	normalizedActive atomic.Int64
	logicalWeight    atomic.Int64
	finalBytes       atomic.Int64
	unexpected       atomic.Int64
	duplicates       atomic.Int64
}

func (t *logicalBackendTracker) respond(
	ctx context.Context,
	request core.Request,
	finalBytes int,
) (core.Result, error) {
	t.calls.Add(1)
	t.active.Add(1)
	t.normalizedActive.Add(1)
	t.logicalWeight.Add(request.Weight())
	t.mu.Lock()
	_, expected := t.expectedInputs[request.Input]
	_, duplicate := t.seenInputs[request.Input]
	if expected {
		t.seenInputs[request.Input] = struct{}{}
	}
	t.mu.Unlock()
	if !expected {
		t.unexpected.Add(1)
	}
	if duplicate {
		t.duplicates.Add(1)
	}
	select {
	case t.entered <- struct{}{}:
	case <-t.context.Done():
		t.active.Add(-1)
		return core.Result{}, t.context.Err()
	}
	select {
	case <-t.release:
	case <-t.context.Done():
		t.active.Add(-1)
		return core.Result{}, t.context.Err()
	case <-ctx.Done():
		t.active.Add(-1)
		return core.Result{}, ctx.Err()
	}
	t.active.Add(-1)
	result := strings.Repeat("f", finalBytes)
	t.finalBytes.Add(int64(len(result)))
	return core.Result{Text: result}, nil
}

func (t *logicalBackendTracker) finishRequest(weight int64, finalBytes int) {
	t.normalizedActive.Add(-1)
	t.logicalWeight.Add(-weight)
	t.finalBytes.Add(-int64(finalBytes))
}

type logicalPublicationTracker struct {
	context     context.Context
	entered     chan struct{}
	release     <-chan struct{}
	active      atomic.Int64
	peak        atomic.Int64
	activeBytes atomic.Int64
	peakBytes   atomic.Int64
	writes      atomic.Int64
}

type publicationBlockingWriter struct {
	*httptest.ResponseRecorder
	tracker *logicalPublicationTracker
	once    sync.Once
}

func newPublicationBlockingWriter(tracker *logicalPublicationTracker) *publicationBlockingWriter {
	return &publicationBlockingWriter{
		ResponseRecorder: httptest.NewRecorder(),
		tracker:          tracker,
	}
}

func (w *publicationBlockingWriter) Write(data []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(data)
	w.tracker.writes.Add(1)
	w.once.Do(func() {
		active := w.tracker.active.Add(1)
		activeBytes := w.tracker.activeBytes.Add(int64(len(data)))
		updateAtomicPeak(&w.tracker.peak, active)
		updateAtomicPeak(&w.tracker.peakBytes, activeBytes)
		select {
		case w.tracker.entered <- struct{}{}:
		case <-w.tracker.context.Done():
			w.tracker.active.Add(-1)
			w.tracker.activeBytes.Add(-int64(len(data)))
			return
		}
		select {
		case <-w.tracker.release:
		case <-w.tracker.context.Done():
		}
		w.tracker.active.Add(-1)
		w.tracker.activeBytes.Add(-int64(len(data)))
	})
	return n, err
}

func (*publicationBlockingWriter) SetReadDeadline(time.Time) error { return nil }

func updateAtomicPeak(peak *atomic.Int64, value int64) {
	for {
		current := peak.Load()
		if value <= current || peak.CompareAndSwap(current, value) {
			return
		}
	}
}

type contextReadCloser struct {
	ctx     context.Context
	entered chan struct{}
	once    sync.Once
}

func (r *contextReadCloser) Read([]byte) (int, error) {
	r.once.Do(func() { close(r.entered) })
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}

func (*contextReadCloser) Close() error { return nil }

type publicationGateIDs struct {
	context context.Context
	reached chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (s *publicationGateIDs) Next(prefix string) string {
	if prefix == "resp" {
		s.once.Do(func() {
			close(s.reached)
			_ = waitForRelease(s.context, s.release)
		})
	}
	return prefix + "_" + serverTestIDBody
}

func assertNoResponseMutation(t *testing.T, writer *trackingResponseWriter) {
	t.Helper()
	if writer.touched.Load() || writer.status.Load() != 0 || writer.body.Len() != 0 {
		t.Fatalf("writer touched=%v status=%d body=%q", writer.touched.Load(), writer.status.Load(), writer.body.String())
	}
}

func assertCanceledOutcome(
	t *testing.T,
	writer *trackingResponseWriter,
	counters *testCounters,
	logs *bytes.Buffer,
) {
	t.Helper()
	assertNoResponseMutation(t, writer)
	if counters.count.Load() != 1 {
		t.Fatalf("cancellations=%d", counters.count.Load())
	}
	if logs.Len() != 0 {
		t.Fatalf("logs=%q", logs.String())
	}
}

func assertTrackingError(
	t *testing.T,
	writer *trackingResponseWriter,
	wantStatus int,
	wantCode string,
) {
	t.Helper()
	if writer.status.Load() != int64(wantStatus) {
		t.Fatalf("status=%d want=%d body=%q", writer.status.Load(), wantStatus, writer.body.String())
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(writer.body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode body=%q: %v", writer.body.String(), err)
	}
	if envelope.Error.Code != wantCode {
		t.Fatalf("code=%q want=%q body=%q", envelope.Error.Code, wantCode, writer.body.String())
	}
}

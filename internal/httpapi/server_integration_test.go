//go:build integration

package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

func startTransportServer(
	t *testing.T,
	mutate func(*Config),
	backend *testBackend,
) (string, *http.Server, *testCounters, func()) {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(
		context.Background(),
		"tcp",
		"127.0.0.1:0",
	)
	if err != nil {
		t.Fatal(err)
	}
	return startTransportServerOnListener(t, listener, mutate, backend, nil)
}

func startTransportServerOnListener(
	t *testing.T,
	listener net.Listener,
	mutate func(*Config),
	backend *testBackend,
	logger *slog.Logger,
) (string, *http.Server, *testCounters, func()) {
	t.Helper()
	return startTransportServerOnListenerWithDependencies(
		t,
		listener,
		mutate,
		nil,
		backend,
		logger,
	)
}

func startTransportServerOnListenerWithDependencies(
	t *testing.T,
	listener net.Listener,
	mutateConfig func(*Config),
	mutateDependencies func(*Dependencies),
	backend *testBackend,
	logger *slog.Logger,
) (string, *http.Server, *testCounters, func()) {
	t.Helper()
	address := listener.Addr().String()
	cfg := validServerConfig()
	cfg.Listen = address
	if mutateConfig != nil {
		mutateConfig(&cfg)
	}
	deps, counters := validServerDependencies()
	deps.Now = time.Now
	if mutateDependencies != nil {
		mutateDependencies(&deps)
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	server, _, err := New(
		cfg,
		deps,
		backend,
		logger,
	)
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		select {
		case err := <-serveDone:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				t.Errorf("serve error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("server did not stop")
		}
	}
	return address, server, counters, cleanup
}

func TestTransportOPTIONSStarAndHEADUseApplicationRouter(t *testing.T) {
	address, _, _, cleanup := startTransportServer(t, nil, validServerBackend())
	defer cleanup()

	connection, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close() }()
	if _, err := fmt.Fprintf(connection,
		"OPTIONS * HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n",
		address,
	); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound ||
		response.Header.Get("Content-Type") != "application/json" ||
		!validGeneratedRequestID(response.Header.Get("X-Request-ID")) ||
		!bytes.Contains(body, []byte(`"code":"not_found"`)) {
		t.Fatalf("status=%d headers=%v body=%q", response.StatusCode, response.Header, body)
	}

	request, err := http.NewRequestWithContext(context.Background(), http.MethodHead, "http://"+address+"/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	headResponse, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	headBody, err := io.ReadAll(headResponse.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = headResponse.Body.Close()
	if headResponse.StatusCode != http.StatusMethodNotAllowed || len(headBody) != 0 ||
		headResponse.Header.Get("Content-Type") != "application/json" ||
		!validGeneratedRequestID(headResponse.Header.Get("X-Request-ID")) {
		t.Fatalf("HEAD status=%d headers=%v body=%q", headResponse.StatusCode, headResponse.Header, headBody)
	}
}

func TestTransportBodyDeadlineIsClearedForKeepAlive(t *testing.T) {
	address, _, counters, cleanup := startTransportServer(t, func(cfg *Config) {
		cfg.BodyReadTimeout = 150 * time.Millisecond
	}, validServerBackend())
	defer cleanup()

	t.Run("partial body times out with JSON", func(t *testing.T) {
		connection, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", address)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = connection.Close() }()
		if err := connection.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintf(connection,
			"POST /v1/responses HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{",
			address,
		); err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(bufio.NewReader(connection), nil)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusRequestTimeout ||
			!response.Close ||
			!bytes.Contains(body, []byte(`"code":"request_timeout"`)) {
			t.Fatalf("status=%d close=%v body=%q cancellations=%d",
				response.StatusCode, response.Close, body, counters.count.Load())
		}
		if counters.count.Load() != 0 {
			t.Fatalf("body deadline counted as client cancellation: %d", counters.count.Load())
		}
	})

	t.Run("completed body does not poison next request", func(t *testing.T) {
		connection, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", address)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = connection.Close() }()
		if err := connection.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		reader := bufio.NewReader(connection)
		body := `{"model":"codex-default","input":"hello"}`
		if _, err := fmt.Fprintf(connection,
			"POST /v1/responses HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s",
			address,
			len(body),
			body,
		); err != nil {
			t.Fatal(err)
		}
		first, err := http.ReadResponse(reader, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, first.Body)
		_ = first.Body.Close()
		if first.StatusCode != http.StatusOK {
			t.Fatalf("first status=%d", first.StatusCode)
		}

		if _, err := fmt.Fprintf(connection,
			"GET /v1/models HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n",
			address,
		); err != nil {
			t.Fatal(err)
		}
		second, err := http.ReadResponse(reader, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, second.Body)
		_ = second.Body.Close()
		if second.StatusCode != http.StatusOK {
			t.Fatalf("second status=%d", second.StatusCode)
		}
	})
}

func TestTransportMalformedChunkReadErrorClosesWithoutPostHandlerDrain(t *testing.T) {
	baseListener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := &bodyObservationListener{
		Listener: baseListener,
		accepted: make(chan *bodyObservationConn, 1),
	}
	address, _, _, cleanup := startTransportServerOnListener(t, listener, func(cfg *Config) {
		cfg.HandlerLimit = 1
		cfg.BodyReaderLimit = 1
		cfg.BodyReadTimeout = 5 * time.Second
	}, validServerBackend(), nil)
	defer cleanup()

	connection, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close() }()
	if err := connection.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	observed := awaitValue(t, listener.accepted, "malformed-chunk connection was not accepted")
	if _, err := fmt.Fprintf(
		connection,
		"POST %s HTTP/1.1\r\nHost: %s\r\nContent-Type: %s\r\nTransfer-Encoding: chunked\r\n\r\nZ\r\n",
		responsesEndpoint,
		address,
		applicationContentType,
	); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if response.StatusCode != http.StatusBadRequest || !response.Close ||
		!bytes.Contains(body, []byte(`"code":"invalid_json"`)) ||
		!strings.Contains(observed.responsePrefix(), "\r\nConnection: close\r\n") {
		t.Fatalf("status=%d close=%v wire=%q body=%q",
			response.StatusCode, response.Close, observed.responsePrefix(), body)
	}
	awaitSignal(t, observed.closed, "malformed-chunk connection did not close")
	if observed.activeReads.Load() != 0 {
		t.Fatalf("active server reads=%d", observed.activeReads.Load())
	}
	assertTransportPOSTStatus(
		t,
		address,
		`{"model":"codex-default","input":"after-read-error"}`,
		http.StatusOK,
	)
}

func TestTransportBodyLimitForContentLengthAndChunked(t *testing.T) {
	address, _, _, cleanup := startTransportServer(t, func(cfg *Config) {
		cfg.HTTPBodyBytes = 128
		cfg.RequestLimits.InputBytes = 128
		cfg.RequestLimits.InstructionsBytes = 128
		cfg.RequestLimits.SchemaBytes = 128
	}, validServerBackend())
	defer cleanup()

	exact := exactSizedRequestBodyWithMarker(t, 128, "")
	tooLarge := exact + " "
	for _, test := range []struct {
		name       string
		body       string
		chunked    bool
		wantStatus int
		wantCode   string
	}{
		{"Content-Length exact", exact, false, http.StatusOK, ""},
		{"Content-Length plus one", tooLarge, false, http.StatusRequestEntityTooLarge, core.CodeRequestTooLarge},
		{"chunked exact", exact, true, http.StatusOK, ""},
		{"chunked plus one", tooLarge, true, http.StatusRequestEntityTooLarge, core.CodeRequestTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			var response *http.Response
			var err error
			if test.chunked {
				response, err = roundTripChunkedBody(address, test.body)
			} else {
				request, requestErr := http.NewRequestWithContext(
					context.Background(),
					http.MethodPost,
					"http://"+address+responsesEndpoint,
					strings.NewReader(test.body),
				)
				if requestErr != nil {
					t.Fatal(requestErr)
				}
				request.Header.Set("Content-Type", applicationContentType)
				if request.ContentLength != int64(len(test.body)) {
					t.Fatalf("Content-Length=%d want=%d", request.ContentLength, len(test.body))
				}
				response, err = http.DefaultClient.Do(request)
			}
			if err != nil {
				t.Fatal(err)
			}
			body, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if response.StatusCode != test.wantStatus {
				t.Fatalf("status=%d want=%d body=%q", response.StatusCode, test.wantStatus, body)
			}
			if test.wantCode != "" && !bytes.Contains(body, []byte(`"code":"`+test.wantCode+`"`)) {
				t.Fatalf("body=%q want code=%q", body, test.wantCode)
			}
		})
	}
}

func TestTransportLiteralHostMatrix(t *testing.T) {
	address, _, _, cleanup := startTransportServer(t, nil, validServerBackend())
	defer cleanup()

	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		host       string
		wantStatus int
	}{
		{"exact configured literal", address, http.StatusOK},
		{"fixed localhost literal", net.JoinHostPort("localhost", port), http.StatusOK},
		{"wrong port", net.JoinHostPort("127.0.0.1", "1"), http.StatusBadRequest},
		{"wrong loopback IP", net.JoinHostPort("127.0.0.2", port), http.StatusBadRequest},
		{"alternate IPv4 spelling", net.JoinHostPort("127.000.000.001", port), http.StatusBadRequest},
		{"uppercase localhost", net.JoinHostPort("LOCALHOST", port), http.StatusBadRequest},
		{"dotted localhost", net.JoinHostPort("localhost.", port), http.StatusBadRequest},
		{"userinfo", "user@" + address, http.StatusBadRequest},
		{"other hostname", net.JoinHostPort("other.invalid", port), http.StatusBadRequest},
		{"localhost without port", "localhost", http.StatusBadRequest},
		{"empty Host", "", http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			var response *http.Response
			var requestErr error
			if test.host == "" || strings.Contains(test.host, "@") {
				response, requestErr = roundTripRawHost(address, test.host)
			} else {
				var request *http.Request
				request, requestErr = http.NewRequestWithContext(
					context.Background(),
					http.MethodGet,
					"http://"+address+modelsEndpoint,
					nil,
				)
				if requestErr == nil {
					request.Host = test.host
					response, requestErr = http.DefaultClient.Do(request)
				}
			}
			if requestErr != nil {
				t.Fatal(requestErr)
			}
			body, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if response.StatusCode != test.wantStatus {
				t.Fatalf("host=%q status=%d want=%d body=%q", test.host, response.StatusCode, test.wantStatus, body)
			}
			if test.wantStatus == http.StatusBadRequest &&
				!strings.Contains(test.host, "@") &&
				!bytes.Contains(body, []byte(`"code":"invalid_request"`)) {
				t.Fatalf("host=%q body=%q", test.host, body)
			}
		})
	}
}

func TestTransportEncodedPathsNeverMatchLiteralRoutes(t *testing.T) {
	address, _, _, cleanup := startTransportServer(t, nil, validServerBackend())
	defer cleanup()

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
			response, err := roundTripRawRequest(
				address,
				fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", target, address),
			)
			if err != nil {
				t.Fatal(err)
			}
			body, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if response.StatusCode != http.StatusNotFound ||
				!bytes.Contains(body, []byte(`"code":"not_found"`)) {
				t.Fatalf("target=%q status=%d body=%q", target, response.StatusCode, body)
			}
		})
	}
}

func TestTransportUnreadEarlyResponsesCloseWithoutDraining(t *testing.T) {
	type earlyCase struct {
		name          string
		method        string
		target        string
		host          func(string) string
		contentType   string
		authorization string
		authEnabled   bool
		largeModels   bool
		wantStatus    int
		wantCode      string
	}
	for _, test := range []earlyCase{
		{
			name: "wrong Host", method: http.MethodPost, target: responsesEndpoint,
			host: func(string) string { return "wrong.invalid:8080" }, contentType: applicationContentType,
			wantStatus: http.StatusBadRequest, wantCode: core.CodeInvalidRequest,
		},
		{
			name: "query", method: http.MethodGet, target: modelsEndpoint + "?PLANTED_QUERY_SECRET=x",
			wantStatus: http.StatusBadRequest, wantCode: core.CodeUnsupportedParameter,
		},
		{
			name: "authentication", method: http.MethodGet, target: modelsEndpoint,
			authEnabled: true, wantStatus: http.StatusUnauthorized, wantCode: core.CodeInvalidBearerKey,
		},
		{
			name: "wrong route", method: http.MethodGet, target: "/PLANTED_UNREAD_ROUTE",
			wantStatus: http.StatusNotFound, wantCode: core.CodeNotFound,
		},
		{
			name: "wrong method", method: http.MethodGet, target: responsesEndpoint,
			wantStatus: http.StatusMethodNotAllowed, wantCode: core.CodeMethodNotAllowed,
		},
		{
			name: "invalid media", method: http.MethodPost, target: responsesEndpoint, contentType: "text/plain",
			wantStatus: http.StatusUnsupportedMediaType, wantCode: core.CodeUnsupportedMediaType,
		},
		{
			name: "large models response", method: http.MethodGet, target: modelsEndpoint, largeModels: true,
			wantStatus: http.StatusOK,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			baseListener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			listener := &bodyObservationListener{
				Listener: baseListener,
				accepted: make(chan *bodyObservationConn, 1),
			}
			backend := validServerBackend()
			if test.largeModels {
				backend.models = transportTestModels(100)
			}
			address, _, _, cleanup := startTransportServerOnListenerWithDependencies(
				t,
				listener,
				func(cfg *Config) {
					cfg.BodyReadTimeout = 100 * time.Millisecond
					cfg.HandlerLimit = 1
					cfg.BodyReaderLimit = 1
					if test.largeModels {
						cfg.MaxModels = 100
					}
					if test.authEnabled {
						cfg.APIKeyEnv = "AI_CLI_GATEWAY_API_KEY"
					}
				},
				func(deps *Dependencies) {
					if test.authEnabled {
						deps.LookupEnv = func(string) (string, bool) { return "gateway-secret", true }
					}
				},
				backend,
				nil,
			)
			defer cleanup()

			host := address
			if test.host != nil {
				host = test.host(address)
			}
			result, observed := roundTripWithheldBody(
				t,
				listener,
				address,
				withheldRequest{
					method: test.method, target: test.target, host: host,
					contentType: test.contentType, authorization: test.authorization,
				},
			)
			assertWithheldResponse(t, result, observed, test.wantStatus, test.wantCode)
			if test.largeModels && len(result.body) <= 4<<10 {
				t.Fatalf("models body=%d bytes, want a large response", len(result.body))
			}

			followupAuthorization := ""
			if test.authEnabled {
				followupAuthorization = "Bearer gateway-secret"
			}
			assertTransportStatus(
				t,
				address,
				http.MethodGet,
				modelsEndpoint,
				followupAuthorization,
				http.StatusOK,
			)
		})
	}
}

func TestTransportUnreadGateRejectionsCloseWithoutDraining(t *testing.T) {
	t.Run("handler gate full", func(t *testing.T) {
		baseListener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listener := &bodyObservationListener{
			Listener: baseListener,
			accepted: make(chan *bodyObservationConn, 1),
		}
		barrierContext, cancelBarrier := context.WithTimeout(t.Context(), 3*time.Second)
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
		address, _, _, cleanup := startTransportServerOnListener(t, listener, func(cfg *Config) {
			cfg.HandlerLimit = 1
			cfg.BodyReaderLimit = 1
			cfg.BodyReadTimeout = 100 * time.Millisecond
		}, backend, nil)
		defer cleanup()

		firstRequest, err := http.NewRequestWithContext(
			context.Background(),
			http.MethodPost,
			"http://"+address+responsesEndpoint,
			strings.NewReader(`{"model":"codex-default","input":"one"}`),
		)
		if err != nil {
			t.Fatal(err)
		}
		firstRequest.Header.Set("Content-Type", applicationContentType)
		firstTransport := &http.Transport{DisableKeepAlives: true}
		defer firstTransport.CloseIdleConnections()
		firstResult := make(chan transportResponseResult, 1)
		go func() {
			response, requestErr := firstTransport.RoundTrip(firstRequest)
			result := transportResponseResult{err: requestErr}
			if requestErr == nil {
				result.status = response.StatusCode
				result.close = response.Close
				result.body, result.err = io.ReadAll(response.Body)
				_ = response.Body.Close()
			}
			firstResult <- result
		}()
		_ = awaitValue(t, listener.accepted, "saturating handler connection was not accepted")
		awaitSignal(t, entered, "saturating handler Backend did not enter")

		result, observed := roundTripWithheldBody(
			t,
			listener,
			address,
			withheldRequest{
				method: http.MethodPost, target: responsesEndpoint, host: address,
				contentType: applicationContentType,
			},
		)
		assertWithheldResponse(t, result, observed, http.StatusTooManyRequests, core.CodeServerBusy)

		close(release)
		first := awaitValue(t, firstResult, "saturating handler request did not finish")
		if first.err != nil {
			t.Fatal(first.err)
		}
		if first.status != http.StatusOK {
			t.Fatalf("status=%d body=%q", first.status, first.body)
		}
		assertTransportStatus(t, address, http.MethodGet, modelsEndpoint, "", http.StatusOK)
	})

	t.Run("body-reader gate full", func(t *testing.T) {
		baseListener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listener := &bodyObservationListener{
			Listener: baseListener,
			accepted: make(chan *bodyObservationConn, 1),
		}
		var backendCalls atomic.Int32
		backend := validServerBackend()
		backend.respond = func(context.Context, core.Request) (core.Result, error) {
			backendCalls.Add(1)
			return core.Result{Text: "ok"}, nil
		}
		address, _, _, cleanup := startTransportServerOnListener(t, listener, func(cfg *Config) {
			cfg.HandlerLimit = 2
			cfg.BodyReaderLimit = 1
			cfg.BodyReadTimeout = 5 * time.Second
		}, backend, nil)
		defer cleanup()

		firstConnection, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", address)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = firstConnection.Close() }()
		if err := firstConnection.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		firstObserved := awaitValue(t, listener.accepted, "blocking body connection was not accepted")
		payload := `{"model":"codex-default","input":"one"}`
		if _, err := fmt.Fprintf(
			firstConnection,
			"POST %s HTTP/1.1\r\nHost: %s\r\nContent-Type: %s\r\nContent-Length: %d\r\n\r\n%s",
			responsesEndpoint,
			address,
			applicationContentType,
			len(payload)+1,
			payload,
		); err != nil {
			t.Fatal(err)
		}
		awaitSignal(t, firstObserved.waitingForBody, "first handler did not block at body EOF")

		result, observed := roundTripWithheldBody(
			t,
			listener,
			address,
			withheldRequest{
				method: http.MethodPost, target: responsesEndpoint, host: address,
				contentType: applicationContentType,
			},
		)
		assertWithheldResponse(t, result, observed, http.StatusTooManyRequests, core.CodeServerBusy)
		if backendCalls.Load() != 0 {
			t.Fatalf("Backend calls before first body EOF=%d", backendCalls.Load())
		}

		if _, err := io.WriteString(firstConnection, " "); err != nil {
			t.Fatal(err)
		}
		firstResponse, err := http.ReadResponse(bufio.NewReader(firstConnection), nil)
		if err != nil {
			t.Fatal(err)
		}
		firstBody, readErr := io.ReadAll(firstResponse.Body)
		_ = firstResponse.Body.Close()
		if readErr != nil || firstResponse.StatusCode != http.StatusOK {
			t.Fatalf("status=%d body=%q err=%v", firstResponse.StatusCode, firstBody, readErr)
		}
		_ = firstConnection.Close()
		assertTransportPOSTStatus(
			t,
			address,
			`{"model":"codex-default","input":"two"}`,
			http.StatusOK,
		)
		if backendCalls.Load() != 2 {
			t.Fatalf("Backend calls=%d want=2", backendCalls.Load())
		}
	})
}

func TestTransportHeaderAndIdleBounds(t *testing.T) {
	address, _, _, cleanup := startTransportServer(t, func(cfg *Config) {
		cfg.ReadHeaderTimeout = 100 * time.Millisecond
		cfg.IdleTimeout = 100 * time.Millisecond
		cfg.MaxHeaderBytes = 1024
	}, validServerBackend())
	defer cleanup()

	t.Run("slow incomplete header is closed", func(t *testing.T) {
		connection, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", address)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = connection.Close() }()
		if err := connection.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		_, _ = fmt.Fprintf(connection, "GET /v1/models HTTP/1.1\r\nHost: %s\r\nX-Incomplete:", address)
		_, err = bufio.NewReader(connection).ReadByte()
		if err == nil {
			t.Fatal("slow header connection remained readable")
		}
	})

	t.Run("grossly oversized header is bounded before handler", func(t *testing.T) {
		connection, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", address)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = connection.Close() }()
		if err := connection.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		_, _ = fmt.Fprintf(connection,
			"GET /v1/models HTTP/1.1\r\nHost: %s\r\nX-Large: %s\r\n\r\n",
			address,
			strings.Repeat("a", 32<<10),
		)
		response, err := http.ReadResponse(bufio.NewReader(connection), nil)
		if err == nil {
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode < 400 {
				t.Fatalf("oversized header status=%d", response.StatusCode)
			}
		}
	})

	t.Run("idle connection is closed", func(t *testing.T) {
		connection, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", address)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = connection.Close() }()
		if err := connection.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		reader := bufio.NewReader(connection)
		_, _ = fmt.Fprintf(connection, "GET /v1/models HTTP/1.1\r\nHost: %s\r\n\r\n", address)
		response, err := http.ReadResponse(reader, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("status=%d", response.StatusCode)
		}
		_, err = reader.ReadByte()
		if err == nil {
			t.Fatal("idle connection remained open")
		}
	})
}

func TestTransportDisconnectCancelsBackend(t *testing.T) {
	entered := make(chan struct{})
	canceled := make(chan struct{})
	var calls atomic.Int32
	backend := validServerBackend()
	backend.respond = func(ctx context.Context, _ core.Request) (core.Result, error) {
		calls.Add(1)
		close(entered)
		<-ctx.Done()
		close(canceled)
		return core.Result{}, ctx.Err()
	}
	address, _, counters, cleanup := startTransportServer(t, nil, backend)
	defer cleanup()

	connection, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"model":"codex-default","input":"hello"}`
	_, err = fmt.Fprintf(connection,
		"POST /v1/responses HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s",
		address,
		len(body),
		body,
	)
	if err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		_ = connection.Close()
		t.Fatal("backend not entered")
	}
	_ = connection.Close()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("backend context not canceled")
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
	deadline := time.Now().Add(2 * time.Second)
	for counters.count.Load() != 1 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if counters.count.Load() != 1 {
		t.Fatalf("cancellations=%d", counters.count.Load())
	}
}

func TestTransportDisconnectDuringBodyReadCountsOnceWithoutBackend(t *testing.T) {
	baseListener, err := (&net.ListenConfig{}).Listen(
		context.Background(),
		"tcp",
		"127.0.0.1:0",
	)
	if err != nil {
		t.Fatal(err)
	}
	listener := &bodyObservationListener{
		Listener: baseListener,
		accepted: make(chan *bodyObservationConn, 1),
	}
	var backendCalls atomic.Int32
	backend := validServerBackend()
	backend.respond = func(context.Context, core.Request) (core.Result, error) {
		backendCalls.Add(1)
		return core.Result{Text: "unexpected"}, nil
	}
	logs := newSynchronizedLogBuffer()
	address, _, counters, cleanup := startTransportServerOnListener(t, listener, func(cfg *Config) {
		cfg.BodyReadTimeout = 5 * time.Second
	}, backend, slog.New(slog.NewTextHandler(logs, nil)))
	defer cleanup()

	connection, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(
		connection,
		"POST %s HTTP/1.1\r\nHost: %s\r\nContent-Type: %s\r\nContent-Length: 100\r\n\r\n{",
		responsesEndpoint,
		address,
		applicationContentType,
	); err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	var observed *bodyObservationConn
	select {
	case observed = <-listener.accepted:
	case <-time.After(2 * time.Second):
		_ = connection.Close()
		t.Fatal("server connection was not accepted")
	}
	select {
	case <-observed.waitingForBody:
	case <-time.After(2 * time.Second):
		_ = connection.Close()
		t.Fatal("handler did not block reading the partial body")
	}
	_ = connection.Close()

	deadline := time.Now().Add(2 * time.Second)
	for counters.count.Load() != 1 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if counters.count.Load() != 1 || backendCalls.Load() != 0 {
		t.Fatalf("cancellations=%d backend_calls=%d", counters.count.Load(), backendCalls.Load())
	}
	select {
	case <-observed.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("disconnected server connection did not close")
	}
	if logs.String() != "" {
		t.Fatalf("logs=%q", logs.String())
	}
}

func TestTransportSimultaneousOwnedDeadlineAndDisconnectPrefersDeadline(t *testing.T) {
	baseListener, err := (&net.ListenConfig{}).Listen(
		context.Background(),
		"tcp",
		"127.0.0.1:0",
	)
	if err != nil {
		t.Fatal(err)
	}
	boundaryContext, cancelBoundary := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancelBoundary()
	listener := &deadlineBoundaryListener{
		Listener: baseListener,
		accepted: make(chan *deadlineBoundaryConn, 1),
		context:  boundaryContext,
	}
	logs := newSynchronizedLogBuffer()
	logger := slog.New(slog.NewTextHandler(logs, nil))
	var backendCalls atomic.Int32
	backend := validServerBackend()
	backend.respond = func(context.Context, core.Request) (core.Result, error) {
		backendCalls.Add(1)
		return core.Result{Text: "unexpected"}, nil
	}
	address, _, counters, cleanup := startTransportServerOnListener(t, listener, func(cfg *Config) {
		// The boundary wrapper distinguishes this long application-body deadline
		// from net/http's short header deadline.
		cfg.ReadHeaderTimeout = 250 * time.Millisecond
		cfg.BodyReadTimeout = 10 * time.Second
	}, backend, logger)
	defer cleanup()

	connection, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(
		connection,
		"POST %s HTTP/1.1\r\nHost: %s\r\nContent-Type: %s\r\nContent-Length: 100\r\n\r\n{",
		responsesEndpoint,
		address,
		applicationContentType,
	); err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	var boundary *deadlineBoundaryConn
	select {
	case boundary = <-listener.accepted:
	case <-time.After(2 * time.Second):
		_ = connection.Close()
		t.Fatal("server connection was not accepted")
	}
	select {
	case <-boundary.bodyDeadlineInstalled:
	case <-time.After(2 * time.Second):
		_ = connection.Close()
		t.Fatal("application body deadline was not installed")
	}
	if err := boundary.Conn.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	select {
	case <-boundary.timeoutObserved:
	case <-time.After(2 * time.Second):
		_ = connection.Close()
		t.Fatal("server read did not observe the owned deadline sentinel")
	}

	// Hold the real os.ErrDeadlineExceeded at the transport boundary, then use a
	// second server-side socket read to prove the peer FIN was observed before
	// releasing the captured timeout. A timeout-only execution cannot cross this
	// barrier. The installed OS sentinel retains stable precedence: fixed 408
	// telemetry, never a client-cancel count.
	if err := boundary.Conn.SetReadDeadline(time.Time{}); err != nil {
		_ = connection.Close()
		t.Fatal(err)
	}
	peerCloseObserved := make(chan error, 1)
	go func() {
		buffer := make([]byte, 1)
		_, readErr := boundary.Conn.Read(buffer)
		peerCloseObserved <- readErr
	}()
	_ = connection.Close()
	peerReadErr := awaitValue(t, peerCloseObserved, "server did not observe peer disconnect")
	if !errors.Is(peerReadErr, io.EOF) {
		t.Fatalf("peer close read error=%v", peerReadErr)
	}
	close(boundary.releaseTimeout)
	select {
	case <-logs.written:
	case <-time.After(2 * time.Second):
		t.Fatal("deadline outcome was not published")
	}
	logText := logs.String()
	if counters.count.Load() != 0 || backendCalls.Load() != 0 ||
		!strings.Contains(logText, "endpoint=/v1/responses") ||
		!strings.Contains(logText, "status=408") ||
		!strings.Contains(logText, "error_code=request_timeout") {
		t.Fatalf("cancellations=%d backend_calls=%d logs=%q",
			counters.count.Load(), backendCalls.Load(), logText)
	}
}

type bodyObservationListener struct {
	net.Listener
	accepted chan *bodyObservationConn
}

func (l *bodyObservationListener) Accept() (net.Conn, error) {
	connection, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	observed := &bodyObservationConn{
		Conn:           connection,
		waitingForBody: make(chan struct{}),
		closed:         make(chan struct{}),
	}
	l.accepted <- observed
	return observed, nil
}

type bodyObservationConn struct {
	net.Conn
	mu             sync.Mutex
	headerBytes    []byte
	headerComplete bool
	waitingForBody chan struct{}
	waitingOnce    sync.Once
	activeReads    atomic.Int64
	writeMu        sync.Mutex
	writePrefix    []byte
	closed         chan struct{}
	closeOnce      sync.Once
}

func (c *bodyObservationConn) Read(buffer []byte) (int, error) {
	c.activeReads.Add(1)
	defer c.activeReads.Add(-1)
	c.mu.Lock()
	headerComplete := c.headerComplete
	c.mu.Unlock()
	if headerComplete {
		c.waitingOnce.Do(func() { close(c.waitingForBody) })
	}

	n, err := c.Conn.Read(buffer)
	if n > 0 {
		c.mu.Lock()
		if !c.headerComplete {
			c.headerBytes = append(c.headerBytes, buffer[:n]...)
			if bytes.Contains(c.headerBytes, []byte("\r\n\r\n")) {
				c.headerComplete = true
				c.headerBytes = nil
			}
		}
		c.mu.Unlock()
	}
	return n, err
}

func (c *bodyObservationConn) Close() error {
	err := c.Conn.Close()
	c.closeOnce.Do(func() { close(c.closed) })
	return err
}

func (c *bodyObservationConn) Write(buffer []byte) (int, error) {
	n, err := c.Conn.Write(buffer)
	if n > 0 {
		c.writeMu.Lock()
		remaining := (4 << 10) - len(c.writePrefix)
		if remaining > 0 {
			if n < remaining {
				remaining = n
			}
			c.writePrefix = append(c.writePrefix, buffer[:remaining]...)
		}
		c.writeMu.Unlock()
	}
	return n, err
}

func (c *bodyObservationConn) responsePrefix() string {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return string(c.writePrefix)
}

type deadlineBoundaryListener struct {
	net.Listener
	accepted chan *deadlineBoundaryConn
	context  context.Context
}

func (l *deadlineBoundaryListener) Accept() (net.Conn, error) {
	connection, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	boundary := &deadlineBoundaryConn{
		Conn:                  connection,
		context:               l.context,
		bodyDeadlineInstalled: make(chan struct{}),
		timeoutObserved:       make(chan struct{}),
		releaseTimeout:        make(chan struct{}),
	}
	l.accepted <- boundary
	return boundary, nil
}

type deadlineBoundaryConn struct {
	net.Conn
	context               context.Context
	bodyDeadlineInstalled chan struct{}
	timeoutObserved       chan struct{}
	releaseTimeout        chan struct{}
	installOnce           sync.Once
	timeoutOnce           sync.Once
}

func (c *deadlineBoundaryConn) SetReadDeadline(deadline time.Time) error {
	err := c.Conn.SetReadDeadline(deadline)
	if err == nil && !deadline.IsZero() && deadline.After(time.Now().Add(2*time.Second)) {
		c.installOnce.Do(func() { close(c.bodyDeadlineInstalled) })
	}
	return err
}

func (c *deadlineBoundaryConn) Read(buffer []byte) (int, error) {
	n, err := c.Conn.Read(buffer)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		c.timeoutOnce.Do(func() { close(c.timeoutObserved) })
		select {
		case <-c.releaseTimeout:
		case <-c.context.Done():
		}
	}
	return n, err
}

type synchronizedLogBuffer struct {
	mu      sync.Mutex
	buffer  bytes.Buffer
	written chan struct{}
	once    sync.Once
}

func newSynchronizedLogBuffer() *synchronizedLogBuffer {
	return &synchronizedLogBuffer{written: make(chan struct{})}
}

func (b *synchronizedLogBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	n, err := b.buffer.Write(data)
	b.mu.Unlock()
	b.once.Do(func() { close(b.written) })
	return n, err
}

func (b *synchronizedLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func roundTripChunkedBody(address string, body string) (*http.Response, error) {
	connection, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", address)
	if err != nil {
		return nil, err
	}
	if err := connection.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		_ = connection.Close()
		return nil, err
	}
	if _, err := fmt.Fprintf(
		connection,
		"POST %s HTTP/1.1\r\nHost: %s\r\nContent-Type: %s\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n%x\r\n%s\r\n0\r\n\r\n",
		responsesEndpoint,
		address,
		applicationContentType,
		len(body),
		body,
	); err != nil {
		_ = connection.Close()
		return nil, err
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	if err != nil {
		_ = connection.Close()
		return nil, err
	}
	response.Body = &connectionResponseBody{
		ReadCloser: response.Body,
		connection: connection,
	}
	return response, nil
}

func roundTripRawHost(address string, host string) (*http.Response, error) {
	return roundTripRawRequest(
		address,
		fmt.Sprintf(
			"GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n",
			modelsEndpoint,
			host,
		),
	)
}

func roundTripRawRequest(address string, rawRequest string) (*http.Response, error) {
	connection, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", address)
	if err != nil {
		return nil, err
	}
	if err := connection.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		_ = connection.Close()
		return nil, err
	}
	if _, err := io.WriteString(connection, rawRequest); err != nil {
		_ = connection.Close()
		return nil, err
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	if err != nil {
		_ = connection.Close()
		return nil, err
	}
	response.Body = &connectionResponseBody{
		ReadCloser: response.Body,
		connection: connection,
	}
	return response, nil
}

type withheldRequest struct {
	method        string
	target        string
	host          string
	contentType   string
	authorization string
}

type transportResponseResult struct {
	status int
	close  bool
	body   []byte
	err    error
}

func roundTripWithheldBody(
	t *testing.T,
	listener *bodyObservationListener,
	address string,
	request withheldRequest,
) (transportResponseResult, *bodyObservationConn) {
	t.Helper()
	connection, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close() }()
	if err := connection.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	observed := awaitValue(t, listener.accepted, "server did not accept withheld-body connection")

	var headers strings.Builder
	_, _ = fmt.Fprintf(
		&headers,
		"%s %s HTTP/1.1\r\nHost: %s\r\nContent-Length: 1\r\n",
		request.method,
		request.target,
		request.host,
	)
	if request.contentType != "" {
		_, _ = fmt.Fprintf(&headers, "Content-Type: %s\r\n", request.contentType)
	}
	if request.authorization != "" {
		_, _ = fmt.Fprintf(&headers, "Authorization: %s\r\n", request.authorization)
	}
	_, _ = io.WriteString(&headers, "\r\n")
	if _, err := io.WriteString(connection, headers.String()); err != nil {
		t.Fatal(err)
	}

	results := make(chan transportResponseResult, 1)
	go func() {
		response, readErr := http.ReadResponse(bufio.NewReader(connection), nil)
		result := transportResponseResult{err: readErr}
		if readErr == nil {
			result.status = response.StatusCode
			result.close = response.Close
			result.body, result.err = io.ReadAll(response.Body)
			_ = response.Body.Close()
		}
		results <- result
	}()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	var result transportResponseResult
	select {
	case result = <-results:
	case <-timer.C:
		_ = connection.Close()
		t.Fatal("withheld-body response exceeded the fixed transport bound")
	}
	if result.err != nil {
		t.Fatal(result.err)
	}
	awaitSignal(t, observed.closed, "withheld-body connection did not close")
	return result, observed
}

func assertWithheldResponse(
	t *testing.T,
	result transportResponseResult,
	observed *bodyObservationConn,
	wantStatus int,
	wantCode string,
) {
	t.Helper()
	if result.status != wantStatus || !result.close {
		t.Fatalf("status=%d close=%v body=%q", result.status, result.close, result.body)
	}
	if !strings.Contains(observed.responsePrefix(), "\r\nConnection: close\r\n") {
		t.Fatalf("wire response did not contain Connection: close: %q", observed.responsePrefix())
	}
	if wantCode != "" && !bytes.Contains(result.body, []byte(`"code":"`+wantCode+`"`)) {
		t.Fatalf("body=%q want code=%q", result.body, wantCode)
	}
	if observed.activeReads.Load() != 0 {
		t.Fatalf("active server reads=%d", observed.activeReads.Load())
	}
}

func assertTransportStatus(
	t *testing.T,
	address string,
	method string,
	target string,
	authorization string,
	wantStatus int,
) {
	t.Helper()
	request, err := http.NewRequestWithContext(
		context.Background(),
		method,
		"http://"+address+target,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("status=%d want=%d body=%q", response.StatusCode, wantStatus, body)
	}
}

func assertTransportPOSTStatus(t *testing.T, address string, body string, wantStatus int) {
	t.Helper()
	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"http://"+address+responsesEndpoint,
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", applicationContentType)
	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	responseBody, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("status=%d want=%d body=%q", response.StatusCode, wantStatus, responseBody)
	}
}

func transportTestModels(count int) []core.Model {
	models := make([]core.Model, count)
	for index := range count {
		models[index] = core.Model{
			ID:            fmt.Sprintf("codex-test-%03d", index),
			Provider:      core.ProviderCodex,
			ProviderModel: fmt.Sprintf("gpt-test-%03d", index),
			Created:       int64(index),
		}
	}
	return models
}

type connectionResponseBody struct {
	io.ReadCloser
	connection net.Conn
}

func (b *connectionResponseBody) Close() error {
	bodyErr := b.ReadCloser.Close()
	connectionErr := b.connection.Close()
	if bodyErr != nil {
		return bodyErr
	}
	return connectionErr
}

package httpclient

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBuildTransport_AppliesProxyTransportOptions(t *testing.T) {
	transport, err := buildTransport(Options{
		ProxyURL: "http://proxy.example.com:8080?_sub2api_force_http1=1&_sub2api_disable_keep_alive=true",
	})
	require.NoError(t, err)
	require.False(t, transport.ForceAttemptHTTP2)
	require.NotNil(t, transport.TLSNextProto)
	require.True(t, transport.DisableKeepAlives)

	req, err := http.NewRequest(http.MethodGet, "http://upstream.example.com/probe", nil)
	require.NoError(t, err)
	configuredProxy, err := transport.Proxy(req)
	require.NoError(t, err)
	require.Equal(t, "http://proxy.example.com:8080", configuredProxy.String())
}

func TestBuildTransport_DisableKeepAliveUsesFreshProxyConnection(t *testing.T) {
	for _, tc := range []struct {
		name            string
		option          string
		wantConnections int32
	}{
		{name: "default reuses connection", wantConnections: 1},
		{name: "disabled opens a new connection", option: "?_sub2api_disable_keep_alive=1", wantConnections: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var connections atomic.Int32
			proxyServer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Length", "2")
				_, _ = io.WriteString(w, "ok")
			}))
			proxyServer.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				if state == http.StateNew {
					connections.Add(1)
				}
			}
			proxyServer.Start()
			defer proxyServer.Close()

			transport, err := buildTransport(Options{ProxyURL: proxyServer.URL + tc.option})
			require.NoError(t, err)
			client := &http.Client{Transport: transport}
			defer client.CloseIdleConnections()

			for range 2 {
				resp, requestErr := client.Get("http://upstream.invalid/probe")
				require.NoError(t, requestErr)
				_, requestErr = io.Copy(io.Discard, resp.Body)
				require.NoError(t, requestErr)
				require.NoError(t, resp.Body.Close())
			}

			require.Equal(t, tc.wantConnections, connections.Load())
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestValidatedTransport_CacheHostValidation(t *testing.T) {
	originalValidate := validateResolvedIP
	defer func() { validateResolvedIP = originalValidate }()

	var validateCalls int32
	validateResolvedIP = func(host string) error {
		atomic.AddInt32(&validateCalls, 1)
		require.Equal(t, "api.openai.com", host)
		return nil
	}

	var baseCalls int32
	base := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		atomic.AddInt32(&baseCalls, 1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Header:     make(http.Header),
		}, nil
	})

	now := time.Unix(1730000000, 0)
	transport := newValidatedTransport(base)
	transport.now = func() time.Time { return now }

	req, err := http.NewRequest(http.MethodGet, "https://api.openai.com/v1/responses", nil)
	require.NoError(t, err)

	_, err = transport.RoundTrip(req)
	require.NoError(t, err)
	_, err = transport.RoundTrip(req)
	require.NoError(t, err)

	require.Equal(t, int32(1), atomic.LoadInt32(&validateCalls))
	require.Equal(t, int32(2), atomic.LoadInt32(&baseCalls))
}

func TestValidatedTransport_ExpiredCacheTriggersRevalidation(t *testing.T) {
	originalValidate := validateResolvedIP
	defer func() { validateResolvedIP = originalValidate }()

	var validateCalls int32
	validateResolvedIP = func(_ string) error {
		atomic.AddInt32(&validateCalls, 1)
		return nil
	}

	base := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Header:     make(http.Header),
		}, nil
	})

	now := time.Unix(1730001000, 0)
	transport := newValidatedTransport(base)
	transport.now = func() time.Time { return now }

	req, err := http.NewRequest(http.MethodGet, "https://api.openai.com/v1/responses", nil)
	require.NoError(t, err)

	_, err = transport.RoundTrip(req)
	require.NoError(t, err)

	now = now.Add(validatedHostTTL + time.Second)
	_, err = transport.RoundTrip(req)
	require.NoError(t, err)

	require.Equal(t, int32(2), atomic.LoadInt32(&validateCalls))
}

func TestValidatedTransport_ValidationErrorStopsRoundTrip(t *testing.T) {
	originalValidate := validateResolvedIP
	defer func() { validateResolvedIP = originalValidate }()

	expectedErr := errors.New("dns rebinding rejected")
	validateResolvedIP = func(_ string) error {
		return expectedErr
	}

	var baseCalls int32
	base := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		atomic.AddInt32(&baseCalls, 1)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})

	transport := newValidatedTransport(base)
	req, err := http.NewRequest(http.MethodGet, "https://api.openai.com/v1/responses", nil)
	require.NoError(t, err)

	_, err = transport.RoundTrip(req)
	require.ErrorIs(t, err, expectedErr)
	require.Equal(t, int32(0), atomic.LoadInt32(&baseCalls))
}

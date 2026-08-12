// Package proxyurl 提供代理 URL 的统一验证（fail-fast，无效代理不回退直连）
//
// 所有需要解析代理 URL 的地方必须通过此包的 Parse 函数。
// 直接使用 url.Parse 处理代理 URL 是被禁止的。
// 这确保了 fail-fast 行为：无效代理配置在创建时立即失败，
// 而不是在运行时静默回退到直连（产生 IP 关联风险）。
package proxyurl

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// allowedSchemes 代理协议白名单
var allowedSchemes = map[string]bool{
	"http":    true,
	"https":   true,
	"socks5":  true,
	"socks5h": true,
}

const (
	OptionForceHTTP1       = "_sub2api_force_http1"
	OptionDisableKeepAlive = "_sub2api_disable_keep_alive"
)

type TransportOptions struct {
	ForceHTTP1       bool
	DisableKeepAlive bool
}

// RequiresHTTP1 reports whether the effective policy must disable HTTP/2.
// HTTP/2 multiplexes concurrent requests over one connection, so strict
// per-request connections require HTTP/1.1 in addition to disabling keep-alive.
func (o TransportOptions) RequiresHTTP1() bool {
	return o.ForceHTTP1 || o.DisableKeepAlive
}

// ApplyToHTTPTransport applies the transport policy embedded in a proxy URL.
// Callers must use the stripped URL returned by ParseWithTransportOptions when
// configuring the proxy so these internal options are never sent downstream.
func (o TransportOptions) ApplyToHTTPTransport(transport *http.Transport) {
	if transport == nil {
		return
	}
	if o.RequiresHTTP1() {
		transport.ForceAttemptHTTP2 = false
		transport.TLSNextProto = make(map[string]func(string, *tls.Conn) http.RoundTripper)
		transport.Protocols = new(http.Protocols)
		transport.Protocols.SetHTTP1(true)
		if transport.TLSClientConfig != nil {
			tlsConfig := transport.TLSClientConfig.Clone()
			nextProtos := make([]string, 0, len(tlsConfig.NextProtos))
			for _, protocol := range tlsConfig.NextProtos {
				if protocol != "h2" {
					nextProtos = append(nextProtos, protocol)
				}
			}
			tlsConfig.NextProtos = nextProtos
			transport.TLSClientConfig = tlsConfig
		}
	}
	if o.DisableKeepAlive {
		transport.DisableKeepAlives = true
	}
}

// Parse 解析并验证代理 URL。
//
// 语义:
//   - 空字符串 → ("", nil, nil)，表示直连
//   - 非空且有效 → (trimmed, *url.URL, nil)
//   - 非空但无效 → ("", nil, error)，fail-fast 不回退
//
// 验证规则:
//   - TrimSpace 后为空视为直连
//   - url.Parse 失败返回 error（不含原始 URL，防凭据泄露）
//   - Host 为空返回 error（用 Redacted() 脱敏）
//   - Scheme 必须为 http/https/socks5/socks5h
//   - socks5:// 自动升级为 socks5h://（确保 DNS 由代理端解析，防止 DNS 泄漏）
func Parse(raw string) (trimmed string, parsed *url.URL, err error) {
	trimmed, parsed, _, err = ParseWithTransportOptions(raw)
	return trimmed, parsed, err
}

// ParseWithTransportOptions parses a proxy URL and extracts Sub2API's internal
// transport policy. The returned URL never contains those internal options.
func ParseWithTransportOptions(raw string) (trimmed string, parsed *url.URL, options TransportOptions, err error) {
	trimmed = strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil, options, nil
	}

	parsed, err = url.Parse(trimmed)
	if err != nil {
		return "", nil, options, errors.New("invalid proxy URL")
	}

	if parsed.Host == "" || parsed.Hostname() == "" {
		return "", nil, options, errors.New("proxy URL missing host")
	}

	scheme := strings.ToLower(parsed.Scheme)
	if !allowedSchemes[scheme] {
		return "", nil, options, fmt.Errorf("unsupported proxy scheme %q (allowed: http, https, socks5, socks5h)", scheme)
	}

	options.ForceHTTP1 = optionEnabled(parsed.Query().Get(OptionForceHTTP1))
	options.DisableKeepAlive = optionEnabled(parsed.Query().Get(OptionDisableKeepAlive))
	parsed.RawQuery = ""
	parsed.ForceQuery = false

	// 自动升级 socks5 → socks5h，确保 DNS 由代理端解析，防止 DNS 泄漏。
	// Go 的 golang.org/x/net/proxy 对 socks5:// 默认在客户端本地解析 DNS，
	// 仅 socks5h:// 才将域名发送给代理端做远程 DNS 解析。
	if scheme == "socks5" {
		parsed.Scheme = "socks5h"
	}
	trimmed = parsed.String()

	return trimmed, parsed, options, nil
}

func optionEnabled(value string) bool {
	value = strings.TrimSpace(value)
	return value == "1" || strings.EqualFold(value, "true")
}

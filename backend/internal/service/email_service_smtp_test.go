//go:build unit

package service

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/smtp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newSMTPTestCert 生成 127.0.0.1/localhost 的自签证书及其信任池。
func newSMTPTestCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}, pool
}

// fakeSMTPServer 是覆盖三种连接形态的最小 SMTP 服务器：
// 隐式 TLS（465 语义）、明文+STARTTLS（587 语义）、纯明文。
type fakeSMTPServer struct {
	listener          net.Listener
	tlsConfig         *tls.Config
	advertiseStartTLS bool

	// 认证行为（默认：公告 PLAIN LOGIN，任何 AUTH 直接成功）
	advertiseAuth  string // EHLO 公告的机制列表，空则跳过 AUTH 公告行
	loginChallenge bool   // AUTH LOGIN 走 334 挑战流程（验证 base64 凭据）
	rejectPlain    bool   // AUTH PLAIN 回 504 5.7.4（模拟仅接受 LOGIN 的服务器）

	mu       sync.Mutex
	commands []string
	conns    atomic.Int64
	wg       sync.WaitGroup
}

type fakeSMTPOptions struct {
	implicitTLS       bool
	advertiseStartTLS bool
	advertiseAuth     string
	loginChallenge    bool
	rejectPlain       bool
}

func startFakeSMTPServer(t *testing.T, implicitTLS, advertiseStartTLS bool) (*fakeSMTPServer, int) {
	t.Helper()
	return startFakeSMTPServerWithOptions(t, fakeSMTPOptions{
		implicitTLS:       implicitTLS,
		advertiseStartTLS: advertiseStartTLS,
	})
}

func startFakeSMTPServerWithOptions(t *testing.T, opts fakeSMTPOptions) (*fakeSMTPServer, int) {
	t.Helper()
	cert, pool := newSMTPTestCert(t)
	prevPool := smtpTestRootCAs
	smtpTestRootCAs = pool
	t.Cleanup(func() { smtpTestRootCAs = prevPool })

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &fakeSMTPServer{
		listener:          listener,
		tlsConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		advertiseStartTLS: opts.advertiseStartTLS,
		advertiseAuth:     opts.advertiseAuth,
		loginChallenge:    opts.loginChallenge,
		rejectPlain:       opts.rejectPlain,
	}
	if srv.advertiseAuth == "" {
		srv.advertiseAuth = "PLAIN LOGIN"
	}
	if opts.implicitTLS {
		srv.listener = tls.NewListener(listener, srv.tlsConfig)
	}
	t.Cleanup(func() {
		_ = srv.listener.Close()
		srv.wg.Wait()
	})

	srv.wg.Add(1)
	go func() {
		defer srv.wg.Done()
		for {
			conn, err := srv.listener.Accept()
			if err != nil {
				return
			}
			srv.conns.Add(1)
			srv.wg.Add(1)
			go func() {
				defer srv.wg.Done()
				defer func() { _ = conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				srv.serve(conn, srv.advertiseStartTLS)
			}()
		}
	}()

	port := listener.Addr().(*net.TCPAddr).Port
	return srv, port
}

func (srv *fakeSMTPServer) record(cmd string) {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	srv.commands = append(srv.commands, cmd)
}

func (srv *fakeSMTPServer) sawCommand(prefix string) bool {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	for _, cmd := range srv.commands {
		if strings.HasPrefix(strings.ToUpper(cmd), prefix) {
			return true
		}
	}
	return false
}

// sawExact 大小写敏感的精确匹配（用于 base64 凭据等）。
func (srv *fakeSMTPServer) sawExact(cmd string) bool {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	for _, seen := range srv.commands {
		if seen == cmd {
			return true
		}
	}
	return false
}

func (srv *fakeSMTPServer) serve(conn net.Conn, allowStartTLS bool) {
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	writeLine := func(line string) bool {
		if _, err := writer.WriteString(line + "\r\n"); err != nil {
			return false
		}
		return writer.Flush() == nil
	}
	if !writeLine("220 fake.test ESMTP ready") {
		return
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimSpace(line)
		srv.record(cmd)
		upper := strings.ToUpper(cmd)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			ok := writeLine("250-fake.test")
			if allowStartTLS {
				ok = ok && writeLine("250-STARTTLS")
			}
			if srv.advertiseAuth != "" {
				ok = ok && writeLine("250-AUTH "+srv.advertiseAuth)
			}
			if !(ok && writeLine("250 8BITMIME")) {
				return
			}
		case upper == "STARTTLS" && allowStartTLS:
			if !writeLine("220 2.0.0 ready to start TLS") {
				return
			}
			tlsConn := tls.Server(conn, srv.tlsConfig)
			if err := tlsConn.Handshake(); err != nil {
				return
			}
			srv.serveUpgraded(tlsConn)
			return
		case strings.HasPrefix(upper, "AUTH"):
			if !srv.handleAuth(reader, writeLine, cmd) {
				return
			}
		case strings.HasPrefix(upper, "MAIL"), strings.HasPrefix(upper, "RCPT"):
			if !writeLine("250 ok") {
				return
			}
		case upper == "DATA":
			if !writeLine("354 go ahead") {
				return
			}
			for {
				dataLine, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(dataLine, "\r\n") == "." {
					break
				}
			}
			if !writeLine("250 message accepted") {
				return
			}
		case upper == "QUIT":
			_ = writeLine("221 bye")
			return
		default:
			if !writeLine("250 ok") {
				return
			}
		}
	}
}

// serveUpgraded 复用命令循环处理 STARTTLS 升级后的会话（升级后不再提供 STARTTLS）。
func (srv *fakeSMTPServer) serveUpgraded(conn net.Conn) {
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	// net/smtp 在 StartTLS 成功后会重新发送 EHLO，直接进入命令循环即可。
	srv.serveCommands(reader, writer)
}

// handleAuth 处理 AUTH 命令。
// loginChallenge=true 时 AUTH LOGIN 走标准 334 挑战流程并记录 base64 凭据；
// rejectPlain=true 时 AUTH PLAIN 回 504 5.7.4（模拟 Outlook 拒绝 PLAIN 的场景）。
func (srv *fakeSMTPServer) handleAuth(reader *bufio.Reader, writeLine func(string) bool, cmd string) bool {
	upper := strings.ToUpper(cmd)
	if srv.rejectPlain && strings.HasPrefix(upper, "AUTH PLAIN") {
		return writeLine("504 5.7.4 Unrecognized authentication type")
	}
	if srv.loginChallenge && strings.HasPrefix(upper, "AUTH LOGIN") {
		if !writeLine("334 VXNlcm5hbWU6") { // base64("Username:")
			return false
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			return false
		}
		srv.record(strings.TrimSpace(line))
		if !writeLine("334 UGFzc3dvcmQ6") { // base64("Password:")
			return false
		}
		line, err = reader.ReadString('\n')
		if err != nil {
			return false
		}
		srv.record(strings.TrimSpace(line))
		return writeLine("235 2.7.0 authentication successful")
	}
	return writeLine("235 2.7.0 authentication successful")
}

func (srv *fakeSMTPServer) serveCommands(reader *bufio.Reader, writer *bufio.Writer) {
	writeLine := func(line string) bool {
		if _, err := writer.WriteString(line + "\r\n"); err != nil {
			return false
		}
		return writer.Flush() == nil
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimSpace(line)
		srv.record(cmd)
		upper := strings.ToUpper(cmd)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			ok := writeLine("250-fake.test")
			if srv.advertiseAuth != "" {
				ok = ok && writeLine("250-AUTH "+srv.advertiseAuth)
			}
			if !(ok && writeLine("250 8BITMIME")) {
				return
			}
		case strings.HasPrefix(upper, "AUTH"):
			if !srv.handleAuth(reader, writeLine, cmd) {
				return
			}
		case strings.HasPrefix(upper, "MAIL"), strings.HasPrefix(upper, "RCPT"):
			if !writeLine("250 ok") {
				return
			}
		case upper == "DATA":
			if !writeLine("354 go ahead") {
				return
			}
			for {
				dataLine, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(dataLine, "\r\n") == "." {
					break
				}
			}
			if !writeLine("250 message accepted") {
				return
			}
		case upper == "QUIT":
			_ = writeLine("221 bye")
			return
		default:
			if !writeLine("250 ok") {
				return
			}
		}
	}
}

func smtpTestConfig(port int, useTLS bool) *SMTPConfig {
	return &SMTPConfig{
		Host:     "127.0.0.1",
		Port:     port,
		Username: "user",
		Password: "pass",
		From:     "noreply@example.com",
		FromName: "Test",
		UseTLS:   useTLS,
	}
}

// 465 语义：UseTLS=true + 隐式 TLS 服务器，原有路径保持可用。
func TestSMTPConnectionImplicitTLS(t *testing.T) {
	srv, port := startFakeSMTPServer(t, true, false)
	svc := &EmailService{}

	if err := svc.TestSMTPConnectionWithConfig(smtpTestConfig(port, true)); err != nil {
		t.Fatalf("expected implicit TLS connection to succeed, got: %v", err)
	}
	if !srv.sawCommand("EHLO") {
		t.Fatal("expected server to receive EHLO")
	}
}

// 587 语义（#1470/#1488 核心场景）：UseTLS=true + 明文问候的 STARTTLS 服务器，
// 隐式 TLS 失败后必须自动降级为强制 STARTTLS 并成功。
func TestSMTPConnectionStartTLSFallbackWhenTLSEnabled(t *testing.T) {
	srv, port := startFakeSMTPServer(t, false, true)
	svc := &EmailService{}

	if err := svc.TestSMTPConnectionWithConfig(smtpTestConfig(port, true)); err != nil {
		t.Fatalf("expected STARTTLS fallback to succeed, got: %v", err)
	}
	if !srv.sawCommand("STARTTLS") {
		t.Fatal("expected server to receive STARTTLS command")
	}
	if got := srv.conns.Load(); got < 2 {
		t.Fatalf("expected implicit TLS attempt before STARTTLS fallback (>=2 connections), got %d", got)
	}
}

// UseTLS=true 但服务器不支持 STARTTLS：必须报错，且绝不能把凭据发到明文连接上。
func TestSMTPConnectionMandatoryStartTLSRefusesPlaintext(t *testing.T) {
	srv, port := startFakeSMTPServer(t, false, false)
	svc := &EmailService{}

	err := svc.TestSMTPConnectionWithConfig(smtpTestConfig(port, true))
	if err == nil {
		t.Fatal("expected error when server does not support STARTTLS")
	}
	if !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("expected STARTTLS-related error, got: %v", err)
	}
	if srv.sawCommand("AUTH") {
		t.Fatal("credentials must not be sent over plaintext when TLS is required")
	}
}

// UseTLS=false + 服务器支持 STARTTLS：测试连接与发送路径一致，机会式升级后认证成功。
// 这是 #1488 评论"测试连接不成功，发送测试邮件实际上能发"的回归用例。
func TestSMTPConnectionOpportunisticStartTLSWhenTLSDisabled(t *testing.T) {
	srv, port := startFakeSMTPServer(t, false, true)
	svc := &EmailService{}

	if err := svc.TestSMTPConnectionWithConfig(smtpTestConfig(port, false)); err != nil {
		t.Fatalf("expected opportunistic STARTTLS test connection to succeed, got: %v", err)
	}
	if !srv.sawCommand("STARTTLS") {
		t.Fatal("expected test connection to upgrade via STARTTLS like the send path")
	}
}

// UseTLS=false + 服务器不支持 STARTTLS：保持明文直连（既有行为不回归）。
func TestSMTPConnectionPlainWhenNoStartTLS(t *testing.T) {
	srv, port := startFakeSMTPServer(t, false, false)
	svc := &EmailService{}

	if err := svc.TestSMTPConnectionWithConfig(smtpTestConfig(port, false)); err != nil {
		t.Fatalf("expected plain connection to succeed, got: %v", err)
	}
	if srv.sawCommand("STARTTLS") {
		t.Fatal("did not expect STARTTLS command when server does not advertise it")
	}
}

// 发送路径全流程：UseTLS=true + STARTTLS 服务器（587 语义）完整走完 MAIL/RCPT/DATA。
func TestSendEmailWithConfigStartTLSFallback(t *testing.T) {
	srv, port := startFakeSMTPServer(t, false, true)
	svc := &EmailService{}

	err := svc.SendEmailWithConfig(smtpTestConfig(port, true), "rcpt@example.com", "subject", "<p>body</p>")
	if err != nil {
		t.Fatalf("expected send via STARTTLS fallback to succeed, got: %v", err)
	}
	if !srv.sawCommand("STARTTLS") {
		t.Fatal("expected send path to upgrade via STARTTLS")
	}
	if !srv.sawCommand("DATA") {
		t.Fatal("expected send path to reach DATA")
	}
}

// 发送路径全流程：UseTLS=true + 隐式 TLS 服务器（465 语义）保持既有行为。
func TestSendEmailWithConfigImplicitTLS(t *testing.T) {
	srv, port := startFakeSMTPServer(t, true, false)
	svc := &EmailService{}

	err := svc.SendEmailWithConfig(smtpTestConfig(port, true), "rcpt@example.com", "subject", "<p>body</p>")
	if err != nil {
		t.Fatalf("expected send via implicit TLS to succeed, got: %v", err)
	}
	if !srv.sawCommand("DATA") {
		t.Fatal("expected send path to reach DATA")
	}
}

// ── AUTH LOGIN 支持 ──────────────────────────────────────────────────────────

func TestSMTPAuthMechanismsOrder(t *testing.T) {
	// PLAIN 优先
	require.Equal(t, []string{"PLAIN", "LOGIN"}, smtpAuthMechanisms("PLAIN LOGIN"))
	// 只公告 LOGIN
	require.Equal(t, []string{"LOGIN"}, smtpAuthMechanisms("LOGIN"))
	// 只公告 PLAIN
	require.Equal(t, []string{"PLAIN"}, smtpAuthMechanisms("PLAIN"))
	// 大小写不敏感
	require.Equal(t, []string{"PLAIN", "LOGIN"}, smtpAuthMechanisms("plain login"))
	// 未公告（SMTP AUTH 半开/禁用）：依次尝试两种机制让服务器裁决
	require.Equal(t, []string{"PLAIN", "LOGIN"}, smtpAuthMechanisms(""))
	require.Equal(t, []string{"PLAIN", "LOGIN"}, smtpAuthMechanisms("XOAUTH2"))
}

func TestLoginAuthChallengeFlow(t *testing.T) {
	auth := &loginAuth{username: "user", password: "pass"}

	mech, resp, err := auth.Start(&smtp.ServerInfo{})
	require.NoError(t, err)
	require.Equal(t, "LOGIN", mech)
	require.Nil(t, resp)

	// 第一次挑战 → 用户名（net/smtp 会 base64 编码后发送）
	first, err := auth.Next([]byte("Username:"), true)
	require.NoError(t, err)
	require.Equal(t, "user", string(first))
	// 第二次挑战 → 密码
	second, err := auth.Next([]byte("Password:"), true)
	require.NoError(t, err)
	require.Equal(t, "pass", string(second))
	// more=false 结束
	done, err := auth.Next(nil, false)
	require.NoError(t, err)
	require.Nil(t, done)
	// 超出步数报错
	_, err = auth.Next([]byte("?"), true)
	require.Error(t, err)
}

// 服务器只公告 AUTH LOGIN：测试连接必须通过 LOGIN 认证成功，且凭据 base64 正确。
func TestSMTPConnectionLoginOnly(t *testing.T) {
	srv, port := startFakeSMTPServerWithOptions(t, fakeSMTPOptions{
		advertiseStartTLS: true,
		advertiseAuth:     "LOGIN",
		loginChallenge:    true,
	})
	svc := &EmailService{}

	if err := svc.TestSMTPConnectionWithConfig(smtpTestConfig(port, true)); err != nil {
		t.Fatalf("expected LOGIN authentication to succeed, got: %v", err)
	}
	if !srv.sawCommand("AUTH LOGIN") {
		t.Fatal("expected server to receive AUTH LOGIN")
	}
	// base64("user") == "dXNlcg==", base64("pass") == "cGFzcw=="
	if !srv.sawExact("dXNlcg==") {
		t.Fatal("expected base64-encoded username, got:", srv.commands)
	}
	if !srv.sawExact("cGFzcw==") {
		t.Fatal("expected base64-encoded password, got:", srv.commands)
	}
}

// 服务器公告 PLAIN+LOGIN 但拒绝 PLAIN（504）：必须重连回退到 LOGIN 并成功。
// 这正是 Outlook/Exchange 5.7.4 报错的覆盖场景。
func TestSMTPConnectionPLAINRejectedFallsBackToLOGIN(t *testing.T) {
	srv, port := startFakeSMTPServerWithOptions(t, fakeSMTPOptions{
		advertiseStartTLS: true,
		advertiseAuth:     "PLAIN LOGIN",
		loginChallenge:    true,
		rejectPlain:       true,
	})
	svc := &EmailService{}

	if err := svc.TestSMTPConnectionWithConfig(smtpTestConfig(port, true)); err != nil {
		t.Fatalf("expected fallback to LOGIN to succeed, got: %v", err)
	}
	if !srv.sawCommand("AUTH PLAIN") {
		t.Fatal("expected first attempt to use AUTH PLAIN")
	}
	if !srv.sawCommand("AUTH LOGIN") {
		t.Fatal("expected fallback attempt to use AUTH LOGIN")
	}
	// 回退需要新连接（net/smtp AUTH 失败会 QUIT 关闭连接）
	if got := srv.conns.Load(); got < 3 {
		t.Fatalf("expected re-dial before LOGIN attempt (>=3 connections), got %d", got)
	}
}

// 发送路径：服务器只公告 LOGIN，整封邮件走 LOGIN 认证并送达 DATA。
func TestSendEmailWithConfigLoginMechanism(t *testing.T) {
	srv, port := startFakeSMTPServerWithOptions(t, fakeSMTPOptions{
		advertiseStartTLS: true,
		advertiseAuth:     "LOGIN",
		loginChallenge:    true,
	})
	svc := &EmailService{}

	err := svc.SendEmailWithConfig(smtpTestConfig(port, true), "rcpt@example.com", "subject", "<p>body</p>")
	if err != nil {
		t.Fatalf("expected send via LOGIN to succeed, got: %v", err)
	}
	if !srv.sawCommand("AUTH LOGIN") {
		t.Fatal("expected send path to use AUTH LOGIN")
	}
	if !srv.sawCommand("DATA") {
		t.Fatal("expected send path to reach DATA")
	}
}

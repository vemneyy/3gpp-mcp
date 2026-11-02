package etsi

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

var defaultClient = &http.Client{Transport: newTransport(nil, http.ProxyFromEnvironment), Timeout: 90 * time.Second}

var errNoHTTP2 = errors.New("server did not negotiate HTTP/2")

type transport struct {
	https, http *http.Transport
	h2          *http2.Transport
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme == "https" {
		resp, err := t.h2.RoundTrip(req)
		if errors.Is(err, errNoHTTP2) {
			return t.https.RoundTrip(req)
		}
		return resp, err
	}
	return t.http.RoundTrip(req)
}

// newTransport uses a Chrome ClientHello for HTTPS, including across HTTP(S)
// CONNECT proxies. net/http cannot apply DialTLSContext to the TLS handshake
// inside its own CONNECT tunnel, so this transport establishes that tunnel.
func newTransport(roots *x509.CertPool, proxy func(*http.Request) (*url.URL, error)) *transport {
	plain := http.DefaultTransport.(*http.Transport).Clone()
	secure := http.DefaultTransport.(*http.Transport).Clone()
	secure.Proxy = nil
	secure.ForceAttemptHTTP2 = false
	secure.TLSNextProto = make(map[string]func(string, *tls.Conn) http.RoundTripper)
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	dialTLS := func(ctx context.Context, network, addr string, wantH2 bool) (net.Conn, error) {
		req := &http.Request{URL: &url.URL{Scheme: "https", Host: addr}}
		p, err := proxy(req)
		if err != nil {
			return nil, fmt.Errorf("resolve proxy: %w", err)
		}
		target := addr
		if p != nil {
			if p.Scheme != "http" && p.Scheme != "https" {
				return nil, fmt.Errorf("unsupported HTTPS proxy scheme %q", p.Scheme)
			}
			target = p.Host
			if p.Port() == "" {
				if p.Scheme == "https" {
					target = net.JoinHostPort(p.Hostname(), "443")
				} else {
					target = net.JoinHostPort(p.Hostname(), "80")
				}
			}
		}
		conn, err := dialer.DialContext(ctx, network, target)
		if err != nil {
			return nil, err
		}
		ok := false
		defer func() {
			if !ok {
				_ = conn.Close()
			}
		}()
		// A client timeout or cancellation must interrupt CONNECT reads as well.
		rawConn := conn
		stop := context.AfterFunc(ctx, func() { _ = rawConn.Close() })
		defer stop()
		if p != nil {
			if p.Scheme == "https" {
				pc := tls.Client(conn, &tls.Config{ServerName: p.Hostname(), RootCAs: roots, MinVersion: tls.VersionTLS12})
				if err = pc.HandshakeContext(ctx); err != nil {
					return nil, fmt.Errorf("TLS to proxy: %w", err)
				}
				conn = pc
			}
			connect := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: addr}, Host: addr, Header: make(http.Header)}
			if p.User != nil {
				password, _ := p.User.Password()
				auth := base64.StdEncoding.EncodeToString([]byte(p.User.Username() + ":" + password))
				connect.Header.Set("Proxy-Authorization", "Basic "+auth)
			}
			if err = connect.Write(conn); err != nil {
				return nil, fmt.Errorf("proxy CONNECT: %w", err)
			}
			reader := bufio.NewReader(conn)
			response, e := http.ReadResponse(reader, connect)
			if e != nil {
				return nil, fmt.Errorf("proxy CONNECT response: %w", e)
			}
			if response.StatusCode != http.StatusOK {
				return nil, fmt.Errorf("proxy CONNECT: %s", response.Status)
			}
			if reader.Buffered() != 0 {
				return nil, fmt.Errorf("proxy CONNECT returned unexpected buffered data")
			}
		}
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		config := &utls.Config{ServerName: host, RootCAs: roots, MinVersion: utls.VersionTLS12}
		var u *utls.UConn
		if wantH2 {
			// The normal path sends the unmodified Chrome ClientHello.
			u = utls.UClient(conn, config, utls.HelloChrome_Auto)
		} else {
			// net/http's HTTP/1 fallback cannot use a connection negotiated as h2.
			spec, e := utls.UTLSIdToSpec(utls.HelloChrome_Auto)
			if e != nil {
				return nil, e
			}
			for _, ext := range spec.Extensions {
				if alpn, yes := ext.(*utls.ALPNExtension); yes {
					alpn.AlpnProtocols = []string{"http/1.1"}
				}
				if app, yes := ext.(*utls.ApplicationSettingsExtension); yes {
					app.SupportedProtocols = []string{"http/1.1"}
				}
			}
			u = utls.UClient(conn, config, utls.HelloCustom)
			if e = u.ApplyPreset(&spec); e != nil {
				return nil, fmt.Errorf("chrome TLS profile: %w", e)
			}
		}
		if err = u.HandshakeContext(ctx); err != nil {
			return nil, fmt.Errorf("TLS to %s: %w", host, err)
		}
		if wantH2 && u.ConnectionState().NegotiatedProtocol != "h2" {
			return nil, errNoHTTP2
		}
		ok = true
		return u, nil
	}
	secure.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dialTLS(ctx, network, addr, false)
	}
	h2 := &http2.Transport{DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
		return dialTLS(ctx, network, addr, true)
	}}
	return &transport{https: secure, http: plain, h2: h2}
}

// Keep the transports reachable for the lifetime of the shared client, so
// directory and PDF requests can reuse their established connections.
var _ http.RoundTripper = (*transport)(nil)

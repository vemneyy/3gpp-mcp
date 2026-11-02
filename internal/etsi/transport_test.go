package etsi

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestChromeTransportDirectAndProxy(t *testing.T) {
	var chrome atomic.Bool
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))
	server.TLS = &tls.Config{GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		if len(hello.CipherSuites) > 0 && len(hello.SupportedProtos) > 0 && hello.SupportedProtos[0] == "h2" && len(hello.SupportedVersions) >= 2 {
			chrome.Store(true)
		}
		return nil, nil
	}}
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	proxyCalls := atomic.Int32{}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			t.Errorf("method=%s", r.Method)
			http.Error(w, "CONNECT required", http.StatusMethodNotAllowed)
			return
		}
		proxyCalls.Add(1)
		dest, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, "dial failed", http.StatusBadGateway)
			return
		}
		client, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			_ = dest.Close()
			return
		}
		if _, err = buf.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			_ = dest.Close()
			_ = client.Close()
			return
		}
		if err = buf.Flush(); err != nil {
			_ = dest.Close()
			_ = client.Close()
			return
		}
		go func() { _, _ = io.Copy(dest, client); _ = dest.Close() }()
		go func() { _, _ = io.Copy(client, dest); _ = client.Close() }()
	}))
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		proxied bool
	}{{"direct", false}, {"proxy", true}} {
		t.Run(tc.name, func(t *testing.T) {
			chrome.Store(false)
			tr := newTransport(pool, func(*http.Request) (*url.URL, error) {
				if tc.proxied {
					return proxyURL, nil
				}
				return nil, nil
			})
			client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
			resp, err := client.Get(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil || string(body) != "ok" {
				t.Fatalf("body %q: %v", body, err)
			}
			if !chrome.Load() {
				t.Fatal("expected Chrome ClientHello")
			}
		})
	}
	if proxyCalls.Load() != 1 {
		t.Fatalf("CONNECT requests: %d", proxyCalls.Load())
	}
}
func TestChromeTransportVerifiesCertificates(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer s.Close()
	tr := newTransport(x509.NewCertPool(), func(*http.Request) (*url.URL, error) { return nil, nil })
	c := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	resp, err := c.Get(s.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("expected certificate error, got %v", err)
	}
}

func TestChromeTransportHTTP1Fallback(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, r.Proto) }))
	defer s.Close()
	roots := x509.NewCertPool()
	roots.AddCert(s.Certificate())
	tr := newTransport(roots, func(*http.Request) (*url.URL, error) { return nil, nil })
	c := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	resp, err := c.Get(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "HTTP/1.1" {
		t.Fatalf("fallback: %q %v", body, err)
	}
}

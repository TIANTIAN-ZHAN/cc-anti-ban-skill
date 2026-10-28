package health

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixtureCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "isolated fixture"}, DNSNames: []string{TargetHost, "proxy.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: priv}))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	return cert, roots
}

func originFixture(t *testing.T, cert tls.Certificate, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	count := &atomic.Int32{}
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		if r.Method != "HEAD" || r.URL.String() != "/" || r.Host != TargetHost || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("unexpected origin request")
		}
		w.Header().Set("Location", "https://never-follow.invalid/private")
		w.Header().Set("Set-Cookie", "private_cookie=DO_NOT_EXPORT")
		w.WriteHeader(status)
	}))
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	s.StartTLS()
	t.Cleanup(s.Close)
	return s, count
}

func TestDirectTLSAndHTTPResponses(t *testing.T) {
	cert, roots := fixtureCertificate(t)
	for _, status := range []int{200, 301, 401, 403, 404, 429, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server, requests := originFixture(t, cert, status)
			s := Inspect(context.Background(), fixtureIO(t))
			p := s.Plan(testTime)
			resolves, dials := 0, 0
			n := NetworkIO{Roots: roots, Now: func() time.Time { return testTime }, Resolve: func(ctx context.Context, dns, host string) ([]net.IP, error) {
				resolves++
				if dns != "192.0.2.53" || host != TargetHost {
					t.Fatal("unexpected DNS")
				}
				return []net.IP{net.ParseIP("93.184.215.14")}, nil
			}, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				dials++
				if address != "93.184.215.14:443" {
					t.Fatal("unexpected dial")
				}
				return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
			}}
			r := s.Probe(context.Background(), p, n)
			want := Pass
			if status == 429 || status >= 500 {
				want = Fail
			}
			if find(r, "connection").Status != want || requests.Load() != 1 || resolves != 1 || dials != 1 {
				t.Fatalf("unexpected probe: %s req=%d dns=%d dial=%d", find(r, "connection").Code, requests.Load(), resolves, dials)
			}
			if find(r, "account").Status != Unknown || find(r, "effective_configuration").Status != Unknown {
				t.Fatal("HTTP status promoted to model access")
			}
			if r.Score.Scope == "" || len(r.Score.Unverified) != 3 {
				t.Fatal("numeric score lost its unverified scope")
			}
			if r.Score.Value == nil {
				t.Fatal("sufficient bounded evidence should score")
			}
			if want == Pass && *r.Score.Value != 100 {
				t.Fatal("wrong score")
			}
			if want == Fail && *r.Score.Value != 49 {
				t.Fatal("failed chain not capped")
			}
			encoded, _ := json.Marshal(r)
			for _, value := range []string{"DO_NOT_EXPORT", "127.0.0.1", "192.0.2.53", "never-follow.invalid"} {
				if strings.Contains(string(encoded), value) {
					t.Fatal("network data leaked into report")
				}
			}
		})
	}
}

func connectFixture(t *testing.T, origin string, cert tls.Certificate, secure bool, reject int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	count := &atomic.Int32{}
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		if r.Method != "CONNECT" || r.Host != TargetHost+":443" || r.Header.Get("Proxy-Authorization") != "" {
			t.Error("unexpected CONNECT")
		}
		if reject != 0 {
			w.WriteHeader(reject)
			return
		}
		upstream, err := net.Dial("tcp", origin)
		if err != nil {
			t.Error(err)
			w.WriteHeader(502)
			return
		}
		client, buff, err := w.(http.Hijacker).Hijack()
		if err != nil {
			_ = upstream.Close()
			t.Error(err)
			return
		}
		_, _ = buff.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buff.Flush()
		go func() { _, _ = io.Copy(upstream, client); _ = upstream.Close(); _ = client.Close() }()
		_, _ = io.Copy(client, upstream)
		_ = client.Close()
		_ = upstream.Close()
	}))
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	if secure {
		s.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		s.StartTLS()
	} else {
		s.Start()
	}
	t.Cleanup(s.Close)
	return s, count
}

func TestHTTPAndHTTPSConnectProxy(t *testing.T) {
	cert, roots := fixtureCertificate(t)
	for _, secure := range []bool{false, true} {
		for _, reject := range []int{0, 407} {
			name := "http"
			if secure {
				name = "https"
			}
			if reject != 0 {
				name += " rejected"
			}
			t.Run(name, func(t *testing.T) {
				origin, requests := originFixture(t, cert, 403)
				proxy, connects := connectFixture(t, origin.Listener.Addr().String(), cert, secure, reject)
				d := fixtureIO(t)
				scheme := "http"
				if secure {
					scheme = "https"
				}
				d.Env["HTTPS_PROXY"] = scheme + "://proxy.test:8443"
				s := Inspect(context.Background(), d)
				p := s.Plan(testTime)
				resolves, dials := 0, 0
				n := NetworkIO{Roots: roots, Now: func() time.Time { return testTime }, Resolve: func(ctx context.Context, dns, host string) ([]net.IP, error) {
					resolves++
					if host != "proxy.test" {
						t.Fatal("proxy route leaked origin DNS")
					}
					return []net.IP{net.ParseIP("93.184.215.14")}, nil
				}, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
					dials++
					if address != "93.184.215.14:8443" {
						t.Fatal("proxy bypassed")
					}
					return (&net.Dialer{}).DialContext(ctx, "tcp", proxy.Listener.Addr().String())
				}}
				r := s.Probe(context.Background(), p, n)
				if resolves != 1 || dials != 1 || connects.Load() != 1 {
					t.Fatal("wrong call budget")
				}
				if reject == 0 {
					if find(r, "connection").Status != Pass || requests.Load() != 1 {
						t.Fatalf("proxy failed: %s", find(r, "connection").Code)
					}
				} else if find(r, "connection").Code != "proxy_auth_required" || requests.Load() != 0 {
					t.Fatal("proxy auth rejection misclassified or contacted origin")
				}
			})
		}
	}
}

func TestProbeStopsOnUnknownAndFailures(t *testing.T) {
	cert, roots := fixtureCertificate(t)
	origin, requests := originFixture(t, cert, 200)
	for _, name := range []string{"dns unavailable", "tcp unavailable", "TLS untrusted", "expired plan", "missing trust", "cancelled", "timeout"} {
		t.Run(name, func(t *testing.T) {
			s := Inspect(context.Background(), fixtureIO(t))
			p := s.Plan(testTime)
			dials := 0
			resolved := 0
			n := NetworkIO{Roots: roots, Now: func() time.Time { return testTime }, Resolve: func(context.Context, string, string) ([]net.IP, error) {
				resolved++
				if name == "dns unavailable" {
					return nil, context.DeadlineExceeded
				}
				return []net.IP{net.ParseIP("93.184.215.14")}, nil
			}, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				dials++
				if name == "tcp unavailable" {
					return nil, errors.New("secret-token private-path 198.51.100.9")
				}
				if name == "timeout" {
					return nil, context.DeadlineExceeded
				}
				return (&net.Dialer{}).DialContext(ctx, "tcp", origin.Listener.Addr().String())
			}}
			if name == "TLS untrusted" {
				n.Roots = x509.NewCertPool()
			}
			if name == "expired plan" {
				p.ExpiresAt = testTime.Add(-time.Second)
			}
			if name == "missing trust" {
				n.Roots = nil
			}
			ctx := context.Background()
			if name == "cancelled" {
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			}
			before := requests.Load()
			r := s.Probe(ctx, p, n)
			if find(r, "connection").Status == Pass || requests.Load() != before {
				t.Fatal("failure allowed later request")
			}
			if name == "dns unavailable" {
				if find(r, "connection").Status != Unknown || dials != 0 {
					t.Fatal("reference DNS failure counted as target failure")
				}
			}
			if (name == "expired plan" || name == "missing trust") && (resolved != 0 || dials != 0) {
				t.Fatal("gate permitted network")
			}
			if name == "TLS untrusted" && find(r, "connection").Code != "certificate_untrusted" {
				t.Fatalf("wrong TLS class %s", find(r, "connection").Code)
			}
			if name == "timeout" && find(r, "connection").Code != "timeout" {
				t.Fatal("wrong timeout class")
			}
			raw, _ := json.Marshal(r)
			for _, secret := range []string{"secret-token", "private-path", "198.51.100.9"} {
				if strings.Contains(string(raw), secret) {
					t.Fatal("raw network error leaked")
				}
			}
		})
	}
}

func TestSocketTimeoutAndCancellation(t *testing.T) {
	_, roots := fixtureCertificate(t)
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "deadline", true: "cancel"}[cancelled], func(t *testing.T) {
			s := Inspect(context.Background(), fixtureIO(t))
			p := s.Plan(testTime)
			client, server := net.Pipe()
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
			defer cancel()
			started := make(chan struct{})
			n := NetworkIO{Roots: roots, Now: func() time.Time { return testTime }, Resolve: func(context.Context, string, string) ([]net.IP, error) {
				return []net.IP{net.ParseIP("93.184.215.14")}, nil
			}, Dial: func(context.Context, string, string) (net.Conn, error) { close(started); return client, nil }}
			if cancelled {
				go func() { <-started; cancel() }()
			}
			start := time.Now()
			r := s.Probe(ctx, p, n)
			if time.Since(start) > time.Second || find(r, "connection").Status == Pass {
				t.Fatal("blocked socket did not stop")
			}
		})
	}
}

func TestBoundedDNSWireFixture(t *testing.T) {
	for _, kind := range []string{"answer", "truncated", "timeout"} {
		t.Run(kind, func(t *testing.T) {
			listener, err := net.ListenPacket("udp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			count := &atomic.Int32{}
			done := make(chan struct{})
			go func() {
				defer close(done)
				buf := make([]byte, 2048)
				for {
					n, addr, err := listener.ReadFrom(buf)
					if err != nil {
						return
					}
					count.Add(1)
					if kind == "timeout" {
						continue
					}
					query := buf[:n]
					end := 12
					for end < len(query) && query[end] != 0 {
						end += int(query[end]) + 1
					}
					end += 5
					if end > len(query) {
						return
					}
					response := append([]byte(nil), query[:end]...)
					binary.BigEndian.PutUint16(response[2:4], 0x8180)
					binary.BigEndian.PutUint16(response[6:8], 1)
					binary.BigEndian.PutUint16(response[8:10], 0)
					binary.BigEndian.PutUint16(response[10:12], 0)
					if kind == "truncated" {
						binary.BigEndian.PutUint16(response[2:4], 0x8380)
						binary.BigEndian.PutUint16(response[6:8], 0)
					} else {
						response = append(response, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 30, 0, 4, 127, 0, 0, 1)
					}
					_, _ = listener.WriteTo(response, addr)
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			ips, err := resolveAt(ctx, listener.LocalAddr().String(), "fixture.test")
			_ = listener.Close()
			<-done
			if count.Load() != 1 {
				t.Fatalf("DNS sent %d packets", count.Load())
			}
			if kind == "answer" {
				if err != nil || len(ips) != 1 || !ips[0].Equal(net.ParseIP("127.0.0.1")) {
					t.Fatalf("DNS fixture failed %v", err)
				}
			} else if err == nil {
				t.Fatal("fallback must not succeed")
			}
		})
	}
}

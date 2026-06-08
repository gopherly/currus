// Copyright 2026 The Gopherly Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package currus

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"
)

// TestTLSConfigFromCurrusNil verifies that a nil TLSConfig produces a nil
// [tls.Config] without error.
func TestTLSConfigFromCurrusNil(t *testing.T) {
	t.Parallel()
	got, err := tlsConfigFromCurrus(nil)
	require.NoError(t, err)
	assert.Nil(t, got)
}

// TestTLSConfigFromCurrus covers the non-nil TLSConfig conversion paths.
func TestTLSConfigFromCurrus(t *testing.T) {
	t.Parallel()

	certPEM, keyPEM := testCertPEM(t)

	tests := []struct {
		name      string
		cfg       *TLSConfig
		wantCerts int
		wantSkip  bool
		wantErr   bool
	}{
		{
			name:     "insecure skip verify propagated",
			cfg:      &TLSConfig{InsecureSkipVerify: true},
			wantSkip: true,
		},
		{
			name:      "valid cert and key produces one certificate",
			cfg:       &TLSConfig{Cert: certPEM, Key: keyPEM},
			wantCerts: 1,
		},
		{
			name:    "garbage cert and key returns error",
			cfg:     &TLSConfig{Cert: []byte("not-a-cert"), Key: []byte("not-a-key")},
			wantErr: true,
		},
		{
			name:      "cert without key skips keypair",
			cfg:       &TLSConfig{Cert: certPEM},
			wantCerts: 0,
		},
		{
			name:      "key without cert skips keypair",
			cfg:       &TLSConfig{Key: keyPEM},
			wantCerts: 0,
		},
		{
			name: "valid CACert sets root pool",
			cfg:  &TLSConfig{CACert: certPEM},
		},
		{
			name:    "invalid CACert returns ErrInvalidSpec",
			cfg:     &TLSConfig{CACert: []byte("not-a-cert")},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tlsConfigFromCurrus(tc.cfg)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, got)
			assert.Equal(t, tc.wantSkip, got.InsecureSkipVerify)
			assert.Len(t, got.Certificates, tc.wantCerts)
		})
	}
}

// TestWithEngine verifies that WithEngine writes the kind field to engineConfig.
func TestWithEngine(t *testing.T) {
	t.Parallel()
	var cfg engineConfig
	WithEngine(Containerd)(&cfg)
	assert.Equal(t, Containerd, cfg.kind)
}

// TestWithEndpoint verifies that WithEndpoint writes both host and namespace
// to engineConfig.
func TestWithEndpoint(t *testing.T) {
	t.Parallel()
	ep := Endpoint{Host: "unix:///tmp/test.sock", Namespace: "myns"}
	var cfg engineConfig
	WithEndpoint(ep)(&cfg)
	require.NotNil(t, cfg.endpoint)
	assert.Equal(t, ep.Host, cfg.endpoint.Host)
	assert.Equal(t, ep.Namespace, cfg.endpoint.Namespace)
}

// TestWithLogger verifies that WithLogger stores the supplied logger.
func TestWithLogger(t *testing.T) {
	t.Parallel()
	lg := slog.Default()
	var cfg engineConfig
	WithLogger(lg)(&cfg)
	assert.Same(t, lg, cfg.logger)
}

// TestWithTracerProvider verifies that WithTracerProvider stores the supplied
// tracer provider.
func TestWithTracerProvider(t *testing.T) {
	t.Parallel()
	tp := noop.NewTracerProvider()
	var cfg engineConfig
	WithTracerProvider(tp)(&cfg)
	assert.Equal(t, tp, cfg.tracer)
}

// testCertPEM generates a self-signed ECDSA certificate and returns the cert
// and key encoded as PEM byte slices.
func testCertPEM(t *testing.T) ([]byte, []byte) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoErrorf(t, err, "generate key")
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "currus-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	require.NoErrorf(t, err, "create certificate")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(priv)
	require.NoErrorf(t, err, "marshal key")
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return certPEM, keyPEM
}

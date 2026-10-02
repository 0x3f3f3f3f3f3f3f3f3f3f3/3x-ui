package tls_test

import (
	"bytes"
	"crypto"
	"crypto/rand"
	gotls "crypto/tls"
	"crypto/x509"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/protocol/tls/cert"
	. "github.com/xtls/xray-core/transport/internet/tls"
	"golang.org/x/crypto/ocsp"
)

// A reload must publish a new certificate without changing one already selected
// by another handshake, or the shared protobuf definition used by other configs.
func TestCertificateFileReloadConcurrentSelection(t *testing.T) {
	initial, _ := cert.MustGenerate(nil, cert.CommonName("reload.example"), cert.DNSNames("reload.example"))
	// Keep the key constant so the worker cannot observe a mismatched pair
	// while the two PEM files are updated separately.
	replacement := renewedCertificate(t, initial)
	entry := ParseCertificate(initial)
	entry.CertificatePath = filepath.Join(t.TempDir(), "certificate.pem")
	entry.KeyPath = filepath.Join(t.TempDir(), "key.pem")
	entry.OcspStapling = 1 // Retain the real one-second file-reload ticker.
	writeCertificate := func(value *cert.Certificate) {
		t.Helper()
		certificatePEM, keyPEM := value.ToPEM()
		if err := os.WriteFile(entry.CertificatePath, certificatePEM, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(entry.KeyPath, keyPEM, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeCertificate(initial)
	c := &Config{Certificate: []*Certificate{entry}}
	configs := []*gotls.Config{c.GetTLSConfig(), c.GetTLSConfig()}
	previous := waitCertificateSelection(t, configs[0], initial.Certificate, nil)
	stopReaders := readCertificatesConcurrently(t, configs)
	defer stopReaders()
	writeCertificate(replacement)
	for _, config := range configs {
		waitCertificateSelection(t, config, replacement.Certificate, nil)
	}
	if !bytes.Equal(previous.Certificate[0], initial.Certificate) {
		t.Fatal("reload changed a previously selected certificate")
	}
	certificatePEM, keyPEM := initial.ToPEM()
	if !bytes.Equal(entry.Certificate, certificatePEM) || !bytes.Equal(entry.Key, keyPEM) {
		t.Fatal("reload changed the shared certificate definition")
	}
}

func TestBuiltCertificatesSnapshotSurvivesFileAndOCSPUpdates(t *testing.T) {
	var certificateDER []byte
	firstRequest := make(chan struct{})
	thirdRequest := make(chan int64, 1)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/issuer" {
			_, _ = w.Write(certificateDER)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		request, err := ocsp.ParseRequest(body)
		if err != nil {
			t.Error(err)
			return
		}
		switch requests.Add(1) {
		case 1:
			close(firstRequest)
		case 3:
			thirdRequest <- request.SerialNumber.Int64()
		}
		_, _ = w.Write([]byte("renewed OCSP response"))
	}))
	defer server.Close()
	initial, _ := cert.MustGenerate(nil, cert.CommonName("snapshot.example"), func(c *x509.Certificate) {
		c.OCSPServer = []string{server.URL}
		c.IssuingCertificateURL = []string{server.URL + "/issuer"}
	})
	certificateDER = initial.Certificate
	entry := ParseCertificate(initial)
	entry.CertificatePath = filepath.Join(t.TempDir(), "certificate.pem")
	entry.KeyPath = filepath.Join(t.TempDir(), "key.pem")
	entry.OcspStapling = 1
	writeCertificate := func(value *cert.Certificate) {
		t.Helper()
		certificatePEM, keyPEM := value.ToPEM()
		if err := os.WriteFile(entry.CertificatePath, certificatePEM, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(entry.KeyPath, keyPEM, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeCertificate(initial)
	snapshot := (&Config{Certificate: []*Certificate{entry}}).BuildCertificates()
	if len(snapshot) != 1 || !bytes.Equal(snapshot[0].Certificate[0], initial.Certificate) {
		t.Fatal("unexpected initial certificate snapshot")
	}
	previous := snapshot[0]
	previousStaple := bytes.Clone(previous.OCSPStaple)
	// Wait for the first response to be requested before changing the files.
	// The third request proves the worker completed another reload/OCSP cycle.
	select {
	case <-firstRequest:
	case <-time.After(5 * time.Second):
		t.Fatal("certificate worker never requested OCSP")
	}
	writeCertificate(renewedCertificate(t, initial))
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(5 * time.Millisecond)
	defer poll.Stop()
	for {
		if snapshot[0] != previous || !bytes.Equal(snapshot[0].Certificate[0], initial.Certificate) ||
			!bytes.Equal(snapshot[0].OCSPStaple, previousStaple) {
			t.Fatal("file reload or OCSP update changed the returned certificate snapshot")
		}
		select {
		case serial := <-thirdRequest:
			if serial != 2 {
				t.Fatal("OCSP worker did not observe the reloaded certificate")
			}
			return
		case <-deadline.C:
			t.Fatal("certificate worker did not complete file reload and OCSP updates")
		case <-poll.C:
		}
	}
}

func renewedCertificate(t *testing.T, initial *cert.Certificate) *cert.Certificate {
	t.Helper()
	key, err := x509.ParsePKCS8PrivateKey(initial.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	template, err := x509.ParseCertificate(initial.Certificate)
	if err != nil {
		t.Fatal(err)
	}
	template.SerialNumber = big.NewInt(2)
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.(crypto.Signer).Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	return &cert.Certificate{Certificate: der, PrivateKey: initial.PrivateKey}
}

// OCSP renewal must leave certificate pointers returned to earlier handshakes
// immutable while concurrent handshakes select the renewed staple.
func TestCertificateOCSPUpdatesPreservePreviousSelection(t *testing.T) {
	firstRequest := make(chan struct{})
	releaseResponse := make(chan struct{})
	var requestOnce, releaseOnce sync.Once
	var staple atomic.Value
	staple.Store([]byte("first OCSP response"))
	var certificateDER []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/issuer" {
			_, _ = w.Write(certificateDER)
			return
		}
		requestOnce.Do(func() { close(firstRequest) })
		<-releaseResponse
		_, _ = w.Write(staple.Load().([]byte))
	}))
	defer func() {
		releaseOnce.Do(func() { close(releaseResponse) })
		server.Close()
	}()
	generated, _ := cert.MustGenerate(nil, cert.CommonName("ocsp.example"), cert.DNSNames("ocsp.example"), func(c *x509.Certificate) {
		c.OCSPServer = []string{server.URL}
		c.IssuingCertificateURL = []string{server.URL + "/issuer"}
	})
	certificateDER = generated.Certificate
	entry := ParseCertificate(generated)
	entry.OcspStapling = 1
	config := (&Config{Certificate: []*Certificate{entry}}).GetTLSConfig()
	select {
	case <-firstRequest:
	case <-time.After(5 * time.Second):
		t.Fatal("certificate worker never requested OCSP")
	}
	previous := waitCertificateSelection(t, config, generated.Certificate, nil)
	stopReaders := readCertificatesConcurrently(t, []*gotls.Config{config})
	defer stopReaders()
	releaseOnce.Do(func() { close(releaseResponse) })
	first := waitCertificateSelection(t, config, generated.Certificate, []byte("first OCSP response"))
	staple.Store([]byte("second OCSP response"))
	waitCertificateSelection(t, config, generated.Certificate, []byte("second OCSP response"))
	if len(previous.OCSPStaple) != 0 || !bytes.Equal(first.OCSPStaple, []byte("first OCSP response")) {
		t.Fatal("OCSP update changed a previously selected certificate")
	}
}

func waitCertificateSelection(t *testing.T, config *gotls.Config, certificate, staple []byte) *gotls.Certificate {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(5 * time.Millisecond)
	defer poll.Stop()
	for {
		selected, err := config.GetCertificate(&gotls.ClientHelloInfo{ServerName: "reload.example"})
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(selected.Certificate[0], certificate) && bytes.Equal(selected.OCSPStaple, staple) {
			return selected
		}
		select {
		case <-deadline.C:
			t.Fatal("certificate update was not published")
		case <-poll.C:
		}
	}
}

func readCertificatesConcurrently(t *testing.T, configs []*gotls.Config) func() {
	t.Helper()
	stop := make(chan struct{})
	errors := make(chan error, 4)
	var readers sync.WaitGroup
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, config := range configs {
					selected, err := config.GetCertificate(&gotls.ClientHelloInfo{ServerName: "reload.example"})
					if err != nil {
						errors <- err
						return
					}
					_ = bytes.Clone(selected.Certificate[0])
					_ = bytes.Clone(selected.OCSPStaple)
				}
			}
		}()
	}
	return func() {
		close(stop)
		readers.Wait()
		close(errors)
		for err := range errors {
			t.Errorf("concurrent certificate selection: %v", err)
		}
	}
}

package internalreleaseimage

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"time"
)

type FakeIRIRegistry struct {
	mux       *http.ServeMux
	server    *httptest.Server
	responses map[string][]registryResponse
}

type registryResponse struct {
	statusCode int
	body       string
}

// NewFakeIRIRegistry creates a new instance of the fake registry.
func NewFakeIRIRegistry() *FakeIRIRegistry {
	return &FakeIRIRegistry{
		responses: make(map[string][]registryResponse),
	}
}

func (fr *FakeIRIRegistry) AddResponse(endpoint string, statusCode int, body string) *FakeIRIRegistry {
	epReplies, found := fr.responses[endpoint]
	if !found {
		epReplies = []registryResponse{}
	}

	epReplies = append(epReplies, registryResponse{
		statusCode: statusCode,
		body:       body,
	})
	fr.responses[endpoint] = epReplies

	return fr
}

// Start configures the handlers, brings up the local server for the
// registry.
func (fr *FakeIRIRegistry) Start() error {
	fr.mux = http.NewServeMux()

	// Ping handler
	fr.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		epReplies, found := fr.responses[r.URL.Path]
		if !found || len(epReplies) == 0 {
			log.Fatalf("unexpected endpoint call received: %s", r.URL.Path)
		}
		reply := epReplies[0]
		fr.responses[r.URL.Path] = epReplies[1:]

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
		w.WriteHeader(reply.statusCode)

		if _, err := w.Write([]byte(reply.body)); err != nil {
			log.Fatal(err)
		}
	})

	err := fr.newTLSServer(fr.mux.ServeHTTP)
	if err != nil {
		return err
	}
	fr.server.StartTLS()

	return nil
}

func (fr *FakeIRIRegistry) newTLSServer(handler http.HandlerFunc) error {
	listener, err := net.Listen("tcp", "127.0.0.1:22625")
	if err != nil {
		return fmt.Errorf("failed to bind port: %v", err)
	}
	fr.server = httptest.NewUnstartedServer(handler)
	fr.server.Listener = listener
	cert, err := fr.generateSelfSignedCert()
	if err != nil {
		return fmt.Errorf("error configuring server cert: %w", err)
	}
	fr.server.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
	}
	return nil
}

func (fr *FakeIRIRegistry) generateSelfSignedCert() (tls.Certificate, error) {
	// Generate the private key
	pk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, err
	}
	// Generate the serial number
	sn, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return tls.Certificate{}, err
	}
	// Create the certificate template
	template := x509.Certificate{
		SerialNumber: sn,
		Subject: pkix.Name{
			Organization: []string{"IRI Tester"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(1 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &pk.PublicKey, pk)
	if err != nil {
		return tls.Certificate{}, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(pk)})
	return tls.X509KeyPair(certPEM, keyPEM)
}

// Close shutdowns the fake registry server.
func (fr *FakeIRIRegistry) Close() {
	fr.server.Close()
}

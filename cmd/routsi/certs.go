package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/muthuishere/routsi/internal/config"
)

// genCerts mints a private CA, a server cert, and client certs for mTLS —
// the whole trust setup in one command, no openssl. Keys are written 0600 and
// never printed.
func genCerts() {
	defDir := filepath.Join(config.ConfigDir(), "certs")
	dir := flag.String("dir", defDir, "output directory")
	hosts := flag.String("host", "localhost,127.0.0.1", "server SANs, comma-separated (DNS names or IPs)")
	clients := flag.Int("clients", 1, "number of client certs")
	flag.Parse()
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		log.Fatalf("certs: %v", err)
	}

	// CA (5 years)
	caKey := mustKey()
	caTpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "routsi-ca", Organization: []string{"routsi"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(5, 0, 0),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER := mustCert(caTpl, caTpl, &caKey.PublicKey, caKey)
	caCert, _ := x509.ParseCertificate(caDER)
	writePEM(*dir, "ca.crt", "CERTIFICATE", caDER, 0o644)
	writeKey(*dir, "ca.key", caKey)

	// Server cert (2 years) with the requested SANs.
	srvKey := mustKey()
	srvTpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "routsi-server"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(2, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range strings.Split(*hosts, ",") {
		h = strings.TrimSpace(h)
		if ip := net.ParseIP(h); ip != nil {
			srvTpl.IPAddresses = append(srvTpl.IPAddresses, ip)
		} else if h != "" {
			srvTpl.DNSNames = append(srvTpl.DNSNames, h)
		}
	}
	writePEM(*dir, "server.crt", "CERTIFICATE", mustCert(srvTpl, caCert, &srvKey.PublicKey, caKey), 0o644)
	writeKey(*dir, "server.key", srvKey)

	// Client certs (2 years).
	for i := 1; i <= *clients; i++ {
		key := mustKey()
		tpl := &x509.Certificate{
			SerialNumber: big.NewInt(int64(100 + i)),
			Subject:      pkix.Name{CommonName: fmt.Sprintf("routsi-client-%d", i)},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().AddDate(2, 0, 0),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		}
		name := fmt.Sprintf("client-%d", i)
		writePEM(*dir, name+".crt", "CERTIFICATE", mustCert(tpl, caCert, &key.PublicKey, caKey), 0o644)
		writeKey(*dir, name+".key", key)
	}

	fmt.Printf("wrote CA + server + %d client cert(s) to %s\n", *clients, *dir)
	fmt.Printf(`
Enable mTLS in models.yaml:
  tls:
    cert: %[1]s/server.crt
    key: %[1]s/server.key
    client_ca: %[1]s/ca.crt

Client call (curl):
  curl --cacert %[1]s/ca.crt --cert %[1]s/client-1.crt --key %[1]s/client-1.key \
    https://localhost:8080/v1/models

OpenAI SDKs: pass an http client configured with the client cert + CA.
Keep *.key files private (written 0600). Revocation = re-run and rotate the CA.
`, *dir)
}

func mustKey() *ecdsa.PrivateKey {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatalf("certs: keygen: %v", err)
	}
	return k
}

func mustCert(tpl, parent *x509.Certificate, pub *ecdsa.PublicKey, signer *ecdsa.PrivateKey) []byte {
	der, err := x509.CreateCertificate(rand.Reader, tpl, parent, pub, signer)
	if err != nil {
		log.Fatalf("certs: create: %v", err)
	}
	return der
}

func writePEM(dir, name, typ string, der []byte, mode os.FileMode) {
	b := pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
	if err := os.WriteFile(filepath.Join(dir, name), b, mode); err != nil {
		log.Fatalf("certs: write %s: %v", name, err)
	}
}

func writeKey(dir, name string, key *ecdsa.PrivateKey) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		log.Fatalf("certs: marshal key: %v", err)
	}
	writePEM(dir, name, "EC PRIVATE KEY", der, 0o600)
}

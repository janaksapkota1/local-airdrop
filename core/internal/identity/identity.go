// Package identity manages this device's TLS keypair, its fingerprint
// (which doubles as the device ID) and the store of trusted peers.
package identity

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// Identity is this device's long-lived key + self-signed certificate.
type Identity struct {
	Cert        tls.Certificate
	Fingerprint string // hex SHA-256 of the DER certificate; used as device ID
	Name        string // human-friendly device name
}

// Load reads the identity from dir, generating a new one on first run.
func Load(dir, name string) (*Identity, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	if _, err := os.Stat(certPath); errors.Is(err, os.ErrNotExist) {
		if err := generate(certPath, keyPath); err != nil {
			return nil, fmt.Errorf("generate identity: %w", err)
		}
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load identity: %w", err)
	}
	return &Identity{Cert: cert, Fingerprint: Fingerprint(cert.Certificate[0]), Name: name}, nil
}

func generate(certPath, keyPath string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "homedrop"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(30, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return err
	}
	return os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}

// Fingerprint returns the hex SHA-256 of a DER certificate.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// Short returns an abbreviated fingerprint for display.
func Short(fp string) string {
	if len(fp) > 8 {
		return fp[:8]
	}
	return fp
}

// PairCode derives a 4-digit code from two fingerprints. Both sides compute
// the same code; the user compares them on first contact to rule out a
// man-in-the-middle.
func PairCode(a, b string) string {
	if a > b {
		a, b = b, a
	}
	sum := sha256.Sum256([]byte(a + ":" + b))
	return fmt.Sprintf("%04d", binary.BigEndian.Uint32(sum[:4])%10000)
}

// ServerTLS accepts any client certificate; the handler reads its fingerprint.
func (id *Identity) ServerTLS() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{id.Cert},
		ClientAuth:   tls.RequireAnyClientCert,
		MinVersion:   tls.VersionTLS13,
	}
}

// ClientTLS pins the server certificate to the expected fingerprint.
// An empty expect accepts any certificate (used only to look up a device by
// IP; the caller must then show the pair code).
func (id *Identity) ClientTLS(expect string) *tls.Config {
	return &tls.Config{
		Certificates:       []tls.Certificate{id.Cert},
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // replaced by the fingerprint check below
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("peer sent no certificate")
			}
			if expect != "" && Fingerprint(raw[0]) != expect {
				return errors.New("peer certificate does not match its device ID")
			}
			return nil
		},
	}
}

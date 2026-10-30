// SPDX-License-Identifier: GPL-3.0-only
package tools

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

var knownHosts = []string{"quasar.yandex.net", "quasar.yandex.ru", "oauth.yandex.ru", "login.yandex.ru", "mobileproxy.passport.yandex.net", "startup.mobile.yandex.net", "report.appmetrica.yandex.net", "clck.yandex.net", "uniproxy.alice.yandex.net", "uniproxy-quic.alice.yandex.net", "push.yandex.ru", "scbh.yandex.net", "rpc.alice.yandex.net", "analytics.mobile.yandex.net", "updater.mobile.yandex.net"}

func serial() (*big.Int, error) {
	n, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if e == nil {
		n.Add(n, big.NewInt(1))
	}
	return n, e
}
func certs(f *flag.FlagSet, args []string, out io.Writer) error {
	dir := f.String("out", "certs", "output directory (private keys mode 0600)")
	var ips, hosts stringsFlag
	f.Var(&ips, "ip", "additional SAN IP; repeatable")
	f.Var(&hosts, "host", "additional SAN hostname; repeatable")
	if e := f.Parse(args); e != nil {
		return e
	}
	if e := noArgs(f); e != nil {
		return e
	}
	ipList := []net.IP{net.ParseIP("127.0.0.1")}
	for _, s := range ips {
		ip := net.ParseIP(s)
		if ip == nil {
			return fmt.Errorf("invalid SAN IP")
		}
		ipList = append(ipList, ip)
	}
	names := append(append([]string{}, knownHosts...), "localhost")
	names = append(names, hosts...)
	for _, s := range names {
		if s == "" {
			return fmt.Errorf("empty SAN hostname")
		}
	}
	if e := privateDir(*dir); e != nil {
		return e
	}
	keyPath, crtPath := filepath.Join(*dir, "ca.key"), filepath.Join(*dir, "ca.crt")
	kb, ke := os.ReadFile(keyPath)
	cb, ce := os.ReadFile(crtPath)
	var ca *x509.Certificate
	var key *ecdsa.PrivateKey
	now := time.Now()
	if ke == nil && ce == nil {
		pair, e := tls.X509KeyPair(cb, kb)
		if e != nil {
			return fmt.Errorf("invalid existing CA key pair")
		}
		key, _ = pair.PrivateKey.(*ecdsa.PrivateKey)
		ca, e = x509.ParseCertificate(pair.Certificate[0])
		if e != nil {
			return e
		}
		if key == nil || key.Curve != elliptic.P256() || !ca.IsCA || ca.KeyUsage&x509.KeyUsageCertSign == 0 || now.Before(ca.NotBefore) || !now.Before(ca.NotAfter) {
			return fmt.Errorf("existing CA must be a valid ECDSA P-256 signing CA")
		}
	} else if errors.Is(ke, os.ErrNotExist) && errors.Is(ce, os.ErrNotExist) {
		var e error
		key, e = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if e != nil {
			return e
		}
		sn, e := serial()
		if e != nil {
			return e
		}
		ca = &x509.Certificate{SerialNumber: sn, Subject: pkix.Name{CommonName: "Quasard Local CA"}, NotBefore: now.Add(-24 * time.Hour), NotAfter: now.AddDate(10, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
		der, e := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
		if e != nil {
			return e
		}
		ca, e = x509.ParseCertificate(der)
		if e != nil {
			return e
		}
		if e = writeKey(keyPath, key); e != nil {
			return e
		}
		if e = privateWrite(crtPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})); e != nil {
			return e
		}
	} else {
		return fmt.Errorf("CA files incomplete or unreadable; refusing to replace")
	}
	for _, p := range []string{keyPath, crtPath} {
		info, e := os.Lstat(p)
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("CA file is not regular")
		}
		if e = os.Chmod(p, 0600); e != nil {
			return e
		}
	}
	serverKey, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		return e
	}
	sn, e := serial()
	if e != nil {
		return e
	}
	until := now.AddDate(2, 0, 0)
	if ca.NotAfter.Before(until) {
		until = ca.NotAfter
	}
	leaf := &x509.Certificate{SerialNumber: sn, Subject: pkix.Name{CommonName: "quasar.yandex.net"}, NotBefore: now.Add(-24 * time.Hour), NotAfter: until, DNSNames: names, IPAddresses: ipList, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, e := x509.CreateCertificate(rand.Reader, leaf, ca, &serverKey.PublicKey, key)
	if e != nil {
		return e
	}
	if e = writeKey(filepath.Join(*dir, "server.key"), serverKey); e != nil {
		return e
	}
	if e = privateWrite(filepath.Join(*dir, "server.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})); e != nil {
		return e
	}
	_, e = fmt.Fprintln(out, "CA and server certificates ready; nothing installed.")
	return e
}
func writeKey(path string, k *ecdsa.PrivateKey) error {
	b, e := x509.MarshalPKCS8PrivateKey(k)
	if e != nil {
		return e
	}
	return privateWrite(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: b}))
}

package identity

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
)

type Identity struct {
	privateKey ed25519.PrivateKey
	publicPEM  string
}

func LoadEd25519PrivateKey(path string) (*Identity, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read private key file: %w", err)
	}

	keyBytes := raw

	// prfpkey.dpapi — это не PEM, а DPAPI-protected blob.
	if strings.HasSuffix(strings.ToLower(path), ".dpapi") {
		keyBytes, err = DPAPIUnprotect(raw)
		if err != nil {
			return nil, fmt.Errorf("dpapi unprotect private key: %w", err)
		}
	}

	return parseEd25519PrivateKey(keyBytes)
}

func parseEd25519PrivateKey(raw []byte) (*Identity, error) {
	raw = bytes.TrimSpace(raw)

	// Основной ожидаемый вариант после DPAPI:
	// -----BEGIN PRIVATE KEY-----
	// ...
	// -----END PRIVATE KEY-----
	if block, _ := pem.Decode(raw); block != nil {
		return parsePKCS8Ed25519(block.Bytes)
	}

	// На случай если Python хранит внутри DPAPI не PEM, а base64 от DER/PEM.
	if decoded, err := base64.StdEncoding.DecodeString(string(raw)); err == nil {
		decoded = bytes.TrimSpace(decoded)

		if block, _ := pem.Decode(decoded); block != nil {
			return parsePKCS8Ed25519(block.Bytes)
		}

		if id, err := parsePKCS8Ed25519(decoded); err == nil {
			return id, nil
		}
	}

	// На случай если после DPAPI лежит чистый DER PKCS#8.
	if id, err := parsePKCS8Ed25519(raw); err == nil {
		return id, nil
	}

	return nil, fmt.Errorf("private key is neither PEM nor PKCS8 DER")
}

func parsePKCS8Ed25519(der []byte) (*Identity, error) {
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("parse PKCS8 private key: %w", err)
	}

	privateKey, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is not Ed25519")
	}

	pubAny := privateKey.Public()
	publicKey, ok := pubAny.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key is not Ed25519")
	}

	pubDER, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		return nil, fmt.Errorf("marshal public key: %w", err)
	}

	pubPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubDER,
	})

	return &Identity{
		privateKey: privateKey,
		publicPEM:  string(pubPEM),
	}, nil
}

func (i *Identity) PublicKeyPEM() string {
	return i.publicPEM
}

func (i *Identity) Sign(message []byte) ([]byte, error) {
	return i.privateKey.Sign(nil, message, crypto.Hash(0))
}

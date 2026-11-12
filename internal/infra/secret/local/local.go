// Package local keeps secrets in an AES-GCM encrypted file (or only in memory
// when Path is empty).
package local

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/infra/secret"
)

// Store is a file-backed secret store.
type Store struct {
	path string
	aead cipher.AEAD

	mu   sync.Mutex
	data map[string]string
}

// New opens (or creates) the store. key is any passphrase; it is hashed to 32 bytes.
func New(path, key string) (*Store, error) {
	sum := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	s := &Store{path: path, aead: aead, data: map[string]string{}}
	if path == "" {
		return s, nil
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	ns := aead.NonceSize()
	if len(raw) < ns {
		return nil, errs.Invalid("secret file corrupt")
	}
	plain, err := aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInvalid, err, "cannot decrypt secret file (wrong key?)")
	}
	if err := json.Unmarshal(plain, &s.data); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Get(ctx context.Context, ref string) (string, error) {
	if !secret.Allowed(ctx, ref) {
		return "", errs.New(errs.CodeForbidden, "secret %q not accessible", ref)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[ref]
	if !ok {
		return "", errs.NotFound("secret", ref)
	}
	return v, nil
}

func (s *Store) Put(ctx context.Context, ref, value string) error {
	if !secret.Allowed(ctx, ref) {
		return errs.New(errs.CodeForbidden, "secret %q not accessible", ref)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[ref] = value
	return s.flush()
}

func (s *Store) Delete(ctx context.Context, ref string) error {
	if !secret.Allowed(ctx, ref) {
		return errs.New(errs.CodeForbidden, "secret %q not accessible", ref)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, ref)
	return s.flush()
}

func (s *Store) flush() error {
	if s.path == "" {
		return nil
	}
	plain, err := json.Marshal(s.data)
	if err != nil {
		return err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	out := s.aead.Seal(nonce, nonce, plain, nil)
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

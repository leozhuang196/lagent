// Package local stores objects in a directory. Signed URLs point at the API's
// /v1/objects endpoint and carry an HMAC over key and expiry.
package local

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/infra/objectstore"
)

// Store is a directory-backed object store.
type Store struct {
	Root    string
	BaseURL string // e.g. http://localhost:8080
	Secret  []byte
}

func New(root, baseURL string, secret []byte) (*Store, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &Store{Root: root, BaseURL: strings.TrimRight(baseURL, "/"), Secret: secret}, nil
}

func (s *Store) path(key string) (string, error) {
	clean := filepath.Clean("/" + key)
	if strings.Contains(key, "..") || clean == "/" {
		return "", errs.Invalid("bad object key %q", key)
	}
	return filepath.Join(s.Root, "objects", clean), nil
}

type sidecar struct {
	ContentType string `json:"content_type"`
}

func (s *Store) Put(_ context.Context, key string, r io.Reader, _ int64, contentType string) error {
	p, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	meta, _ := json.Marshal(sidecar{ContentType: contentType})
	if err := os.WriteFile(p+".meta", meta, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (s *Store) Get(_ context.Context, key string) (io.ReadCloser, objectstore.ObjectInfo, error) {
	p, err := s.path(key)
	if err != nil {
		return nil, objectstore.ObjectInfo{}, err
	}
	f, err := os.Open(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, objectstore.ObjectInfo{}, errs.NotFound("object", key)
		}
		return nil, objectstore.ObjectInfo{}, err
	}
	st, _ := f.Stat()
	info := objectstore.ObjectInfo{Key: key, Size: st.Size(), ModTime: st.ModTime()}
	if raw, err := os.ReadFile(p + ".meta"); err == nil {
		var sc sidecar
		_ = json.Unmarshal(raw, &sc)
		info.ContentType = sc.ContentType
	}
	return f, info, nil
}

func (s *Store) List(_ context.Context, prefix string) ([]objectstore.ObjectInfo, error) {
	base := filepath.Join(s.Root, "objects")
	var out []objectstore.ObjectInfo
	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() || strings.HasSuffix(p, ".meta") || strings.HasSuffix(p, ".tmp") {
			return nil
		}
		rel, _ := filepath.Rel(base, p)
		key := filepath.ToSlash(rel)
		if !strings.HasPrefix(key, prefix) {
			return nil
		}
		info, _ := d.Info()
		out = append(out, objectstore.ObjectInfo{Key: key, Size: info.Size(), ModTime: info.ModTime()})
		return nil
	})
	return out, err
}

func (s *Store) Delete(_ context.Context, key string) error {
	p, err := s.path(key)
	if err != nil {
		return err
	}
	os.Remove(p + ".meta")
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Sign computes the HMAC for key and expiry.
func (s *Store) Sign(key string, exp int64) string {
	m := hmac.New(sha256.New, s.Secret)
	fmt.Fprintf(m, "%s\n%d", key, exp)
	return hex.EncodeToString(m.Sum(nil))
}

// Verify checks a signature produced by SignedURL.
func (s *Store) Verify(key, expStr, sig string, now time.Time) bool {
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || now.Unix() > exp {
		return false
	}
	return hmac.Equal([]byte(s.Sign(key, exp)), []byte(sig))
}

func (s *Store) SignedURL(_ context.Context, key string, ttl time.Duration) (string, error) {
	exp := time.Now().Add(ttl).Unix()
	q := url.Values{"key": {key}, "exp": {strconv.FormatInt(exp, 10)}, "sig": {s.Sign(key, exp)}}
	return s.BaseURL + "/v1/objects?" + q.Encode(), nil
}

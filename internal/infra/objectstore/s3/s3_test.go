package s3

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eternityx/lagent/internal/core/errs"
)

// fakeS3 is a tiny path-style S3 emulator: enough of PutObject, GetObject,
// DeleteObject and ListObjectsV2 to check request shaping and error mapping.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string]fakeObj
	auth    []string
}

type fakeObj struct {
	data        []byte
	contentType string
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
	if parts[0] != "bucket" {
		http.Error(w, "wrong bucket "+parts[0], 400)
		return
	}
	key := ""
	if len(parts) == 2 {
		key = parts[1]
	}
	switch {
	case r.Method == http.MethodPut:
		b, _ := io.ReadAll(r.Body)
		if r.ContentLength >= 0 && int64(len(b)) != r.ContentLength {
			http.Error(w, "length mismatch", 400)
			return
		}
		f.objects[key] = fakeObj{data: b, contentType: r.Header.Get("Content-Type")}
		w.WriteHeader(200)
	case r.Method == http.MethodGet && key == "" && r.URL.Query().Get("list-type") == "2":
		prefix := r.URL.Query().Get("prefix")
		var keys []string
		for k := range f.objects {
			if strings.HasPrefix(k, prefix) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		var sb strings.Builder
		sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>bucket</Name>`)
		fmt.Fprintf(&sb, "<KeyCount>%d</KeyCount><IsTruncated>false</IsTruncated>", len(keys))
		for _, k := range keys {
			var esc bytes.Buffer
			xml.EscapeText(&esc, []byte(k))
			fmt.Fprintf(&sb, "<Contents><Key>%s</Key><Size>%d</Size><LastModified>2026-01-02T03:04:05.000Z</LastModified></Contents>", esc.String(), len(f.objects[k].data))
		}
		sb.WriteString("</ListBucketResult>")
		w.Header().Set("Content-Type", "application/xml")
		io.WriteString(w, sb.String())
	case r.Method == http.MethodGet:
		o, ok := f.objects[key]
		if !ok {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(404)
			io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`)
			return
		}
		w.Header().Set("Content-Type", o.contentType)
		w.Header().Set("Content-Length", fmt.Sprint(len(o.data)))
		w.Header().Set("Last-Modified", "Fri, 02 Jan 2026 03:04:05 GMT")
		w.Write(o.data)
	case r.Method == http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(204)
	default:
		http.Error(w, "unsupported", 405)
	}
}

func newFake(t *testing.T) (*Store, *fakeS3, string) {
	t.Helper()
	f := &fakeS3{objects: map[string]fakeObj{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	t.Setenv("AWS_CONFIG_FILE", os.DevNull)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", os.DevNull)
	st, err := New(context.Background(), Config{
		Bucket: "bucket", Region: "us-east-1", Endpoint: srv.URL, UsePathStyle: true,
		AccessKey: "AKID", SecretKey: "SECRET", HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return st, f, srv.URL
}

func TestRoundTrip(t *testing.T) {
	st, f, base := newFake(t)
	ctx := context.Background()

	if err := st.Put(ctx, "tenants/t1/a.txt", strings.NewReader("hello"), 5, "text/plain"); err != nil {
		t.Fatal(err)
	}
	// A non-seekable reader with unknown size is spooled first.
	pr, pw := io.Pipe()
	go func() { pw.Write([]byte("streamed body")); pw.Close() }()
	if err := st.Put(ctx, "tenants/t1/dir/b.bin", pr, -1, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.Put(ctx, "tenants/t2/c.txt", strings.NewReader("x"), 1, ""); err != nil {
		t.Fatal(err)
	}
	if string(f.objects["tenants/t1/dir/b.bin"].data) != "streamed body" {
		t.Fatalf("spooled put: %q", f.objects["tenants/t1/dir/b.bin"].data)
	}
	if !strings.HasPrefix(f.auth[0], "AWS4-HMAC-SHA256 Credential=AKID/") {
		t.Fatalf("not SigV4 signed: %q", f.auth[0])
	}

	rc, info, err := st.Get(ctx, "tenants/t1/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "hello" || info.Size != 5 || info.ContentType != "text/plain" || info.ModTime.IsZero() {
		t.Fatalf("get %q %+v", b, info)
	}

	list, err := st.List(ctx, "tenants/t1/")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Key != "tenants/t1/a.txt" || list[1].Key != "tenants/t1/dir/b.bin" || list[1].Size != 13 {
		t.Fatalf("list %+v", list)
	}

	if err := st.Delete(ctx, "tenants/t1/a.txt"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Get(ctx, "tenants/t1/a.txt"); !errs.Is(err, errs.CodeNotFound) {
		t.Fatalf("missing key: %v", err)
	}
	if err := st.Delete(ctx, "tenants/t1/a.txt"); err != nil {
		t.Fatalf("delete is idempotent: %v", err)
	}

	u, err := st.SignedURL(ctx, "tenants/t2/c.txt", 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	pu, _ := url.Parse(u)
	q := pu.Query()
	if !strings.HasPrefix(u, base+"/bucket/tenants/t2/c.txt?") || q.Get("X-Amz-Expires") != "600" || q.Get("X-Amz-Signature") == "" {
		t.Fatalf("signed url %s", u)
	}
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "x" {
		t.Fatalf("presigned get %q", body)
	}
}

func TestBadKey(t *testing.T) {
	st, _, _ := newFake(t)
	if err := st.Put(context.Background(), "../x", strings.NewReader(""), 0, ""); !errs.Is(err, errs.CodeInvalid) {
		t.Fatalf("err %v", err)
	}
}

// TestRealEndpoint runs against a real S3/MinIO when LAGENT_TEST_S3_ENDPOINT
// is set (bucket from LAGENT_TEST_S3_BUCKET, credentials from the AWS chain).
func TestRealEndpoint(t *testing.T) {
	ep := os.Getenv("LAGENT_TEST_S3_ENDPOINT")
	if ep == "" {
		t.Skip("LAGENT_TEST_S3_ENDPOINT not set")
	}
	bucket := os.Getenv("LAGENT_TEST_S3_BUCKET")
	if bucket == "" {
		bucket = "lagent-test"
	}
	ctx := context.Background()
	st, err := New(ctx, Config{Bucket: bucket, Region: os.Getenv("AWS_REGION"), Endpoint: ep, UsePathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf("lagent-test/%d.txt", time.Now().UnixNano())
	if err := st.Put(ctx, key, strings.NewReader("hi"), 2, "text/plain"); err != nil {
		t.Fatal(err)
	}
	defer st.Delete(ctx, key)
	rc, info, err := st.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "hi" || info.Size != 2 {
		t.Fatalf("get %q %+v", b, info)
	}
	list, err := st.List(ctx, "lagent-test/")
	if err != nil || len(list) == 0 {
		t.Fatalf("list %v %v", list, err)
	}
	if _, _, err := st.Get(ctx, key+".missing"); !errs.Is(err, errs.CodeNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := st.SignedURL(ctx, key, time.Minute); err != nil {
		t.Fatal(err)
	}
}

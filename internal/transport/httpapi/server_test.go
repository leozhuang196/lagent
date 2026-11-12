package httpapi

import (
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eternityx/lagent/internal/core/errs"
)

func TestReadJSON(t *testing.T) {
	const limit = 4 << 20
	for _, tt := range []struct {
		name, body string
		valid      bool
	}{
		{"object", `{"name":"hello"}`, true},
		{"trailing whitespace", "{\"name\":\"hello\"}\r\n\t ", true},
		{"empty", "", false},
		{"whitespace", " \n\t", false},
		{"null", " \nnull\t", false},
		{"unknown field", `{"extra":true}`, false},
		{"wrong type", `{"name":123}`, false},
		{"incomplete", `{"name":`, false},
		{"second object", `{} {}`, false},
		{"second null", `{} null`, false},
		{"trailing garbage", `{} garbage`, false},
		{"exact limit", `{}` + strings.Repeat(" ", limit-2), true},
		{"oversized whitespace", `{}` + strings.Repeat(" ", limit-1), false},
		{"oversized string", `{"name":"` + strings.Repeat("x", limit) + `"}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/", strings.NewReader(tt.body))
			var got struct {
				Name string `json:"name"`
			}
			err := ReadJSON(r, &got)
			if tt.valid {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errs.Is(err, errs.CodeInvalid) {
				t.Fatalf("expected invalid, got %v", err)
			}
		})
	}
}

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (failingBody) Close() error             { return nil }

func TestReadJSONBodyErrors(t *testing.T) {
	for _, body := range []io.ReadCloser{nil, failingBody{}} {
		r := httptest.NewRequest("POST", "/", nil)
		r.Body = body
		var got map[string]any
		if err := ReadJSON(r, &got); !errs.Is(err, errs.CodeInvalid) {
			t.Fatalf("expected invalid, got %v", err)
		}
	}
}

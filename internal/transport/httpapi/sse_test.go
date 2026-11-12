package httpapi

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eternityx/lagent/internal/core/types"
)

type streamBus struct {
	read    func(int64, int) ([]types.Event, bool, error)
	live    chan types.Event
	last    int64
	lastErr error
	stopped bool
}

func (b *streamBus) Append(context.Context, types.Event) (int64, error) { panic("unused") }
func (b *streamBus) Read(_ context.Context, _ string, after int64, limit int) ([]types.Event, bool, error) {
	return b.read(after, limit)
}
func (b *streamBus) Subscribe(context.Context, string) (<-chan types.Event, func(), error) {
	return b.live, func() { b.stopped = true }, nil
}
func (b *streamBus) LastSeq(context.Context, string) (int64, error) { return b.last, b.lastErr }

func TestStreamCursorValidation(t *testing.T) {
	for _, cursor := range []string{"-1", "oops", "9223372036854775808"} {
		r := httptest.NewRequest("GET", "/events?after="+cursor, nil)
		w := httptest.NewRecorder()
		StreamEvents(w, r, nil, "s", nil)
		if w.Code != 422 {
			t.Fatalf("cursor %s: status %d", cursor, w.Code)
		}
	}
}

func TestStreamGapRecovery(t *testing.T) {
	for _, mode := range []string{"paged", "expired", "read failure", "cursor failure"} {
		t.Run(mode, func(t *testing.T) {
			ch := make(chan types.Event, 1)
			ch <- types.Event{Seq: 2501}
			close(ch)
			calls := 0
			bus := &streamBus{live: ch, last: 2501}
			bus.read = func(after int64, limit int) ([]types.Event, bool, error) {
				calls++
				if calls == 1 {
					return nil, false, nil
				}
				if mode == "read failure" {
					return nil, false, errors.New("unavailable")
				}
				if mode == "expired" || mode == "cursor failure" {
					return nil, true, nil
				}
				out := []types.Event{}
				for seq := after + 1; seq <= 2501 && len(out) < limit; seq++ {
					out = append(out, types.Event{Seq: seq, Type: types.EvMessageDelta})
				}
				return out, false, nil
			}
			if mode == "cursor failure" {
				bus.lastErr = errors.New("unavailable")
			}
			w := httptest.NewRecorder()
			StreamEvents(w, httptest.NewRequest("GET", "/events", nil), bus, "s", nil)
			body := w.Body.String()
			if !bus.stopped {
				t.Fatal("subscription leaked")
			}
			switch mode {
			case "paged":
				if got := strings.Count(body, "data: "); got != 2501 {
					t.Fatalf("want 2501 events, got %d", got)
				}
				if strings.Contains(body, "stream.reset") {
					t.Fatal("unexpected reset")
				}
			case "expired":
				if !strings.Contains(body, "event: stream.reset") || !strings.Contains(body, "id: 2501") {
					t.Fatalf("missing reset: %s", body)
				}
			default:
				if body != "" {
					t.Fatalf("must not advance cursor on failure: %s", body)
				}
			}
		})
	}
}

package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/eternityx/lagent/internal/core/clock"
	"github.com/eternityx/lagent/internal/core/errs"
	"github.com/eternityx/lagent/internal/core/types"
	"github.com/eternityx/lagent/internal/infra/eventbus"
)

// events streams a session's events over SSE. Clients resume with the
// standard Last-Event-ID header (or ?after=); when the cursor is older than
// the buffer a stream.reset event tells them to reload the snapshot.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	stream := chi.URLParam(r, "id")
	ctx := r.Context()
	// Authorisation: the snapshot check enforces ownership.
	if _, err := s.Sessions.Snapshot(ctx, stream, 1); err != nil {
		WriteError(w, err)
		return
	}
	StreamEvents(w, r, s.Bus, stream, s.Clock)
}

// StreamEvents serves an authorised stream. Notifications are hints; the log
// supplies ordered events, including recovery after subscriber overflow (§6.3).
func StreamEvents(w http.ResponseWriter, r *http.Request, bus eventbus.Bus, stream string, clk clock.Clock) {
	ctx := r.Context()
	if clk == nil {
		clk = clock.Real{}
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		WriteError(w, errs.New(errs.CodeInternal, "streaming unsupported"))
		return
	}
	after := int64(0)
	cursor := r.Header.Get("Last-Event-ID")
	if cursor == "" {
		cursor = r.URL.Query().Get("after")
	}
	if cursor != "" {
		var err error
		after, err = strconv.ParseInt(cursor, 10, 64)
		if err != nil || after < 0 {
			WriteError(w, errs.Invalid("event cursor must be a non-negative integer"))
			return
		}
	}
	// Subscribe before reading the backlog so nothing falls in between.
	live, stop, err := bus.Subscribe(ctx, stream)
	if err != nil {
		WriteError(w, err)
		return
	}
	defer stop()
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	write := func(ev types.Event) error {
		data, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Type, data)
		return err
	}
	last := after
	// Always page through the log. A gap can exceed a batch or outlive retention.
	drain := func() error {
		for {
			backlog, reset, err := bus.Read(ctx, stream, last, 1000)
			if err != nil {
				return err
			}
			if reset {
				cur, err := bus.LastSeq(ctx, stream)
				if err != nil {
					return err
				}
				if err := write(types.Event{Seq: cur, Stream: stream, Type: types.EvStreamReset, TS: clk.Now()}); err != nil {
					return err
				}
				last = cur
				fl.Flush()
				return nil
			}
			for _, ev := range backlog {
				if err := write(ev); err != nil {
					return err
				}
				last = ev.Seq
			}
			fl.Flush()
			if len(backlog) < 1000 {
				return nil
			}
		}
	}
	if err := drain(); err != nil {
		return
	}
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			// Recover even when the final notification was dropped and no next event arrives.
			if err := drain(); err != nil {
				return
			}
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			fl.Flush()
		case ev, ok := <-live:
			if !ok {
				return
			}
			if ev.Seq <= last {
				continue
			}
			if err := drain(); err != nil {
				return
			}
		}
	}
}

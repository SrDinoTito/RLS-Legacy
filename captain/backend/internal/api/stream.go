// stream.go: Server-Sent Events endpoint (TASK-004, REQ-018 / DSG-004).
// Session-cookie authenticated (a cookie is a native SSE credential:
// EventSource cannot set headers). Emits typed frames:
//
//	event: heartbeat|agent_offline|agent_online|alert_new|alert_resolved
//	data: {json}
//
// plus a comment ping every 15s to keep intermediaries from closing the
// connection. The DB write-path never blocks on this handler: the broker
// broadcasts fire-and-forget with buffered channels.
package api

import (
	"fmt"
	"io"
	"net/http"
	"time"
)

// pingEvery is the keepalive cadence.
const pingEvery = 15 * time.Second

// GET /api/v1/stream — upgrades the HTTP connection into an SSE stream.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no") // nginx: do not buffer the stream

	rc := http.NewResponseController(w)
	// Write deadline keeps a hung client from holding a connection forever;
	// refreshed on every successful frame below.
	_ = rc.SetWriteDeadline(time.Now().Add(90 * time.Second))

	w.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(w, ": connected\n\n"); err != nil {
		return
	}
	flusher.Flush()

	events, cancel := s.broker.Subscribe()
	defer cancel()

	ping := time.NewTicker(pingEvery)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return // client hung up; deferred cancel unregisters the sub
		case <-ping.C:
			if _, err := fmt.Fprintf(w, ": ping %s\n\n", pingEvery); err != nil {
				return
			}
			flusher.Flush()
		case frame, open := <-events:
			if !open {
				return // broker must never close; defensive
			}
			_ = rc.SetWriteDeadline(time.Now().Add(90 * time.Second))
			if _, err := w.Write(frame); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

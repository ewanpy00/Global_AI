package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

type sseWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func newSSE(w http.ResponseWriter) (*sseWriter, error) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming is not supported by this response writer")
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	f.Flush()
	return &sseWriter{w: w, f: f}, nil
}

// send writes one named event. Write errors mean the client went away; the request
// context is cancelled in that case, so they are not reported here.
func (s *sseWriter) send(event string, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		b, _ = json.Marshal(map[string]string{"message": err.Error()})
		event = "error"
	}
	if event != "" {
		fmt.Fprintf(s.w, "event: %s\n", event)
	}
	fmt.Fprintf(s.w, "data: %s\n\n", b)
	s.f.Flush()
}

// sendData writes an unnamed event (the OpenAI streaming format uses only data lines).
func (s *sseWriter) sendData(raw string) {
	fmt.Fprintf(s.w, "data: %s\n\n", raw)
	s.f.Flush()
}

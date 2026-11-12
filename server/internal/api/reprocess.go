package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"runtime/debug"
	"sync"
	"time"
)

// reprocessTimeout begrenzt einen Reprocess-Lauf. Er haengt nicht am
// Request-Kontext, damit ein Verbindungsabbruch oder der WriteTimeout des
// HTTP-Servers die Neuberechnung nicht mittendrin abbricht.
const reprocessTimeout = 30 * time.Minute

// reprocessJob haelt den (hoechstens einen) laufenden bzw. letzten
// Reprocess-Lauf.
type reprocessJob struct {
	mu       sync.Mutex
	running  bool
	done     chan struct{}
	started  time.Time
	finished time.Time
	matches  int
	failed   bool
}

type reprocessStatus struct {
	Running    bool   `json:"running"`
	Matches    int    `json:"matches"`
	Error      string `json:"error,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
}

func (j *reprocessJob) statusLocked() reprocessStatus {
	st := reprocessStatus{Running: j.running, Matches: j.matches}
	if !j.started.IsZero() {
		st.StartedAt = j.started.UTC().Format(time.RFC3339)
	}
	if !j.running && !j.finished.IsZero() {
		st.FinishedAt = j.finished.UTC().Format(time.RFC3339)
		if j.failed {
			st.Error = "interner Fehler"
		}
	}
	return st
}

// start startet einen Lauf oder haengt sich an den laufenden an und liefert
// dessen done-Kanal.
func (s *Server) startReprocess() <-chan struct{} {
	j := &s.reprocess
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.running {
		return j.done
	}
	j.running, j.failed, j.matches = true, false, 0
	j.started, j.finished = time.Now(), time.Time{}
	j.done = make(chan struct{})
	done := j.done
	go func() {
		n, err := s.runReprocess()
		if err != nil {
			log.Printf("reprocess: %v", err)
		} else {
			log.Printf("reprocess: %d Matches neu berechnet", n)
		}
		j.mu.Lock()
		j.running, j.matches, j.failed, j.finished = false, n, err != nil, time.Now()
		j.mu.Unlock()
		close(done)
	}()
	return done
}

// runReprocess fuehrt den Lauf aus und macht aus einer Panic einen Fehler,
// damit der Job nicht ewig als laufend gilt und der Prozess weiterlebt.
func (s *Server) runReprocess() (n int, err error) {
	defer func() {
		if p := recover(); p != nil {
			log.Printf("reprocess: panic: %v\n%s", p, debug.Stack())
			n, err = 0, fmt.Errorf("panic: %v", p)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), reprocessTimeout)
	defer cancel()
	return s.Ingest.Reprocess(ctx)
}

// adminReprocess startet die Neuberechnung. Ist sie innerhalb von
// reprocessWait fertig, kommt wie bisher 200 {"matches": n}; sonst 202 mit
// dem Status, und der Lauf geht im Hintergrund weiter
// (GET /api/admin/reprocess fragt ihn ab).
func (s *Server) adminReprocess(w http.ResponseWriter, r *http.Request) {
	done := s.startReprocess()
	timer := time.NewTimer(s.reprocessWait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	case <-r.Context().Done():
	}
	s.reprocess.mu.Lock()
	st := s.reprocess.statusLocked()
	s.reprocess.mu.Unlock()
	switch {
	case st.Running:
		writeJSON(w, http.StatusAccepted, st)
	case st.Error != "":
		writeError(w, http.StatusInternalServerError, st.Error)
	default:
		writeJSON(w, http.StatusOK, st)
	}
}

func (s *Server) adminReprocessStatus(w http.ResponseWriter, r *http.Request) {
	s.reprocess.mu.Lock()
	st := s.reprocess.statusLocked()
	s.reprocess.mu.Unlock()
	writeJSON(w, http.StatusOK, st)
}

// Package querylog batches DNS query records into the store off the hot path.
package querylog

import (
	"context"
	"log"
	"time"

	"netguard/internal/store"
)

type Logger struct {
	store *store.Store
	ch    chan store.QueryEntry
}

func New(s *store.Store) *Logger {
	return &Logger{store: s, ch: make(chan store.QueryEntry, 20000)}
}

// Log queues an entry; it never blocks the DNS server (drops when full).
func (l *Logger) Log(e store.QueryEntry) {
	select {
	case l.ch <- e:
	default:
	}
}

func (l *Logger) Run(ctx context.Context) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	batch := make([]store.QueryEntry, 0, 1000)
	seen := map[string]store.Seen{}
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := l.store.WriteBatch(batch, seen); err != nil {
			log.Printf("querylog: %v", err)
		}
		batch = batch[:0]
		seen = map[string]store.Seen{}
	}
	for {
		select {
		case e := <-l.ch:
			batch = append(batch, e)
			seen[e.Identifier] = store.Seen{DeviceID: e.DeviceID, IP: e.ClientIP, TS: e.TS}
			if len(batch) >= 1000 {
				flush()
			}
		case <-tick.C:
			flush()
		case <-ctx.Done():
			flush()
			return
		}
	}
}

package bridge

import (
	"encoding/json"
	"log"
)

// eventFrame is one server→client /rpc frame (master plan §5):
//
//	{"event":"terminal:status","data":{…}}
type eventFrame struct {
	Event string `json:"event"`
	Data  any    `json:"data"`
}

// Emit implements api.Emitter: marshal one {event,data} frame and fan it out
// to every connected /rpc client. Events emitted before any client connects
// are dropped (there is nothing to deliver them to, exactly like the
// LateEmitter they replace); a full per-client queue blocks the emitter rather
// than dropping a lifecycle event. Terminal bytes never use this path — the
// /terminal sink is authoritative.
func (s *Server) Emit(event string, payload any) {
	frame, err := json.Marshal(eventFrame{Event: event, Data: payload})
	if err != nil {
		log.Printf("bridge: marshal event %q: %v", event, err)
		return
	}
	s.mu.Lock()
	clients := make([]*rpcClient, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.Unlock()
	for _, c := range clients {
		c.enqueue(frame)
	}
}

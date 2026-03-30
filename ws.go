package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type WsEvent struct {
	Type   string       `json:"type"`
	At     int64        `json:"at"`
	Track  *TrackMeta   `json:"track,omitempty"`
	Window *TrackWindow `json:"window,omitempty"`
	Error  *string      `json:"error,omitempty"`
}

type Hub struct {
	mu      sync.Mutex
	clients map[*websocket.Conn]struct{}
}

var wsHub = &Hub{clients: make(map[*websocket.Conn]struct{})}

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		// Local network device connections.
		return true
	},
}

func handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	wsHub.add(conn)
	log.Printf("ws connected: %s", r.RemoteAddr)

	// Send initial snapshot.
	track, queue, _ := playbackState.snapshot()
	window := cacheState.buildWindowWithQueue(queue)
	wsHub.send(conn, WsEvent{Type: "snapshot", At: time.Now().UnixMilli(), Track: track, Window: &window})

	go wsHub.readPump(conn)
}

func (h *Hub) add(c *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[c] = struct{}{}
}

func (h *Hub) remove(c *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, c)
}

func (h *Hub) readPump(c *websocket.Conn) {
	defer func() {
		h.remove(c)
		_ = c.Close()
	}()

	for {
		// We don't currently accept client messages; just keep connection alive.
		if _, _, err := c.ReadMessage(); err != nil {
			return
		}
	}
}

func (h *Hub) send(c *websocket.Conn, ev WsEvent) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	_ = c.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_ = c.WriteMessage(websocket.TextMessage, b)
}

func (h *Hub) broadcastEvent(ev WsEvent) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}

	h.mu.Lock()
	clients := make([]*websocket.Conn, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.Unlock()

	for _, c := range clients {
		_ = c.SetWriteDeadline(time.Now().Add(2 * time.Second))
		if err := c.WriteMessage(websocket.TextMessage, b); err != nil {
			h.remove(c)
			_ = c.Close()
		}
	}
}

package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hkjang/moina/backend/internal/model"
)

type notificationClient struct {
	queue chan model.Notification
}

type notificationHub struct {
	mu      sync.RWMutex
	clients map[string]map[*notificationClient]struct{}
}

func newNotificationHub() *notificationHub {
	return &notificationHub{clients: make(map[string]map[*notificationClient]struct{})}
}

func (h *notificationHub) add(userID string, client *notificationClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[userID] == nil {
		h.clients[userID] = make(map[*notificationClient]struct{})
	}
	h.clients[userID][client] = struct{}{}
}

func (h *notificationHub) remove(userID string, client *notificationClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients[userID], client)
	if len(h.clients[userID]) == 0 {
		delete(h.clients, userID)
	}
}

func (h *notificationHub) publish(userID string, notification model.Notification) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for client := range h.clients[userID] {
		select {
		case client.queue <- notification:
		default:
			// A slow tab must not block persistence or other connected sessions.
		}
	}
}

func (s *Server) notificationsWebSocket(w http.ResponseWriter, r *http.Request) {
	connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	defer connection.Close(websocket.StatusNormalClosure, "연결 종료")
	connection.SetReadLimit(8 << 10)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	client := &notificationClient{queue: make(chan model.Notification, 32)}
	userID := getPrincipal(r).User.ID
	s.hub.add(userID, client)
	defer s.hub.remove(userID, client)

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			if _, _, readErr := connection.Read(ctx); readErr != nil {
				return
			}
		}
	}()

	connected, _ := json.Marshal(map[string]any{"type": "connected", "createdAt": time.Now().UTC()})
	if err := connection.Write(ctx, websocket.MessageText, connected); err != nil {
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-readDone:
			return
		case item := <-client.queue:
			payload, marshalErr := json.Marshal(item)
			if marshalErr != nil || connection.Write(ctx, websocket.MessageText, payload) != nil {
				return
			}
		case <-ticker.C:
			pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
			err := connection.Ping(pingCtx)
			pingCancel()
			if err != nil {
				return
			}
		}
	}
}

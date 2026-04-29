package ws

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/coder/websocket"
)

type Hub struct {
	clients    map[*client]bool
	broadcast  chan []byte
	register   chan *client
	unregister chan *client
	mu         sync.Mutex
}

type client struct {
	hub  *Hub
	conn *websocket.Conn
}

func NewHub() *Hub {
	return &Hub{
		broadcast:  make(chan []byte),
		register:   make(chan *client),
		unregister: make(chan *client),
		clients:    make(map[*client]bool),
	}
}

func (h *Hub) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				client.conn.Close(websocket.StatusNormalClosure, "")
			}
			h.mu.Unlock()
		case message := <-h.broadcast:
			h.mu.Lock()
			for client := range h.clients {
				err := client.conn.Write(ctx, websocket.MessageText, message)
				if err != nil {
					log.Printf("websocket write error: %v", err)
					client.conn.Close(websocket.StatusInternalError, "")
					delete(h.clients, client)
				}
			}
			h.mu.Unlock()
		}
	}
}

var globalHub *Hub
var once sync.Once

func GetHub() *Hub {
	once.Do(func() {
		globalHub = NewHub()
		go globalHub.Run(context.Background())
	})
	return globalHub
}

func HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // In a real app, you might want to check Origin
	})
	if err != nil {
		log.Printf("websocket accept error: %v", err)
		return
	}

	hub := GetHub()
	cl := &client{hub: hub, conn: c}
	hub.register <- cl

	// Keep connection alive and handle disconnection
	ctx := r.Context()
	for {
		_, _, err := c.Read(ctx)
		if err != nil {
			hub.unregister <- cl
			break
		}
	}
}

type TaskUpdate struct {
	TaskID  string `json:"task_id"`
	Status  string `json:"status"`
	Percent int    `json:"percent"`
	Message string `json:"message,omitempty"`
}

func BroadcastTaskUpdate(taskID, status string, percent int, msg string) {
	update := TaskUpdate{
		TaskID:  taskID,
		Status:  status,
		Percent: percent,
		Message: msg,
	}
	data, err := json.Marshal(update)
	if err != nil {
		log.Printf("json marshal error: %v", err)
		return
	}
	select {
	case GetHub().broadcast <- data:
	default:
		// Hub bận hoặc không có client, drop update để không block goroutine upload
	}
}

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
	hub      *Hub
	conn     *websocket.Conn
	username string
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
			// message format expected to be handled elsewhere if needed, 
			// but we'll focus on the specific TaskUpdate for targeted broadcast
			h.mu.Lock()
			for client := range h.clients {
				client.conn.Write(ctx, websocket.MessageText, message)
			}
			h.mu.Unlock()
		}
	}
}

// BroadcastToUser sends a message only to clients belonging to a specific user.
func (h *Hub) BroadcastToUser(ctx context.Context, username string, message []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for client := range h.clients {
		if client.username == username {
			err := client.conn.Write(ctx, websocket.MessageText, message)
			if err != nil {
				log.Printf("websocket write error for user %s: %v", username, err)
				client.conn.Close(websocket.StatusInternalError, "")
				delete(h.clients, client)
			}
		}
	}
}

var globalHub *Hub
var once sync.Once

// InitHub initialises the singleton hub with the given context.
// Must be called once before any call to GetHub or HandleWebSocket.
// When ctx is cancelled (e.g. on graceful shutdown), the hub goroutine exits.
func InitHub(ctx context.Context) {
	once.Do(func() {
		globalHub = NewHub()
		go globalHub.Run(ctx)
	})
}

func GetHub() *Hub {
	// Fallback: if InitHub was never called, start with background context.
	once.Do(func() {
		globalHub = NewHub()
		go globalHub.Run(context.Background())
	})
	return globalHub
}

func HandleWebSocket(w http.ResponseWriter, r *http.Request, username string) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // In a real app, you might want to check Origin
	})
	if err != nil {
		log.Printf("websocket accept error: %v", err)
		return
	}

	hub := GetHub()
	cl := &client{hub: hub, conn: c, username: username}
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
	TaskID        string  `json:"task_id"`
	Status        string  `json:"status"`
	Phase         string  `json:"phase,omitempty"`
	Progress      float64 `json:"progress"`
	Percent       int     `json:"percent"`
	Message       string  `json:"message,omitempty"`
	Size          int64   `json:"size,omitempty"`
	UploadedBytes int64   `json:"uploaded_bytes,omitempty"`
	Speed         int64   `json:"speed,omitempty"`
	ETA           int     `json:"eta,omitempty"`
}

func BroadcastTaskUpdate(owner, taskID, status string, percent int, msg string, size int64, uploadedBytes int64, speed int64, eta int) {
	phase := status
	switch status {
	case "telegram":
		phase = "telegram_upload"
	case "downloading":
		phase = "remote_download"
	case "uploading_to_server":
		phase = "server_upload"
	}

	progress := float64(percent)
	if size > 0 {
		progress = (float64(uploadedBytes) / float64(size)) * 100
	}

	update := TaskUpdate{
		TaskID:        taskID,
		Status:        status,
		Phase:         phase,
		Progress:      progress,
		Percent:       percent,
		Message:       msg,
		Size:          size,
		UploadedBytes: uploadedBytes,
		Speed:         speed,
		ETA:           eta,
	}
	data, err := json.Marshal(update)
	if err != nil {
		log.Printf("json marshal error: %v", err)
		return
	}
	
	// If owner is empty, broadcast to everyone (fallback)
	if owner == "" {
		select {
		case GetHub().broadcast <- data:
		default:
		}
		return
	}

	// Targeted broadcast
	go GetHub().BroadcastToUser(context.Background(), owner, data)
}

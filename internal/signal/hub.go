package signal

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/centrifugal/centrifuge"
	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
)

var allowedOrigins = strings.Split(os.Getenv("ALLOWED_ORIGINS"), ",")
var jwtSecretKey = []byte(os.Getenv("JWT_SECRET_KEY"))

// validateJWT parses the token and extracts the user ID and role for a specific school.
func validateJWT(tokenString, schoolID string) (string, string, error) {
	if len(jwtSecretKey) == 0 {
		jwtSecretKey = []byte("mysupersecret_secretsecretsecretkey!123")
	}

	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return jwtSecretKey, nil
	})

	if err != nil || !token.Valid {
		return "", "", fmt.Errorf("invalid token: %v", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", "", fmt.Errorf("invalid token claims")
	}

	userIdVal, ok := claims["userId"].(string)
	if !ok {
		return "", "", fmt.Errorf("userId claim missing or invalid type")
	}

	role := "student"
	switch v := claims["school_role"].(type) {
	case string:
		if strings.HasPrefix(v, schoolID+":") {
			rolePart := strings.Split(v, ":")
			if len(rolePart) == 2 {
				role = strings.ToLower(rolePart[1])
			}
		}
	case []interface{}:
		for _, roleClaim := range v {
			if strClaim, ok := roleClaim.(string); ok {
				if strings.HasPrefix(strClaim, schoolID+":") {
					rolePart := strings.Split(strClaim, ":")
					if len(rolePart) == 2 {
						role = strings.ToLower(rolePart[1])
						break
					}
				}
			}
		}
	}

	if role == "teacher" || role == "owner" || role == "admin" {
		role = "admin"
	} else {
		role = "student"
	}

	return userIdVal, role, nil
}

// Hub manages all active rooms and the Centrifuge Node.
type Hub struct {
	Node             *centrifuge.Node
	Rooms            map[string]*Room
	Mu               sync.RWMutex
	Redis            *redis.Client
	DB               *sql.DB
	LiveKitURL       string
	LiveKitAPIKey    string
	LiveKitAPISecret string
	CoturnURL        string
	CoturnSecret     string
}

// NewHub initializes a new Hub and Centrifuge node.
func NewHub(rdb *redis.Client, db *sql.DB, lkURL, lkKey, lkSecret, coturnURL, coturnSecret string) (*Hub, error) {
	node, err := centrifuge.New(centrifuge.Config{})
	if err != nil {
		return nil, err
	}

	h := &Hub{
		Node:             node,
		Rooms:            make(map[string]*Room),
		Redis:            rdb,
		DB:               db,
		LiveKitURL:       lkURL,
		LiveKitAPIKey:    lkKey,
		LiveKitAPISecret: lkSecret,
		CoturnURL:        coturnURL,
		CoturnSecret:     coturnSecret,
	}

	node.OnConnecting(h.handleConnecting)
	node.OnConnect(h.handleConnect)

	return h, nil
}

// Run starts the underlying Centrifuge node.
func (h *Hub) Run() error {
	return h.Node.Run()
}

// Shutdown gracefully stops the Centrifuge node.
func (h *Hub) Shutdown(ctx context.Context) error {
	return h.Node.Shutdown(ctx)
}

// RemoveRoom safely deletes an empty room.
func (h *Hub) RemoveRoom(roomID string) {
	h.Mu.Lock()
	defer h.Mu.Unlock()
	
	// Check and save whiteboard before removing room
	ctx := context.Background()
	stateKey := fmt.Sprintf("room:%s:whiteboard", roomID)
	if val, err := h.Redis.Get(ctx, stateKey).Result(); err == nil && val != "" {
		if h.DB != nil {
			query := `
				INSERT INTO whiteboards (room_id, state) 
				VALUES ($1, $2) 
				ON CONFLICT (room_id) 
				DO UPDATE SET state = EXCLUDED.state, updated_at = CURRENT_TIMESTAMP
			`
			if _, dbErr := h.DB.Exec(query, roomID, val); dbErr != nil {
				log.Printf("Failed to save whiteboard to DB for room %s: %v", roomID, dbErr)
			} else {
				log.Printf("Successfully saved whiteboard to DB for room %s", roomID)
			}
		}
		// Clean up redis
		h.Redis.Del(ctx, stateKey)
	}

	delete(h.Rooms, roomID)
	log.Printf("Room %s removed from Hub.", roomID)
}

// WebsocketHandler returns the HTTP handler for the Centrifuge WebSocket transport.
func (h *Hub) WebsocketHandler() http.Handler {
	wsConfig := centrifuge.WebsocketConfig{
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if origin == "" {
				return true
			}
			if len(allowedOrigins) == 1 && allowedOrigins[0] == "" {
				return true
			}
			for _, allowed := range allowedOrigins {
				if origin == strings.TrimSpace(allowed) {
					return true
				}
			}
			log.Printf("Origin blocked: %s", origin)
			return false
		},
	}
	handler := centrifuge.NewWebsocketHandler(h.Node, wsConfig)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		ctx = context.WithValue(ctx, "roomId", r.URL.Query().Get("roomId"))
		ctx = context.WithValue(ctx, "schoolId", r.URL.Query().Get("schoolId"))
		ctx = context.WithValue(ctx, "sessionId", r.URL.Query().Get("sessionId"))
		ctx = context.WithValue(ctx, "token", r.URL.Query().Get("token"))
		
		handler.ServeHTTP(w, r.WithContext(ctx))
	})
}

// handleConnecting authenticates incoming connections.
func (h *Hub) handleConnecting(ctx context.Context, e centrifuge.ConnectEvent) (centrifuge.ConnectReply, error) {
	token, _ := ctx.Value("token").(string)
	if token == "" {
		token = e.Token
	}
	schoolID, _ := ctx.Value("schoolId").(string)
	roomID, _ := ctx.Value("roomId").(string)
	sessionID, _ := ctx.Value("sessionId").(string)

	if roomID == "" || sessionID == "" || schoolID == "" {
		return centrifuge.ConnectReply{}, centrifuge.ErrorBadRequest
	}

	clientID, role, err := validateJWT(token, schoolID)
	if err != nil {
		log.Printf("Authentication failed: %v", err)
		return centrifuge.ConnectReply{}, centrifuge.ErrorUnauthorized
	}

	infoBytes, _ := json.Marshal(map[string]string{
		"role":      role,
		"sessionId": sessionID,
		"roomId":    roomID,
	})

	return centrifuge.ConnectReply{
		Credentials: &centrifuge.Credentials{
			UserID: clientID,
			Info:   infoBytes,
		},
	}, nil
}

// handleConnect attaches callbacks for a successfully authenticated client.
func (h *Hub) handleConnect(client *centrifuge.Client) {
	var info map[string]string
	_ = json.Unmarshal(client.Info(), &info)

	role := info["role"]
	sessionID := info["sessionId"]
	roomID := info["roomId"]
	clientID := client.UserID()

	h.Mu.Lock()
	room, ok := h.Rooms[roomID]
	if !ok {
		room = &Room{
			ID:      roomID,
			Clients: make(map[string]*ClientState),
			Hub:     h,
			Mode:    "p2p",
		}
		if role == "admin" {
			room.AdminID = clientID
		}
		h.Rooms[roomID] = room
		log.Printf("Created new room: %s", roomID)
	}
	h.Mu.Unlock()

	room.Mu.Lock()
	state, exists := room.Clients[clientID]
	if exists && state.Disconnected && state.SessionID == sessionID {
		log.Printf("Client %s reconnecting to room %s", clientID, roomID)
		if state.Timer != nil {
			state.Timer.Stop()
		}
		state.Disconnected = false
		state.Client = client
		room.Mu.Unlock()
	} else if exists && !state.Disconnected {
		log.Printf("Client %s already connected and active. Rejecting new connection.", clientID)
		room.Mu.Unlock()
		client.Disconnect(centrifuge.DisconnectForceNoReconnect)
		return
	} else {
		log.Printf("Client %s joining room %s as %s", clientID, roomID, role)
		state = &ClientState{
			ID:        clientID,
			SessionID: sessionID,
			Role:      role,
			Client:    client,
		}
		room.Clients[clientID] = state
		if room.AdminID == "" && role == "admin" {
			room.AdminID = clientID
		}

		shouldShiftToSFU := false
		if room.Mode == "p2p" && len(room.Clients) >= 3 {
			room.Mode = "sfu"
			shouldShiftToSFU = true
		}
		isSFU := room.Mode == "sfu"

		room.Mu.Unlock()

		if shouldShiftToSFU {
			room.TriggerSFUShift()
		} else if isSFU {
			room.SendSFUToken(clientID, role)
		} else {
			room.SendTurnCredentials(clientID)
		}

		joinEnv := Envelope{
			Type:     "peer-joined",
			SenderID: clientID,
			RoomID:   roomID,
			Role:     role,
		}
		joinMsg, _ := json.Marshal(joinEnv)
		room.Broadcast(joinMsg, clientID)

		// Send current whiteboard state to newly joined client
		ctx := context.Background()
		stateKey := fmt.Sprintf("room:%s:whiteboard", roomID)
		val, err := h.Redis.Get(ctx, stateKey).Result()
		if err == nil && val != "" {
			stateEnv := Envelope{
				Type:     "whiteboard-state",
				SenderID: "server",
				RoomID:   roomID,
				Payload:  json.RawMessage(val),
			}
			stateMsg, _ := json.Marshal(stateEnv)
			_ = client.Send(stateMsg)
		}
	}

	client.OnMessage(func(e centrifuge.MessageEvent) {
		var env Envelope
		if err := json.Unmarshal(e.Data, &env); err != nil {
			log.Printf("Invalid JSON from %s: %v", clientID, err)
			return
		}
		env.SenderID = clientID
		env.RoomID = roomID
		room.ProcessMessage(clientID, env)
	})

	client.OnDisconnect(func(e centrifuge.DisconnectEvent) {
		room.HandleDisconnect(clientID)
	})
}
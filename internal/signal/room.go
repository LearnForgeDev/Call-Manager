package signal

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/centrifugal/centrifuge"
	"github.com/livekit/protocol/auth"
)

const reconnectGrace = 15 * time.Second

// ClientState holds the state of a connected user.
type ClientState struct {
	ID             string
	SessionID      string
	Role           string
	Client         *centrifuge.Client
	Disconnected   bool
	DisconnectTime time.Time
	Timer          *time.Timer
}

// Room manages a set of connected clients and floor control.
type Room struct {
	ID               string
	AdminID          string
	CurrentSpeakerID string
	Clients          map[string]*ClientState
	Mu               sync.RWMutex
	Hub              *Hub
	Mode             string // "p2p" or "sfu"
}

func (r *Room) generateLiveKitToken(clientID string, role string) (string, error) {
	at := auth.NewAccessToken(r.Hub.LiveKitAPIKey, r.Hub.LiveKitAPISecret)
	
	grant := &auth.VideoGrant{
		RoomJoin: true,
		Room:     r.ID,
	}
	if role == "admin" {
		grant.RoomAdmin = true
	}
	
	at.AddGrant(grant).
		SetIdentity(clientID).
		SetValidFor(time.Hour * 2)

	return at.ToJWT()
}

func (r *Room) TriggerSFUShift() {
	r.Mu.RLock()
	defer r.Mu.RUnlock()

	log.Printf("Room %s shifting to SFU mode", r.ID)
	for id, state := range r.Clients {
		if state.Disconnected || state.Client == nil {
			continue
		}
		token, err := r.generateLiveKitToken(id, state.Role)
		if err != nil {
			log.Printf("Failed to generate token for %s: %v", id, err)
			continue
		}

		payload, _ := json.Marshal(map[string]string{
			"livekitUrl": r.Hub.LiveKitURL,
			"livekitToken": token,
		})

		env := Envelope{
			Type:     "shift-to-sfu",
			SenderID: "server",
			RoomID:   r.ID,
			TargetID: id,
			Payload:  payload,
		}
		envMsg, _ := json.Marshal(env)
		state.Client.Send(envMsg)
	}
}

func (r *Room) SendSFUToken(clientID, role string) {
	r.Mu.RLock()
	defer r.Mu.RUnlock()
	
	state, ok := r.Clients[clientID]
	if !ok || state.Disconnected || state.Client == nil {
		return
	}
	
	token, err := r.generateLiveKitToken(clientID, role)
	if err != nil {
		log.Printf("Failed to generate token for %s: %v", clientID, err)
		return
	}

	payload, _ := json.Marshal(map[string]string{
		"livekitUrl": r.Hub.LiveKitURL,
		"livekitToken": token,
	})

	env := Envelope{
		Type:     "shift-to-sfu",
		SenderID: "server",
		RoomID:   r.ID,
		TargetID: clientID,
		Payload:  payload,
	}
	envMsg, _ := json.Marshal(env)
	state.Client.Send(envMsg)
}

func (r *Room) SendTurnCredentials(clientID string) {
	r.Mu.RLock()
	defer r.Mu.RUnlock()

	if r.Hub.CoturnURL == "" || r.Hub.CoturnSecret == "" {
		return
	}

	state, ok := r.Clients[clientID]
	if !ok || state.Disconnected || state.Client == nil {
		return
	}

	// Generate short-lived credential
	timestamp := time.Now().Add(24 * time.Hour).Unix()
	username := fmt.Sprintf("%d:%s", timestamp, clientID)

	mac := hmac.New(sha1.New, []byte(r.Hub.CoturnSecret))
	mac.Write([]byte(username))
	password := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	payload, _ := json.Marshal(map[string]interface{}{
		"iceServers": []map[string]interface{}{
			{
				"urls":       []string{r.Hub.CoturnURL},
				"username":   username,
				"credential": password,
			},
		},
	})

	env := Envelope{
		Type:     "ice-servers",
		SenderID: "server",
		RoomID:   r.ID,
		TargetID: clientID,
		Payload:  payload,
	}
	envMsg, _ := json.Marshal(env)
	state.Client.Send(envMsg)
}

// ProcessMessage handles an incoming envelope from a client.
func (r *Room) ProcessMessage(senderID string, env Envelope) {
	// Anti-Spoofing Logic
	if env.Type == "peer-joined" || env.Type == "peer-left" || env.Type == "error" {
		r.sendError(senderID, "Permission denied: Cannot spoof system events")
		return
	}

	// Floor Control Logic
	if env.TrackType == "screen" && (env.Type == "offer" || env.Type == "answer") {
		r.Mu.RLock()
		isAdmin := senderID == r.AdminID
		isSpeaker := senderID == r.CurrentSpeakerID
		r.Mu.RUnlock()

		if !isAdmin && !isSpeaker {
			r.sendError(senderID, "Permission denied: You are not the active speaker")
			return
		}
	}

	rawMsg, err := json.Marshal(env)
	if err != nil {
		log.Printf("Failed to marshal message: %v", err)
		return
	}

	// Routing Logic
	switch env.Type {
	case "whiteboard-update":
		ctx := context.Background()
		stateKey := fmt.Sprintf("room:%s:whiteboard", r.ID)
		if err := r.Hub.Redis.Set(ctx, stateKey, env.Payload, 0).Err(); err != nil {
			log.Printf("Failed to cache whiteboard state: %v", err)
		}
		r.Broadcast(rawMsg, senderID)
	case "request-whiteboard":
		ctx := context.Background()
		stateKey := fmt.Sprintf("room:%s:whiteboard", r.ID)
		if val, err := r.Hub.Redis.Get(ctx, stateKey).Result(); err == nil && val != "" {
			stateEnv := Envelope{
				Type:     "whiteboard-state",
				SenderID: "server",
				RoomID:   r.ID,
				TargetID: senderID,
				Payload:  json.RawMessage(val),
			}
			stateMsg, _ := json.Marshal(stateEnv)
			r.sendToClient(senderID, stateMsg)
		}
	case "offer", "answer", "ice":
		if env.TargetID != "" {
			r.sendToClient(env.TargetID, rawMsg)
		}
	case "peer-joined", "peer-left", "grant-speaker", "request-speaker", "revoke-speaker":
		if env.Type == "grant-speaker" {
			r.Mu.RLock()
			isAdmin := senderID == r.AdminID
			r.Mu.RUnlock()

			if isAdmin {
				r.Mu.Lock()
				r.CurrentSpeakerID = env.TargetID
				r.Mu.Unlock()
			}
		}
		
		r.Broadcast(rawMsg, senderID)
	}
}

// sendError sends an error message back to the target client.
func (r *Room) sendError(targetID string, errMsg string) {
	env := Envelope{
		Type:     "error",
		TargetID: targetID,
		SenderID: "server",
		RoomID:   r.ID,
		Payload:  json.RawMessage(`"` + errMsg + `"`),
	}
	raw, _ := json.Marshal(env)
	r.sendToClient(targetID, raw)
}

// sendToClient sends a raw message to a specific client.
func (r *Room) sendToClient(targetID string, msg []byte) {
	r.Mu.RLock()
	defer r.Mu.RUnlock()

	if state, ok := r.Clients[targetID]; ok && !state.Disconnected && state.Client != nil {
		err := state.Client.Send(msg)
		if err != nil {
			log.Printf("Failed to send message to %s: %v", targetID, err)
		}
	}
}

// Broadcast sends a raw message to all clients in the room except the excludeID.
func (r *Room) Broadcast(msg []byte, excludeID string) {
	r.Mu.RLock()
	defer r.Mu.RUnlock()

	for id, state := range r.Clients {
		if id == excludeID || state.Disconnected || state.Client == nil {
			continue
		}
		err := state.Client.Send(msg)
		if err != nil {
			log.Printf("Failed to broadcast message to %s: %v", id, err)
		}
	}
}

// HandleDisconnect manages connection resilience and cleanup timeouts.
func (r *Room) HandleDisconnect(clientID string) {
	r.Mu.Lock()
	defer r.Mu.Unlock()

	state, ok := r.Clients[clientID]
	if !ok || state.Disconnected {
		return
	}

	state.Disconnected = true
	state.DisconnectTime = time.Now()

	log.Printf("Client %s in room %s disconnected. Starting 15s grace period.", clientID, r.ID)

	state.Timer = time.AfterFunc(reconnectGrace, func() {
		r.Mu.Lock()
		defer r.Mu.Unlock()

		if state.Disconnected && time.Since(state.DisconnectTime) >= reconnectGrace {
			log.Printf("Client %s in room %s grace period expired. Removing.", clientID, r.ID)
			
			delete(r.Clients, clientID)

			leaveEnv := Envelope{
				Type:     "peer-left",
				SenderID: clientID,
				RoomID:   r.ID,
			}
			leaveMsg, _ := json.Marshal(leaveEnv)
			
			go r.Broadcast(leaveMsg, "")

			if len(r.Clients) == 0 {
				log.Printf("Room %s is empty. Scheduling for removal.", r.ID)
				go r.Hub.RemoveRoom(r.ID)
			}
		}
	})
}
// Package lgtv implementa un cliente mínimo del protocolo SSAP de las TVs LG webOS,
// suficiente para encender/apagar pantalla, apagar la TV y emparejar.
package lgtv

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/gorilla/websocket"

	"lgtv2pc/internal/config"
)

// Client controla una TV LG. No mantiene conexión persistente: cada acción
// abre un WebSocket, se registra con la client-key y envía el comando. Esto
// es más robusto frente a reconexiones y suficientemente rápido (<1s).
type Client struct {
	cfg *config.Config
	log *slog.Logger
}

// New crea un cliente.
func New(cfg *config.Config, log *slog.Logger) *Client {
	return &Client{cfg: cfg, log: log}
}

// ssapMessage es un mensaje del protocolo SSAP.
type ssapMessage struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"`
	URI     string          `json:"uri,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// dial abre el WebSocket hacia la TV.
func (c *Client) dial(ctx context.Context) (*websocket.Conn, error) {
	d := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := d.DialContext(ctx, c.cfg.WSURL(), nil)
	if err != nil {
		return nil, fmt.Errorf("conectando a %s: %w", c.cfg.WSURL(), err)
	}
	return conn, nil
}

// register realiza el handshake SSAP. Si key != "" intenta registro silencioso;
// si key == "" dispara el prompt de emparejamiento en la TV. Devuelve la
// client-key resultante (la misma que se pasó, o una nueva tras emparejar).
func (c *Client) register(ctx context.Context, conn *websocket.Conn, key string) (string, error) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(handshakePayload), &payload); err != nil {
		return "", fmt.Errorf("handshake interno inválido: %w", err)
	}
	if key != "" {
		payload["client-key"] = key
	}
	raw, _ := json.Marshal(payload)
	if err := conn.WriteJSON(ssapMessage{Type: "register", ID: "register_0", Payload: raw}); err != nil {
		return "", fmt.Errorf("enviando registro: %w", err)
	}

	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetReadDeadline(dl)
	}
	for {
		var msg ssapMessage
		if err := conn.ReadJSON(&msg); err != nil {
			return "", fmt.Errorf("leyendo respuesta de registro: %w", err)
		}
		switch msg.Type {
		case "registered":
			var p struct {
				ClientKey string `json:"client-key"`
			}
			_ = json.Unmarshal(msg.Payload, &p)
			if p.ClientKey == "" {
				p.ClientKey = key
			}
			return p.ClientKey, nil
		case "response":
			// Normalmente {"pairingType":"PROMPT"}: la TV muestra el diálogo.
			c.log.Info("acepta el emparejamiento en la pantalla de la TV…")
		case "error":
			return "", fmt.Errorf("la TV rechazó el registro: %s", msg.Error)
		}
	}
}

// command abre conexión, se registra con la client-key guardada y envía un
// request SSAP, esperando la respuesta.
func (c *Client) command(ctx context.Context, uri string, payload any) error {
	if c.cfg.ClientKey == "" {
		return fmt.Errorf("sin client-key: ejecuta `lgtv2pc -pair` primero")
	}
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := c.register(ctx, conn, c.cfg.ClientKey); err != nil {
		return err
	}

	var rawPayload json.RawMessage
	if payload != nil {
		rawPayload, _ = json.Marshal(payload)
	}
	if err := conn.WriteJSON(ssapMessage{Type: "request", ID: "cmd_0", URI: uri, Payload: rawPayload}); err != nil {
		return fmt.Errorf("enviando comando %s: %w", uri, err)
	}

	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetReadDeadline(dl)
	}
	for {
		var msg ssapMessage
		if err := conn.ReadJSON(&msg); err != nil {
			return fmt.Errorf("leyendo respuesta de %s: %w", uri, err)
		}
		if msg.ID != "cmd_0" {
			continue // ignora mensajes asíncronos no relacionados
		}
		if msg.Type == "error" {
			return fmt.Errorf("comando %s falló: %s", uri, msg.Error)
		}
		// Verifica returnValue si está presente.
		var p struct {
			ReturnValue *bool `json:"returnValue"`
		}
		_ = json.Unmarshal(msg.Payload, &p)
		if p.ReturnValue != nil && !*p.ReturnValue {
			return fmt.Errorf("comando %s devolvió returnValue=false: %s", uri, string(msg.Payload))
		}
		return nil
	}
}

// Pair dispara el emparejamiento y devuelve la nueva client-key.
func (c *Client) Pair(ctx context.Context) (string, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	return c.register(ctx, conn, c.cfg.ClientKey)
}

// TurnOff "suspende" la TV según el modo configurado.
func (c *Client) TurnOff(ctx context.Context) error {
	switch c.cfg.PowerMode {
	case config.ModeFull:
		c.log.Info("apagando TV (system/turnOff)")
		return c.command(ctx, "ssap://system/turnOff", nil)
	default: // ModeScreen
		c.log.Info("apagando panel de la TV (turnOffScreen)")
		// standbyMode "active" mantiene la TV accesible en la red (webOS 4+).
		return c.command(ctx, "ssap://com.webos.service.tvpower/power/turnOffScreen",
			map[string]string{"standbyMode": "active"})
	}
}

// TurnOn enciende la TV según el modo configurado.
func (c *Client) TurnOn(ctx context.Context) error {
	switch c.cfg.PowerMode {
	case config.ModeFull:
		c.log.Info("encendiendo TV (Wake-on-LAN)", "mac", c.cfg.TVMAC)
		return WakeOnLAN(c.cfg.TVMAC)
	default: // ModeScreen
		c.log.Info("encendiendo panel de la TV (turnOnScreen)")
		return c.command(ctx, "ssap://com.webos.service.tvpower/power/turnOnScreen",
			map[string]string{"standbyMode": "active"})
	}
}

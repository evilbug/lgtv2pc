// Package lgtv implementa un cliente mínimo del protocolo SSAP de las TVs LG
// webOS: encender/apagar pantalla, apagar la TV, consultar la entrada activa y
// emparejar.
package lgtv

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"lgtv2pc/internal/config"
)

// URIs SSAP usadas.
const (
	uriTurnOff       = "ssap://system/turnOff"
	uriScreenOff     = "ssap://com.webos.service.tvpower/power/turnOffScreen"
	uriScreenOn      = "ssap://com.webos.service.tvpower/power/turnOnScreen"
	uriForegroundApp = "ssap://com.webos.applicationManager/getForegroundAppInfo"
	uriSystemInfo    = "ssap://system/getSystemInfo"
	uriCreateToast   = "ssap://system.notifications/createToast"
)

// Client controla una TV LG. Cada acción abre un WebSocket, se registra con la
// client-key y envía uno o más comandos en esa misma sesión.
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

// dial abre el WebSocket hacia la TV. En wss se omite la verificación del
// certificado: las TVs LG presentan uno autofirmado y nos conectamos por IP.
func (c *Client) dial(ctx context.Context) (*websocket.Conn, error) {
	d := websocket.Dialer{
		HandshakeTimeout: 8 * time.Second,
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: true},
	}
	conn, _, err := d.DialContext(ctx, c.cfg.WSURL(), nil)
	if err != nil {
		return nil, fmt.Errorf("conectando a %s: %w", c.cfg.WSURL(), err)
	}
	return conn, nil
}

// register realiza el handshake SSAP. Si key != "" intenta registro silencioso;
// si key == "" dispara el prompt de emparejamiento en la TV. Devuelve la
// client-key resultante.
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
			c.log.Info("acepta el emparejamiento en la pantalla de la TV…")
		case "error":
			return "", fmt.Errorf("la TV rechazó el registro: %s", msg.Error)
		}
	}
}

// session es una conexión ya registrada sobre la que enviar requests.
type session struct {
	conn   *websocket.Conn
	nextID int
}

// openSession conecta y se registra con la client-key guardada.
func (c *Client) openSession(ctx context.Context) (*session, error) {
	if c.cfg.ClientKey == "" {
		return nil, fmt.Errorf("sin client-key: ejecuta `lgtv2pc -setup` primero")
	}
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := c.register(ctx, conn, c.cfg.ClientKey); err != nil {
		conn.Close()
		return nil, err
	}
	return &session{conn: conn}, nil
}

func (s *session) close() { _ = s.conn.Close() }

// request envía un request SSAP y espera su respuesta, devolviendo el payload.
func (s *session) request(ctx context.Context, uri string, payload any) (json.RawMessage, error) {
	s.nextID++
	id := fmt.Sprintf("cmd_%d", s.nextID)

	var raw json.RawMessage
	if payload != nil {
		raw, _ = json.Marshal(payload)
	}
	if err := s.conn.WriteJSON(ssapMessage{Type: "request", ID: id, URI: uri, Payload: raw}); err != nil {
		return nil, fmt.Errorf("enviando %s: %w", uri, err)
	}

	if dl, ok := ctx.Deadline(); ok {
		_ = s.conn.SetReadDeadline(dl)
	}
	for {
		var msg ssapMessage
		if err := s.conn.ReadJSON(&msg); err != nil {
			return nil, fmt.Errorf("leyendo respuesta de %s: %w", uri, err)
		}
		if msg.ID != id {
			continue // mensaje asíncrono no relacionado
		}
		if msg.Type == "error" {
			return nil, fmt.Errorf("%s falló: %s", uri, msg.Error)
		}
		var p struct {
			ReturnValue *bool `json:"returnValue"`
		}
		_ = json.Unmarshal(msg.Payload, &p)
		if p.ReturnValue != nil && !*p.ReturnValue {
			return nil, fmt.Errorf("%s devolvió returnValue=false: %s", uri, string(msg.Payload))
		}
		return msg.Payload, nil
	}
}

// foregroundApp devuelve el appId de la app/entrada en primer plano.
func (s *session) foregroundApp(ctx context.Context) (string, error) {
	raw, err := s.request(ctx, uriForegroundApp, nil)
	if err != nil {
		return "", err
	}
	var p struct {
		AppID string `json:"appId"`
	}
	_ = json.Unmarshal(raw, &p)
	return p.AppID, nil
}

// gatedOut indica si NO debemos actuar porque la TV está en otra entrada/app
// distinta a la configurada en hdmi_input. Si no hay restricción, nunca veta.
// Ante un error de consulta, no veta (se prefiere actuar).
func (c *Client) gatedOut(ctx context.Context, s *session) bool {
	want := c.cfg.HDMIAppID()
	if want == "" {
		return false
	}
	app, err := s.foregroundApp(ctx)
	if err != nil || app == "" {
		return false
	}
	if strings.EqualFold(app, want) {
		return false
	}
	c.log.Info("la TV está en otra entrada; no se envía el comando",
		"entrada_actual", app, "entrada_configurada", want)
	return true
}

// CurrentInput consulta el appId en primer plano (usado por el onboarding).
func (c *Client) CurrentInput(ctx context.Context) (string, error) {
	s, err := c.openSession(ctx)
	if err != nil {
		return "", err
	}
	defer s.close()
	return s.foregroundApp(ctx)
}

// ModelName devuelve el nombre de modelo de la TV (para identificarla en el onboarding).
func (c *Client) ModelName(ctx context.Context) (string, error) {
	s, err := c.openSession(ctx)
	if err != nil {
		return "", err
	}
	defer s.close()
	raw, err := s.request(ctx, uriSystemInfo, nil)
	if err != nil {
		return "", err
	}
	var p struct {
		ModelName string `json:"modelName"`
	}
	_ = json.Unmarshal(raw, &p)
	return p.ModelName, nil
}

// Toast muestra un aviso emergente en la TV (útil para confirmar cuál es).
func (c *Client) Toast(ctx context.Context, message string) error {
	s, err := c.openSession(ctx)
	if err != nil {
		return err
	}
	defer s.close()
	_, err = s.request(ctx, uriCreateToast, map[string]string{"message": message})
	return err
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

// TurnOff "suspende" la TV según el modo configurado, respetando el filtro HDMI.
func (c *Client) TurnOff(ctx context.Context) error {
	s, err := c.openSession(ctx)
	if err != nil {
		return err
	}
	defer s.close()
	if c.gatedOut(ctx, s) {
		return nil
	}
	if c.cfg.PowerMode == config.ModeFull {
		c.log.Info("apagando TV (system/turnOff)")
		_, err = s.request(ctx, uriTurnOff, nil)
		return err
	}
	c.log.Info("apagando panel de la TV (turnOffScreen)")
	_, err = s.request(ctx, uriScreenOff, map[string]string{"standbyMode": "active"})
	return err
}

// TurnOn enciende la TV según el modo configurado, respetando el filtro HDMI.
func (c *Client) TurnOn(ctx context.Context) error {
	if c.cfg.PowerMode == config.ModeFull {
		// La TV está apagada: no hay nada que interferir, se enciende con WoL.
		c.log.Info("encendiendo TV (Wake-on-LAN)", "mac", c.cfg.TVMAC)
		return WakeOnLAN(c.cfg.TVMAC)
	}
	s, err := c.openSession(ctx)
	if err != nil {
		return err
	}
	defer s.close()
	if c.gatedOut(ctx, s) {
		return nil
	}
	c.log.Info("encendiendo panel de la TV (turnOnScreen)")
	_, err = s.request(ctx, uriScreenOn, map[string]string{"standbyMode": "active"})
	return err
}

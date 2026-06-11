// Package config carga y persiste la configuración del servicio.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// PowerMode define cómo se apaga/enciende la TV.
type PowerMode string

const (
	// ModeScreen apaga solo el panel (turnOffScreen). La TV sigue en la red.
	ModeScreen PowerMode = "screen"
	// ModeFull apaga la TV por completo (system/turnOff) y la enciende con Wake-on-LAN.
	ModeFull PowerMode = "full"
)

// Config es la configuración del servicio.
type Config struct {
	// TVIP es la IP de la TV (ej: "192.168.1.50").
	TVIP string `json:"tv_ip"`
	// TVMAC es la MAC de la TV, necesaria para Wake-on-LAN en modo "full".
	TVMAC string `json:"tv_mac,omitempty"`
	// ClientKey es la clave de emparejamiento. Se rellena al ejecutar `lgtv2pc -pair`.
	ClientKey string `json:"client_key"`
	// PowerMode: "screen" (solo panel) o "full" (apagado total + WoL).
	PowerMode PowerMode `json:"power_mode"`
	// Secure usa wss://TV:3001 en lugar de ws://TV:3000.
	Secure bool `json:"secure"`
	// DoubleTapMS es la ventana máxima entre dos pulsaciones para contar como doble (ms).
	DoubleTapMS int `json:"double_tap_ms"`
	// SuspendKey es la tecla cuyo doble toque apaga la TV (nombre o keycode).
	SuspendKey string `json:"suspend_key"`
	// WakeKey es la tecla cuyo doble toque enciende la TV (nombre o keycode).
	WakeKey string `json:"wake_key"`

	// path es la ruta del archivo cargado, para poder reescribirlo.
	path string `json:"-"`
}

// Default devuelve una configuración con valores razonables.
func Default() *Config {
	return &Config{
		PowerMode:   ModeScreen,
		Secure:      false,
		DoubleTapMS: 400,
		SuspendKey:  "rightctrl",
		WakeKey:     "rightshift",
	}
}

// Load lee la config desde path. Si no existe, devuelve error.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("leyendo config %q: %w", path, err)
	}
	cfg := Default()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parseando config %q: %w", path, err)
	}
	cfg.path = path
	if cfg.TVIP == "" {
		return nil, fmt.Errorf("config %q: 'tv_ip' es obligatorio", path)
	}
	if cfg.PowerMode == ModeFull && cfg.TVMAC == "" {
		return nil, fmt.Errorf("config %q: power_mode 'full' requiere 'tv_mac' para Wake-on-LAN", path)
	}
	if cfg.DoubleTapMS <= 0 {
		cfg.DoubleTapMS = 400
	}
	if cfg.SuspendKey == "" {
		cfg.SuspendKey = "rightctrl"
	}
	if cfg.WakeKey == "" {
		cfg.WakeKey = "rightshift"
	}
	return cfg, nil
}

// Save escribe la configuración completa en su archivo de forma atómica.
func (c *Config) Save() error {
	if c.path == "" {
		return fmt.Errorf("config sin ruta asociada, no se puede guardar")
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, c.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// SaveClientKey persiste la client-key obtenida en el emparejamiento.
func (c *Config) SaveClientKey(key string) error {
	c.ClientKey = key
	return c.Save()
}

// Path devuelve la ruta del archivo de config asociado.
func (c *Config) Path() string { return c.path }

// WSURL devuelve la URL del WebSocket SSAP de la TV.
func (c *Config) WSURL() string {
	if c.Secure {
		return fmt.Sprintf("wss://%s:3001", c.TVIP)
	}
	return fmt.Sprintf("ws://%s:3000", c.TVIP)
}

// SetPath fija la ruta (usado al crear una config nueva para -pair).
func (c *Config) SetPath(p string) { c.path = filepath.Clean(p) }

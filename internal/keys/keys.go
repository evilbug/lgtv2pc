// Package keys detecta dobles pulsaciones globales de teclas configurables,
// leyendo los dispositivos evdev (/dev/input/event*). Funciona tanto en X11
// como en Wayland porque opera a nivel de kernel. Requiere permisos de lectura
// sobre /dev/input (el servicio corre como root).
//
// Importante: leer evdev NO consume la pulsación; el SO la sigue recibiendo.
// Por eso conviene usar teclas que el escritorio no tenga asociadas (p.ej.
// modificadores derechos pulsados solos, o F13-F24). Además, el doble toque
// solo cuenta si no se pulsó otra tecla en medio, evitando falsos positivos
// como Ctrl+C seguido de Ctrl.
package keys

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Códigos de evdev (linux/input-event-codes.h).
const (
	evKey       = 0x01 // EV_KEY
	valKeyDown  = 1    // pulsación (no autorepeat=2, no suelta=0)
	eventSize   = 24   // sizeof(struct input_event) en amd64
	devInputDir = "/dev/input"
)

// keyNames mapea nombres legibles a keycodes evdev. Pensado para teclas que el
// SO no usa por defecto, pero incluye las habituales por flexibilidad.
var keyNames = map[string]uint16{
	"rightctrl":  97,
	"leftctrl":   29,
	"rightshift": 54,
	"leftshift":  42,
	"rightalt":   100, // AltGr en algunas distribuciones de teclado
	"leftalt":    56,
	"rightmeta":  126,
	"leftmeta":   125,
	"rightsuper": 126,
	"leftsuper":  125,
	"menu":       127, // tecla de menú contextual (compose)
	"compose":    127,
	"scrolllock": 70,
	"pause":      119,
	"capslock":   58,
	"numlock":    69,
	"esc":        1,
	"escape":     1,
	"enter":      28,
	"kpenter":    96,
	"space":      57,
	"f13":        183, "f14": 184, "f15": 185, "f16": 186,
	"f17": 187, "f18": 188, "f19": 189, "f20": 190,
	"f21": 191, "f22": 192, "f23": 193, "f24": 194,
}

// ParseKey traduce un nombre ("rightctrl") o un código numérico ("97", "0x61")
// a un keycode evdev.
func ParseKey(s string) (uint16, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0, fmt.Errorf("tecla vacía")
	}
	if code, ok := keyNames[s]; ok {
		return code, nil
	}
	// Permite código numérico decimal o hexadecimal.
	if n, err := strconv.ParseUint(strings.TrimPrefix(s, "0x"), pickBase(s), 16); err == nil && n > 0 && n < 0xffff {
		return uint16(n), nil
	}
	return 0, fmt.Errorf("tecla desconocida %q (usa un nombre conocido o un keycode numérico)", s)
}

func pickBase(s string) int {
	if strings.HasPrefix(s, "0x") {
		return 16
	}
	return 10
}

// Handlers se invocan ante una doble pulsación de la tecla correspondiente.
type Handlers struct {
	// OnSuspend: doble pulsación de la tecla de suspensión.
	OnSuspend func(ctx context.Context)
	// OnWake: doble pulsación de la tecla de encendido.
	OnWake func(ctx context.Context)
}

// Watcher vigila los teclados.
type Watcher struct {
	log         *slog.Logger
	window      time.Duration
	suspendCode uint16
	wakeCode    uint16
	h           Handlers

	mu     sync.Mutex
	open   map[string]bool // dispositivos ya abiertos
	events chan uint16     // keycodes de pulsaciones (key down)
}

// New crea el watcher. window es la ventana máxima entre las dos pulsaciones.
func New(log *slog.Logger, window time.Duration, suspendCode, wakeCode uint16, h Handlers) *Watcher {
	return &Watcher{
		log:         log,
		window:      window,
		suspendCode: suspendCode,
		wakeCode:    wakeCode,
		h:           h,
		open:        make(map[string]bool),
		events:      make(chan uint16, 128),
	}
}

// Run abre los teclados, re-escanea periódicamente por dispositivos nuevos
// (hotplug) y procesa las dobles pulsaciones hasta que ctx se cancele.
func (w *Watcher) Run(ctx context.Context) error {
	w.scan(ctx)

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// pendingCode/pendingTime guardan la primera pulsación de una posible doble.
	// Cualquier tecla distinta intercalada cancela la secuencia (anti-interferencia).
	var pendingCode uint16
	var pendingTime time.Time

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			w.scan(ctx) // detecta teclados conectados en caliente
		case code := <-w.events:
			now := time.Now()
			isTarget := code == w.suspendCode || code == w.wakeCode

			switch {
			case code == pendingCode && !pendingTime.IsZero() && now.Sub(pendingTime) <= w.window:
				// Segundo toque de la misma tecla objetivo, sin nada en medio.
				pendingCode, pendingTime = 0, time.Time{}
				w.fire(ctx, code)
			case isTarget:
				// Primer toque (o reinicio de la ventana).
				pendingCode, pendingTime = code, now
			default:
				// Otra tecla: rompe cualquier secuencia pendiente.
				pendingCode, pendingTime = 0, time.Time{}
			}
		}
	}
}

func (w *Watcher) fire(ctx context.Context, code uint16) {
	switch code {
	case w.suspendCode:
		w.log.Info("doble pulsación de suspensión detectada")
		if w.h.OnSuspend != nil {
			w.h.OnSuspend(ctx)
		}
	case w.wakeCode:
		w.log.Info("doble pulsación de encendido detectada")
		if w.h.OnWake != nil {
			w.h.OnWake(ctx)
		}
	}
}

// scan abre cualquier /dev/input/event* que aún no esté abierto.
func (w *Watcher) scan(ctx context.Context) {
	matches, err := filepath.Glob(filepath.Join(devInputDir, "event*"))
	if err != nil {
		return
	}
	for _, path := range matches {
		w.mu.Lock()
		already := w.open[path]
		w.mu.Unlock()
		if already {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			continue // sin permisos o desaparecido; se reintenta en el próximo scan
		}
		w.mu.Lock()
		w.open[path] = true
		w.mu.Unlock()
		go w.readDevice(ctx, path, f)
	}
}

// readDevice lee un dispositivo y reenvía todas las pulsaciones (key down).
// Se reenvía todo (no solo las teclas objetivo) para poder detectar teclas
// intercaladas y cancelar la secuencia.
func (w *Watcher) readDevice(ctx context.Context, path string, f *os.File) {
	defer func() {
		f.Close()
		w.mu.Lock()
		delete(w.open, path)
		w.mu.Unlock()
	}()

	buf := make([]byte, eventSize)
	for {
		if ctx.Err() != nil {
			return
		}
		if _, err := io.ReadFull(f, buf); err != nil {
			return // dispositivo desconectado; el scan lo reabrirá si vuelve
		}
		typ := binary.LittleEndian.Uint16(buf[16:18])
		code := binary.LittleEndian.Uint16(buf[18:20])
		value := int32(binary.LittleEndian.Uint32(buf[20:24]))

		if typ != evKey || value != valKeyDown {
			continue
		}
		select {
		case w.events <- code:
		default: // canal lleno: descarta, no bloquea la lectura
		}
	}
}

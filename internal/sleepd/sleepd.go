// Package sleepd observa eventos de systemd-logind por D-Bus:
//   - PrepareForSleep: el sistema va a suspenderse / acaba de resumir.
//   - Bloqueo/desbloqueo de sesión: detectado por dos vías complementarias para
//     cubrir tanto KDE como GNOME:
//     a) las señales Lock/Unlock (las dispara p.ej. `loginctl lock-session`);
//     b) los cambios de la propiedad LockedHint de la sesión (lo que actualizan
//     KDE y GNOME al bloquear/desbloquear manualmente — la vía habitual).
package sleepd

import (
	"context"
	"fmt"
	"log/slog"
	"syscall"

	"github.com/godbus/dbus/v5"
)

// Handlers son las funciones invocadas ante cada evento. Cualquiera puede ser nil.
type Handlers struct {
	// OnSleep se llama ANTES de suspender, mientras aún hay red (inhibidor activo).
	OnSleep func(ctx context.Context)
	// OnResume se llama al volver de la suspensión.
	OnResume func(ctx context.Context)
	// OnLock se llama cuando la sesión pasa a bloqueada.
	OnLock func(ctx context.Context)
	// OnUnlock se llama cuando la sesión pasa a desbloqueada.
	OnUnlock func(ctx context.Context)
}

const (
	loginService   = "org.freedesktop.login1"
	loginPath      = "/org/freedesktop/login1"
	sessionPathNS  = "/org/freedesktop/login1/session"
	mgrIface       = "org.freedesktop.login1.Manager"
	sessIface      = "org.freedesktop.login1.Session"
	propsIface     = "org.freedesktop.DBus.Properties"
	lockedHintProp = "LockedHint"
)

// Watcher escucha los eventos de logind.
type Watcher struct {
	conn      *dbus.Conn
	log       *slog.Logger
	handlers  Handlers
	inhibitFD int

	// lockedSessions registra qué sesiones están bloqueadas. El estado agregado
	// "hay bloqueo" es len()>0; así varias sesiones (o eventos duplicados por las
	// dos vías) no se contradicen. Solo se toca desde el goroutine de Run.
	lockedSessions map[dbus.ObjectPath]bool
}

// New conecta al bus del sistema y registra los matches de señales.
func New(log *slog.Logger, h Handlers) (*Watcher, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("conectando al bus del sistema: %w", err)
	}
	w := &Watcher{
		conn:           conn,
		log:            log,
		handlers:       h,
		inhibitFD:      -1,
		lockedSessions: make(map[dbus.ObjectPath]bool),
	}

	// PrepareForSleep en el Manager.
	if err := conn.AddMatchSignal(
		dbus.WithMatchInterface(mgrIface),
		dbus.WithMatchMember("PrepareForSleep"),
	); err != nil {
		conn.Close()
		return nil, fmt.Errorf("match PrepareForSleep: %w", err)
	}
	// Lock / Unlock en cualquier sesión.
	for _, member := range []string{"Lock", "Unlock"} {
		if err := conn.AddMatchSignal(
			dbus.WithMatchInterface(sessIface),
			dbus.WithMatchMember(member),
			dbus.WithMatchPathNamespace(sessionPathNS),
		); err != nil {
			conn.Close()
			return nil, fmt.Errorf("match %s: %w", member, err)
		}
	}
	// PropertiesChanged de los objetos sesión (para captar LockedHint).
	if err := conn.AddMatchSignal(
		dbus.WithMatchInterface(propsIface),
		dbus.WithMatchMember("PropertiesChanged"),
		dbus.WithMatchPathNamespace(sessionPathNS),
	); err != nil {
		conn.Close()
		return nil, fmt.Errorf("match PropertiesChanged: %w", err)
	}
	return w, nil
}

// takeInhibitor toma un bloqueo "delay" sobre sleep para tener una ventana
// (por defecto hasta InhibitDelayMaxSec) antes de que el equipo se suspenda.
func (w *Watcher) takeInhibitor() {
	if w.inhibitFD >= 0 {
		return
	}
	obj := w.conn.Object(loginService, dbus.ObjectPath(loginPath))
	var fd dbus.UnixFD
	err := obj.Call(mgrIface+".Inhibit", 0,
		"sleep", "lgtv2pc", "Apagar la TV antes de suspender", "delay").Store(&fd)
	if err != nil {
		w.log.Warn("no se pudo tomar inhibidor de sleep (la TV podría no apagarse a tiempo)", "err", err)
		return
	}
	w.inhibitFD = int(fd)
}

// releaseInhibitor libera el bloqueo, permitiendo que el sistema se suspenda.
func (w *Watcher) releaseInhibitor() {
	if w.inhibitFD >= 0 {
		_ = syscall.Close(w.inhibitFD)
		w.inhibitFD = -1
	}
}

// Run procesa señales hasta que ctx se cancele.
func (w *Watcher) Run(ctx context.Context) error {
	defer w.conn.Close()
	defer w.releaseInhibitor()

	w.takeInhibitor()

	ch := make(chan *dbus.Signal, 16)
	w.conn.Signal(ch)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case sig := <-ch:
			if sig == nil {
				return fmt.Errorf("canal de señales D-Bus cerrado")
			}
			w.dispatch(ctx, sig)
		}
	}
}

func (w *Watcher) dispatch(ctx context.Context, sig *dbus.Signal) {
	switch sig.Name {
	case mgrIface + ".PrepareForSleep":
		if len(sig.Body) < 1 {
			return
		}
		going, _ := sig.Body[0].(bool)
		if going {
			w.log.Info("el sistema va a suspenderse")
			if w.handlers.OnSleep != nil {
				w.handlers.OnSleep(ctx)
			}
			// Soltamos el inhibidor: ya hicimos nuestro trabajo, que duerma.
			w.releaseInhibitor()
		} else {
			w.log.Info("el sistema resumió")
			// Volvemos a armar el inhibidor para la próxima suspensión.
			w.takeInhibitor()
			if w.handlers.OnResume != nil {
				w.handlers.OnResume(ctx)
			}
		}

	case sessIface + ".Lock":
		w.setSessionLocked(ctx, sig.Path, true)
	case sessIface + ".Unlock":
		w.setSessionLocked(ctx, sig.Path, false)

	case propsIface + ".PropertiesChanged":
		// Body: (string interface, map[string]Variant changed, []string invalidated)
		if len(sig.Body) < 3 {
			return
		}
		iface, _ := sig.Body[0].(string)
		if iface != sessIface {
			return
		}
		if changed, ok := sig.Body[1].(map[string]dbus.Variant); ok {
			if v, ok := changed[lockedHintProp]; ok {
				if b, ok := v.Value().(bool); ok {
					w.setSessionLocked(ctx, sig.Path, b)
				}
				return
			}
		}
		// Algunas propiedades llegan como "invalidadas": hay que consultarlas.
		if inval, ok := sig.Body[2].([]string); ok {
			for _, name := range inval {
				if name == lockedHintProp {
					w.setSessionLocked(ctx, sig.Path, w.lockedHint(sig.Path))
				}
			}
		}
	}
}

// setSessionLocked actualiza el estado de una sesión y dispara los handlers
// solo en la transición del estado agregado (alguna sesión bloqueada o ninguna).
func (w *Watcher) setSessionLocked(ctx context.Context, path dbus.ObjectPath, locked bool) {
	wasLocked := len(w.lockedSessions) > 0
	if locked {
		w.lockedSessions[path] = true
	} else {
		delete(w.lockedSessions, path)
	}
	nowLocked := len(w.lockedSessions) > 0
	if nowLocked == wasLocked {
		return
	}
	if nowLocked {
		w.log.Info("sesión bloqueada", "session", path)
		if w.handlers.OnLock != nil {
			w.handlers.OnLock(ctx)
		}
	} else {
		w.log.Info("sesión desbloqueada", "session", path)
		if w.handlers.OnUnlock != nil {
			w.handlers.OnUnlock(ctx)
		}
	}
}

// lockedHint consulta la propiedad LockedHint de una sesión.
func (w *Watcher) lockedHint(path dbus.ObjectPath) bool {
	obj := w.conn.Object(loginService, path)
	v, err := obj.GetProperty(sessIface + "." + lockedHintProp)
	if err != nil {
		w.log.Warn("no se pudo leer LockedHint", "session", path, "err", err)
		return false
	}
	b, _ := v.Value().(bool)
	return b
}

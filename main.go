// lgtv2pc es un servicio en segundo plano que sincroniza el estado de una TV
// LG (webOS) con el de un PC Linux:
//   - al suspender el PC, apaga la pantalla de la TV (antes de dormir);
//   - al resumir, la enciende (si la sesión no está bloqueada);
//   - al bloquear la sesión (lockscreen), apaga la pantalla; al desbloquear, la enciende;
//   - con el PC activo: doble Escape apaga la TV, doble Enter la enciende.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"lgtv2pc/internal/config"
	"lgtv2pc/internal/keys"
	"lgtv2pc/internal/lgtv"
	"lgtv2pc/internal/sleepd"
)

const defaultConfigPath = "/etc/lgtv2pc/config.json"

// cmdTimeout limita cada acción contra la TV.
const cmdTimeout = 6 * time.Second

func main() {
	var (
		configPath = flag.String("config", defaultConfigPath, "ruta del archivo de configuración")
		setup      = flag.Bool("setup", false, "onboarding interactivo: localiza la TV, empareja y crea la config")
		pair       = flag.Bool("pair", false, "re-empareja con la TV y guarda la client-key (config ya existente)")
		disco      = flag.Bool("discover", false, "lista las TVs encontradas en la red y sale (diagnóstico)")
	)
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if *disco {
		runDiscover()
		return
	}

	if *setup {
		if err := runSetup(log, *configPath); err != nil {
			log.Error("onboarding fallido", "err", err)
			os.Exit(1)
		}
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Error("error de configuración", "err", err)
		os.Exit(1)
	}

	tv := lgtv.New(cfg, log)

	if *pair {
		runPair(log, cfg, tv)
		return
	}

	if cfg.ClientKey == "" {
		log.Error("no hay client-key; ejecuta primero: lgtv2pc -pair -config " + *configPath)
		os.Exit(1)
	}

	if err := runDaemon(log, cfg, tv); err != nil {
		log.Error("el servicio terminó con error", "err", err)
		os.Exit(1)
	}
}

// runPair dispara el emparejamiento (la TV muestra un prompt) y persiste la clave.
func runPair(log *slog.Logger, cfg *config.Config, tv *lgtv.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	log.Info("emparejando con la TV; acepta el diálogo en la pantalla…", "tv", cfg.TVIP)
	key, err := tv.Pair(ctx)
	if err != nil {
		log.Error("emparejamiento fallido", "err", err)
		os.Exit(1)
	}
	if err := cfg.SaveClientKey(key); err != nil {
		log.Error("no se pudo guardar la client-key", "err", err)
		fmt.Fprintf(os.Stderr, "\nclient-key obtenida (guárdala manualmente en client_key):\n%s\n", key)
		os.Exit(1)
	}
	log.Info("emparejamiento correcto; client-key guardada")
}

// runDaemon arranca los watchers y bloquea hasta recibir SIGINT/SIGTERM.
func runDaemon(log *slog.Logger, cfg *config.Config, tv *lgtv.Client) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ctrl := newController(log, tv)

	watcher, err := sleepd.New(log, sleepd.Handlers{
		OnSleep:  func(ctx context.Context) { ctrl.setAsleep(true) },
		OnResume: func(ctx context.Context) { ctrl.setAsleep(false) },
		OnLock:   func(ctx context.Context) { ctrl.setLocked(true) },
		OnUnlock: func(ctx context.Context) { ctrl.setLocked(false) },
	})
	if err != nil {
		return err
	}

	suspendCode, err := keys.ParseKey(cfg.SuspendKey)
	if err != nil {
		return fmt.Errorf("suspend_key: %w", err)
	}
	wakeCode, err := keys.ParseKey(cfg.WakeKey)
	if err != nil {
		return fmt.Errorf("wake_key: %w", err)
	}
	kw := keys.New(log, time.Duration(cfg.DoubleTapMS)*time.Millisecond, suspendCode, wakeCode, keys.Handlers{
		OnSuspend: func(ctx context.Context) { ctrl.manualOff() },
		OnWake:    func(ctx context.Context) { ctrl.manualOn() },
	})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := watcher.Run(ctx); err != nil && ctx.Err() == nil {
			log.Error("watcher de logind terminó", "err", err)
			stop()
		}
	}()
	go func() {
		defer wg.Done()
		if err := kw.Run(ctx); err != nil && ctx.Err() == nil {
			log.Error("watcher de teclado terminó", "err", err)
			stop()
		}
	}()

	log.Info("lgtv2pc en marcha", "tv", cfg.TVIP, "modo", cfg.PowerMode,
		"suspend_key", cfg.SuspendKey, "wake_key", cfg.WakeKey)
	<-ctx.Done()
	log.Info("apagando lgtv2pc…")
	wg.Wait()
	return nil
}

// controller decide el estado deseado de la TV a partir de las condiciones
// automáticas (suspensión y bloqueo de sesión) y aplica cambios solo en las
// transiciones. La TV debe estar ENCENDIDA solo si el equipo está despierto Y
// la sesión desbloqueada. Las dobles pulsaciones son comandos manuales directos
// (solo ocurren con el equipo activo y desbloqueado, así que no entran en
// conflicto con la lógica automática).
type controller struct {
	log *slog.Logger
	tv  *lgtv.Client

	mu     sync.Mutex
	asleep bool
	locked bool
	tvOn   bool // último estado aplicado
	inited bool
}

func newController(log *slog.Logger, tv *lgtv.Client) *controller {
	return &controller{log: log, tv: tv, tvOn: true}
}

func (c *controller) setAsleep(v bool) {
	c.mu.Lock()
	c.asleep = v
	c.mu.Unlock()
	c.apply()
}

func (c *controller) setLocked(v bool) {
	c.mu.Lock()
	c.locked = v
	c.mu.Unlock()
	c.apply()
}

// apply calcula el estado deseado y lo aplica si cambió.
func (c *controller) apply() {
	c.mu.Lock()
	desiredOn := !c.asleep && !c.locked
	if c.inited && desiredOn == c.tvOn {
		c.mu.Unlock()
		return
	}
	c.inited = true
	c.tvOn = desiredOn
	c.mu.Unlock()

	if desiredOn {
		c.do("encender", c.tv.TurnOn)
	} else {
		c.do("apagar", c.tv.TurnOff)
	}
}

func (c *controller) manualOff() {
	c.mu.Lock()
	c.tvOn = false
	c.mu.Unlock()
	c.do("apagar (manual)", c.tv.TurnOff)
}

func (c *controller) manualOn() {
	c.mu.Lock()
	c.tvOn = true
	c.mu.Unlock()
	c.do("encender (manual)", c.tv.TurnOn)
}

func (c *controller) do(what string, fn func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	if err := fn(ctx); err != nil {
		c.log.Error("acción sobre la TV falló", "accion", what, "err", err)
	}
}

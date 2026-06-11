package main

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"lgtv2pc/internal/config"
	"lgtv2pc/internal/discover"
	"lgtv2pc/internal/keys"
	"lgtv2pc/internal/lgtv"
)

// runSetup guía el primer arranque: localiza la TV, empareja (auth), detecta la
// MAC y escribe el config.json. Es interactivo (lee de stdin).
func runSetup(log *slog.Logger, configPath string) error {
	in := bufio.NewReader(os.Stdin)

	fmt.Println("== Configuración de lgtv2pc ==")
	fmt.Println("Asegúrate de que la TV está ENCENDIDA y en la misma red.")
	fmt.Println()

	// Parte de una config existente si la hay, para no perder ajustes.
	cfg, err := config.Load(configPath)
	if err != nil {
		cfg = config.Default()
		cfg.SetPath(configPath)
	} else {
		fmt.Printf("Se encontró configuración previa en %s; se actualizará.\n\n", configPath)
	}

	// 1) IP de la TV: descubrimiento por SSDP + opción manual.
	ip, err := chooseTVIP(in)
	if err != nil {
		return err
	}
	cfg.TVIP = ip

	// 2) Modo de apagado.
	cfg.PowerMode = choosePowerMode(in, cfg.PowerMode)

	// 3) Emparejamiento (auth) con la TV.
	tv := lgtv.New(cfg, log)
	fmt.Println()
	fmt.Println("Conectando con la TV para emparejar…")
	fmt.Println(">> MIRA LA PANTALLA DE LA TV y acepta la solicitud de conexión.")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	key, err := tv.Pair(ctx)
	if err != nil {
		return fmt.Errorf("emparejamiento fallido: %w", err)
	}
	cfg.ClientKey = key
	fmt.Println("✓ Emparejado correctamente.")

	// 4) MAC para Wake-on-LAN (necesaria en modo full; útil guardarla igual).
	if mac := discover.MACForIP(ip); mac != "" {
		cfg.TVMAC = mac
		fmt.Printf("✓ MAC detectada automáticamente: %s\n", mac)
	} else if cfg.PowerMode == config.ModeFull {
		cfg.TVMAC = prompt(in, "No se pudo detectar la MAC. Introdúcela (AA:BB:CC:DD:EE:FF)", cfg.TVMAC)
	} else {
		fmt.Println("No se detectó la MAC (no hace falta en modo screen).")
	}

	// 5) Teclas (se aceptan los valores por defecto con Enter).
	cfg.SuspendKey = chooseKey(in, "Tecla para APAGAR la TV (doble toque)", cfg.SuspendKey)
	cfg.WakeKey = chooseKey(in, "Tecla para ENCENDER la TV (doble toque)", cfg.WakeKey)

	// 6) Guardar.
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("guardando %s: %w", configPath, err)
	}

	fmt.Println()
	fmt.Printf("✓ Configuración guardada en %s\n", configPath)
	fmt.Println()
	fmt.Println("Para arrancar el servicio:")
	fmt.Println("  sudo systemctl enable --now lgtv2pc")
	fmt.Println("  journalctl -u lgtv2pc -f")
	return nil
}

// chooseTVIP descubre TVs por SSDP y deja elegir, con opción de IP manual.
func chooseTVIP(in *bufio.Reader) (string, error) {
	fmt.Println("Buscando TVs LG en la red (SSDP)…")
	tvs, _ := discover.Discover(4 * time.Second)

	if len(tvs) == 0 {
		fmt.Println("No se encontró ninguna TV automáticamente.")
		ip := prompt(in, "Introduce la IP de la TV", "")
		if ip == "" {
			return "", fmt.Errorf("se necesita una IP de la TV")
		}
		return ip, nil
	}

	fmt.Printf("\nTVs encontradas:\n")
	for i, tv := range tvs {
		desc := tv.Server
		if desc == "" {
			desc = "(LG webOS)"
		}
		fmt.Printf("  [%d] %s  %s\n", i+1, tv.IP, desc)
	}
	fmt.Printf("  [m] introducir IP manualmente\n")

	for {
		ans := prompt(in, "Elige una opción", "1")
		if strings.EqualFold(ans, "m") {
			ip := prompt(in, "Introduce la IP de la TV", "")
			if ip != "" {
				return ip, nil
			}
			continue
		}
		var idx int
		if _, err := fmt.Sscanf(ans, "%d", &idx); err == nil && idx >= 1 && idx <= len(tvs) {
			return tvs[idx-1].IP, nil
		}
		fmt.Println("Opción no válida.")
	}
}

// choosePowerMode pregunta el modo de apagado.
func choosePowerMode(in *bufio.Reader, current config.PowerMode) config.PowerMode {
	fmt.Println()
	fmt.Println("Modo de apagado:")
	fmt.Println("  [1] screen  – apaga solo el panel; la TV sigue en red (recomendado)")
	fmt.Println("  [2] full    – apaga la TV del todo y la enciende con Wake-on-LAN")
	def := "1"
	if current == config.ModeFull {
		def = "2"
	}
	ans := prompt(in, "Elige modo", def)
	if strings.HasPrefix(ans, "2") || strings.EqualFold(ans, "full") {
		return config.ModeFull
	}
	return config.ModeScreen
}

// chooseKey pide una tecla validando con keys.ParseKey; reintenta si es inválida.
func chooseKey(in *bufio.Reader, question, def string) string {
	for {
		ans := prompt(in, question, def)
		if _, err := keys.ParseKey(ans); err != nil {
			fmt.Printf("  %v\n", err)
			continue
		}
		return ans
	}
}

// prompt muestra una pregunta con valor por defecto y devuelve la respuesta.
func prompt(in *bufio.Reader, question, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", question, def)
	} else {
		fmt.Printf("%s: ", question)
	}
	line, _ := in.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

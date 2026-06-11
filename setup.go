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
// MAC, ofrece fijar la entrada HDMI y escribe el config.json. Es interactivo.
func runSetup(log *slog.Logger, configPath string) error {
	in := bufio.NewReader(os.Stdin)

	fmt.Println("== Configuración de lgtv2pc ==")
	fmt.Println("Asegúrate de que la TV está ENCENDIDA y en la misma red.")
	fmt.Println()

	cfg, err := config.Load(configPath)
	if err != nil {
		cfg = config.Default()
		cfg.SetPath(configPath)
	} else {
		fmt.Printf("Se encontró configuración previa en %s; se actualizará.\n\n", configPath)
	}

	// 1) Localizar la TV (SSDP + escaneo de puertos) y elegir una.
	tv, err := chooseTV(in)
	if err != nil {
		return err
	}
	cfg.TVIP = tv.IP
	cfg.Secure = tv.Secure
	if tv.MAC != "" {
		cfg.TVMAC = tv.MAC
	}
	if tv.Secure {
		fmt.Println("(La TV solo expone el puerto seguro: se usará wss/3001.)")
	}

	// 2) Modo de apagado.
	cfg.PowerMode = choosePowerMode(in, cfg.PowerMode)

	// 3) Emparejamiento (auth) con la TV.
	client := lgtv.New(cfg, log)
	fmt.Println()
	fmt.Println("Conectando con la TV para emparejar…")
	fmt.Println(">> MIRA LA PANTALLA DE LA TV y acepta la solicitud de conexión.")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	key, err := client.Pair(ctx)
	if err != nil {
		return fmt.Errorf("emparejamiento fallido: %w", err)
	}
	cfg.ClientKey = key
	// Identifica la TV elegida: muestra su modelo y le manda un aviso visible,
	// para confirmar que es la correcta (p.ej. esta y no la del salón).
	if model, err := client.ModelName(ctx); err == nil && model != "" {
		fmt.Printf("✓ Emparejado con: %s  (%s)\n", model, cfg.TVIP)
	} else {
		fmt.Println("✓ Emparejado correctamente.")
	}
	if err := client.Toast(ctx, "lgtv2pc: esta TV quedará configurada ✓"); err == nil {
		fmt.Println("  (Comprueba que el aviso ha aparecido en la TV correcta.)")
		if !yes(prompt(in, "¿Apareció el aviso en la TV que querías? (S/n)", "s")) {
			return fmt.Errorf("TV incorrecta; vuelve a ejecutar -setup y elige otra de la lista")
		}
	}

	// 4) MAC para Wake-on-LAN si aún no la tenemos.
	if cfg.TVMAC == "" {
		if mac := discover.MACForIP(cfg.TVIP); mac != "" {
			cfg.TVMAC = mac
		}
	}
	if cfg.TVMAC != "" {
		fmt.Printf("✓ MAC: %s\n", cfg.TVMAC)
	} else if cfg.PowerMode == config.ModeFull {
		cfg.TVMAC = prompt(in, "No se detectó la MAC (necesaria en modo full). Introdúcela", "")
	}

	// 5) Filtro por entrada HDMI: ofrecer fijar la entrada actual.
	cfg.HDMIInput = chooseHDMI(in, client)

	// 6) Teclas (Enter acepta los valores por defecto).
	cfg.SuspendKey = chooseKey(in, "Tecla para APAGAR la TV (doble toque)", cfg.SuspendKey)
	cfg.WakeKey = chooseKey(in, "Tecla para ENCENDER la TV (doble toque)", cfg.WakeKey)

	// 7) Guardar.
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("guardando %s: %w", configPath, err)
	}

	fmt.Println()
	fmt.Printf("✓ Configuración guardada en %s\n", configPath)
	fmt.Println("\nPara arrancar el servicio:")
	fmt.Println("  sudo systemctl enable --now lgtv2pc")
	fmt.Println("  journalctl -u lgtv2pc -f")
	return nil
}

// runDiscover lista las TVs encontradas (diagnóstico, sin emparejar).
func runDiscover() {
	fmt.Println("Buscando TVs LG (SSDP + escaneo de puertos 3000/3001)…")
	tvs := discover.Discover(4 * time.Second)
	if len(tvs) == 0 {
		fmt.Println("No se encontró ninguna TV.")
		return
	}
	for _, tv := range tvs {
		secure := "ws/3000"
		if tv.Secure {
			secure = "wss/3001"
		}
		mac := tv.MAC
		if mac == "" {
			mac = "?"
		}
		fmt.Printf("  %-15s  %-17s  %-8s  %s\n", tv.IP, mac, secure, tv.Server)
	}
}

// chooseTV descubre TVs y deja elegir, con opción de IP manual.
func chooseTV(in *bufio.Reader) (discover.TV, error) {
	fmt.Println("Buscando TVs LG en la red (SSDP + escaneo de puertos)…")
	tvs := discover.Discover(4 * time.Second)

	if len(tvs) == 0 {
		fmt.Println("No se encontró ninguna TV automáticamente.")
		ip := prompt(in, "Introduce la IP de la TV", "")
		if ip == "" {
			return discover.TV{}, fmt.Errorf("se necesita una IP de la TV")
		}
		return discover.TV{IP: ip, MAC: discover.MACForIP(ip)}, nil
	}

	fmt.Println("\nTVs encontradas:")
	for i, tv := range tvs {
		tags := tv.Server
		if tv.Secure {
			tags = strings.TrimSpace(tags + " [seguro/wss]")
		}
		mac := tv.MAC
		if mac == "" {
			mac = "MAC desconocida"
		}
		fmt.Printf("  [%d] %-15s %s  %s\n", i+1, tv.IP, mac, tags)
	}
	fmt.Println("  [m] introducir IP manualmente")

	for {
		ans := prompt(in, "Elige una opción", "1")
		if strings.EqualFold(ans, "m") {
			ip := prompt(in, "Introduce la IP de la TV", "")
			if ip != "" {
				return discover.TV{IP: ip, MAC: discover.MACForIP(ip)}, nil
			}
			continue
		}
		var idx int
		if _, err := fmt.Sscanf(ans, "%d", &idx); err == nil && idx >= 1 && idx <= len(tvs) {
			return tvs[idx-1], nil
		}
		fmt.Println("Opción no válida.")
	}
}

// choosePowerMode pregunta el modo de apagado.
func choosePowerMode(in *bufio.Reader, current config.PowerMode) config.PowerMode {
	fmt.Println("\nModo de apagado:")
	fmt.Println("  [1] screen   – apaga solo el panel; la TV sigue encendida. Instantáneo.")
	fmt.Println("  [2] standby  – standby real, como el mando (LED); reenciende por SSAP o WoL")
	fmt.Println("  [3] full     – standby + encendido SIEMPRE por Wake-on-LAN (requiere MAC)")
	def := "1"
	switch current {
	case config.ModeStandby:
		def = "2"
	case config.ModeFull:
		def = "3"
	}
	ans := prompt(in, "Elige modo", def)
	switch {
	case strings.HasPrefix(ans, "3") || strings.EqualFold(ans, "full"):
		return config.ModeFull
	case strings.HasPrefix(ans, "2") || strings.EqualFold(ans, "standby"):
		return config.ModeStandby
	default:
		return config.ModeScreen
	}
}

// chooseHDMI consulta la entrada actual de la TV y ofrece restringir a ella.
func chooseHDMI(in *bufio.Reader, client *lgtv.Client) string {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	app, err := client.CurrentInput(ctx)
	if err != nil || app == "" {
		fmt.Println("\n(No se pudo leer la entrada actual; sin restricción de HDMI.)")
		return ""
	}
	fmt.Printf("\nLa TV está ahora en: %s\n", app)
	if !strings.HasPrefix(app, "com.webos.app.hdmi") {
		fmt.Println("No es una entrada HDMI; sin restricción.")
		return ""
	}
	fmt.Println("Puedes limitar la integración a esta entrada: si la TV está en otra")
	fmt.Println("entrada/app, lgtv2pc no enviará ningún comando (no interfiere otros usos).")
	if yes(prompt(in, "¿Restringir a esta entrada? (s/N)", "n")) {
		return app
	}
	return ""
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

// yes interpreta una respuesta afirmativa.
func yes(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "s" || s == "si" || s == "sí" || s == "y" || s == "yes"
}

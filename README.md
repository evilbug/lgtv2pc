# lgtv2pc

Servicio en segundo plano que sincroniza el estado de una **TV LG (webOS)** con tu **PC Linux**.

## Qué hace

| Evento del PC | Acción sobre la TV |
|---|---|
| El PC se **suspende/duerme** | Apaga la pantalla (antes de dormir, mientras aún hay red) |
| El PC **resume** | Enciende la pantalla (solo si la sesión no está bloqueada) |
| La sesión se **bloquea** (lockscreen) | Apaga la pantalla |
| La sesión se **desbloquea** | Enciende la pantalla |
| PC activo: **doble Right Ctrl** | Apaga la TV (manual) |
| PC activo: **doble Right Shift** | Enciende la TV (manual) |

Las teclas son configurables (ver `suspend_key`/`wake_key`).

La lógica automática mantiene una regla simple: **la TV está encendida solo si el PC está despierto _y_ la sesión desbloqueada.** Así, si resumes el equipo pero sigues en la pantalla de contraseña, la TV no se enciende hasta que desbloqueas.

## Cómo funciona

- **Suspensión / bloqueo**: escucha por D-Bus a `systemd-logind` (`PrepareForSleep`, y `Lock`/`Unlock` de sesión). Para la suspensión toma un *inhibidor `delay`* y así apaga la TV **antes** de que el equipo pierda la red.
- **TV**: habla el protocolo **SSAP** de webOS por WebSocket (`ws://TV:3000`). Requiere un emparejamiento inicial (la TV muestra un diálogo y devuelve una `client-key`).
- **Dobles pulsaciones**: lee `/dev/input/event*` (evdev) a nivel de kernel, así que funciona igual en **X11 y Wayland**. Leer evdev **no consume** la pulsación (el SO la sigue viendo), por eso por defecto se usan modificadores derechos pulsados solos (`Right Ctrl`/`Right Shift`): existen en todo teclado, no escriben texto y ni KDE ni GNOME asignan acción a su doble toque. Además, el doble toque solo cuenta si no se pulsó otra tecla en medio.

## Requisitos

- Go ≥ 1.21 para compilar.
- La TV y el PC en la misma red.
- En la TV: **Configuración → General → Dispositivos móviles / Encendido móvil** activado (necesario para que responda en red; imprescindible para el modo `full` con Wake-on-LAN).
- El servicio corre como **root** (necesita `/dev/input` y el bus de sistema).

## Instalación

```sh
sudo make install            # compila e instala binario + unit systemd
sudo lgtv2pc -setup          # onboarding: localiza la TV, empareja y crea la config
sudo systemctl enable --now lgtv2pc
journalctl -u lgtv2pc -f     # ver logs
```

### Onboarding (`lgtv2pc -setup`)

Con la TV **encendida** y en la misma red, ejecuta `sudo lgtv2pc -setup`. El asistente:

1. **Localiza las TVs** en la red por SSDP y, como respaldo, escaneando los puertos SSAP (3000/3001) del `/24` local. **Si hay varias** (p.ej. una en el salón), las lista con IP/MAC y eliges; también puedes escribir la IP a mano. Detecta solo si la TV requiere conexión segura (wss/3001).
2. Pregunta el **modo de apagado** (`screen` / `full`).
3. **Empareja (auth)**: la TV muestra un diálogo de "solicitud de conexión" — **acéptalo en la pantalla**. Se guarda la `client-key`. Después muestra el **modelo** y manda un **aviso a la TV** para que confirmes que es la correcta.
4. **Detecta la MAC** automáticamente (vía tabla ARP) para Wake-on-LAN.
5. Permite ajustar las **teclas** (Enter acepta los valores por defecto).
6. Escribe `/etc/lgtv2pc/config.json`.

`sudo lgtv2pc -pair` re-empareja sobre una config ya existente (p.ej. si reseteaste la TV).

## Configuración (`/etc/lgtv2pc/config.json`)

```json
{
  "tv_ip": "192.168.1.50",
  "tv_mac": "AA:BB:CC:DD:EE:FF",
  "client_key": "",
  "power_mode": "screen",
  "secure": false,
  "double_tap_ms": 400,
  "suspend_key": "rightctrl",
  "wake_key": "rightshift"
}
```

| Campo | Descripción |
|---|---|
| `tv_ip` | IP de la TV (recomendado fijarla por DHCP). **Obligatorio.** |
| `tv_mac` | MAC de la TV. Solo necesaria en `power_mode: full` (Wake-on-LAN). |
| `client_key` | Se rellena sola al ejecutar `-pair`. No la edites a mano. |
| `power_mode` | `screen` (apaga solo el panel, la TV sigue en red — recomendado) o `full` (apaga la TV del todo y la enciende con WoL). |
| `secure` | `true` usa `wss://TV:3001` en lugar de `ws://TV:3000` (webOS recientes). |
| `double_tap_ms` | Ventana máxima entre dos pulsaciones para contar como "doble". |
| `suspend_key` | Tecla cuyo **doble toque** apaga la TV. Nombre (`rightctrl`, `rightshift`, `scrolllock`, `pause`, `f13`…`f24`, `menu`…) o keycode numérico (`97`, `0x61`). |
| `wake_key` | Tecla cuyo **doble toque** enciende la TV. Mismo formato. |
| `hdmi_input` | Si se indica, **solo se actúa cuando la TV está en esa entrada** (`hdmi1`…`hdmi4`, solo el número, o un appId completo). Si la TV está en otra entrada/app, no se envía ningún comando. Vacío = sin restricción. |

### Teclas que no interfieren con el SO

Por defecto: doble `Right Ctrl` (apagar) / doble `Right Shift` (encender). Buenas alternativas inertes: `scrolllock`, `pause`, y `f13`–`f24` (estas últimas no existen físicamente en teclados normales; puedes mapear una tecla libre a F13 con [`keyd`](https://github.com/rvaiya/keyd) o una regla `udev/hwdb` y usar `"suspend_key": "f13"`). Evita `rightalt` si usas AltGr.

### Filtro por entrada HDMI

Si configuras `hdmi_input` (p.ej. `"hdmi2"`), **antes de cada comando** lgtv2pc consulta la entrada en primer plano de la TV (`getForegroundAppInfo`) y solo actúa si coincide. Así, si tienes la TV en otra entrada (una consola, otro PC) o viendo TV/una app, el servicio **no la toca**, aunque suspendas o bloquees el PC. El onboarding detecta la entrada actual y ofrece fijarla automáticamente.

> Solo aplica en `power_mode: screen` (la TV sigue accesible). En `full`, al encender la TV está apagada y no hay nada que consultar, así que el encendido por WoL no se filtra.

### `screen` vs `full`

- **`screen`** (por defecto): usa `turnOffScreen`/`turnOnScreen`. La TV queda encendida con el panel apagado; reacción instantánea, sin Wake-on-LAN. Ideal para OLED.
- **`full`**: usa `system/turnOff` y enciende con un magic packet de Wake-on-LAN. Ahorra más energía pero el encendido tarda ~10 s en arrancar y exige tener WoL activado en la TV.

## Notas y límites

- El bloqueo/desbloqueo depende de que tu escritorio informe a `logind` (`loginctl lock-session`). GNOME y KDE Plasma lo hacen de serie. Si tu bloqueador no lo integra, los eventos de lock no llegarán (suspensión y dobles pulsaciones siguen funcionando). Compruébalo con:
  ```sh
  dbus-monitor --system "interface='org.freedesktop.login1.Session'"
  ```
- Las dobles pulsaciones son **globales**: se detectan sin importar la app con foco. Elige `double_tap_ms` corto para evitar disparos accidentales.
- Si cambias la TV de sitio o se resetea, vuelve a ejecutar `sudo lgtv2pc -pair`.
- **Descubrimiento**: muchas redes (switches/APs con IGMP snooping) filtran el multicast SSDP, así que el onboarding también escanea los puertos SSAP del `/24`. Para ver qué encuentra sin emparejar: `lgtv2pc -discover`.

## Desarrollo

```sh
make build      # compila ./lgtv2pc
go vet ./...
# probar sin instalar (necesita root para /dev/input):
sudo ./lgtv2pc -config ./config.example.json
```

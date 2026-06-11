# lgtv2pc

Background service that syncs the state of an **LG TV (webOS)** with your **Linux PC**.

## What it does

| PC event | Action on the TV |
|---|---|
| The PC **suspends/sleeps** | Turns off/suspends the TV (before sleeping, while the network is still up) |
| The PC **resumes** | Turns on the TV (only if the session is not locked) |
| The session **locks** (lockscreen) | Turns off/suspends the TV |
| The session **unlocks** | Turns on the TV |
| PC active: **double Right Ctrl** | Turns off/suspends the TV (manual) |
| PC active: **double Right Shift** | Turns on the TV (manual) |

What "turn off" means exactly depends on `power_mode` (turn off just the panel, real standby, or standby + WoL — see below).

The keys are configurable (see `suspend_key`/`wake_key`).

The automatic logic keeps a simple rule: **the TV is on only if the PC is awake _and_ the session is unlocked.** So if you resume the machine but are still on the password screen, the TV won't turn on until you unlock.

## How it works

- **Suspend / lock**: listens over D-Bus to `systemd-logind` (`PrepareForSleep`, and session `Lock`/`Unlock`). For suspend it takes a *`delay` inhibitor* so it can turn off the TV **before** the machine loses the network.
- **TV**: speaks webOS's **SSAP** protocol over WebSocket (`ws://TV:3000` or, on recent models, `wss://TV:3001` with a self-signed certificate). Requires an initial pairing (the TV shows a dialog and returns a `client-key`).
- **Double taps**: reads `/dev/input/event*` (evdev) at the kernel level, so it works the same on **X11 and Wayland**. Reading evdev **does not consume** the keypress (the OS still sees it), which is why right-side modifiers pressed alone (`Right Ctrl`/`Right Shift`) are used by default: they exist on every keyboard, don't type text, and neither KDE nor GNOME assign an action to their double tap. Also, the double tap only counts if no other key was pressed in between.

## Requirements

- Go ≥ 1.21 to build.
- The TV and the PC on the same network.
- On the TV: **Settings → General → Mobile devices / Mobile TV On** enabled (so it responds over the network; required for Wake-on-LAN in `full` and in the `standby` fallback).
- The service runs as **root** (it needs `/dev/input` and the system bus).

## Installation

```sh
sudo make install            # build and install binary + systemd unit
sudo lgtv2pc -setup          # onboarding: locate the TV, pair, and create the config
sudo systemctl enable --now lgtv2pc
journalctl -u lgtv2pc -f     # view logs
```

### Onboarding (`lgtv2pc -setup`)

With the TV **on** and on the same network, run `sudo lgtv2pc -setup`. The wizard:

1. **Locates the TVs** on the network via SSDP and, as a fallback, scanning the SSAP ports (3000/3001) of the local `/24`. **If there are several** (e.g. one in the living room), it lists them with IP/MAC and you choose; you can also type the IP by hand. It detects whether the TV requires a secure connection (wss/3001).
2. Asks for the **power-off mode** (`screen` / `standby` / `full`).
3. **Pairs (auth)**: the TV shows a "connection request" dialog — **accept it on the screen**. The `client-key` is saved. Afterwards it shows the **model** and sends a **notice to the TV** so you can confirm it's the right one.
4. **Detects the MAC** automatically (via the ARP table) for Wake-on-LAN.
5. If the TV is on an HDMI input, it offers to **restrict the integration to that input** (`hdmi_input`).
6. Lets you adjust the **keys** (Enter accepts the defaults).
7. Writes `/etc/lgtv2pc/config.json`.

`sudo lgtv2pc -pair` re-pairs on top of an existing config (e.g. if you reset the TV).

### Commands

| Command | What for |
|---|---|
| `lgtv2pc` | Starts the service (normally via systemd). |
| `lgtv2pc -setup` | Interactive onboarding (locate, pair, create the config). |
| `lgtv2pc -pair` | Re-pairs on top of an existing config. |
| `lgtv2pc -discover` | Lists the TVs found on the network and exits (diagnostics). |
| `lgtv2pc -test on\|off\|cycle` | Tests an action and exits. `cycle` = turn off, wait, and turn back on. |
| `lgtv2pc -config <path>` | Uses another config path (defaults to `/etc/lgtv2pc/config.json`). |

## Configuration (`/etc/lgtv2pc/config.json`)

```json
{
  "tv_ip": "192.168.1.50",
  "tv_mac": "AA:BB:CC:DD:EE:FF",
  "client_key": "",
  "power_mode": "screen",
  "secure": false,
  "double_tap_ms": 400,
  "suspend_key": "rightctrl",
  "wake_key": "rightshift",
  "hdmi_input": ""
}
```

| Field | Description |
|---|---|
| `tv_ip` | TV IP (recommended to pin it via DHCP). **Required.** |
| `tv_mac` | TV MAC. Needed in `full` and used as a fallback in `standby` (Wake-on-LAN). |
| `client_key` | Filled in automatically when you run `-pair`. Don't edit it by hand. |
| `power_mode` | `screen`, `standby`, or `full` (see below). |
| `secure` | `true` uses `wss://TV:3001` instead of `ws://TV:3000` (recent webOS). |
| `double_tap_ms` | Maximum window between two keypresses to count as a "double". |
| `suspend_key` | Key whose **double tap** turns off the TV. Name (`rightctrl`, `rightshift`, `scrolllock`, `pause`, `f13`…`f24`, `menu`…) or numeric keycode (`97`, `0x61`). |
| `wake_key` | Key whose **double tap** turns on the TV. Same format. |
| `hdmi_input` | If set, **action is only taken when the TV is on that input** (`hdmi1`…`hdmi4`, just the number, or a full appId). If the TV is on another input/app, no command is sent. Empty = no restriction. |

### Keys that don't interfere with the OS

By default: double `Right Ctrl` (turn off) / double `Right Shift` (turn on). Good inert alternatives: `scrolllock`, `pause`, and `f13`–`f24` (the latter don't physically exist on normal keyboards; you can map a free key to F13 with [`keyd`](https://github.com/rvaiya/keyd) or a `udev/hwdb` rule and use `"suspend_key": "f13"`). Avoid `rightalt` if you use AltGr.

### HDMI input filter

If you configure `hdmi_input` (e.g. `"hdmi2"`), **before each command** lgtv2pc queries the TV's foreground input (`getForegroundAppInfo`) and only acts if it matches. So if your TV is on another input (a console, another PC) or watching TV/an app, the service **won't touch it**, even if you suspend or lock the PC. Onboarding detects the current input and offers to pin it automatically.

> It applies whenever the TV is reachable over SSAP: when **turning off** in any mode, and when **turning on** except when pure Wake-on-LAN is used (`full`, or the `standby` fallback), because there the TV is off and there's no input to query.

### Power-off modes (`power_mode`)

- **`screen`** (default): `turnOffScreen`/`turnOnScreen`. The TV stays **fully on with the panel off** (no standby LED); instant reaction, no WoL. Turns off *only the image*.
- **`standby`**: puts the TV in **real standby** (`system/turnOff`), like the remote's button (LED on, still on the network). When turning on it **tries to reconnect over SSAP and only falls back to Wake-on-LAN if the connection fails** (and there's a `tv_mac`). So if your TV wakes over the network, WoL isn't used.
- **`full`**: standby (`system/turnOff`) and **always** turns on with Wake-on-LAN. Requires `tv_mac` and having "Mobile TV On / LAN" enabled on the TV.

> To test the chosen mode without waiting for a real suspend/lock: `sudo lgtv2pc -test off` and `sudo lgtv2pc -test on`. If **the TV is your monitor**, use `sudo lgtv2pc -test cycle`: it turns off, waits a few seconds, and turns back on by itself (so you're not left without a screen to type the second command). Check the logs to see which path it used to turn on (`turnOnScreen` = SSAP; `Wake-on-LAN` = WoL).

## Notes and limits

- Lock/unlock depends on your desktop reporting to `logind` (`loginctl lock-session`). GNOME and KDE Plasma do this out of the box. If your locker doesn't integrate it, lock events won't arrive (suspend and double taps still work). Check it with:
  ```sh
  dbus-monitor --system "interface='org.freedesktop.login1.Session'"
  ```
- Double taps are **global**: they're detected regardless of the focused app. Pick a short `double_tap_ms` to avoid accidental triggers.
- If you move the TV or it gets reset, run `sudo lgtv2pc -pair` again.
- **Discovery**: many networks (switches/APs with IGMP snooping) filter SSDP multicast, so onboarding also scans the SSAP ports of the `/24`. To see what it finds without pairing: `lgtv2pc -discover`.

## Development

```sh
make build      # builds ./lgtv2pc
go vet ./...
# test without installing (needs root for /dev/input):
sudo ./lgtv2pc -config ./config.example.json
```

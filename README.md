# Windstream

Stream your Windows gaming PC to any browser over authenticated HTTPS, with
low-latency WebRTC video and audio and remote controller support.

```
 Browser (phone / laptop / TV)                     Windows game PC
 ┌──────────────────────────┐   HTTPS + WSS    ┌──────────────────────────────────────────┐
 │ login (password + TOTP)  │ ───────────────► │ windstream.exe                           │
 │ <video> WebRTC playback  │ ◄─ UDP (DTLS/SRTP)│   Virtual Display Driver monitor         │
 │ Gamepad API (Bluetooth   │ ── data channel ►│   ddagrab → NVENC / AMF / QSV  → RTP     │
 │ pads), keyboard, mouse   │                  │   WASAPI loopback → Opus                 │
 └──────────────────────────┘                  │   SendInput (kb/mouse), ViGEm (Xbox 360) │
                                               └──────────────────────────────────────────┘
```

## What it does

- **Virtual display.** Windstream turns on an Indirect Display Driver monitor
  while it runs, so the PC needs no physical monitor or dummy plug. It sets
  the resolution and refresh rate, makes that screen primary so games open
  there, and restores your layout on exit. You can stream an existing monitor
  instead.
- **Low-latency video.** Desktop Duplication (`ddagrab`) captures straight
  into a GPU texture that NVENC, AMF or Quick Sync encode without a copy
  through system memory. Encoder settings are tuned for latency: no
  B-frames, no lookahead, CBR with about two frames of rate-control buffer.
  Frames reach WebRTC as RTP packets the moment they are encoded. All media
  uses one UDP port.
- **Audio.** WASAPI loopback captures whatever the PC is playing, encoded to
  10 ms Opus frames.
- **Remote controllers.** Pair a Bluetooth or USB controller with the device
  you play on. The browser's Gamepad API reads it and Windstream recreates it
  on the PC as a virtual Xbox 360 controller through ViGEmBus. Games, Steam
  and XInput see a normal pad. Up to 4 pads per client are supported, and
  keyboard and mouse (pointer lock) are supported too.
- **Security.** Logins use argon2id password hashes, optional TOTP and per-IP
  rate limiting. Session cookies are HttpOnly, Secure and SameSite=Strict.
  Login and WebSocket requests must come from the same origin. Strict
  CSP and HSTS headers are set. Media is encrypted with DTLS-SRTP. Nothing
  except the login page is reachable without a session.

## Quick start

See **[docs/windows-setup.md](docs/windows-setup.md)** for the full guide. In
short:

1. Install the prerequisites: ffmpeg 6.1 or newer (`winget install Gyan.FFmpeg`),
   the [Virtual Display Driver](https://github.com/VirtualDrivers/Virtual-Display-Driver),
   and [ViGEmBus](https://github.com/nefarius/ViGEmBus/releases) with
   `ViGEmClient.dll` for controllers.
2. Download the `windstream-windows-amd64` build from the latest CI run, or
   build it yourself with `make windows`.
3. From an elevated PowerShell in that folder, run the installer:
   `powershell -ExecutionPolicy Bypass -File .\install.ps1`
4. Create a user:
   `windstream.exe hash-password` and `windstream.exe totp-setup -user you`.
   Put the results in `C:\ProgramData\Windstream\windstream.toml` and set up TLS.
5. Validate the setup with `windstream.exe check`. Then run
   `Start-ScheduledTask Windstream`.
6. Forward **TCP 8443** and **UDP 8444** on your router. Open
   `https://your-domain:8443` in Chrome, Edge or Safari.

## Commands

| Command | Purpose |
|---|---|
| `serve` | Run the server. |
| `check` | Validate the config and probe ffmpeg, the encoders, the virtual display, ViGEm, audio and TLS. |
| `displays` | List monitors and GPUs with the index used by `display.monitor`. |
| `audio-devices` | List playback devices for `audio.device`. |
| `hash-password` | Generate an argon2id hash. |
| `totp-setup -user NAME` | Generate a TOTP secret and authenticator URL. |
| `gen-cert -hosts a,b` | Create a self-signed certificate. |

## Layout

```
cmd/windstream        CLI and server wiring
internal/auth         argon2id, TOTP (RFC 6238), sessions, rate limiting
internal/config       TOML config with strict validation
internal/display      Virtual Display Driver control, display modes, DXGI output lookup
internal/media        ffmpeg pipelines (ddagrab + GPU encoders → RTP), WASAPI loopback → Opus
internal/input        SendInput (scan codes), ViGEm virtual Xbox 360 pads
internal/protocol     binary input protocol on the WebRTC data channels
internal/server       HTTPS, security headers, login API, WebSocket signaling
internal/stream       WebRTC hub: shared encoder, per-client peers, input routing
web                   browser client (embedded in the binary)
deploy                install.ps1 / uninstall.ps1
```

## Development

The server builds and runs on any OS with `display.mode = "test"` and
`audio.backend = "test"`. That mode streams a synthetic pattern, so you can
work on the server, client and network path without Windows. CI runs the
unit tests and the ffmpeg integration tests on Linux, and it builds and tests
on Windows.

```
make lint test     # vet both platforms + race tests
make windows       # dist/windstream.exe + dist/windstreamw.exe
make run-test      # local server with the synthetic source (needs dev.toml)
```

## Known limitations

- **Secure desktop.** The UAC prompts and the lock screen are on Windows'
  secure desktop, which Desktop Duplication cannot capture. Video pauses
  until the secure desktop closes.
- **Rumble.** Controller vibration is not sent back to the client yet.
- **Keyframes.** ffmpeg cannot produce a keyframe on request. A new viewer,
  or recovery from heavy packet loss, waits for the next keyframe
  (`video.gop_seconds`). NACK retransmission covers ordinary loss.
- **Firefox.** It decodes H.264 through OpenH264. If video does not
  start, set `video.profile = "baseline"` or use a Chromium browser or Safari.

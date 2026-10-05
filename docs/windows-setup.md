# Windows setup

This guide sets up Windstream on a Windows 10 (21H2 or later) or Windows 11
gaming PC. It takes about 20 minutes.

## 1. Prerequisites

### ffmpeg

ffmpeg 6.1 or newer is required. It must include `ddagrab`, the hardware
encoders and libopus. The gyan.dev builds have all of them:

```powershell
winget install Gyan.FFmpeg
```

Open a new terminal and confirm with `ffmpeg -version`. If the scheduled
task cannot find `ffmpeg`, set the full path in `video.ffmpeg_binary`.

Keep your GPU driver current, because NVENC, AMF and Quick Sync ship with the
driver.

### Virtual Display Driver

Install the [Virtual Display Driver](https://github.com/VirtualDrivers/Virtual-Display-Driver)
(MIT licensed) using its installer. After that, Device Manager shows it
under **Display adapters**. Use its settings to:

- set the number of monitors to **1**, and
- add the resolutions and refresh rates you want to stream, for example
  1920×1080 at 60 Hz or 2560×1440 at 120 Hz. `display.width`,
  `display.height` and `display.refresh` must match one of these.

Windstream enables the adapter at start, applies the mode, makes that screen
primary, and disables the adapter again at exit. To keep it on, set
`display.virtual_keep_enabled = true`.

If you would rather stream a real monitor or an HDMI dummy plug, set
`display.mode = "monitor"`. Then pick the screen with `windstream displays`.

### Controllers: ViGEmBus

1. Install the driver from the
   [ViGEmBus releases](https://github.com/nefarius/ViGEmBus/releases).
   ViGEmBus is no longer developed, but it is still the standard virtual
   controller driver that Sunshine, Steam tools and DS4Windows rely on.
2. Windstream also needs `ViGEmClient.dll` next to `windstream.exe`. Build it
   from [ViGEmClient](https://github.com/nefarius/ViGEmClient) in Visual
   Studio using the `Release_DLL | x64` configuration. Then copy the DLL into
   the install folder.

Without the DLL everything else still works, and Windstream logs that
gamepads are disabled.

On the device you play from, pair the controller over Bluetooth as you
normally would. Open Windstream and press any button on the controller.
Browsers only expose a gamepad after a button press. The 🎮 counter in the
toolbar then goes up and a virtual Xbox 360 controller appears on the PC.

## 2. Install

Run this from an elevated PowerShell in the folder with the Windstream
build:

```powershell
powershell -ExecutionPolicy Bypass -File .\install.ps1
```

The installer does the following:

- It copies the binaries to `C:\Program Files\Windstream`.
- It creates `C:\ProgramData\Windstream\windstream.toml`. Only SYSTEM and
  Administrators can read that folder, because it holds password hashes and
  TLS keys.
- It adds firewall rules for TCP 8443 and UDP 8444, scoped to the Windstream
  executables.
- It registers a **Windstream** scheduled task that starts the server hidden
  at your logon, elevated, and restarts it if it exits.

Windstream has to run inside your desktop session. Screen capture and input
injection do not work from a Windows service. That is why the installer uses
a logon task instead.

Elevation is needed for two things: toggling the virtual display adapter and
sending input to elevated windows. Some launchers and anti-cheat games run
elevated. To run unelevated, install with `-Limited` and set
`display.virtual_keep_enabled = true`, then enable the adapter once in
Device Manager.

## 3. Configure

Generate the user credentials:

```powershell
cd "C:\Program Files\Windstream"
.\windstream.exe hash-password            # paste into password_hash
.\windstream.exe totp-setup -user you     # paste totp_secret; add the URL to your authenticator app
```

Edit `C:\ProgramData\Windstream\windstream.toml` from an elevated editor:

- `[[users]]`: enter your name, hash and TOTP secret. Then set
  `auth.require_totp = true`.
- `[tls]`: choose a certificate source. See section 4.
- `[display]`: set the resolution and refresh rate. Optionally add `launch`,
  for example Steam Big Picture.
- `[video]`: set `bitrate_kbps` to about 70–80% of your upload speed.

Then validate the setup and start the server:

```powershell
.\windstream.exe check
Start-ScheduledTask -TaskName Windstream
Get-Content C:\ProgramData\Windstream\logs\windstream.log -Wait
```

## 4. HTTPS certificate

Browsers require HTTPS for WebRTC, the Gamepad API and pointer lock. Pick one
of these options:

- **Let's Encrypt (built in).** Point a hostname at your home IP, for
  example a free DuckDNS name. Forward TCP 80 to this PC. Then set
  `tls.acme = true`, `tls.acme_domains = ["you.duckdns.org"]` and
  `server.redirect_http = ":80"`. Certificates renew automatically. Add a
  firewall rule for TCP 80 as well.
- **Your own certificate.** Use win-acme, `tailscale cert` or a similar
  tool. Then set `tls.cert_file` and `tls.key_file`. Windstream reloads
  renewed files without a restart.
- **Self-signed.** Use `tls.self_signed = true` for LAN testing only.

## 5. Router and network

| Forward | Purpose |
|---|---|
| TCP 8443 | HTTPS: login, client page and signaling |
| UDP 8444 | WebRTC media and input (all clients share one port) |
| TCP 80 | Only for Let's Encrypt |

If your home IP is static, set `webrtc.public_ips = ["<your IP>"]` so the
server skips STUN. If a client network blocks UDP, set
`webrtc.tcp_port = 8444` and forward that TCP port too. It adds latency but
keeps the stream working.

## 6. Keep the PC streamable

- **Auto-login.** The server starts when you sign in. For a headless PC, use
  Sysinternals **Autologon**, which stores the password encrypted, so the PC
  signs in after a reboot.
- **No sleep or display-off.** Desktop Duplication stops when the display
  turns off. Run these commands to keep it on:
  ```powershell
  powercfg /change standby-timeout-ac 0
  powercfg /change monitor-timeout-ac 0
  ```
- **Lock screen and UAC.** These cannot be captured or controlled remotely.
  Avoid locking the PC while you stream.

## 7. Troubleshooting

| Symptom | Fix |
|---|---|
| `virtual monitor did not appear` | Check the driver's monitor count is 1 or more, and that `virtual_device` matches its Device Manager name. |
| `ChangeDisplaySettingsEx ... returned -2` | Add that resolution and refresh rate to the Virtual Display Driver settings. |
| `no working H.264 encoder` | Update the GPU driver and run `windstream check` to see each encoder's error. |
| Stream stuck at "Negotiating media…" | UDP 8444 is not reachable. Check the port forward, or enable `webrtc.tcp_port`. |
| Controller not detected | Press a button on it with the page focused. Check that `windstream check` shows ViGEmBus connected. |
| Keys work on the desktop but not in a game | Run elevated (the default install). The game is probably elevated. |

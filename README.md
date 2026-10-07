# Windstream

Stream your Windows gaming PC to any browser, from anywhere, over a private
HTTPS link with two-step sign-in. Windstream is one program, `Windstream.exe`.

## Using it

1. Download `Windstream.exe` from the
   [latest release](../../releases/latest) onto the gaming PC and
   double-click it.
2. Approve the one Windows administrator prompt.
3. Your browser opens the Windstream dashboard. Create your account and scan
   the QR code with an authenticator app.
4. Open the link shown on the dashboard (or scan its QR code) on your phone,
   laptop or TV. Sign in and play.

There are no scripts, config files or command lines to deal with.

### If Microsoft Defender blocks it

Windstream is not code-signed yet. It installs itself, starts at sign-in,
opens network ports and accepts remote keyboard, mouse and controller input.
That is what remote-play software does, but to Defender's machine-learning
checks an unsigned program doing it can look like a backdoor
(e.g. `Behavior:Win32/Persistence.A!ml`). To restore it:

1. Open **Windows Security → Virus & threat protection → Protection history**.
2. Open the Windstream entry, choose **Actions → Allow on device**, and confirm.
3. Run `Windstream.exe` again.

Please also report the false positive at
<https://www.microsoft.com/wdsi/filesubmission> (choose *Incorrectly detected
as malware*). The lasting fix is signing the executable; see *Code signing*
below.

## What the program does for you

- **Installs itself.** It copies itself to `C:\Program Files\Windstream`,
  starts at sign-in in the system tray, adds a firewall rule for itself, and
  adds a Start Menu entry plus an *Apps & features* entry for uninstalling.
- **Installs what it needs.** It downloads and installs ffmpeg, the
  [Virtual Display Driver](https://github.com/VirtualDrivers/Virtual-Display-Driver)
  and [ViGEmBus](https://github.com/nefarius/ViGEmBus). Each download is
  pinned to an exact version and checked against its SHA-256 hash before use.
  The dashboard shows progress.
- **Makes itself reachable.** It opens the router ports with UPnP and finds
  your public IP. It then gets a real Let's Encrypt certificate for a name
  like `203-0-113-5.sslip.io`, so the link works with no browser warnings. If
  your router refuses UPnP, the dashboard lists the two port forwards to add
  by hand.
- **Creates a screen to stream.** When someone connects, it switches on a
  virtual monitor at your chosen resolution and makes it the main display. It
  can also open Steam Big Picture. When the last viewer leaves, your normal
  desktop layout comes back.
- **Streams with low latency.** Desktop Duplication capture feeds the GPU
  encoder (NVENC, AMF or Quick Sync) with no copy through system memory.
  Each viewer gets the most efficient codec that both your GPU encodes and
  their device decodes in hardware: AV1, then HEVC, then H.264. Frames go
  out over WebRTC as soon as they are encoded. Chrome and Edge are told to
  show each frame the moment it is decoded, with no smoothing buffer.
  Audio is sent as 10 ms Opus frames on its own stream, so the video is
  never held back to lip-sync with it. Mouse movement and controller state
  are sent as soon as they change, not once per screen refresh.
- **Recovers by itself.** The streaming server runs as a separate process
  watched by the tray app. If it ever crashes or locks up, it is restarted
  within seconds with the screen layout restored, and browsers reconnect on
  their own. The cause is written to `logs\crash.log` and shown on the
  dashboard. **Download logs** on the dashboard (or **Open logs folder** in
  the tray menu) gives you everything needed for a bug report in one zip,
  with passwords and authenticator secrets left out.
- **Supports controllers.** Pair a Bluetooth or USB controller with the
  device you play on. Each one appears on the PC as an Xbox 360 controller.
  Keyboard and mouse work too.

## Security

- **Accounts.** Every account needs a password of 12 or more characters and
  an authenticator code. Passwords are stored as argon2id hashes, and used
  codes cannot be replayed.
- **Login protection.** Logins are rate-limited per IP. Session cookies are
  `HttpOnly`, `Secure` and `SameSite=Strict`, and sessions expire when idle
  and after a maximum age.
- **Request checks.** Login requests and stream connections must come from
  Windstream's own page. Strict CSP and HSTS headers are set.
- **Encryption.** HTTPS uses TLS 1.2 or newer with AEAD ciphers only. Media
  is encrypted with DTLS-SRTP.
- **Dashboard.** The dashboard only listens on `127.0.0.1`. It rejects
  foreign `Host` and `Origin` headers, and it requires sign-in once an account
  exists.
- **Secrets on disk.** Settings, password hashes, TOTP secrets and TLS keys
  live in `C:\ProgramData\Windstream`. Only SYSTEM and Administrators can
  read that folder.

## Good to know

- **Remote start after reboot.** Windstream starts when you sign in to
  Windows. To play remotely after a reboot, the PC must sign in by itself,
  for example with Sysinternals Autologon.
- **Secure screens.** Windows does not let any app capture the lock screen
  or UAC prompts, so the stream pauses while they show.
- **Shared IPs.** If your internet provider puts you behind a shared IP
  (CGNAT), the dashboard says so. In that case the link only works at home.
- **Reboot after first setup.** If the controller driver asks for a reboot,
  restart the PC once after the first setup.
- **Uninstalling.** Use *Apps & features → Windstream*, or the button at the
  bottom of the dashboard. ViGEmBus stays installed, because other apps share
  it. Remove it separately under *Apps & features* if you want.

## Code signing

Unsigned executables get no reputation with SmartScreen or Defender. The
cheapest route is Azure Trusted Signing (about $10/month; individuals in the
US and Canada can enroll). Once an account exists, add a signing step to
`.github/workflows/release.yml` using `azure/trusted-signing-action`.

## Development

The app runs on any OS in a mode that changes nothing on the system. It uses
a test pattern, test audio and the system's ffmpeg:

```
make run-dev       # dashboard at http://127.0.0.1:47333, stream at https://localhost:8443
make lint test     # vet (Linux + Windows) and race tests, incl. ffmpeg/WebRTC integration
make exe           # dist/Windstream.exe

Releasing: push a tag starting with `v` (`git tag v1.0.1 && git push origin v1.0.1`).
GitHub Actions builds `Windstream.exe` and `Windstream-arm64.exe` and publishes
them as a release.
make resources     # regenerate the exe icon/manifest and tray icon
```

| Package | Purpose |
|---|---|
| `cmd/windstream` | Entry points: double-click launcher, `--run` tray app, `--uninstall`, dev CLI |
| `internal/winapp` | Self-install, logon task, firewall, shortcuts, tray, uninstall |
| `internal/control` | App core: config, setup, engine lifecycle, local dashboard API |
| `internal/deps` | Pinned and verified downloads; driver installs |
| `internal/netx` | UPnP, public IP (UPnP/STUN), sslip.io hostname |
| `internal/server` | HTTPS, Let's Encrypt (TLS-ALPN-01), login, signaling |
| `internal/stream` | WebRTC hub: shared encoder, per-viewer peers, input |
| `internal/media` | ffmpeg ddagrab plus GPU encoders over RTP, WASAPI loopback to Opus |
| `internal/display` | Virtual display control, primary-screen swap, DXGI lookup |
| `internal/input` | SendInput (scan codes), native ViGEmBus client |
| `web` | Streaming page and dashboard (embedded in the exe) |

// Package deps downloads, verifies and installs Windstream's third-party
// runtime dependencies: ffmpeg, the Virtual Display Driver and ViGEmBus.
// Every artifact is pinned by URL and SHA-256; nothing unverified is run.
package deps

// Artifact is a pinned download.
type Artifact struct {
	Name   string
	URL    string
	SHA256 string
	Size   int64
}

var (
	// FFmpeg is the gyan.dev "essentials" static build. It includes ddagrab,
	// the NVENC/AMF/Quick Sync AV1, HEVC and H.264 encoders, libx264 and
	// libopus.
	FFmpeg = Artifact{
		Name:   "ffmpeg 9.0.2",
		URL:    "https://github.com/GyanD/codexffmpeg/releases/download/9.0.2/ffmpeg-9.0.2-essentials_build.zip",
		SHA256: "60f467265b1e312373dbcd92200c2618a74850f98d3d078e94296bb3fa2047ba",
		Size:   114768076,
	}
	// FFmpegMacARM64 / FFmpegMacX64 are the static macOS builds published by
	// the ffmpeg-static project (osxexperts.net / evermeet.cx builds): only
	// system frameworks, with AVFoundation capture, VideoToolbox, libx264
	// and libopus. The Apple silicon build is code-signed (required there).
	FFmpegMacARM64 = Artifact{
		Name:   "ffmpeg 6.1.1 (Apple silicon)",
		URL:    "https://github.com/eugeneware/ffmpeg-static/releases/download/b6.1.1/ffmpeg-darwin-arm64.gz",
		SHA256: "8923876afa8db5585022d7860ec7e589af192f441c56793971276d450ed3bbfa",
		Size:   19246198,
	}
	FFmpegMacX64 = Artifact{
		Name:   "ffmpeg 6.1.1 (Intel)",
		URL:    "https://github.com/eugeneware/ffmpeg-static/releases/download/b6.1.1/ffmpeg-darwin-x64.gz",
		SHA256: "929b375c1182d956c51f7ac25e0b2b0411fb01f6f407aa15c9758efeb4242106",
		Size:   25296431,
	}
	// VirtualDisplayDriver is the signed IddCx driver package (x64, despite
	// the "x86" in the upstream file name).
	VirtualDisplayDriver = Artifact{
		Name:   "Virtual Display Driver 25.7.23",
		URL:    "https://github.com/VirtualDrivers/Virtual-Display-Driver/releases/download/25.7.23/VirtualDisplayDriver-x86.Driver.Only.zip",
		SHA256: "e24210692b442b39af763536330ce78b423f19342b7a7792c26de3944e418b3a",
		Size:   132118,
	}
	// ViGEmBus is the virtual gamepad bus driver installer.
	ViGEmBus = Artifact{
		Name:   "ViGEmBus 1.22.0",
		URL:    "https://github.com/nefarius/ViGEmBus/releases/download/v1.22.0/ViGEmBus_1.22.0_x64_x86_arm64.exe",
		SHA256: "89220a7865076b342892f98865f3499fb7c4cfd673159e89d352c360fd014c6a",
		Size:   6278576,
	}
)

// VDDHardwareID is the root-enumerated hardware ID from MttVDD.inf.
const VDDHardwareID = `Root\MttVDD`

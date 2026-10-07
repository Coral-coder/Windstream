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

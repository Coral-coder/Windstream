// Command genres turns the Windstream logo into the Windows resources
// linked into windstream.exe (icon, manifest, version info) and the tray
// icon. Run with `go generate ./cmd/windstream`.
//
// The logo's source is assets/logo.svg (assets/logo-small.svg is a
// simplified mark for 32 px and below). If rsvg-convert is installed, the
// PNGs in assets/icon are re-rendered from the SVGs first; otherwise the
// committed PNGs are used as they are.
package main

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"log"
	"os"
	"os/exec"

	"github.com/tc-hib/winres"
	"github.com/tc-hib/winres/version"
)

var sizes = []int{16, 20, 24, 32, 40, 48, 64, 256}

func main() {
	if _, err := exec.LookPath("rsvg-convert"); err == nil {
		for _, sz := range sizes {
			src := "../../assets/logo.svg"
			if sz <= 32 {
				src = "../../assets/logo-small.svg" // stays legible when tiny
			}
			out := fmt.Sprintf("../../assets/icon/%d.png", sz)
			if b, err := exec.Command("rsvg-convert", "-w", fmt.Sprint(sz), "-h", fmt.Sprint(sz), "-o", out, src).CombinedOutput(); err != nil {
				log.Fatalf("render %s: %v %s", out, err, b)
			}
		}
	} else {
		log.Print("rsvg-convert not found; using the committed PNGs in assets/icon")
	}
	// The web pages embed copies of the same artwork.
	for src, dsts := range map[string][]string{
		"../../assets/logo.svg":       {"../../web/logo.svg", "../../web/admin/logo.svg"},
		"../../assets/logo-small.svg": {"../../web/favicon.svg", "../../web/admin/favicon.svg"},
		"../../assets/icon/256.png":   {"../../web/icon.png"},
	} {
		b, err := os.ReadFile(src)
		if err != nil {
			log.Fatal(err)
		}
		for _, d := range dsts {
			if err := os.WriteFile(d, b, 0o644); err != nil {
				log.Fatal(err)
			}
		}
	}

	var imgs []image.Image
	for _, sz := range sizes {
		f, err := os.Open(fmt.Sprintf("../../assets/icon/%d.png", sz))
		if err != nil {
			log.Fatal(err)
		}
		img, err := png.Decode(f)
		f.Close()
		if err != nil {
			log.Fatal(err)
		}
		imgs = append(imgs, img)
	}
	icon, err := winres.NewIconFromImages(imgs)
	if err != nil {
		log.Fatal(err)
	}
	var ico bytes.Buffer
	if err := icon.SaveICO(&ico); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile("../../internal/winapp/icon.ico", ico.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}

	rs := &winres.ResourceSet{}
	if err := rs.SetIcon(winres.Name("APPICON"), icon); err != nil {
		log.Fatal(err)
	}
	rs.SetManifest(winres.AppManifest{
		Identity:            winres.AssemblyIdentity{Name: "Windstream"},
		Description:         "Windstream game streaming server",
		ExecutionLevel:      winres.AsInvoker, // elevates itself only when it needs to install
		DPIAwareness:        winres.DPIPerMonitorV2,
		UseCommonControlsV6: true,
		LongPathAware:       true,
	})
	vi := version.Info{}
	vi.SetFileVersion("1.0.0.0")
	vi.SetProductVersion("1.0.0.0")
	for k, v := range map[string]string{
		version.ProductName: "Windstream", version.FileDescription: "Windstream game streaming",
		version.CompanyName: "Windstream", version.OriginalFilename: "Windstream.exe",
		version.LegalCopyright: "Windstream contributors",
	} {
		_ = vi.Set(0, k, v)
	}
	rs.SetVersionInfo(vi)
	for _, arch := range []winres.Arch{winres.ArchAMD64, winres.ArchARM64} {
		f, err := os.Create("rsrc_windows_" + string(arch) + ".syso")
		if err != nil {
			log.Fatal(err)
		}
		if err := rs.WriteObject(f, arch); err != nil {
			log.Fatal(err)
		}
		f.Close()
	}
}

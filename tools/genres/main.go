// Command genres draws the Windstream icon and writes the Windows resources
// (icon, manifest, version info) linked into windstream.exe, plus the tray
// icon. Run with `go generate ./cmd/windstream`.
package main

import (
	"bytes"
	"image"
	"image/color"
	"log"
	"math"
	"os"

	"github.com/tc-hib/winres"
	"github.com/tc-hib/winres/version"
)

func drawIcon(size int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	bg := color.NRGBA{0x10, 0x18, 0x28, 0xff}
	wave := color.NRGBA{0x5e, 0xea, 0xd4, 0xff}
	s := float64(size)
	radius := s * 0.22
	const ss = 4 // supersampling for anti-aliasing
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var bgHits, waveHits int
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					px := float64(x) + (float64(sx)+0.5)/ss
					py := float64(y) + (float64(sy)+0.5)/ss
					if !insideRounded(px, py, s, radius) {
						continue
					}
					bgHits++
					for _, base := range []float64{0.38, 0.62} {
						cy := s*base + s*0.07*math.Sin((px/s)*2*math.Pi*1.15-0.6)
						if math.Abs(py-cy) < s*0.055 && px > s*0.17 && px < s*0.83 {
							waveHits++
							break
						}
					}
				}
			}
			n := float64(ss * ss)
			if bgHits == 0 {
				continue
			}
			wa := float64(waveHits) / float64(bgHits)
			c := color.NRGBA{
				uint8(float64(bg.R)*(1-wa) + float64(wave.R)*wa),
				uint8(float64(bg.G)*(1-wa) + float64(wave.G)*wa),
				uint8(float64(bg.B)*(1-wa) + float64(wave.B)*wa),
				uint8(255 * float64(bgHits) / n),
			}
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

func insideRounded(x, y, s, r float64) bool {
	cx := math.Max(r, math.Min(s-r, x))
	cy := math.Max(r, math.Min(s-r, y))
	return (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r
}

func main() {
	var imgs []image.Image
	for _, sz := range []int{16, 20, 24, 32, 40, 48, 64, 256} {
		imgs = append(imgs, drawIcon(sz))
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

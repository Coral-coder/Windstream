// Command genres turns the Windstream logo into the Windows resources
// linked into windstream.exe (icon, manifest, version info), the tray icon
// and the images the web pages use. Run with `go generate ./cmd/windstream`.
//
// The logo is assets/logo-render.png, a Blender render produced by
// tools/logo/render.py; every size shows the whole scene (sun, sails and
// sea keep it recognizable even at 16 px).
package main

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"log"
	"math"
	"os"

	"github.com/tc-hib/winres"
	"github.com/tc-hib/winres/version"
	"golang.org/x/image/draw"
)

var sizes = []int{16, 20, 24, 32, 40, 48, 64, 256}

func load(path string) image.Image {
	f, err := os.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		log.Fatalf("%s: %v", path, err)
	}
	return img
}

// scaled returns src (optionally cropped to frac) resized to size x size,
// with rounded corners (radius as a fraction of size; 0 = square).
func scaled(src image.Image, size int, frac *[4]float64, radius float64) *image.NRGBA {
	b := src.Bounds()
	r := b
	if frac != nil {
		w, h := float64(b.Dx()), float64(b.Dy())
		r = image.Rect(b.Min.X+int(frac[0]*w), b.Min.Y+int(frac[1]*h), b.Min.X+int(frac[2]*w), b.Min.Y+int(frac[3]*h))
	}
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, r, draw.Src, nil)
	if radius > 0 {
		roundCorners(dst, radius*float64(size))
	}
	return dst
}

// roundCorners makes the corners transparent with 4x4 supersampled edges.
func roundCorners(img *image.NRGBA, rad float64) {
	s := float64(img.Bounds().Dx())
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			in := 0
			for sy := 0; sy < 4; sy++ {
				for sx := 0; sx < 4; sx++ {
					px, py := float64(x)+(float64(sx)+0.5)/4, float64(y)+(float64(sy)+0.5)/4
					cx := math.Max(rad, math.Min(s-rad, px))
					cy := math.Max(rad, math.Min(s-rad, py))
					if (px-cx)*(px-cx)+(py-cy)*(py-cy) <= rad*rad {
						in++
					}
				}
			}
			c := img.NRGBAAt(x, y)
			c.A = uint8(int(c.A) * in / 16)
			img.SetNRGBA(x, y, c)
		}
	}
}

func save(path string, img image.Image) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
}

func main() {
	master := load("../../assets/logo-render.png")
	var imgs []image.Image
	for _, sz := range sizes {
		img := scaled(master, sz, nil, 0.2)
		save(fmt.Sprintf("../../assets/icon/%d.png", sz), img)
		imgs = append(imgs, img)
	}
	// Web: the big logo, the browser-tab icon and the phone home-screen icon
	// (iOS rounds that one itself, so it stays square).
	logo := scaled(master, 512, nil, 0.2)
	fav := scaled(master, 64, nil, 0.2)
	save("../../web/logo.png", logo)
	save("../../web/admin/logo.png", logo)
	save("../../web/favicon.png", fav)
	save("../../web/admin/favicon.png", fav)
	save("../../web/icon.png", scaled(master, 180, nil, 0))

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

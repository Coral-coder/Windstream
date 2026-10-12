// Command macbundle assembles Windstream.app from a macOS executable:
//
//	go run ./tools/macbundle -exe dist/windstream-universal -version 1.0.15 -out dist
//
// It writes Info.plist, the app icon (built from assets/logo-render.png)
// and the executable. Signing and zipping are done by the release workflow
// on a Mac (codesign, ditto).
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"image"
	"image/png"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"

	"golang.org/x/image/draw"

	"github.com/coral-coder/windstream/internal/macapp"
)

func main() {
	exe := flag.String("exe", "", "macOS executable (universal) to put in the bundle")
	version := flag.String("version", "dev", "version string")
	out := flag.String("out", "dist", "directory to create Windstream.app in")
	logo := flag.String("logo", "assets/logo-render.png", "square logo image")
	flag.Parse()
	if *exe == "" {
		log.Fatal("-exe is required")
	}
	app := filepath.Join(*out, macapp.AppName)
	must(os.RemoveAll(app))
	for _, d := range []string{"Contents/MacOS", "Contents/Resources"} {
		must(os.MkdirAll(filepath.Join(app, d), 0o755))
	}
	must(copyFile(*exe, filepath.Join(app, "Contents/MacOS/Windstream"), 0o755))
	must(os.WriteFile(filepath.Join(app, "Contents/Info.plist"), []byte(macapp.InfoPlist(*version)), 0o644))
	must(os.WriteFile(filepath.Join(app, "Contents/PkgInfo"), []byte("APPL????"), 0o644))

	f, err := os.Open(*logo)
	must(err)
	src, err := png.Decode(f)
	f.Close()
	must(err)
	icns, err := buildICNS(src)
	must(err)
	must(os.WriteFile(filepath.Join(app, "Contents/Resources/Windstream.icns"), icns, 0o644))
	log.Printf("wrote %s", app)
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// icnsSizes are the PNG icon entries macOS uses (ic10 is 512@2x).
var icnsSizes = []struct {
	typ  string
	size int
}{{"ic07", 128}, {"ic08", 256}, {"ic09", 512}, {"ic10", 1024}}

// buildICNS renders the logo as a macOS app icon (rounded square on the
// standard grid: the shape fills about 80% of the canvas) in an .icns file.
func buildICNS(src image.Image) ([]byte, error) {
	var body bytes.Buffer
	for _, e := range icnsSizes {
		img := appIcon(src, e.size)
		var p bytes.Buffer
		if err := png.Encode(&p, img); err != nil {
			return nil, err
		}
		body.WriteString(e.typ)
		_ = binary.Write(&body, binary.BigEndian, uint32(8+p.Len()))
		body.Write(p.Bytes())
	}
	var out bytes.Buffer
	out.WriteString("icns")
	_ = binary.Write(&out, binary.BigEndian, uint32(8+body.Len()))
	out.Write(body.Bytes())
	return out.Bytes(), nil
}

func appIcon(src image.Image, size int) *image.NRGBA {
	canvas := image.NewNRGBA(image.Rect(0, 0, size, size))
	inset := int(math.Round(float64(size) * 0.1))
	inner := image.Rect(inset, inset, size-inset, size-inset)
	draw.CatmullRom.Scale(canvas, inner, src, src.Bounds(), draw.Src, nil)
	// Rounded corners (macOS "squircle" approximated by a 22.5% radius).
	rad := 0.225 * float64(inner.Dx())
	for y := inner.Min.Y; y < inner.Max.Y; y++ {
		for x := inner.Min.X; x < inner.Max.X; x++ {
			in := 0
			for sy := 0; sy < 4; sy++ {
				for sx := 0; sx < 4; sx++ {
					px := float64(x-inner.Min.X) + (float64(sx)+0.5)/4
					py := float64(y-inner.Min.Y) + (float64(sy)+0.5)/4
					w := float64(inner.Dx())
					cx := math.Max(rad, math.Min(w-rad, px))
					cy := math.Max(rad, math.Min(w-rad, py))
					if (px-cx)*(px-cx)+(py-cy)*(py-cy) <= rad*rad {
						in++
					}
				}
			}
			c := canvas.NRGBAAt(x, y)
			c.A = uint8(int(c.A) * in / 16)
			canvas.SetNRGBA(x, y, c)
		}
	}
	return canvas
}

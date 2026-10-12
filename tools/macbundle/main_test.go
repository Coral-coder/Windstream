package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func TestBuildICNS(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for i := range src.Pix {
		src.Pix[i] = 200
		if i%4 == 3 {
			src.Pix[i] = 255
		}
	}
	data, err := buildICNS(src)
	if err != nil {
		t.Fatal(err)
	}
	if string(data[:4]) != "icns" || int(binary.BigEndian.Uint32(data[4:8])) != len(data) {
		t.Fatal("bad icns header")
	}
	off, n := 8, 0
	for off < len(data) {
		typ := string(data[off : off+4])
		l := int(binary.BigEndian.Uint32(data[off+4 : off+8]))
		img, err := png.Decode(bytes.NewReader(data[off+8 : off+l]))
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		if want := icnsSizes[n].size; img.Bounds().Dx() != want {
			t.Errorf("%s is %d px, want %d", typ, img.Bounds().Dx(), want)
		}
		// Corners transparent, middle opaque.
		if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
			t.Errorf("%s: corner not transparent", typ)
		}
		mid := img.Bounds().Dx() / 2
		if c := color.NRGBAModel.Convert(img.At(mid, mid)).(color.NRGBA); c.A != 255 {
			t.Errorf("%s: centre not opaque", typ)
		}
		off += l
		n++
	}
	if n != len(icnsSizes) {
		t.Fatalf("%d entries", n)
	}
}

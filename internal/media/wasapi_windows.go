//go:build windows

package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/moutend/go-wca/pkg/wca"
	"golang.org/x/sys/windows"
)

var (
	winmm               = windows.NewLazySystemDLL("winmm.dll")
	procTimeBeginPeriod = winmm.NewProc("timeBeginPeriod")
	procTimeEndPeriod   = winmm.NewProc("timeEndPeriod")
)

const (
	waveFormatIEEEFloat  = 0x0003
	waveFormatExtensible = 0xFFFE
)

func withCOM(fn func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED); err != nil {
		var oe *ole.OleError
		// S_FALSE (already initialised) is fine.
		if !errors.As(err, &oe) || oe.Code() != 1 {
			return fmt.Errorf("CoInitializeEx: %w", err)
		}
	}
	defer ole.CoUninitialize()
	return fn()
}

func renderDevice(name string) (*wca.IMMDevice, error) {
	var mmde *wca.IMMDeviceEnumerator
	if err := wca.CoCreateInstance(wca.CLSID_MMDeviceEnumerator, 0, wca.CLSCTX_ALL, wca.IID_IMMDeviceEnumerator, &mmde); err != nil {
		return nil, fmt.Errorf("create device enumerator: %w", err)
	}
	defer mmde.Release()
	if name == "" {
		var mmd *wca.IMMDevice
		if err := mmde.GetDefaultAudioEndpoint(wca.ERender, wca.EConsole, &mmd); err != nil {
			return nil, fmt.Errorf("no default playback device: %w", err)
		}
		return mmd, nil
	}
	var dc *wca.IMMDeviceCollection
	if err := mmde.EnumAudioEndpoints(wca.ERender, wca.DEVICE_STATE_ACTIVE, &dc); err != nil {
		return nil, err
	}
	defer dc.Release()
	var count uint32
	if err := dc.GetCount(&count); err != nil {
		return nil, err
	}
	for i := uint32(0); i < count; i++ {
		var mmd *wca.IMMDevice
		if err := dc.Item(i, &mmd); err != nil {
			continue
		}
		if strings.Contains(strings.ToLower(deviceName(mmd)), strings.ToLower(name)) {
			return mmd, nil
		}
		mmd.Release()
	}
	return nil, fmt.Errorf("no active playback device matching %q", name)
}

func deviceName(mmd *wca.IMMDevice) string {
	var ps *wca.IPropertyStore
	if err := mmd.OpenPropertyStore(wca.STGM_READ, &ps); err != nil {
		return ""
	}
	defer ps.Release()
	var pv wca.PROPVARIANT
	if err := ps.GetValue(&wca.PKEY_Device_FriendlyName, &pv); err != nil {
		return ""
	}
	return pv.String()
}

// ListPlaybackDevices lists active WASAPI render endpoints.
func ListPlaybackDevices() ([]string, error) {
	var names []string
	err := withCOM(func() error {
		var mmde *wca.IMMDeviceEnumerator
		if err := wca.CoCreateInstance(wca.CLSID_MMDeviceEnumerator, 0, wca.CLSCTX_ALL, wca.IID_IMMDeviceEnumerator, &mmde); err != nil {
			return err
		}
		defer mmde.Release()
		var dc *wca.IMMDeviceCollection
		if err := mmde.EnumAudioEndpoints(wca.ERender, wca.DEVICE_STATE_ACTIVE, &dc); err != nil {
			return err
		}
		defer dc.Release()
		var count uint32
		_ = dc.GetCount(&count)
		for i := uint32(0); i < count; i++ {
			var mmd *wca.IMMDevice
			if dc.Item(i, &mmd) == nil {
				names = append(names, deviceName(mmd))
				mmd.Release()
			}
		}
		return nil
	})
	return names, err
}

func pcmFormat(wfx *wca.WAVEFORMATEX) (PCMFormat, error) {
	f := PCMFormat{Rate: int(wfx.NSamplesPerSec), Channels: int(wfx.NChannels), Bits: int(wfx.WBitsPerSample)}
	switch wfx.WFormatTag {
	case waveFormatIEEEFloat:
		f.Float = true
	case waveFormatExtensible:
		// WAVEFORMATEXTENSIBLE.SubFormat starts 24 bytes in; Data1 is 3 for
		// IEEE float and 1 for integer PCM.
		sub := *(*uint32)(unsafe.Add(unsafe.Pointer(wfx), 24))
		f.Float = sub == waveFormatIEEEFloat
	}
	if f.Float && f.Bits != 32 {
		return f, fmt.Errorf("unsupported float sample size %d", f.Bits)
	}
	if !f.Float && f.Bits != 16 && f.Bits != 24 && f.Bits != 32 {
		return f, fmt.Errorf("unsupported PCM sample size %d", f.Bits)
	}
	return f, nil
}

// runLoopback captures what the playback device is playing. It calls start
// once with the stream format and a reader of raw PCM, then blocks until ctx
// is done. Silence is synthesised when nothing is playing (WASAPI loopback
// delivers no packets then), so the Opus stream keeps a steady clock.
func runLoopback(ctx context.Context, device string, log *slog.Logger, start func(PCMFormat, io.Reader)) error {
	return withCOM(func() error {
		procTimeBeginPeriod.Call(1)
		defer procTimeEndPeriod.Call(1)

		mmd, err := renderDevice(device)
		if err != nil {
			return err
		}
		defer mmd.Release()
		var ac *wca.IAudioClient
		if err := mmd.Activate(wca.IID_IAudioClient, wca.CLSCTX_ALL, nil, &ac); err != nil {
			return fmt.Errorf("activate audio client: %w", err)
		}
		defer ac.Release()
		var wfx *wca.WAVEFORMATEX
		if err := ac.GetMixFormat(&wfx); err != nil {
			return fmt.Errorf("get mix format: %w", err)
		}
		defer ole.CoTaskMemFree(uintptr(unsafe.Pointer(wfx)))
		pcm, err := pcmFormat(wfx)
		if err != nil {
			return err
		}
		// 20 ms shared-mode buffer: small enough for low latency, large
		// enough that a busy game does not cause overruns.
		if err := ac.Initialize(wca.AUDCLNT_SHAREMODE_SHARED, wca.AUDCLNT_STREAMFLAGS_LOOPBACK, wca.REFERENCE_TIME(20*10000), 0, wfx, nil); err != nil {
			return fmt.Errorf("initialize loopback: %w", err)
		}
		var acc *wca.IAudioCaptureClient
		if err := ac.GetService(wca.IID_IAudioCaptureClient, &acc); err != nil {
			return fmt.Errorf("get capture client: %w", err)
		}
		defer acc.Release()
		if err := ac.Start(); err != nil {
			return fmt.Errorf("start capture: %w", err)
		}
		defer ac.Stop()

		pr, pw := io.Pipe()
		defer pw.Close()
		log.Info("capturing playback audio", "device", deviceName(mmd),
			"rate", pcm.Rate, "channels", pcm.Channels, "bits", pcm.Bits, "float", pcm.Float)
		start(pcm, pr)

		frameBytes := int(wfx.NBlockAlign)
		silence := make([]byte, frameBytes*pcm.Rate/100) // 10 ms
		began := time.Now()
		var written int64 // frames
		for ctx.Err() == nil {
			var packet uint32
			if err := acc.GetNextPacketSize(&packet); err != nil {
				return fmt.Errorf("capture: %w", err)
			}
			got := false
			for packet > 0 {
				var data *byte
				var frames, flags uint32
				var devPos, qpcPos uint64
				if err := acc.GetBuffer(&data, &frames, &flags, &devPos, &qpcPos); err != nil {
					return fmt.Errorf("capture: %w", err)
				}
				n := int(frames) * frameBytes
				var chunk []byte
				if flags&wca.AUDCLNT_BUFFERFLAGS_SILENT != 0 || data == nil {
					chunk = make([]byte, n)
				} else {
					chunk = append([]byte(nil), unsafe.Slice(data, n)...)
				}
				if err := acc.ReleaseBuffer(frames); err != nil {
					return fmt.Errorf("capture: %w", err)
				}
				if _, err := pw.Write(chunk); err != nil {
					return nil // encoder gone; ctx is being cancelled
				}
				written += int64(frames)
				got = true
				if err := acc.GetNextPacketSize(&packet); err != nil {
					return fmt.Errorf("capture: %w", err)
				}
			}
			if !got {
				// Fill gaps with silence once we fall 20 ms behind real time.
				due := int64(time.Since(began).Seconds() * float64(pcm.Rate))
				for due-written > int64(pcm.Rate/50) {
					if _, err := pw.Write(silence); err != nil {
						return nil
					}
					written += int64(pcm.Rate / 100)
				}
			}
			time.Sleep(2 * time.Millisecond)
		}
		return nil
	})
}

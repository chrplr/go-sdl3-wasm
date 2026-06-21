// Verification harness for core SDL3 audio (not SDL3_mixer) on the
// WebAssembly (GOOS=js/GOARCH=wasm) path.
//
// It opens a default playback device stream and, on each click/keypress, queues
// a generated sine-wave tone. Browsers suspend audio until a user gesture, so
// you must click the window for sound to play.
//
// Run natively:  go run ./examples/audio
// Run in browser: go run ./cmd/wasmsdl serve ./examples/audio
package main

import (
	"encoding/binary"
	"math"

	"github.com/Zyko0/go-sdl3/bin/binsdl"
	"github.com/Zyko0/go-sdl3/sdl"
)

const (
	sampleRate = 48000
	freq       = 440.0 // A4
	seconds    = 1.0
	amplitude  = 0.2
)

// makeTone returns one second of mono 32-bit float PCM (little-endian) for a
// sine wave, matching the AUDIO_F32 stream format.
func makeTone() []byte {
	n := int(sampleRate * seconds)
	buf := make([]byte, n*4)
	for i := 0; i < n; i++ {
		t := float64(i) / float64(sampleRate)
		v := float32(amplitude * math.Sin(2*math.Pi*freq*t))
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}

func main() {
	defer binsdl.Load().Unload()
	defer sdl.Quit()

	if err := sdl.Init(sdl.INIT_VIDEO | sdl.INIT_AUDIO); err != nil {
		panic(err)
	}

	window, renderer, err := sdl.CreateWindowAndRenderer("examples/audio", 480, 160, 0)
	if err != nil {
		panic(err)
	}
	defer window.Destroy()
	defer renderer.Destroy()

	spec := sdl.AudioSpec{
		Format:   sdl.AUDIO_F32,
		Channels: 1,
		Freq:     sampleRate,
	}
	// nil callback: we push samples manually with PutData.
	stream := sdl.AUDIO_DEVICE_DEFAULT_PLAYBACK.OpenAudioDeviceStream(&spec, sdl.AudioStreamCallback(0))
	if stream == nil {
		panic("failed to open audio device stream")
	}
	defer stream.Destroy()
	if err := stream.ResumeDevice(); err != nil {
		panic(err)
	}

	tone := makeTone()

	sdl.RunLoop(func() error {
		var event sdl.Event
		for sdl.PollEvent(&event) {
			switch event.Type {
			case sdl.EVENT_QUIT:
				return sdl.EndLoop
			case sdl.EVENT_KEY_DOWN, sdl.EVENT_MOUSE_BUTTON_DOWN:
				// Queue another tone. (Browsers resume the audio context on
				// this same user gesture.)
				if err := stream.PutData(tone); err != nil {
					return err
				}
			}
		}

		queued, _ := stream.Queued()

		renderer.SetDrawColor(20, 20, 30, 255)
		renderer.Clear()
		renderer.SetDrawColor(220, 220, 220, 255)
		renderer.DebugText(20, 40, "Click or press a key to play a 440Hz tone.")
		if queued > 0 {
			renderer.DebugText(20, 70, "playing...")
		}
		renderer.Present()
		return nil
	})
}

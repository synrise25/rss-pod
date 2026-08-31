package audioinfo

import (
	"os"
	"testing"
	"time"
)

func TestMP3Duration(t *testing.T) {
	const frames = 100
	const frameSize = 417 // MPEG-1 Layer III, 128 kbps, 44.1 kHz, no padding.
	data := make([]byte, frames*frameSize)
	for offset := 0; offset < len(data); offset += frameSize {
		copy(data[offset:], []byte{0xff, 0xfb, 0x90, 0x00})
	}

	got, err := MP3Duration(data)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Duration(frames*1152) * time.Second / 44100
	if difference := got - want; difference < -time.Microsecond || difference > time.Microsecond {
		t.Fatalf("MP3Duration() = %s, want %s", got, want)
	}
}

func TestMP3DurationSkipsID3v2(t *testing.T) {
	const frameSize = 417
	data := append([]byte("ID3\x04\x00\x00\x00\x00\x00\x04meta"), make([]byte, frameSize)...)
	copy(data[14:], []byte{0xff, 0xfb, 0x90, 0x00})

	got, err := MP3Duration(data)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Duration(1152) * time.Second / 44100
	if got != want {
		t.Fatalf("MP3Duration() = %s, want %s", got, want)
	}
}

func TestMP3DurationRejectsInvalidData(t *testing.T) {
	if _, err := MP3Duration([]byte("not an mp3")); err == nil {
		t.Fatal("MP3Duration() accepted invalid data")
	}
}

func TestMP3DurationDemoAudio(t *testing.T) {
	data, err := os.ReadFile("../../web/demo.mp3")
	if err != nil {
		t.Fatal(err)
	}
	duration, err := MP3Duration(data)
	if err != nil {
		t.Fatal(err)
	}
	if duration < 29*time.Second || duration > 31*time.Second {
		t.Fatalf("MP3Duration(demo.mp3) = %s, want about 30s", duration)
	}
}

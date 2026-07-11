// Package media handles encoding of audio/image payloads on the field
// client side and chunk reassembly on the server side.
package media

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// EncodeAudioOpus shells out to the opusenc CLI to transcode a WAV file at
// srcWav into a low-bitrate Opus file, returning the encoded bytes.
// Shelling out avoids cgo Opus bindings and their build headaches.
func EncodeAudioOpus(srcWav string, bitrateKbps int) ([]byte, error) {
	if _, err := exec.LookPath("opusenc"); err != nil {
		return nil, fmt.Errorf("media: opusenc not found on PATH: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "vitallink-audio-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	outPath := filepath.Join(tmpDir, "out.opus")
	cmd := exec.Command("opusenc", "--bitrate", fmt.Sprintf("%d", bitrateKbps), srcWav, outPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("media: opusenc failed: %w: %s", err, out)
	}

	return os.ReadFile(outPath)
}

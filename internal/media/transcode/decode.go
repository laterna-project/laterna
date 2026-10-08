package transcode

import (
	"context"
	"os"
	"path/filepath"
)

// Decoder decodes the source on the graphics card, while scaling, tone mapping and burn-in stay in
// memory as in the CPU chain. It saves less than the chain on the card (GPU), which keeps every
// step there, but it works where that chain does not: in a container under Docker Desktop (WSL 2),
// the NVIDIA driver brings CUDA and NVENC but no Vulkan. Decoding is most of the CPU time of a
// transcode (two thirds for a 1080p 10-bit HEVC film). A stream the card cannot decode (10-bit
// H.264, for one) is decoded on the CPU by FFmpeg, as without a decoder.
type Decoder struct {
	// Name is "cuda" (NVDEC).
	Name string
	// args go before the input: frames come back to memory, or stay in it when the card cannot
	// decode the stream.
	args []string
	// test keeps the frames on the card, so that a decoder that does not work fails its test
	// instead of falling back to the CPU unnoticed.
	test []string
}

// decoders in order of preference. Only the ones tried on real hardware are listed.
var decoders = []Decoder{
	{
		Name: "cuda",
		args: []string{"-hwaccel", "cuda"},
		test: []string{"-hwaccel", "cuda", "-hwaccel_output_format", "cuda"},
	},
}

// DecoderByName returns the decoder with that name if it is one we know.
func DecoderByName(name string) (Decoder, bool) {
	for _, d := range decoders {
		if d.Name == name {
			return d, true
		}
	}
	return Decoder{}, false
}

// DetectDecoder tries each hardware decoder on one second of H.264 made by the chosen encoder,
// re-encoded with it the way playback would, and returns the first that works; nil if none does.
func DetectDecoder(ctx context.Context, ffmpeg string, e Encoder) *Decoder {
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	dir, err := os.MkdirTemp("", "laterna-decoder-")
	if err != nil {
		return nil
	}
	defer func() { _ = os.RemoveAll(dir) }()
	sample := filepath.Join(dir, "h264.mkv")
	args := append([]string{"-hide_banner", "-v", "error", "-nostdin"}, e.device...)
	args = append(args, "-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=24:duration=1", "-vf", videoFilter(e, 0, nil))
	if runQuiet(ctx, ffmpeg, append(append(args, e.encode()...), "-y", "file:"+sample)) != nil {
		return nil
	}
	for _, d := range decoders {
		args := append([]string{"-hide_banner", "-v", "error", "-nostdin"}, e.device...)
		args = append(args, d.test...)
		args = append(args, "-i", "file:"+sample, "-vf", "hwdownload,format=nv12,"+encoderFilter(e))
		if runQuiet(ctx, ffmpeg, append(append(args, e.encode()...), "-f", "null", "-")) == nil {
			return &d
		}
	}
	return nil
}

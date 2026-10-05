package transcode

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// The "on the card" chain: Vulkan decoding, scaling and HDR to SDR conversion by libplacebo on
// frames that stay on the card, then only the scaled frames are downloaded. For a 4K source this is
// twice as fast as decoding and scaling on the CPU. A stream the card cannot decode (10-bit H.264,
// for one) is decoded on the CPU and libplacebo then works on frames from memory, so the chain
// handles any format once it has passed the test. A run that fails anyway is restarted on the CPU
// (see app).

// gpuDevice is the Vulkan device shared by the decoder and the filters (set before the input).
var gpuDevice = []string{
	"-init_hw_device", "vulkan=gpu", "-filter_hw_device", "gpu",
	"-hwaccel", "vulkan", "-hwaccel_device", "gpu", "-hwaccel_output_format", "vulkan",
}

// gpuFilter scales to maxHeight and, if toneMap is set, converts HDR to BT.709 SDR (BT.2390 curve)
// on the card. Frames come back as 8-bit and are then put in the encoder's format.
func gpuFilter(e Encoder, maxHeight int, toneMap bool) string {
	f := "libplacebo=w=-2:h='min(ih," + strconv.Itoa(maxHeight) + ")'"
	if toneMap {
		f += ":tonemapping=bt.2390:colorspace=bt709:color_primaries=bt709:color_trc=bt709:range=tv"
	}
	return f + ":format=nv12,hwdownload,format=nv12," + encoderFilter(e)
}

// DetectGPU tries the chain on the card with the chosen encoder, on one second of an HDR10 test
// pattern (conversion included). The sample is encoded with FFV1, which every FFmpeg has.
func DetectGPU(ctx context.Context, ffmpeg string, e Encoder) bool {
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	dir, err := os.MkdirTemp("", "laterna-gpu-")
	if err != nil {
		return false
	}
	defer func() { _ = os.RemoveAll(dir) }()
	sample := filepath.Join(dir, "hdr10.mkv")
	if runQuiet(ctx, ffmpeg, []string{
		"-hide_banner", "-v", "error", "-nostdin", "-f", "lavfi", "-i", hdrTestSource,
		"-c:v", "ffv1", "-y", "file:" + sample,
	}) != nil {
		return false
	}
	args := append([]string{"-hide_banner", "-v", "error", "-nostdin"}, e.device...)
	args = append(args, gpuDevice...)
	args = append(args, "-i", "file:"+sample, "-vf", gpuFilter(e, 1080, true))
	args = append(args, e.encode()...)
	return runQuiet(ctx, ffmpeg, append(args, "-f", "null", "-")) == nil
}

func runQuiet(ctx context.Context, ffmpeg string, args []string) error {
	return trial(ctx, 30*time.Second, ffmpeg, args)
}

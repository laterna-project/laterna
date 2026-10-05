package transcode

import (
	"context"
	"fmt"
	"time"
)

// ToneMapper converts an HDR picture (PQ or HLG, BT.2020) to BT.709 SDR, for video re-encoded to
// 8-bit H.264. It gets frames that are already scaled down and returns frames in memory, ready for
// the encoder.
type ToneMapper struct {
	// Name is "libplacebo", "opencl", "vaapi" or "zscale".
	Name string
	// Hardware means the conversion runs on the GPU (on the CPU otherwise).
	Hardware bool
	// device holds the filter device arguments (set before the input).
	device []string
	filter string
}

// Filter returns the filter chain of the conversion (scaled frames in).
func (t ToneMapper) Filter() string { return t.filter }

// toneMappers in order of preference. libplacebo (Vulkan) gives the best picture (BT.2390 curve)
// and handles Dolby Vision. OpenCL and VAAPI are fast. zscale runs on the CPU and works everywhere
// (GPL builds), only slower.
var toneMappers = []ToneMapper{
	{
		Name: "libplacebo", Hardware: true,
		device: []string{"-init_hw_device", "vulkan=tm", "-filter_hw_device", "tm"},
		filter: "libplacebo=tonemapping=bt.2390:colorspace=bt709:color_primaries=bt709:color_trc=bt709:range=tv:format=yuv420p",
	},
	{
		Name: "opencl", Hardware: true,
		device: []string{"-init_hw_device", "opencl=tm", "-filter_hw_device", "tm"},
		filter: "format=p010le,hwupload,tonemap_opencl=tonemap=hable:desat=0:transfer=bt709:matrix=bt709:primaries=bt709:range=tv:format=nv12,hwdownload,format=nv12",
	},
	{
		Name: "vaapi", Hardware: true,
		device: []string{"-init_hw_device", "vaapi=tm:/dev/dri/renderD128", "-filter_hw_device", "tm"},
		filter: "format=p010le,hwupload,tonemap_vaapi=format=nv12:t=bt709:m=bt709:p=bt709,hwdownload,format=nv12",
	},
	{
		Name:   "zscale",
		filter: "zscale=transfer=linear:npl=100,format=gbrpf32le,zscale=primaries=bt709,tonemap=tonemap=hable:desat=0,zscale=transfer=bt709:matrix=bt709:range=tv,format=yuv420p",
	},
}

// ToneMapperByName returns the tone mapper with that name if it is one we know.
func ToneMapperByName(name string) (ToneMapper, bool) {
	for _, tm := range toneMappers {
		if tm.Name == name {
			return tm, true
		}
	}
	return ToneMapper{}, false
}

// hdrTestSource is one second of an HDR10 test pattern (10 bits, BT.2020, PQ), like a real source.
const hdrTestSource = "testsrc2=size=1280x720:rate=24:duration=1,format=yuv420p10le," +
	"setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc:range=tv"

// DetectToneMappers tries each HDR to SDR conversion with the chosen encoder, the way playback
// would, and keeps those that work, in order of preference. A missing driver, a missing device, a
// conversion that does not go with the encoder: the next one takes over. zscale, on the CPU, is the
// last resort.
func DetectToneMappers(ctx context.Context, ffmpeg string, e Encoder) []ToneMapper {
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	var out []ToneMapper
	for _, tm := range toneMappers {
		if err := tryToneMap(ctx, ffmpeg, e, tm); err == nil {
			out = append(out, tm)
		}
	}
	return out
}

func tryToneMap(ctx context.Context, ffmpeg string, e Encoder, tm ToneMapper) error {
	args := append([]string{"-hide_banner", "-v", "error", "-nostdin"}, e.device...)
	args = append(args, tm.device...)
	args = append(args, "-f", "lavfi", "-i", hdrTestSource, "-vf", videoFilter(e, 0, &tm))
	args = append(args, e.encode()...)
	args = append(args, "-f", "null", "-")
	if err := trial(ctx, 30*time.Second, ffmpeg, args); err != nil {
		return fmt.Errorf("%s: %w", tm.Name, err)
	}
	return nil
}

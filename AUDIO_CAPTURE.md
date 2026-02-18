# Audio Capture Implementation

## Overview

This project now uses **platform-native audio capture APIs** for the best compatibility and performance:

- **Windows**: WASAPI (Windows Audio Session API) with loopback mode
- **Linux**: miniaudio (CGO) with PulseAudio/PipeWire, fallback to FFmpeg
- **macOS**: miniaudio (CGO) with CoreAudio, fallback to FFmpeg

## What Changed

### Windows - WASAPI (New!)

Windows now uses **WASAPI loopback capture**, the same approach used by OBS Studio:

- **No CGO required** - Pure Go implementation using `github.com/cyberxnomad/wasapi`
- **System audio capture** - Captures what you hear (game audio, voice chat, etc.)
- **Windows 10/11 compatible** - Uses `AUDCLNT_STREAMFLAGS_LOOPBACK`
- **Automatic device selection** - Defaults to system default output device

### Why WASAPI?

OBS Studio uses WASAPI because:
1. **Native Windows API** - No external dependencies or drivers needed
2. **Process-level audio capture** - Can capture specific application audio
3. **Loopback mode** - Captures system output (what you hear) without quality loss
4. **Low latency** - Direct access to audio stream
5. **No CGO required** - Easy to build and distribute

## Usage

### List Available Audio Devices

```bash
# Windows
cs-translate.exe -list-audio-devices

# Linux
cs-translate -list-audio-devices
```

### Specify Audio Device

```bash
# Windows - Use specific output device
cs-translate.exe -audiodevice "Speakers (Realtek Audio)" -voice

# Linux - Use specific PulseAudio monitor
cs-translate -audiodevice "alsa_output.pci-0000_00_1b.0.analog-stereo.monitor" -voice
```

### Default Behavior

- **Windows**: Automatically uses default output device (speakers/headphones)
- **Linux**: Automatically uses default monitor source
- **Fallback**: If native capture fails, automatically falls back to FFmpeg

## Architecture

### File Structure

```
audio/
├── audio.go                      # Common interface
├── wasapi_listener.go            # Windows WASAPI implementation
├── wasapi_stub.go                # Non-Windows stub
├── malgo_listener.go             # Linux/macOS miniaudio (CGO)
├── malgo_listener_fallback.go    # Non-CGO fallback (Linux/macOS)
├── malgo_listener_windows.go     # Windows CGO fallback to WASAPI
├── listener.go                   # FFmpeg fallback implementation
├── devices_windows.go            # Windows device enumeration
└── devices_other.go              # Linux/macOS device enumeration
```

### Build Tags

- `windows` - Windows-specific WASAPI implementation
- `!windows` - Linux/macOS miniaudio/FFmpeg
- `cgo` - Miniaudio requires CGO
- `!cgo` - Falls back to FFmpeg or WASAPI (Windows)

## Fallback Chain

### Windows
1. **WASAPI** (native, no CGO) ← **Primary**
2. **FFmpeg** (dshow/wasapi) ← Fallback

### Linux/macOS
1. **miniaudio** (CGO, PulseAudio/PipeWire) ← Primary
2. **FFmpeg** (pulse/avfoundation) ← Fallback

## Troubleshooting

### Windows

**Issue**: "No audio devices found"
- Ensure audio output device is connected and enabled
- Run as Administrator if having permission issues

**Issue**: "Silent capture"
- Make sure system audio is playing during capture
- WASAPI loopback captures output, not microphone

**Issue**: Build fails with WASAPI errors
- Ensure you have `golang.org/x/sys/windows` dependency
- Run `go mod tidy`

### Linux

**Issue**: "Failed to initialize miniaudio"
- Install PulseAudio development files: `sudo apt install libpulse-dev`
- Or use FFmpeg fallback

**Issue**: "No such device"
- List available sources: `pactl list sources short`
- Use the monitor source: `<sink_name>.monitor`

## Dependencies

### Windows
- `github.com/cyberxnomad/wasapi` - WASAPI bindings for Go
- `golang.org/x/sys/windows` - Windows system calls

### Linux/macOS
- `github.com/gen2brain/malgo` - miniaudio bindings (optional, requires CGO)
- `ffmpeg` - Fallback audio capture (required if miniaudio unavailable)

## Technical Details

### WASAPI Loopback

WASAPI loopback mode captures the audio stream that would be sent to the output device:

```go
audioClient.Initialize(
    AUDCLNT_SHAREMODE_SHARED,
    AUDCLNT_STREAMFLAGS_LOOPBACK,  // ← Loopback mode
    REFTIMES_PER_SEC, 0, &format, nil,
)
```

This captures:
- Game audio
- Voice chat (your teammates)
- System sounds
- Music/media playback

### Audio Format

All platforms convert to a consistent format for Whisper:
- **Sample Rate**: 16kHz (downsampled from source)
- **Format**: 16-bit PCM
- **Channels**: Mono (downmixed from stereo)
- **Segment Duration**: 5 seconds

## Comparison with Previous Implementation

| Feature | Old (miniaudio) | New (WASAPI) |
|---------|----------------|--------------|
| Windows Support | ❌ Unreliable | ✅ Native |
| CGO Required | ✅ Yes | ❌ No |
| Build Complexity | High | Low |
| Loopback Capture | Limited | ✅ Full support |
| Device Enumeration | Basic | ✅ Complete |
| OBS Compatibility | Different | ✅ Same approach |

## References

- [OBS Studio Audio Capture](https://github.com/obsproject/obs-studio)
- [WASAPI Loopback Documentation](https://docs.microsoft.com/en-us/windows/win32/coreaudio/capturing-a-stream)
- [cyberxnomad/wasapi](https://github.com/cyberxnomad/wasapi)

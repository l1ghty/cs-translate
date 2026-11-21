# /// script
# requires-python = ">=3.10,<3.14"
# dependencies = [
#   "faster-whisper>=1.2,<2",
#   "sentencepiece>=0.2",
#   "soundcard>=0.4.4",
#   "numpy>=1.26",
#   "msvc-runtime; sys_platform == 'win32'",
#   # Windows per-application loopback (native extension; only it is used)
#   "proc-tap>=1.1.1,<2; sys_platform == 'win32' and platform_machine == 'AMD64'",
#   # RNNoise denoiser (ships librnnoise as a wheel). Only platforms that have a
#   # wheel, or uv could not resolve the script at all.
#   "pyrnnoise>=0.4.6,<0.5; (sys_platform == 'win32' and platform_machine == 'AMD64') or (sys_platform == 'linux' and (platform_machine == 'x86_64' or platform_machine == 'aarch64'))",
# ]
# # pyrnnoise declares ffmpeg/matplotlib dependencies for its file tools; only its
# # small ctypes wrapper is used here.
# [tool.uv]
# exclude-dependencies = ["audiolab", "av", "matplotlib", "soxr", "soundfile", "smart-open", "jinja2", "humanize", "requests", "click", "scipy"]
# ///
"""cs-translate speech worker.

Captures system audio (loopback), finds speech with Silero VAD and transcribes
it with faster-whisper (CTranslate2, int8). Everything stays in memory: no
temp files, no ffmpeg, no PyTorch.

The process is designed to stay out of the game's way: it lowers its own
scheduling priority, runs on a small fixed number of CPU threads and only
runs Whisper when the VAD has found speech (live mode) or when asked to
(capture mode).

Protocol (driven by the Go host):
  stdout  one JSON object per line ("event": status | progress | ready |
          job_start | transcript | job_done | devices | error | warning)
  stdin   one command per line: "capture <seconds>", "prompt <text>",
          "translate <id> <lang> <text>" (fast offline translation to English,
          answered with a "translation" event) or "quit". EOF = quit.
  stderr  human-readable logs (passed through to the terminal).
"""

import argparse
import json
import os
import queue
import re
import sys
import threading
import time
import warnings

SAMPLE_RATE = 16000
VAD_FRAME = 512  # 32 ms at 16 kHz, the frame size Silero VAD expects
VAD_CONTEXT = 64

# Phrases Whisper is known to hallucinate on noise / music.
HALLUCINATIONS = {
    "thank you.", "thank you", "thanks for watching!", "thanks for watching.",
    "thank you for watching.", "thank you for watching!", "you", "bye.",
    "subtitles by the amara.org community", "please subscribe.",
    "продолжение следует...", "субтитры сделал dimatorzok",
    "редактор субтитров а.семкин корректор а.егорова",
    "untertitel der amara.org-community", "sous-titres réalisés par la communauté d'amara.org",
    "ご視聴ありがとうございました", "字幕由amara.org社区提供",
}

_out_lock = threading.Lock()
_proto = None  # protocol stream (real stdout); sys.stdout is redirected to stderr
_dll_dirs = []  # keep add_dll_directory handles alive


def emit(event, **fields):
    fields["event"] = event
    line = json.dumps(fields, ensure_ascii=False)
    with _out_lock:
        _proto.write(line + "\n")
        _proto.flush()


def log(msg):
    print(f"[speech] {msg}", file=sys.stderr, flush=True)


def status(msg):
    """Tell the host what is happening (shown next to its spinner)."""
    emit("status", message=msg)


def lower_priority():
    """Make sure the game always wins the CPU scheduler."""
    try:
        if sys.platform == "win32":
            import ctypes

            BELOW_NORMAL_PRIORITY_CLASS = 0x00004000
            k32 = ctypes.windll.kernel32
            k32.SetPriorityClass(k32.GetCurrentProcess(), BELOW_NORMAL_PRIORITY_CLASS)
        else:
            os.nice(10)
    except Exception as e:  # never fatal
        log(f"could not lower process priority: {e}")


def ensure_msvc_runtime():
    """Make sure the Microsoft C++ runtime (msvcp140.dll) can be found on Windows.

    ctranslate2.dll needs it, but the Python that uv installs only ships the C
    runtime (vcruntime140*.dll), and many PCs don't have the "Visual C++
    Redistributable" installed. The msvc-runtime wheel puts the DLLs into the
    environment; load them from there when the system has none, so loading
    ctranslate2 never fails with "Could not find module ... ctranslate2.dll".
    """
    if sys.platform != "win32":
        return
    import ctypes

    try:
        ctypes.WinDLL("msvcp140.dll")  # system-wide Visual C++ Redistributable
        return
    except OSError:
        pass
    dirs = [d for d in (sys.prefix, os.path.join(sys.prefix, "Scripts"))
            if os.path.isfile(os.path.join(d, "msvcp140.dll"))]
    for d in dirs:
        _dll_dirs.append(os.add_dll_directory(d))
    for d in dirs:
        try:
            # Once loaded, every later import of msvcp140.dll resolves to this
            # copy. vcruntime140.dll is already loaded by Python itself.
            ctypes.WinDLL(os.path.join(d, "msvcp140.dll"))
            return
        except OSError as e:
            log(f"could not load the bundled C++ runtime from {d}: {e}")
    emit("warning", message="Microsoft Visual C++ runtime (msvcp140.dll) not found; if speech "
         "recognition fails to start, install the Visual C++ Redistributable: "
         "https://aka.ms/vs/17/release/vc_redist.x64.exe")


def preload_cuda_libs():
    """Make pip-installed CUDA libraries (nvidia-cublas-cu12, nvidia-cudnn-cu12) visible."""
    try:
        import nvidia  # namespace package from the nvidia-* wheels
    except ImportError:
        return
    import glob

    roots = list(getattr(nvidia, "__path__", []))
    if sys.platform == "win32":
        for root in roots:
            for d in glob.glob(os.path.join(root, "*", "bin")):
                _dll_dirs.append(os.add_dll_directory(d))
                os.environ["PATH"] = d + os.pathsep + os.environ.get("PATH", "")
    else:
        import ctypes

        for pattern in ("cublas/lib/libcublasLt.so.*", "cublas/lib/libcublas.so.*", "cudnn/lib/libcudnn*.so.*"):
            for root in roots:
                for lib in sorted(glob.glob(os.path.join(root, pattern))):
                    try:
                        ctypes.CDLL(lib, mode=ctypes.RTLD_GLOBAL)
                    except OSError:
                        pass


# --------------------------------------------------------------------------
# Audio devices
# --------------------------------------------------------------------------

def open_soundcard():
    """Import soundcard and make sure the OS audio system is reachable."""
    try:
        import soundcard as sc

        # soundcard forces its warnings to "always" on import. A discontinuity
        # (a few ms of audio lost, e.g. when playback resumes after silence)
        # is harmless for speech recognition, so keep it out of the console.
        warnings.filterwarnings("ignore", message="data discontinuity")
        sc.all_microphones(include_loopback=True)
        return sc
    except Exception as e:
        hint = ""
        if sys.platform.startswith("linux"):
            hint = " - is PulseAudio or PipeWire (pipewire-pulse) running?"
        raise RuntimeError(f"cannot access the audio system ({type(e).__name__}: {e}){hint}") from e


LOOPBACK_HINTS = ("blackhole", "loopback", "soundflower", "vb-audio", "cable output", "monitor")


def list_devices(sc):
    devices = []
    for m in sc.all_microphones(include_loopback=True):
        devices.append({"name": m.name, "id": str(m.id), "loopback": bool(getattr(m, "isloopback", False))})
    try:
        default = sc.default_speaker().name
    except Exception:
        default = ""
    return devices, default


def resolve_device(sc, wanted):
    """Return a soundcard microphone that records what the user hears."""
    mics = sc.all_microphones(include_loopback=True)
    if wanted and wanted != "default":
        w = wanted.lower()
        for m in mics:
            if str(m.id) == wanted or m.name == wanted:
                return m
        for m in mics:
            if w in m.name.lower() or w in str(m.id).lower():
                return m
        raise RuntimeError(f"audio device '{wanted}' not found (use -list-audio-devices)")

    # Windows (WASAPI) and Linux (PulseAudio/PipeWire): loopback of the default output.
    try:
        speaker = sc.default_speaker()
        for m in mics:
            if getattr(m, "isloopback", False) and (m.name == speaker.name or str(speaker.id) in str(m.id)):
                return m
        return sc.get_microphone(str(speaker.name), include_loopback=True)
    except Exception:
        pass

    # macOS has no native loopback; look for a virtual loopback device (e.g. BlackHole).
    for m in mics:
        if any(h in m.name.lower() for h in LOOPBACK_HINTS):
            return m
    hint = ""
    if sys.platform == "darwin":
        hint = " On macOS install a loopback driver first: brew install blackhole-2ch"
    raise RuntimeError("no loopback audio device found." + hint)


# --------------------------------------------------------------------------
# Audio pipeline
# --------------------------------------------------------------------------


class RingBuffer:
    """Fixed-size in-memory history of the last N seconds of audio."""

    def __init__(self, np, seconds):
        self.np = np
        self.buf = np.zeros(int(seconds * SAMPLE_RATE), dtype=np.float32)
        self.pos = 0
        self.filled = 0
        self.lock = threading.Lock()

    def write(self, x):
        n = len(x)
        size = len(self.buf)
        with self.lock:
            if n >= size:
                self.buf[:] = x[-size:]
                self.pos = 0
                self.filled = size
                return
            end = self.pos + n
            if end <= size:
                self.buf[self.pos:end] = x
            else:
                k = size - self.pos
                self.buf[self.pos:] = x[:k]
                self.buf[: n - k] = x[k:]
            self.pos = end % size
            self.filled = min(size, self.filled + n)

    def last(self, seconds):
        with self.lock:
            n = min(int(seconds * SAMPLE_RATE), self.filled)
            if n <= 0:
                return self.np.zeros(0, dtype=self.np.float32)
            start = (self.pos - n) % len(self.buf)
            if start + n <= len(self.buf):
                return self.buf[start:start + n].copy()
            return self.np.concatenate((self.buf[start:], self.buf[: (start + n) - len(self.buf)]))


class VadSegmenter:
    """Streaming Silero VAD that turns a continuous stream into utterances."""

    def __init__(self, np, on_utterance, threshold=0.5, min_silence_s=0.3,
                 min_speech_s=0.25, max_speech_s=5.0, pad_s=0.2, overlap_s=0.2,
                 on_partial=None, partial_every_s=1.0):
        from faster_whisper.vad import get_vad_model

        self.np = np
        self.session = get_vad_model().session  # single-threaded ONNX session
        self.on_utterance = on_utterance  # (audio, utterance id)
        self.on_partial = on_partial      # (audio so far, utterance id), about every partial_every_s of speech
        self.partial_every = int(partial_every_s * SAMPLE_RATE)
        self.last_partial = 0
        self.utt = 0                      # id of the current utterance
        self.threshold = threshold
        self.neg_threshold = max(threshold - 0.15, 0.01)
        self.min_silence = int(min_silence_s * SAMPLE_RATE / VAD_FRAME)
        self.min_speech = int(min_speech_s * SAMPLE_RATE)
        self.max_speech = int(max_speech_s * SAMPLE_RATE)
        self.pad_frames = max(1, int(pad_s * SAMPLE_RATE / VAD_FRAME))
        self.overlap_frames = max(1, int(overlap_s * SAMPLE_RATE / VAD_FRAME))
        self.h = np.zeros((1, 1, 128), dtype=np.float32)
        self.c = np.zeros((1, 1, 128), dtype=np.float32)
        self.ctx = np.zeros(VAD_CONTEXT, dtype=np.float32)
        self.pending = np.zeros(0, dtype=np.float32)
        self.preroll = []  # last few non-speech frames, prepended to speech
        self.speech = []
        self.speech_len = 0
        self.silence = 0
        self.active = False

    def prob(self, frame):
        x = self.np.concatenate((self.ctx, frame))[None, :]
        out, self.h, self.c = self.session.run(None, {"input": x, "h": self.h, "c": self.c})
        self.ctx = frame[-VAD_CONTEXT:]
        return float(out.reshape(-1)[0])

    def feed(self, x):
        np = self.np
        if len(self.pending):
            x = np.concatenate((self.pending, x))
        n = len(x) // VAD_FRAME * VAD_FRAME
        self.pending = x[n:].copy()
        for i in range(0, n, VAD_FRAME):
            self._frame(x[i:i + VAD_FRAME])

    def _frame(self, frame):
        p = self.prob(frame)
        if not self.active:
            self.preroll.append(frame)
            if len(self.preroll) > self.pad_frames:
                self.preroll.pop(0)
            if p >= self.threshold:
                self.active = True
                self.utt += 1
                self.speech = list(self.preroll)
                self.speech_len = sum(len(f) for f in self.speech)
                self.last_partial = self.speech_len
                self.preroll = []
                self.silence = 0
            return

        self.speech.append(frame)
        self.speech_len += len(frame)
        if p < self.neg_threshold:
            self.silence += 1
        elif p >= self.threshold:
            self.silence = 0

        if self.silence >= self.min_silence:
            self._finish()
        elif self.speech_len >= self.max_speech:
            self._finish(keep_overlap=True)
        elif (self.on_partial is not None and self.silence == 0
              and self.speech_len - self.last_partial >= self.partial_every):
            self.last_partial = self.speech_len
            self.on_partial(self.np.concatenate(self.speech), self.utt)

    def _finish(self, keep_overlap=False):
        """Hand over the utterance. With keep_overlap the speaker is still
        talking: the next piece starts with the last moments of this one, so a
        word cut at the boundary is heard whole by Whisper."""
        np = self.np
        # keep a little trailing silence, drop the rest
        drop = max(0, self.silence - self.pad_frames)
        frames = self.speech[: len(self.speech) - drop] if drop else self.speech
        audio = np.concatenate(frames) if frames else np.zeros(0, dtype=np.float32)
        voiced = self.speech_len - self.silence * VAD_FRAME
        utt = self.utt
        tail = self.speech[-self.overlap_frames:] if keep_overlap else []
        self.active = keep_overlap
        self.speech = list(tail)
        self.speech_len = sum(len(f) for f in tail)
        self.last_partial = self.speech_len
        self.silence = 0
        if keep_overlap:
            self.utt += 1  # the speaker goes on: the rest is a new utterance
        if voiced >= self.min_speech:
            self.on_utterance(audio, utt)


# --------------------------------------------------------------------------
# Noise suppression (RNNoise)
# --------------------------------------------------------------------------

RNNOISE_RATE = 48000
RNNOISE_FRAME = 480  # 10 ms at 48 kHz
_UP = RNNOISE_RATE // SAMPLE_RATE  # 3


def load_rnnoise():
    """Load pyrnnoise's small ctypes wrapper without importing the package
    (whose __init__ needs ffmpeg bindings that aren't installed). None if the
    library isn't available on this platform."""
    try:
        import importlib.util

        spec = importlib.util.find_spec("pyrnnoise")  # finds the package without running it
        if spec is None or not spec.submodule_search_locations:
            return None
        path = os.path.join(list(spec.submodule_search_locations)[0], "rnnoise.py")
        mod_spec = importlib.util.spec_from_file_location("_cs_rnnoise", path)
        mod = importlib.util.module_from_spec(mod_spec)
        mod_spec.loader.exec_module(mod)
        return mod
    except Exception as e:
        log(f"RNNoise unavailable ({type(e).__name__}: {e})")
        return None


class Denoiser:
    """RNNoise noise suppression for 16 kHz mono float32 audio (one instance
    per clip; chunks may also be streamed through process()).

    RNNoise works on 10 ms frames at 48 kHz, so audio is upsampled 3x (linear),
    denoised, then low-passed and decimated back to 16 kHz. It removes steady
    noise (gunfire spray, ambience, ringing)."""

    TAPS = 64

    def __init__(self, np, rnnoise):
        self.np = np
        self.rn = rnnoise
        self.state = rnnoise.create()
        n = np.arange(self.TAPS) - (self.TAPS - 1) / 2
        h = np.sinc(2 * 7600 / RNNOISE_RATE * n) * np.hanning(self.TAPS)  # 7.6 kHz low-pass
        self.h = (h / h.sum()).astype(np.float32)
        self.pending = np.zeros(0, dtype=np.float32)       # 16 kHz samples not yet a full frame
        self.prev = np.float32(0.0)                         # last input sample (interpolation)
        self.tail = np.zeros(self.TAPS - 1, dtype=np.float32)  # last 48 kHz samples (filter history)

    def __del__(self):
        try:
            self.rn.destroy(self.state)
        except Exception:
            pass

    def process(self, x):
        """Denoise a chunk; returns the samples that are ready (may be fewer or
        more than given, because audio is handled in 10 ms frames)."""
        np = self.np
        if len(self.pending):
            x = np.concatenate((self.pending, x))
        frame16 = RNNOISE_FRAME // _UP
        n = len(x) // frame16 * frame16
        self.pending = x[n:].copy()
        if n == 0:
            return np.zeros(0, dtype=np.float32)
        x = x[:n]
        ext = np.concatenate(([self.prev], x))
        a, b = ext[:-1], ext[1:]
        self.prev = x[-1]
        up = (a[:, None] + (b - a)[:, None] * np.array([1 / 3, 2 / 3, 1.0], dtype=np.float32)).ravel()
        buf = np.ascontiguousarray(up * 32768.0, dtype=np.float32)  # RNNoise wants int16-range floats
        import ctypes

        fptr = ctypes.POINTER(ctypes.c_float)
        for i in range(0, len(buf), RNNOISE_FRAME):
            frame = buf[i:i + RNNOISE_FRAME]
            self.rn.lib.rnnoise_process_frame(self.state, frame.ctypes.data_as(fptr), frame.ctypes.data_as(fptr))
        buf /= 32768.0
        ext48 = np.concatenate((self.tail, buf))
        self.tail = ext48[-(self.TAPS - 1):].copy()
        filtered = np.convolve(ext48, self.h, mode="valid")  # same length as buf
        return np.clip(filtered[::_UP], -1.0, 1.0).astype(np.float32)

    def process_all(self, x):  # noqa: D401
        """Denoise a complete clip (e.g. an F9 capture) and return it at its original length."""
        np = self.np
        out = self.process(np.concatenate((x, np.zeros(RNNOISE_FRAME // _UP, dtype=np.float32))))
        return out[: len(x)] if len(out) >= len(x) else np.concatenate((out, np.zeros(len(x) - len(out), dtype=np.float32)))


def denoise(np, rnnoise, audio):
    """Clean one utterance or capture before Whisper. Done on the audio that is
    transcribed only, never on the continuous stream: RNNoise costs ~6% of a
    core when running all the time, but this runs only while someone talks."""
    if rnnoise is None or len(audio) == 0:
        return audio
    try:
        return Denoiser(np, rnnoise).process_all(audio)
    except Exception as e:  # never fatal: fall back to the raw audio
        log(f"noise suppression failed: {e}")
        return audio


# --------------------------------------------------------------------------
# Per-application capture (Windows "Application Loopback")
# --------------------------------------------------------------------------

# Windows build that introduced process loopback (Windows 10 20H1+ / 11).
APP_LOOPBACK_MIN_BUILD = 20438


def app_loopback_supported():
    """True on a Windows build that can capture a single process's audio."""
    return sys.platform == "win32" and sys.getwindowsversion().build >= APP_LOOPBACK_MIN_BUILD


def find_pid(name):
    """Process id of the first running process named `name` (e.g. cs2.exe), or None."""
    if sys.platform != "win32":
        return None
    import ctypes
    from ctypes import wintypes

    class ENTRY(ctypes.Structure):
        _fields_ = [("dwSize", wintypes.DWORD), ("cntUsage", wintypes.DWORD),
                    ("th32ProcessID", wintypes.DWORD), ("th32DefaultHeapID", ctypes.c_void_p),
                    ("th32ModuleID", wintypes.DWORD), ("cntThreads", wintypes.DWORD),
                    ("th32ParentProcessID", wintypes.DWORD), ("pcPriClassBase", wintypes.LONG),
                    ("dwFlags", wintypes.DWORD), ("szExeFile", ctypes.c_wchar * 260)]

    k32 = ctypes.windll.kernel32
    k32.CreateToolhelp32Snapshot.restype = wintypes.HANDLE
    snap = k32.CreateToolhelp32Snapshot(0x2, 0)  # TH32CS_SNAPPROCESS
    if snap in (None, wintypes.HANDLE(-1).value):
        return None
    try:
        entry = ENTRY()
        entry.dwSize = ctypes.sizeof(ENTRY)
        ok = k32.Process32FirstW(wintypes.HANDLE(snap), ctypes.byref(entry))
        while ok:
            if entry.szExeFile.lower() == name.lower():
                return int(entry.th32ProcessID)
            ok = k32.Process32NextW(wintypes.HANDLE(snap), ctypes.byref(entry))
    finally:
        k32.CloseHandle(wintypes.HANDLE(snap))
    return None


def load_proctap_native():
    """Load the ProcTap C++ extension without importing the proctap package
    (its __init__ needs scipy, which isn't installed). None if unavailable."""
    try:
        import glob
        import importlib.machinery
        import importlib.util

        spec = importlib.util.find_spec("proctap")
        if spec is None or not spec.submodule_search_locations:
            return None
        files = glob.glob(os.path.join(list(spec.submodule_search_locations)[0], "_native*.pyd"))
        if not files:
            return None
        # the module name must be "_native": the extension's init function is PyInit__native
        mod_spec = importlib.util.spec_from_file_location(
            "_native", files[0], loader=importlib.machinery.ExtensionFileLoader("_native", files[0]))
        mod = importlib.util.module_from_spec(mod_spec)
        mod_spec.loader.exec_module(mod)
        return mod
    except Exception as e:
        log(f"application loopback unavailable ({type(e).__name__}: {e})")
        return None


class Resampler16k:
    """Streaming mono resampler to 16 kHz: a low-pass (below 16 kHz / 2) then
    linear interpolation, keeping its position between chunks."""

    TAPS = 64

    def __init__(self, np, rate):
        self.np = np
        self.rate = rate
        self.step = rate / SAMPLE_RATE
        n = np.arange(self.TAPS) - (self.TAPS - 1) / 2
        h = np.sinc(2 * 7600 / rate * n) * np.hanning(self.TAPS)
        self.h = (h / h.sum()).astype(np.float32)
        self.tail = np.zeros(self.TAPS - 1, dtype=np.float32)
        self.rest = np.zeros(0, dtype=np.float32)  # filtered samples not yet consumed
        self.pos = 0.0                              # next output position within `rest`

    def process(self, x):
        np = self.np
        if self.rate == SAMPLE_RATE:
            return x.astype(np.float32, copy=False)
        if len(x) == 0:
            return np.zeros(0, dtype=np.float32)
        ext = np.concatenate((self.tail, x.astype(np.float32, copy=False)))
        self.tail = ext[-(self.TAPS - 1):].copy()
        sig = np.concatenate((self.rest, np.convolve(ext, self.h, mode="valid")))
        count = int((len(sig) - 1 - self.pos) // self.step) + 1 if len(sig) - 1 >= self.pos else 0
        if count <= 0:
            self.rest = sig
            return np.zeros(0, dtype=np.float32)
        idx = self.pos + np.arange(count) * self.step
        out = np.interp(idx, np.arange(len(sig)), sig).astype(np.float32)
        nxt = self.pos + count * self.step
        keep = min(int(nxt), len(sig))  # the next position may lie beyond this chunk
        self.rest = sig[keep:]
        self.pos = nxt - keep
        return out


class AppCapture:
    """Audio of one application (and its child processes) as 16 kHz mono
    float32, using Windows Application Loopback. Other programs (Discord,
    music, videos) are not captured."""

    def __init__(self, np, native, pid, clock=time.monotonic):
        self.np = np
        self.pid = pid
        self.clock = clock
        self.cap = native.ProcessLoopback(pid)
        fmt = self.cap.get_format()
        self.rate, self.channels, self.bits = fmt["sample_rate"], fmt["channels"], fmt["bits_per_sample"]
        self.resampler = Resampler16k(np, self.rate)
        self.t0 = None
        self.delivered = 0

    def start(self):
        self.cap.start()
        if not self.cap.is_process_specific():
            # Windows quietly fell back to recording everything: that is what we avoid
            err = self.cap.get_last_error()
            self.stop()
            raise RuntimeError("Windows could not capture a single application" + (f" ({err})" if err else ""))
        self.t0 = self.clock()

    def stop(self):
        try:
            self.cap.stop()
        except Exception:
            pass

    def _mono16k(self, raw):
        np = self.np
        if self.bits == 32:
            x = np.frombuffer(raw, dtype=np.float32)
        elif self.bits == 16:
            x = np.frombuffer(raw, dtype=np.int16).astype(np.float32) / 32768.0
        else:
            raise RuntimeError(f"unsupported capture format ({self.bits} bit)")
        usable = len(x) // self.channels * self.channels
        x = x[:usable].reshape(-1, self.channels).mean(axis=1) if self.channels > 1 else x[:usable]
        return self.resampler.process(x)

    def read(self):
        """New 16 kHz samples. A process only produces audio while it plays
        something, but the voice detector needs the silence in between to tell
        when a sentence ended, so gaps are filled with silence in real time."""
        np = self.np
        raw = self.cap.read()
        now = self.clock()
        out = self._mono16k(raw) if raw else np.zeros(0, dtype=np.float32)
        self.delivered += len(out)
        expected = (now - self.t0) * SAMPLE_RATE
        if len(out) == 0:
            gap = int(expected - self.delivered)
            if gap >= SAMPLE_RATE // 10:
                self.delivered += gap
                return np.zeros(gap, dtype=np.float32)
        elif self.delivered > expected + SAMPLE_RATE:  # data ran ahead of the clock: re-sync
            self.t0 = now - self.delivered / SAMPLE_RATE
        return out


def capture_loop(sc, np, args, ring, segmenter, stop, native=None):
    """Feed the ring buffer and VAD. While the app named by --app-audio is
    running (Windows), only its audio is used; otherwise (or if that can't
    work) everything the user hears."""
    block = SAMPLE_RATE // 10  # 100 ms per read keeps wake-ups (and CPU) low
    app = args.app_audio if not args.audio_device and app_loopback_supported() else ""
    warned = False
    failed_pid = None  # a process that app capture didn't work for; don't retry it

    def deliver(data):
        ring.write(data)
        if segmenter is not None:
            segmenter.feed(data)

    while not stop.is_set():
        try:
            pid = find_pid(app) if app else None
            if pid and pid != failed_pid:
                native = native or load_proctap_native()
                try:
                    if native is None:
                        raise RuntimeError("application loopback component not available")
                    cap = AppCapture(np, native, pid)
                    cap.start()
                except Exception as e:
                    failed_pid = pid
                    emit("warning", message=f"cannot capture only {app} ({e}); using all system audio")
                    continue
                log(f"capturing audio from: {app} only (pid {pid})")
                last_check = time.monotonic()
                try:
                    while not stop.is_set():
                        data = cap.read()
                        if len(data):
                            deliver(data)
                        stop.wait(0.02)
                        if time.monotonic() - last_check >= 1.0:
                            last_check = time.monotonic()
                            if find_pid(app) != pid:
                                break  # the game closed
                finally:
                    cap.stop()
                log(f"{app} stopped; capturing all system audio again")
                continue

            mic = resolve_device(sc, args.audio_device)
            log(f"capturing audio from: {mic.name}")
            blocks = 0
            with mic.recorder(samplerate=SAMPLE_RATE, channels=1, blocksize=block) as rec:
                while not stop.is_set():
                    data = rec.record(numframes=block)
                    if data.ndim > 1:
                        data = data.mean(axis=1) if data.shape[1] > 1 else data[:, 0]
                    deliver(np.ascontiguousarray(data, dtype=np.float32))
                    blocks += 1
                    if app and blocks % 10 == 0:  # once a second: has the game started?
                        p = find_pid(app)
                        if p and p != failed_pid:
                            break
        except Exception as e:
            if not warned:
                emit("warning", message=f"audio capture problem: {e}; retrying")
                warned = True
            stop.wait(2.0)  # device may have changed (headset plugged in, etc.)


# --------------------------------------------------------------------------
# Speech recognition
# --------------------------------------------------------------------------


class DownloadProgress:
    """Collects the progress bars huggingface_hub creates while downloading
    and reports overall progress to the host as "progress" events.

    Depending on the huggingface_hub version the bars passed through
    `tqdm_class` count bytes or only files; bytes are preferred."""

    def __init__(self, message):
        self.message = message
        self.bars = []
        self.lock = threading.Lock()
        self.last = 0.0

    def report(self, force=False):
        now = time.monotonic()
        with self.lock:
            if not force and now - self.last < 0.2:
                return
            self.last = now
            bars = list(self.bars)
        byte_bars = [b for b in bars if b.unit == "B" and (b.total or 0) > 0]
        use = byte_bars or [b for b in bars if (b.total or 0) > 0]
        if not use:
            return
        total = max(int(b.total) for b in use)
        done = max(min(int(b.n), int(b.total)) for b in use)
        emit("progress", message=self.message, done=done, total=total, unit="B" if byte_bars else "files")

    def tqdm_class(self):
        from tqdm import tqdm

        reporter = self
        devnull = open(os.devnull, "w")

        class ReportingTqdm(tqdm):
            def __init__(self, *a, **kw):
                kw.pop("name", None)  # only huggingface_hub's own tqdm accepts it
                kw["file"] = devnull
                kw["disable"] = False
                super().__init__(*a, **kw)
                with reporter.lock:
                    reporter.bars.append(self)

            def update(self, n=1):
                r = super().update(n)
                reporter.report()
                return r

            def refresh(self, *a, **kw):
                r = super().refresh(*a, **kw)
                reporter.report()
                return r

        return ReportingTqdm


MODEL_FILES = ["config.json", "preprocessor_config.json", "model.bin", "tokenizer.json", "vocabulary.*"]


def ensure_model(args):
    """Return a local path to the Whisper model, downloading it (with progress
    reports) when it is not cached yet. Cached models start without any
    network access."""
    from faster_whisper import utils as fw_utils

    name = args.model
    if os.path.isdir(name):
        return name
    cache_dir = args.models_dir or None
    status(f"Checking Whisper model '{name}'")
    try:
        return fw_utils.download_model(name, local_files_only=True, cache_dir=cache_dir)
    except ValueError:
        raise  # unknown model name
    except Exception:
        pass  # not downloaded yet

    message = f"Downloading Whisper model '{name}' (one-time)"
    status(message)
    try:
        import huggingface_hub

        repo_id = name if "/" in name else fw_utils._MODELS[name]
        progress = DownloadProgress(message)
        path = huggingface_hub.snapshot_download(
            repo_id, cache_dir=cache_dir, allow_patterns=MODEL_FILES, tqdm_class=progress.tqdm_class())
        progress.report(force=True)
        return path
    except Exception as e:
        log(f"download with progress failed ({type(e).__name__}: {e}), retrying")
        return fw_utils.download_model(name, cache_dir=cache_dir)


def load_model(args):
    from faster_whisper import WhisperModel

    path = ensure_model(args)
    status(f"Loading Whisper model '{args.model}' into memory")
    device = args.device
    if device in ("cuda", "auto"):
        preload_cuda_libs()
        try:
            import ctranslate2

            if ctranslate2.get_cuda_device_count() > 0:
                compute = args.compute_type or "int8_float16"
                model = WhisperModel(path, device="cuda", compute_type=compute)
                return model, "cuda", compute
            if device == "cuda":
                emit("warning", message="no CUDA GPU usable, falling back to CPU")
        except Exception as e:
            emit("warning", message=f"GPU init failed ({e}), falling back to CPU")

    compute = args.compute_type or "int8"
    model = WhisperModel(path, device="cpu", compute_type=compute,
                         cpu_threads=args.threads, num_workers=1)
    return model, "cpu", compute


def warm_up(np, model, languages):
    """Run the VAD and Whisper once on a little noise, so the first real
    transcription doesn't pay for lazy loading and first-call setup."""
    status("Warming up speech recognition")
    try:
        noise = (np.random.default_rng(0).standard_normal(SAMPLE_RATE) * 0.01).astype(np.float32)
        split_speech(noise)  # loads the Silero VAD model used by captures
        transcribe(model, languages, noise)
    except Exception as e:  # never fatal: real requests will just be slower
        emit("warning", message=f"speech warm-up failed: {e}")


# Whisper's own signals that a segment is noise or a hallucination.
MAX_NO_SPEECH_PROB = 0.6
MIN_AVG_LOGPROB = -1.0
MAX_COMPRESSION_RATIO = 2.4

# Words/callouts Whisper should expect; steers spelling of CS2 slang.
BASE_PROMPT = "AWP, eco, force buy, clutch, lit, rotate, plant, defuse, rush B, A site, B site, mid, flash, smoke, molly."

_prompt_lock = threading.Lock()
_prompt = BASE_PROMPT


def set_prompt(text):
    global _prompt
    with _prompt_lock:
        _prompt = text.strip() or BASE_PROMPT


def get_prompt():
    with _prompt_lock:
        return _prompt


def good_segment(seg):
    """False for segments Whisper itself marks as silence/noise/looping text."""
    return not (seg.no_speech_prob > MAX_NO_SPEECH_PROB
                or seg.avg_logprob < MIN_AVG_LOGPROB
                or seg.compression_ratio > MAX_COMPRESSION_RATIO)


def _decode(model, audio, language):
    return model.transcribe(
        audio,
        language=language,
        task="transcribe",
        beam_size=1,               # greedy decoding: much cheaper, small quality cost
        best_of=1,
        temperature=0.0,           # no temperature fallback re-decoding
        condition_on_previous_text=False,  # prevents looping hallucinations
        initial_prompt=get_prompt(),
        without_timestamps=True,
        vad_filter=False,          # audio is already VAD-segmented
        no_speech_threshold=0.6,
        log_prob_threshold=-1.0,
        compression_ratio_threshold=2.4,
    )


def transcribe(model, languages, audio):
    """Transcribe one utterance. `languages` is [] (auto), [code] (fixed) or a
    list of candidates: auto-detection is then kept within those languages."""
    t0 = time.perf_counter()
    segments, info = _decode(model, audio, languages[0] if len(languages) == 1 else None)
    language, probability = info.language, info.language_probability
    if len(languages) > 1 and language not in languages:
        # Short clips are often mis-detected; fall back to the most likely
        # allowed language. Costs a second decode only in this case.
        probs = dict(info.all_language_probs or [])
        language = max(languages, key=lambda code: probs.get(code, 0.0))
        probability = probs.get(language, 0.0)
        segments, _ = _decode(model, audio, language)
    parts = []
    for seg in segments:
        text = seg.text.strip()
        if text and good_segment(seg) and text.lower() not in HALLUCINATIONS:
            parts.append(text)
    return " ".join(parts).strip(), language, probability, time.perf_counter() - t0


def split_speech(audio):
    """Split captured audio into utterances so each gets its own language detection."""
    from faster_whisper.vad import VadOptions, get_speech_timestamps

    chunks = get_speech_timestamps(audio, VadOptions(min_silence_duration_ms=600, speech_pad_ms=200))
    return [audio[c["start"]:c["end"]] for c in chunks if c["end"] - c["start"] >= SAMPLE_RATE // 4]


def parse_languages(spec):
    return [l.strip().lower() for l in spec.split(",") if l.strip()]


class LocalAgreement2:
    """LocalAgreement-2 for streaming recognition: Whisper is run again and
    again on a growing utterance, and a word is *confirmed* once two passes in
    a row agree on it (together with everything before it). The words after
    the confirmed prefix are tentative and may still change."""

    def __init__(self):
        self.prev = []
        self.confirmed = []

    @staticmethod
    def norm(word):
        return re.sub(r"[\W_]+", "", word.lower())

    def update(self, words):
        """Feed the words of the latest pass; returns (confirmed, tentative)."""
        n = 0
        while n < min(len(self.prev), len(words)) and self.norm(self.prev[n]) == self.norm(words[n]):
            n += 1
        if n > len(self.confirmed):  # confirmed text only ever grows; shown words never change
            self.confirmed += words[len(self.confirmed):n]
        self.prev = list(words)
        return list(self.confirmed), list(words[len(self.confirmed):])


_CJK = re.compile("[\u3040-\u30ff\u3400-\u9fff\uac00-\ud7af]")


def split_words(text):
    """Words of a transcript; one token per character for Chinese/Japanese/Korean."""
    if _CJK.search(text):
        return [c for c in text if not c.isspace()]
    return text.split()


def join_words(words, text):
    return ("" if _CJK.search(text) else " ").join(words)


class PartialSlot:
    """Holds the newest partial request only: while Whisper is busy, older
    snapshots of the same speech are not worth decoding."""

    def __init__(self):
        self.lock = threading.Lock()
        self.item = None

    def put(self, item):
        with self.lock:
            self.item = item

    def take(self):
        with self.lock:
            item, self.item = self.item, None
            return item

    def peek(self):
        with self.lock:
            return self.item


def asr_loop(model, args, jobs, np=None, rnnoise=None, partials=None):
    """Transcribe queued utterances. With `partials` (streaming), the time
    between jobs is used to decode the speech still being spoken, so the
    host can show text before the sentence ends; final jobs always go first."""
    languages = parse_languages(args.language)
    la, la_utt, last_shown = LocalAgreement2(), None, None
    finished = 0           # utterances up to this id have been transcribed for good
    next_partial_at = 0.0  # a partial pass takes at most half of the thread's time

    def partial_pass():
        nonlocal la, la_utt, last_shown, next_partial_at
        if time.monotonic() < next_partial_at or partials.peek() is None:
            return
        audio, utt = partials.take()
        if utt <= finished:
            return  # the final transcription already ran
        if utt != la_utt:
            la, la_utt, last_shown = LocalAgreement2(), utt, None
        text, language, probability, took = transcribe(model, languages, denoise(np, rnnoise, audio))
        next_partial_at = time.monotonic() + took
        confirmed, tentative = la.update(split_words(text))
        shown = (tuple(confirmed), tuple(tentative))
        if shown != last_shown and utt > finished:
            last_shown = shown
            emit("partial", source="live", text=join_words(confirmed, text), tentative=join_words(tentative, text),
                 language=language, language_probability=round(probability, 3), utterance=utt,
                 audio_seconds=round(len(audio) / SAMPLE_RATE, 2), processing_seconds=round(took, 2))

    while True:
        try:
            source, audio, utt = jobs.get(timeout=0.05) if partials is not None else jobs.get()
        except queue.Empty:
            try:
                partial_pass()
            except Exception as e:
                emit("error", message=f"streaming transcription failed: {e}", fatal=False)
            continue
        if source == "quit":
            return
        if source == "live":
            finished = max(finished, utt)
            if la_utt == utt:
                la, la_utt = LocalAgreement2(), None
        found = False
        try:
            audio = denoise(np, rnnoise, audio)
            pieces = split_speech(audio) if source == "capture" else [audio]
            emit("job_start", source=source, pieces=len(pieces), utterance=utt)
            for piece in pieces:
                if len(piece) < SAMPLE_RATE // 4:
                    continue
                text, language, probability, took = transcribe(model, languages, piece)
                if not text:
                    continue
                found = True
                emit("transcript", source=source, text=text, utterance=utt,
                     language=language, language_probability=round(probability, 3),
                     audio_seconds=round(len(piece) / SAMPLE_RATE, 2), processing_seconds=round(took, 2))
        except Exception as e:
            emit("error", message=f"transcription failed: {e}", fatal=False)
        finally:
            emit("job_done", source=source, found=found, utterance=utt)



# --------------------------------------------------------------------------
# Fast offline translation (Opus-MT on CTranslate2, CPU only)
# --------------------------------------------------------------------------

# CTranslate2 conversions of the Helsinki-NLP Opus-MT models, one per language.
NMT_REPO = "gaudi/opus-mt-{lang}-en-ctranslate2"
NMT_FILES = ["config.json", "model.bin", "source.spm", "target.spm", "*vocabulary*"]


def nmt_download(models_dir, lang, message=None):
    """Return the local path of a language's fast-translation model. With a
    message, a missing model is downloaded (reporting progress); without one
    only the local cache is used, so nothing is ever fetched mid-game."""
    import huggingface_hub

    repo = NMT_REPO.format(lang=lang)
    cache_dir = models_dir or None
    try:
        return huggingface_hub.snapshot_download(repo, cache_dir=cache_dir, allow_patterns=NMT_FILES,
                                                 local_files_only=True)
    except Exception:
        if message is None:
            raise
    progress = DownloadProgress(message)
    path = huggingface_hub.snapshot_download(repo, cache_dir=cache_dir, allow_patterns=NMT_FILES,
                                             tqdm_class=progress.tqdm_class())
    progress.report(force=True)
    return path


def prefetch_nmt(models_dir, langs):
    """Download the fast-translation models up front (startup / setup), so
    they are on disk before the game starts. A language without a model only
    produces a warning: its speech goes to the LLM."""
    for i, lang in enumerate(langs, 1):
        message = f"Downloading fast translation model {lang} -> en ({i}/{len(langs)})"
        status(message)
        try:
            nmt_download(models_dir, lang, message)
        except Exception as e:
            emit("warning", message=f"fast translation for '{lang}' unavailable ({type(e).__name__}); "
                 "its speech will be translated by the LLM")


class NmtEngine:
    """Tiny int8 Marian models for <source>-to-English. Models are downloaded
    beforehand (prefetch_nmt) and loaded from disk in the background; while a
    model is not loaded (or doesn't exist) translate() answers None and the
    host falls back to the LLM."""

    def __init__(self, models_dir, load=None):
        self.models_dir = models_dir
        self.models = {}   # lang -> (ctranslate2.Translator, src spm, tgt spm)
        self.loading = set()
        self.failed = set()
        self.lock = threading.Lock()
        self._load = load or self._load_model

    def _load_model(self, lang):
        import ctranslate2
        import sentencepiece as spm

        path = nmt_download(self.models_dir, lang)  # cache only: no downloads while playing
        tr = ctranslate2.Translator(path, device="cpu", compute_type="int8", inter_threads=1, intra_threads=1)
        return (tr, spm.SentencePieceProcessor(model_file=os.path.join(path, "source.spm")),
                spm.SentencePieceProcessor(model_file=os.path.join(path, "target.spm")))

    def prepare(self, lang):
        """Start loading a language's model in the background (idempotent)."""
        with self.lock:
            if lang in self.models or lang in self.loading or lang in self.failed:
                return
            self.loading.add(lang)
        threading.Thread(target=self._prepare, args=(lang,), daemon=True).start()

    def _prepare(self, lang):
        try:
            model = self._load(lang)
            with self.lock:
                self.models[lang] = model
            log(f"fast translation ready: {lang} -> en")
        except Exception as e:
            with self.lock:
                self.failed.add(lang)  # unsupported language or offline: use the LLM
            log(f"fast translation for '{lang}' unavailable ({type(e).__name__}: {e}); using the LLM")
        finally:
            with self.lock:
                self.loading.discard(lang)

    def translate(self, lang, text):
        """English translation of text, or None when no model is ready."""
        with self.lock:
            model = self.models.get(lang)
        if model is None:
            self.prepare(lang)
            return None
        tr, src, tgt = model
        tokens = src.encode(text, out_type=str) + ["</s>"]
        result = tr.translate_batch([tokens], beam_size=2, max_decoding_length=64, max_batch_size=1)
        out = tgt.decode(result[0].hypotheses[0]).strip()
        return out or None


def nmt_loop(engine, jobs):
    while True:
        job = jobs.get()
        if job is None:
            return
        ident, lang, text = job
        try:
            out = engine.translate(lang, text)
        except Exception as e:
            log(f"fast translation failed: {e}")
            out = None
        emit("translation", id=ident, text=out or "", ok=out is not None)


# --------------------------------------------------------------------------
# Main
# --------------------------------------------------------------------------


def main():
    global _proto
    # Keep the protocol stream clean: anything printed by libraries goes to stderr.
    _proto = open(os.dup(sys.stdout.fileno()), "w", encoding="utf-8", buffering=1)
    sys.stdout = sys.stderr

    ap = argparse.ArgumentParser()
    ap.add_argument("--model", default="small")
    ap.add_argument("--device", default="cpu", choices=["cpu", "cuda", "auto"])
    ap.add_argument("--compute-type", default="")
    ap.add_argument("--threads", type=int, default=2)
    ap.add_argument("--language", default="")
    ap.add_argument("--audio-device", default="")
    ap.add_argument("--live", action="store_true", help="continuously transcribe detected speech")
    ap.add_argument("--buffer-seconds", type=float, default=30.0)
    ap.add_argument("--max-speech-seconds", type=float, default=5.0,
                    help="live mode: transcribe after this much continuous speech even without a pause")
    ap.add_argument("--app-audio", default="",
                    help="Windows: capture only this program's audio (e.g. cs2.exe) while it is running")
    ap.add_argument("--stream", action="store_true",
                    help="live mode: also report what is being said before the sentence ends (LocalAgreement-2)")
    ap.add_argument("--denoise", action="store_true", help="suppress noise (RNNoise) before voice detection")
    ap.add_argument("--nmt-langs", default="",
                    help="enable fast offline translation to English for these languages (comma-separated); "
                        "their models are downloaded at startup / --prepare")
    ap.add_argument("--models-dir", default="")
    ap.add_argument("--list-devices", action="store_true")
    ap.add_argument("--prepare", action="store_true", help="download model, then exit")
    ap.add_argument("--no-capture", action="store_true", help="do not open an audio device (testing)")
    args = ap.parse_args()

    # Limit every thread pool *before* numpy/onnxruntime/ctranslate2 are imported.
    threads = str(max(1, args.threads))
    for var in ("OMP_NUM_THREADS", "OPENBLAS_NUM_THREADS", "MKL_NUM_THREADS"):
        os.environ[var] = threads
    warnings.filterwarnings("ignore")
    lower_priority()
    ensure_msvc_runtime()

    if args.list_devices:
        status("Looking for audio devices")
        devices, default = list_devices(open_soundcard())
        emit("devices", devices=devices, default=default)
        return

    status("Starting speech engine")
    import numpy as np

    # Resolve the audio device first so a missing loopback device fails fast.
    sc, audio_name = None, ""
    if not args.no_capture and not args.prepare:
        status("Opening audio device")
        sc = open_soundcard()
        audio_name = resolve_device(sc, args.audio_device).name

    model, device, compute = load_model(args)
    warm_up(np, model, parse_languages(args.language))
    nmt_langs = [l for l in parse_languages(args.nmt_langs) if l != "en"]
    if nmt_langs:
        prefetch_nmt(args.models_dir, nmt_langs)

    if args.prepare:
        emit("ready", model=args.model, device=device, compute_type=compute, threads=args.threads, audio_device="")
        return

    stop = threading.Event()
    jobs = queue.Queue()
    ring = RingBuffer(np, max(args.buffer_seconds, 1.0))

    def on_utterance(audio, utt):
        # Never let a backlog build up: if Whisper can't keep up, drop the oldest speech.
        while jobs.qsize() >= 3:
            try:
                jobs.get_nowait()
                emit("warning", message="transcription is falling behind, dropped an utterance")
            except queue.Empty:
                break
        jobs.put(("live", audio, utt))

    rnnoise = load_rnnoise() if args.denoise else None
    if args.denoise and rnnoise is None:
        emit("warning", message="noise suppression is not available on this system; continuing without it")

    partials = PartialSlot() if args.live and args.stream else None
    segmenter = None
    if args.live:
        segmenter = VadSegmenter(np, on_utterance, max_speech_s=max(args.max_speech_seconds, 1.0),
                                 on_partial=(lambda audio, utt: partials.put((audio, utt))) if partials else None)

    if sc is not None:
        threading.Thread(target=capture_loop, args=(sc, np, args, ring, segmenter, stop), daemon=True).start()

    asr = threading.Thread(target=asr_loop, args=(model, args, jobs, np, rnnoise, partials), daemon=True)
    asr.start()

    nmt_jobs = None
    if nmt_langs:
        engine = NmtEngine(args.models_dir)
        nmt_jobs = queue.Queue()
        threading.Thread(target=nmt_loop, args=(engine, nmt_jobs), daemon=True).start()
        for lang in nmt_langs:
            engine.prepare(lang)  # from disk, in the background

    emit("ready", model=args.model, device=device, compute_type=compute, threads=args.threads, audio_device=audio_name)

    for line in sys.stdin:  # EOF means the host went away
        cmd = line.strip().split()
        if not cmd:
            continue
        if cmd[0] == "quit":
            break
        if cmd[0] == "capture":
            seconds = float(cmd[1]) if len(cmd) > 1 else 15.0
            jobs.put(("capture", ring.last(seconds), 0))
        elif cmd[0] == "prompt":
            # "prompt <text>": vocabulary hint for Whisper (e.g. the map's callouts)
            set_prompt(line.strip()[len("prompt"):])
        elif cmd[0] == "translate" and len(cmd) >= 4:
            # "translate <id> <lang> <text>"; without fast translation enabled
            # the host is told right away to use the LLM
            ident, lang = int(cmd[1]), cmd[2]
            text = line.strip().split(None, 3)[3]
            if nmt_jobs is None:
                emit("translation", id=ident, text="", ok=False)
            else:
                nmt_jobs.put((ident, lang, text))
        elif cmd[0] == "feed" and args.no_capture:
            # testing hook: feed a raw float32 mono 16 kHz file through the pipeline
            data = np.fromfile(cmd[1], dtype=np.float32)
            ring.write(data)
            if segmenter is not None:
                segmenter.feed(data)
    stop.set()
    if nmt_jobs is not None:
        nmt_jobs.put(None)
    jobs.put(("quit", None, 0))
    asr.join(timeout=30)  # let queued work (e.g. a pending capture) finish


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        pass
    except Exception as e:
        msg = str(e) or type(e).__name__
        if sys.platform.startswith("linux") and "soundcard" in repr(e.__traceback__.tb_next) + msg.lower():
            msg += " (is PulseAudio/PipeWire running?)"
        if _proto is not None:
            emit("error", message=msg, fatal=True)
        else:
            print(f"fatal: {e}", file=sys.stderr)
        sys.exit(1)

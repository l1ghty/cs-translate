# /// script
# requires-python = ">=3.10,<3.14"
# dependencies = [
#   "faster-whisper>=1.2,<2",
#   "soundcard>=0.4.4",
#   "numpy>=1.26",
# ]
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
  stdin   one command per line: "capture <seconds>" or "quit". EOF = quit.
  stderr  human-readable logs (passed through to the terminal).
"""

import argparse
import json
import os
import queue
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

    def __init__(self, np, on_utterance, threshold=0.5, min_silence_s=0.5,
                 min_speech_s=0.3, max_speech_s=12.0, pad_s=0.25):
        from faster_whisper.vad import get_vad_model

        self.np = np
        self.session = get_vad_model().session  # single-threaded ONNX session
        self.on_utterance = on_utterance
        self.threshold = threshold
        self.neg_threshold = max(threshold - 0.15, 0.01)
        self.min_silence = int(min_silence_s * SAMPLE_RATE / VAD_FRAME)
        self.min_speech = int(min_speech_s * SAMPLE_RATE)
        self.max_speech = int(max_speech_s * SAMPLE_RATE)
        self.pad_frames = max(1, int(pad_s * SAMPLE_RATE / VAD_FRAME))
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
                self.speech = list(self.preroll)
                self.speech_len = sum(len(f) for f in self.speech)
                self.preroll = []
                self.silence = 0
            return

        self.speech.append(frame)
        self.speech_len += len(frame)
        if p < self.neg_threshold:
            self.silence += 1
        elif p >= self.threshold:
            self.silence = 0

        if self.silence >= self.min_silence or self.speech_len >= self.max_speech:
            self._finish()

    def _finish(self):
        np = self.np
        # keep a little trailing silence, drop the rest
        drop = max(0, self.silence - self.pad_frames)
        frames = self.speech[: len(self.speech) - drop] if drop else self.speech
        audio = np.concatenate(frames) if frames else np.zeros(0, dtype=np.float32)
        voiced = self.speech_len - self.silence * VAD_FRAME
        self.active = False
        self.speech = []
        self.speech_len = 0
        self.silence = 0
        if voiced >= self.min_speech:
            self.on_utterance(audio)


def capture_loop(sc, np, args, ring, segmenter, stop):
    block = SAMPLE_RATE // 10  # 100 ms per read keeps wake-ups (and CPU) low
    warned = False
    while not stop.is_set():
        try:
            mic = resolve_device(sc, args.audio_device)
            log(f"capturing audio from: {mic.name}")
            with mic.recorder(samplerate=SAMPLE_RATE, channels=1, blocksize=block) as rec:
                while not stop.is_set():
                    data = rec.record(numframes=block)
                    if data.ndim > 1:
                        data = data.mean(axis=1) if data.shape[1] > 1 else data[:, 0]
                    data = np.ascontiguousarray(data, dtype=np.float32)
                    ring.write(data)
                    if segmenter is not None:
                        segmenter.feed(data)
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


def _decode(model, audio, language):
    return model.transcribe(
        audio,
        language=language,
        task="transcribe",
        beam_size=1,               # greedy decoding: much cheaper, small quality cost
        best_of=1,
        temperature=0.0,           # no temperature fallback re-decoding
        condition_on_previous_text=False,
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
        if text and text.lower() not in HALLUCINATIONS:
            parts.append(text)
    return " ".join(parts).strip(), language, probability, time.perf_counter() - t0


def split_speech(audio):
    """Split captured audio into utterances so each gets its own language detection."""
    from faster_whisper.vad import VadOptions, get_speech_timestamps

    chunks = get_speech_timestamps(audio, VadOptions(min_silence_duration_ms=600, speech_pad_ms=200))
    return [audio[c["start"]:c["end"]] for c in chunks if c["end"] - c["start"] >= SAMPLE_RATE // 4]


def parse_languages(spec):
    return [l.strip().lower() for l in spec.split(",") if l.strip()]


def asr_loop(model, args, jobs):
    languages = parse_languages(args.language)
    while True:
        source, audio = jobs.get()
        if source == "quit":
            return
        found = False
        try:
            pieces = split_speech(audio) if source == "capture" else [audio]
            emit("job_start", source=source, pieces=len(pieces))
            for piece in pieces:
                if len(piece) < SAMPLE_RATE // 4:
                    continue
                text, language, probability, took = transcribe(model, languages, piece)
                if not text:
                    continue
                found = True
                emit("transcript", source=source, text=text,
                     language=language, language_probability=round(probability, 3),
                     audio_seconds=round(len(piece) / SAMPLE_RATE, 2), processing_seconds=round(took, 2))
        except Exception as e:
            emit("error", message=f"transcription failed: {e}", fatal=False)
        finally:
            emit("job_done", source=source, found=found)


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

    if args.prepare:
        emit("ready", model=args.model, device=device, compute_type=compute, threads=args.threads, audio_device="")
        return

    stop = threading.Event()
    jobs = queue.Queue()
    ring = RingBuffer(np, max(args.buffer_seconds, 1.0))

    def on_utterance(audio):
        # Never let a backlog build up: if Whisper can't keep up, drop the oldest speech.
        while jobs.qsize() >= 3:
            try:
                jobs.get_nowait()
                emit("warning", message="transcription is falling behind, dropped an utterance")
            except queue.Empty:
                break
        jobs.put(("live", audio))

    segmenter = VadSegmenter(np, on_utterance) if args.live else None

    if sc is not None:
        threading.Thread(target=capture_loop, args=(sc, np, args, ring, segmenter, stop), daemon=True).start()

    asr = threading.Thread(target=asr_loop, args=(model, args, jobs), daemon=True)
    asr.start()

    emit("ready", model=args.model, device=device, compute_type=compute, threads=args.threads, audio_device=audio_name)

    for line in sys.stdin:  # EOF means the host went away
        cmd = line.strip().split()
        if not cmd:
            continue
        if cmd[0] == "quit":
            break
        if cmd[0] == "capture":
            seconds = float(cmd[1]) if len(cmd) > 1 else 15.0
            jobs.put(("capture", ring.last(seconds)))
        elif cmd[0] == "feed" and args.no_capture:
            # testing hook: feed a raw float32 mono 16 kHz file through the pipeline
            data = np.fromfile(cmd[1], dtype=np.float32)
            ring.write(data)
            if segmenter is not None:
                segmenter.feed(data)
    stop.set()
    jobs.put(("quit", None))
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

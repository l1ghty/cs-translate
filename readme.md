# CS2 In-Game Translate & Voice Transcription

Translates Counter-Strike 2 chat in real time and transcribes and translates voice chat. Everything runs **locally**:

- **Speech recognition:** [faster-whisper](https://github.com/SYSTRAN/faster-whisper) (Whisper on CTranslate2, int8) with Silero VAD.
- **Translation:** Tencent's [Hy-MT2 1.8B](https://github.com/Tencent-Hunyuan/Hy-MT2), a small model made only for translation, running locally in [Ollama](https://ollama.com).

It is built to stay out of your game's way, even on weaker PCs. Setup is automatic on Windows, Linux and macOS.

## Install

1. Download the binary for your system from the [Releases](https://github.com/l1ghty/cs-translate/releases) page, or build it yourself with Go 1.24+ (`go build -o cs-translate .`).
2. Run it. On first start it sets up everything it needs:

| Component | What happens |
|-----------|--------------|
| **Ollama** (translation) | Detected if already installed. Otherwise it is installed natively without any clicks (`winget` on Windows, or the official installer in silent mode when winget is missing; `brew` on macOS; the official script on Linux) and the translation model is downloaded. On Windows, the Ollama window the installer opens is hidden right away; Ollama keeps running in the tray. |
| **Speech engine** (voice) | A private copy of [uv](https://docs.astral.sh/uv/) (~20 MB, checksum-verified) is downloaded. uv installs its own Python plus faster-whisper. No system Python, admin rights, or PATH changes are needed. |
| **Whisper model** | Downloaded once. The size is chosen to match your PC (see [Performance](#performance)). |

Before installing, it lists everything that is missing and asks once whether to install all of it. Answer **y** and the rest of the setup runs without further questions; answer **n** to confirm each step separately.

To install everything ahead of time without being asked any questions (handy before a match, or for scripting), run:

```bash
./cs-translate -setup -yes
```

You do **not** need Docker, ffmpeg, a Python install, CUDA, or any virtual audio driver on Windows or Linux.

### Platform notes

| | Voice capture | F9 hotkey |
|---|---|---|
| **Windows** | Works out of the box. It records what you hear through WASAPI loopback. | Works out of the box. |
| **Linux** | Works out of the box with PulseAudio or PipeWire. It records the monitor of your default output. | Your user must be in the `input` group: run `sudo usermod -aG input $USER`, then log out and back in. Without it, press **Enter** in the terminal instead. |
| **macOS** | macOS cannot record system audio by itself. Install [BlackHole](https://github.com/ExistentialAudio/BlackHole) with `brew install blackhole-2ch`. Then, in Audio MIDI Setup, create a *Multi-Output Device* that contains your speakers and BlackHole, and use it as the output. The device is picked up automatically. | Press **Enter** in the terminal. |

macOS binaries are not signed. If Gatekeeper blocks the binary, run:

```bash
xattr -d com.apple.quarantine cs-translate
```

## Usage

```bash
./cs-translate -lang Spanish        # Linux / macOS (default target language: English)
cs-translate.exe -lang Spanish      # Windows
```

You then choose a mode:

0. **Change target language:** pick the language to translate into: English, Chinese, Russian, Spanish, Portuguese (the most used languages on Steam), or type any other language name. Your choice is remembered for the next start.
1. **In-game translate:** translates console chat. You can also turn on **live** voice transcription, which transcribes everything that is said.
2. **Echo:** translates chat, and when you press **F9** (or **Enter**) it transcribes and translates the **last 15 seconds** of audio. Each utterance in that window is recognized separately, so mixed languages work. This is the lightest option, because speech recognition does nothing until you press the key.
3. **Auto voice:** translates chat, and transcribes and translates voice **automatically**. Each sentence is sent as soon as the speaker pauses, or after **5 seconds** of non-stop speech (`-voice-interval`), whichever comes first. No key to press, so it costs more CPU than echo mode while people talk.

CS2 must be started with the `-condebug` launch option so that it writes `console.log`. The tool checks for this and offers to open the Steam settings for you. The log file is found automatically in every Steam library, including libraries on other drives and Flatpak Steam.

### Command-line options

| Flag | Description | Default |
|------|-------------|---------|
| `-lang` | Target language for translation. Overrides the language saved with menu option 0. | `English` |
| `-voice` | Enable live voice transcription without asking | off |
| `-voice-interval` | Live voice (modes 1 and 3): transcribe after this many seconds of speech even if the speaker hasn't paused yet (pieces overlap by 0.2s so no word is cut) | `5` |
| `-voice-lang` | Language spoken in voice chat. One code (`ru`) skips detection entirely. A list (`ru,en,de`) keeps detection within those languages, which avoids misdetected short clips. | auto-detect |
| `-whisper-model` | `auto`, `tiny`, `base`, `small`, `medium`, `large-v3-turbo`, or `distil-large-v3.5` (English only) | `auto` |
| `-whisper-device` | `cpu`, or `cuda` for an NVIDIA GPU (see [Performance](#performance)) | `cpu` |
| `-whisper-threads` | CPU threads for speech recognition (`0` means auto) | `0` |
| `-audiodevice` | Device to record from, as a name or part of one | what you hear |
| `-list-audio-devices` | List audio devices and exit | |
| `-model` | Ollama model used for translation. The default, Tencent's [Hy-MT2 1.8B](https://github.com/Tencent-Hunyuan/Hy-MT2), is made only for translation (33 languages, ~1.1 GB). Any other Ollama model works too, e.g. `translategemma` for 55 languages. | `hf.co/tencent/Hy-MT2-1.8B-GGUF:Q4_K_M` |
| `-translate-cpu` | Run the translation model on the CPU only, which keeps the GPU free | off |
| `-log` | Path to CS2 `console.log` | auto-detect |
| `-setup` | Install and download everything, then exit | |
| `-yes` | Answer "yes" to every setup question | |

## Performance

The game always gets priority:

- **Speech runs on the CPU at low priority** (`nice 10` on Linux and macOS, *below normal* on Windows), on a fixed small number of threads. The GPU and its VRAM stay free for CS2.
- **Whisper only runs when someone speaks.** A tiny voice-activity detector (Silero VAD) uses about **0.3% of one CPU core**. It skips gunfire, footsteps and music, so Whisper never wastes time on non-speech audio. In echo mode Whisper runs only when you press F9.
- **Fast settings:** int8 quantization, greedy decoding, and no temperature fallback. If recognition ever falls behind, old speech is dropped instead of building up a backlog.
- **Nothing touches the disk.** Audio stays in a small in-memory buffer (about 2 MB). There are no temp files and no external processes per clip.
- **Same-language speech is not translated.** If someone already speaks your target language, the translation step is skipped.
- **Both models are loaded and warmed up at startup**, in parallel, so pressing F9 (or a new chat line) is handled immediately instead of waiting for a model to load. Startup shows what is happening, with progress bars for downloads.
- **The translation model stays loaded while the tool runs**, so it is not reloaded in the middle of a match. It is unloaded when you exit (and after 30 minutes if the tool is killed).
- **F9 never waits.** Transcription and translation run in the background, with a status line showing what is in progress.

`-whisper-model auto` picks settings based on your CPU:

| Logical CPU cores | Model | Threads |
|---|---|---|
| up to 4 | `base` (~150 MB) | 1 |
| 5–8 | `small` (~480 MB) | 2 |
| 9–12 | `small` | 3 |
| 13 or more | `small` | 4 |

**More accuracy:** if you have spare CPU, use `-whisper-model medium`. If you have a strong NVIDIA GPU with spare VRAM, use `-whisper-device cuda`, which selects `large-v3-turbo`. The CUDA libraries are fetched automatically, so no CUDA install is needed. If the GPU can't be used, the tool falls back to the CPU.

**Less load:** use `-whisper-model tiny`, or use echo mode. Use `-translate-cpu` if your GPU is the bottleneck.

## Where files are stored

uv, the speech worker and the Whisper models are kept in one folder. Delete it to remove them:

- Windows: `%LOCALAPPDATA%\cs-translate`
- macOS: `~/Library/Caches/cs-translate`
- Linux: `~/.cache/cs-translate`

uv also caches Python packages in its own cache. You can clear that with `uv cache clean`. The Ollama model is managed by Ollama; remove it with `ollama rm <model>`.

### Upgrading from the Docker-based version

The Docker container, the `venv` folder, ffmpeg and screen-capture-recorder are no longer used. You can remove them:

```bash
docker rm -f cs-translate
```

```bash
docker rmi cs-translate
```

```bash
docker volume rm cs-translate-models
```

## Tuned for fast callouts

- **Short pauses count:** a 0.3s pause ends an utterance, so one-word callouts are sent right away.
- **Map-aware recognition:** when `console.log` shows a map load (`de_mirage`, ...), Whisper is primed with that map's callouts (palace, apps, banana, ...).
- **Noise filtering:** segments Whisper itself rates as silence, noise or looping text are dropped before translation.
- **Callout cache:** very common callouts (`раш б` -> `Rush B`) are translated instantly without the LLM when translating to English.
- **Short voice answers:** voice translations are streamed from Ollama and capped at 96 tokens.
- **Offline fast translation:** short callouts (up to 12 words) spoken in a supported language are translated to English by a small Opus-MT model on the CPU in tens of milliseconds, falling back to the LLM otherwise. Its models (~75 MB per language) are downloaded **beforehand**: by `-setup`, or at startup before the speech engine reports ready, never while you play. They cover the languages in `-voice-lang`, or `ru, uk, pl, tr, de, es, fr` when that is unset. Turn it off with `-fast-translate=false`.

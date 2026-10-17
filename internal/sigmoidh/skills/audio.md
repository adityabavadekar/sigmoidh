---
name: audio
aliases: tts, speech, mp3, wav, voice, edge-tts
description: Synthesize speech and edit or convert audio.
when: The user wants narration, text-to-speech, a voiceover, or audio editing.
requires: ffmpeg; edge-tts (demos) or piper/espeak-ng (offline)
---

# Audio

## Where from

| Task | Tool |
|------|------|
| Text-to-speech — **best for demos** | **edge-tts** · `pip install edge-tts` · many natural voices, needs network |
| Text-to-speech — fully offline | `piper --model voice.onnx -f out.wav` (natural) or `espeak-ng` (robotic); macOS `say -o out.aiff` |
| Edit/convert: trim, concat, mix, fade, normalise | `ffmpeg` (e.g. `loudnorm`) |
| Tones / noise | ffmpeg `sine`/`anoisesrc`, or a WAV generated with Python (`wave`, `numpy`) |
| Speech-to-text | `whisper.cpp` or `whisper` |

### edge-tts

```bash
edge-tts --list-voices                                   # pick a voice
edge-tts --voice en-US-AriaNeural --text "Hello there" --write-media out.mp3
edge-tts --voice en-US-GuyNeural --file script.txt --write-media out.mp3
edge-tts --voice en-US-AriaNeural --rate=+10% --pitch=-2Hz --text "…" --write-media out.mp3
```

Useful voices: `en-US-AriaNeural`, `en-US-GuyNeural`, `en-GB-SoniaNeural`,
`en-US-JennyNeural`. It produces MP3 directly; no ffmpeg needed for a single clip.

## Workflow

- Narration demo: script → edge-tts for the voice → ffmpeg to bed it under music
  and set loudness. For multi-paragraph, generate one clip per paragraph, concat
  with short silences.
- Normalise speech to about -16 LUFS.

## Gotchas

- edge-tts needs network access; fall back to piper/espeak-ng offline.
- Sample-rate mismatches break concat — resample every input to the same rate.
- piper needs its voice `.onnx` model file; provide it explicitly.
- Trim leading/trailing silence the synthesizer adds.

## Verify

`ffprobe` for format and duration; listen, or inspect the waveform:

```bash
ffmpeg -i out.mp3 -filter_complex showwavespic -frames:v 1 wave.png
```

---
name: video
aliases: mp4, ffmpeg, movie, animation, gif
description: Compose, edit, or encode video, gifs, and animation.
when: The user asks for a video, mp4/gif, screen recording, animation, or subtitles.
requires: ffmpeg; optional moviepy, manim, imagemagick
---

# Video

ffmpeg does almost everything; reach for a library only for frame-level logic.

## Where from

| Task | Tool |
|------|------|
| Trim, concat, scale, encode, audio | `ffmpeg` |
| Slideshow from images | ffmpeg concat demuxer, or `-loop 1 -t <sec> -i img.png` |
| Programmatic cutting/overlays/text | `moviepy` (Python) |
| Math/technical animation | `manim` (Python) — renders mp4 |
| Frames ↔ video | `ffmpeg -i in.mp4 frames/%04d.png` and back |
| Subtitles | burn `-vf subtitles=subs.srt`; or mux soft subs |

Common commands:

```bash
ffmpeg -i in.mp4 -vf scale=1280:-2 -c:v libx264 -crf 20 -c:a aac out.mp4
ffmpeg -i in.mp4 -vf "fps=15,scale=640:-1" out.gif
```

## Workflow

Target mp4 (H.264 + AAC) unless asked otherwise: 30 fps, 720p or 1080p, CRF 18-23.

## Gotchas

- H.264 needs **even** dimensions — use `scale=1280:-2`.
- `concat` requires identical codec/params; re-encode clips to match first.
- Image sequences need a constant fps; keep audio at 48 kHz.

## Verify

```bash
ffprobe -v error -show_format -show_streams out.mp4
```

Extract a few frames and view them; confirm duration and audio stream.

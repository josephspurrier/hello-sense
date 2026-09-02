"""Voice sidecar for the Sense with Voice pipeline.

orb hands us the two halves it cannot do in Go:

    POST /stt   body = WAV (16-bit mono)   -> {"text": "..."}
    POST /tts   body = {"text": "..."}       -> MP3 bytes (32 kHz mono)

STT is faster-whisper (base.en, int8 on CPU). TTS is selectable with
TTS_ENGINE:

    piper  (default)  piper lessac-medium, the original sidecar voice
    kokoro            kokoro-onnx, voice chosen by TTS_VOICE (e.g. af_heart)

Both engines produce raw PCM that ffmpeg wraps into the 32 kHz mono MP3 the
device's libmad player expects (the device plays at AUDIO_SAMPLE_RATE=32000,
so any other rate comes out at the wrong speed and pitch). Everything runs on
CPU with the models baked into the image, so there is no per-request network
dependency.

TTS_VOICE also accepts a blend spec like "af_heart:3,bf_emma:1": a weighted
average of Kokoro style embeddings is itself a working voice (weights are
normalized before mixing; an omitted weight defaults to 1). Same scheme as
the Scarlet assistant's voice_blend, so a mix auditioned there carries over.
"""
import os
import subprocess
import tempfile
from collections import OrderedDict

import numpy as np
from flask import Flask, request, jsonify, Response
from faster_whisper import WhisperModel

app = Flask(__name__)

# int8 keeps the model small and fast on a CPU VM. tiny.en is ~2x faster than
# base.en and transcribes the short command vocabulary essentially as well
# (its rare misses fall to the harmless "can't help" fallback, verified
# against the full command set); base.en stays available for rollback.
STT_MODEL = os.environ.get("STT_MODEL", "base.en")
_model = WhisperModel(STT_MODEL, device="cpu", compute_type="int8")

TTS_ENGINE = os.environ.get("TTS_ENGINE", "piper")

PIPER_MODEL = os.environ.get("PIPER_MODEL", "/models/voice.onnx")
# lessac-medium synthesizes at 22.05 kHz; ffmpeg resamples to the device's rate.
PIPER_RATE = os.environ.get("PIPER_RATE", "22050")

KOKORO_MODEL = os.environ.get("KOKORO_MODEL", "/models/kokoro-v1.0.onnx")
KOKORO_VOICES = os.environ.get("KOKORO_VOICES", "/models/voices-v1.0.bin")
TTS_VOICE = os.environ.get("TTS_VOICE", "af_heart")
TTS_SPEED = float(os.environ.get("TTS_SPEED", "1.0"))


def _kokoro_style(kokoro, spec):
    """Resolve TTS_VOICE into what kokoro.create() takes: a plain voice name,
    or a blended style embedding for a "name:weight,name:weight" spec.

    Raises ValueError on unknown voices or malformed weights, so a typo in
    TTS_VOICE fails at container start instead of mid-request.
    """
    if "," not in spec and ":" not in spec:
        if spec not in kokoro.get_voices():
            raise ValueError(
                f"Unknown voice {spec!r}. Available: {', '.join(sorted(kokoro.get_voices()))}"
            )
        return spec
    pairs = []
    for piece in spec.split(","):
        name, _, w = piece.partition(":")
        name = name.strip()
        if name not in kokoro.get_voices():
            raise ValueError(f"Unknown voice {name!r} in blend {spec!r}")
        weight = float(w) if w else 1.0
        if weight <= 0:
            raise ValueError(f"Weight for {name!r} must be positive in blend {spec!r}")
        pairs.append((name, weight))
    total = sum(w for _, w in pairs)
    # Normalization matters: the model was only trained on unit-scale style
    # vectors, and an unnormalized sum changes the vector's magnitude.
    return sum(kokoro.get_voice_style(n) * (w / total) for n, w in pairs)


# Load Kokoro at startup only when selected, so the piper configuration keeps
# its smaller memory footprint and start time.
_kokoro = None
_kokoro_voice = None
if TTS_ENGINE == "kokoro":
    from kokoro_onnx import Kokoro

    _kokoro = Kokoro(KOKORO_MODEL, KOKORO_VOICES)
    _kokoro_voice = _kokoro_style(_kokoro, TTS_VOICE)
elif TTS_ENGINE != "piper":
    raise SystemExit(f"TTS_ENGINE must be 'piper' or 'kokoro', got {TTS_ENGINE!r}")


@app.get("/health")
def health():
    return jsonify({"ok": True, "tts_engine": TTS_ENGINE, "stt_model": STT_MODEL})


@app.post("/stt")
def stt():
    audio = request.get_data()
    if not audio:
        return jsonify({"text": ""})
    with tempfile.NamedTemporaryFile(suffix=".wav", delete=False) as f:
        f.write(audio)
        path = f.name
    try:
        # beam_size 1 is greedy: fastest, and plenty for short spoken commands.
        segments, _info = _model.transcribe(path, language="en", beam_size=1)
        text = " ".join(seg.text for seg in segments).strip()
    finally:
        os.unlink(path)
    return jsonify({"text": text})


def _pcm_to_mp3(pcm, rate):
    """Wrap raw s16le mono PCM into the 32 kHz mono MP3 the device plays.

    Each clip is peak-normalized to -1 dBFS first: the TTS engines leave
    several dB of headroom, and on the device's small mono speaker that level
    is free loudness and perceived clarity. -write_xing 0 drops the LAME info
    frame so streamed fragments concatenate without stray silent frames.
    """
    samples = np.frombuffer(pcm, np.int16).astype(np.float32)
    peak = np.abs(samples).max() if len(samples) else 0.0
    if peak > 0:
        samples *= (32767 * 0.891) / peak
        pcm = samples.astype(np.int16).tobytes()
    ff = subprocess.run(
        ["ffmpeg", "-hide_banner", "-loglevel", "error",
         "-f", "s16le", "-ar", str(rate), "-ac", "1", "-i", "pipe:0",
         "-ar", "32000", "-ac", "1", "-b:a", "96k", "-write_xing", "0",
         "-f", "mp3", "pipe:1"],
        input=pcm,
        capture_output=True,
    )
    if ff.returncode != 0:
        app.logger.error("ffmpeg failed: %s", ff.stderr.decode(errors="replace")[:400])
        return None
    return ff.stdout


def _synthesize_piper(text):
    # piper writes raw 16-bit LE PCM to stdout with --output-raw.
    piper = subprocess.run(
        ["piper", "--model", PIPER_MODEL, "--output-raw"],
        input=text.encode(),
        capture_output=True,
    )
    if piper.returncode != 0:
        app.logger.error("piper failed: %s", piper.stderr.decode(errors="replace")[:400])
        return None, None
    return piper.stdout, int(PIPER_RATE)


def _synthesize_kokoro(text):
    samples, rate = _kokoro.create(
        text, voice=_kokoro_voice, speed=TTS_SPEED, lang="en-us"
    )
    pcm = (np.clip(samples, -1.0, 1.0) * 32767).astype(np.int16).tobytes()
    return pcm, rate


# Replies are highly repetitive ("It's 75 degrees."), so cache finished MP3s
# by exact reply text. Engine, voice, and speed are fixed for the process
# lifetime, so text alone is a sufficient key; a restart (the only way those
# change) empties the cache. ~15 KB per entry keeps 256 entries under 4 MB.
_tts_cache = OrderedDict()
_TTS_CACHE_MAX = 256
# TTS_CACHE=0 turns the cache off, mainly to measure true synthesis latency.
_TTS_CACHE_ON = os.environ.get("TTS_CACHE", "1") == "1"


@app.post("/tts")
def tts():
    data = request.get_json(force=True, silent=True) or {}
    text = (data.get("text") or "").strip()
    if not text:
        return Response(b"", mimetype="audio/mpeg")

    cached = _tts_cache.get(text) if _TTS_CACHE_ON else None
    if cached is not None:
        _tts_cache.move_to_end(text)
        return Response(cached, mimetype="audio/mpeg")

    if TTS_ENGINE == "kokoro":
        pcm, rate = _synthesize_kokoro(text)
    else:
        pcm, rate = _synthesize_piper(text)
    if pcm is None:
        return Response(b"", status=500)

    mp3 = _pcm_to_mp3(pcm, rate)
    if mp3 is None:
        return Response(b"", status=500)
    if _TTS_CACHE_ON:
        _tts_cache[text] = mp3
        if len(_tts_cache) > _TTS_CACHE_MAX:
            _tts_cache.popitem(last=False)
    return Response(mp3, mimetype="audio/mpeg")


# --- Streamed synthesis -----------------------------------------------------
#
# The device plays the response MP3 progressively as bytes arrive (kitsune
# hands the open HTTP stream straight to its MP3 decoder), so first-audio
# latency is set by how soon the FIRST fragment is synthesized, not the whole
# reply. /tts_stream splits the reply text, synthesizes fragment by fragment,
# and streams each finished fragment immediately. orb declares a padded
# Content-Length from X-Estimated-Bytes and fills the tail with silence.

# Measured for af_heart on this pipeline: ~0.062 s of audio per character of
# spoken text, with digits expanding to words ("75" -> "seventy five").
_SECONDS_PER_CHAR = 0.062
_CHARS_PER_DIGIT = 5.5
_MP3_BYTES_PER_SECOND = 96000 // 8  # CBR 96 kbps


def _estimate_bytes(text):
    """Upper-bound MP3 size for text, for orb's Content-Length declaration.

    Deliberately generous (the overrun would truncate speech; the cost of
    overestimating is only trailing silence at playback rate), but not so
    generous the tail drags: ~15 percent margin plus a third of a second.
    """
    chars = sum(_CHARS_PER_DIGIT if c.isdigit() else 1 for c in text)
    seconds = _SECONDS_PER_CHAR * chars * 1.15 + 0.35
    return int(seconds * _MP3_BYTES_PER_SECOND)


def _split_fragments(text):
    """Split reply text so the first fragment's playback covers the rest.

    The device's socket recv times out after one second, so once audio starts
    the stream must never starve: fragment one must play for longer than the
    remainder takes to synthesize (~1.15x real time). Splitting at 60 percent
    keeps that margin. Prefer a comma in the middle (a natural pause); fall
    back to the word boundary nearest 60 percent; leave short texts whole.
    """
    if len(text) < 24:
        return [text]
    lo, hi = int(len(text) * 0.2), int(len(text) * 0.7)
    comma = text.find(",", lo, hi)
    if comma != -1:
        return [text[: comma + 1], text[comma + 1 :].strip()]
    target = int(len(text) * 0.6)
    space = text.rfind(" ", 0, target)
    if space <= 0:
        return [text]
    return [text[:space], text[space + 1 :]]


@app.post("/tts_stream")
def tts_stream():
    data = request.get_json(force=True, silent=True) or {}
    text = (data.get("text") or "").strip()
    if not text:
        return Response(b"", mimetype="audio/mpeg")

    fragments = _split_fragments(text)

    def gen():
        for frag in fragments:
            if TTS_ENGINE == "kokoro":
                pcm, rate = _synthesize_kokoro(frag)
            else:
                pcm, rate = _synthesize_piper(frag)
            if pcm is None:
                return
            mp3 = _pcm_to_mp3(pcm, rate)
            if mp3 is None:
                return
            yield mp3

    resp = Response(gen(), mimetype="audio/mpeg")
    resp.headers["X-Estimated-Bytes"] = str(_estimate_bytes(text))
    return resp


if __name__ == "__main__":
    app.run(host="0.0.0.0", port=int(os.environ.get("PORT", "8090")))

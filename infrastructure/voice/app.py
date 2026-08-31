"""Voice sidecar for the Sense with Voice pipeline.

orb hands us the two halves it cannot do in Go:

    POST /stt   body = WAV (16-bit mono)   -> {"text": "..."}
    POST /tts   body = {"text": "..."}       -> MP3 bytes (16 kHz mono)

STT is faster-whisper (base.en, int8 on CPU); TTS is piper piped through
ffmpeg to the 16 kHz mono MP3 the device's libmad player expects. Everything
runs on CPU with the models baked into the image, so there is no per-request
network dependency.
"""
import os
import subprocess
import tempfile

from flask import Flask, request, jsonify, Response
from faster_whisper import WhisperModel

app = Flask(__name__)

# int8 keeps base.en small and fast enough for short commands on a CPU VM.
_model = WhisperModel("base.en", device="cpu", compute_type="int8")

PIPER_MODEL = os.environ.get("PIPER_MODEL", "/models/voice.onnx")
# lessac-medium synthesizes at 22.05 kHz; ffmpeg downsamples to the device's 16k.
PIPER_RATE = os.environ.get("PIPER_RATE", "22050")


@app.get("/health")
def health():
    return jsonify({"ok": True})


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


@app.post("/tts")
def tts():
    data = request.get_json(force=True, silent=True) or {}
    text = (data.get("text") or "").strip()
    if not text:
        return Response(b"", mimetype="audio/mpeg")

    # piper writes raw 16-bit LE PCM to stdout with --output-raw; ffmpeg wraps
    # and re-samples it to the 16 kHz mono MP3 the device plays.
    piper = subprocess.run(
        ["piper", "--model", PIPER_MODEL, "--output-raw"],
        input=text.encode(),
        capture_output=True,
    )
    if piper.returncode != 0:
        app.logger.error("piper failed: %s", piper.stderr.decode(errors="replace")[:400])
        return Response(b"", status=500)

    ff = subprocess.run(
        ["ffmpeg", "-hide_banner", "-loglevel", "error",
         "-f", "s16le", "-ar", PIPER_RATE, "-ac", "1", "-i", "pipe:0",
         "-ar", "16000", "-ac", "1", "-b:a", "48k", "-f", "mp3", "pipe:1"],
        input=piper.stdout,
        capture_output=True,
    )
    if ff.returncode != 0:
        app.logger.error("ffmpeg failed: %s", ff.stderr.decode(errors="replace")[:400])
        return Response(b"", status=500)
    return Response(ff.stdout, mimetype="audio/mpeg")


if __name__ == "__main__":
    app.run(host="0.0.0.0", port=int(os.environ.get("PORT", "8090")))

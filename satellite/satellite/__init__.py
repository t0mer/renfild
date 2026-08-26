"""Renfild satellite: the always-on ear.

Captures microphone audio, watches for the wake word, records the command that
follows, ships both to the Renfild server and plays back whatever comes home.
Everything heavy (STT, speaker ID, LLM, TTS) happens server-side.
"""

__version__ = "0.0.0-dev"

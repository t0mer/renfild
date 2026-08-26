"""Renfild speaker-embedding sidecar.

Wraps SpeechBrain's ECAPA-TDNN speaker-recognition model behind a tiny HTTP API.
Audio in, 192-dimensional embedding out — all matching logic lives in the Go
server, this service stays deliberately dumb.
"""

__version__ = "0.0.0-dev"

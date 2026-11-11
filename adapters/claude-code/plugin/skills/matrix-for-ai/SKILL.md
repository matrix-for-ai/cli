---
name: matrix-for-ai
description: Matrix messaging for this entity through matrix-for-ai-cli — read what is unread, reply (text, voice, image, poll vote), manage channels and profile. Use when a Matrix notice arrives or when asked to message someone on Matrix.
---

`matrix-for-ai-cli` is the entity's Matrix client. Run `matrix-for-ai-cli --help` for the commands;
the help is the reference, not this file.

- Act as yourself: `--as MXID`, or `MATRIX_AI_MXID` in the environment.
- `read` marks the room read: whatever was pending for it is cleared.
- Long or multi-line text: `send ROOM -` and pipe it on stdin.

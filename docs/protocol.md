# Protocol reference

Candidate rows use exactly `KIND<TAB>ESCAPED_DISPLAY<TAB>PADDED_BASE64_PAYLOAD`, framed by NUL for fzf input. The closed kind set is local, directory, file, zoxide, drive, and `KindVirtual`. Payloads are nonempty canonical payload base64: decoding then re-encoding must produce the identical padded text. The only virtual payload is the target `drives`, whose canonical payload is `ZHJpdmVz`; it is navigation metadata, not a filesystem path.

Callbacks encode commands such as `e:mi`, `e:en`, and loads such as `l:GENERATION`. `FZF_CURRENT_ITEM` carries the selected wire row. The callback parser rejects malformed actions instead of interpreting shell text.

## Loopback IPC

The listener binds `127.0.0.1:0`. Routes use the literal `RequestURI` `/v1/event`, `/v1/load`, or `/v1/preview`; `RawQuery` and `RawPath` must both be empty. Before constant-time token comparison, the server requires exactly one Authorization field with exactly one value in the grammar `Authorization: Bearer <token>`.

JSON event, preview, and error responses are limited to 64 KiB. Load `application/octet-stream` responses are limited to 64 MiB, and telemetry-empty client responses to 1 KiB. Decoding and draining use limit plus one bytes to distinguish overlimit input; every body is closed, and an overlimit response is not eligible for connection reuse.

Trace records are newline-delimited JSON schema 1. They contain a redacted session/path identity, event/outcome, generation, candidate and renderer counters, zoxide policy/counters/outcome, and microsecond durations. Paths and credentials are not emitted.

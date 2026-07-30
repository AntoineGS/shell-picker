# Protocol reference

Candidate rows use exactly `KIND<TAB>ESCAPED_DISPLAY<TAB>PADDED_BASE64_PAYLOAD`, framed by NUL for fzf input. The closed kind set is local, directory, file, zoxide, drive, and `KindVirtual`. Payloads are nonempty canonical payload base64: decoding then re-encoding must produce the identical padded text. The only virtual payload is the target `drives`, whose canonical payload is `ZHJpdmVz`; it is navigation metadata, not a filesystem path.

Callbacks have a closed grammar: events are exactly `e:mi`, `e:ma`, `e:es`, `e:fw`, `e:up`, `e:sl`, `e:hm`, or `e:en`; preview is exactly `p`; load is `l:<positive decimal generation>` (no zero, sign, or leading zero). `FZF_CURRENT_ITEM` carries the selected wire row. The callback parser rejects every other form instead of interpreting shell text.

Event JSON is `opcode`, `key`, `query_base64`, and `current_item_base64`; load JSON is `generation`; preview JSON is `phase`, `current_item_base64`, and, only where the phase permits it, `renderer`, `duration_us`, `child_starts`, `max_live_children`, and `outcome`. Preview phases are `resolve`, `started`, and `finished`; native finished telemetry has zero child starts/live children. The callback environment accepts only the controlled loopback address and token credentials (`SHELL_PICKER_ADDR` and `SHELL_PICKER_TOKEN`); credentials are strict canonical encodings, not shell arguments.

## Loopback IPC

```trace-schema
event,outcomes,generation,renderer,counters,optional_fields
@renderers,bat|bat-fallback|chafa|chafa-fallback|exiftool|exiftool-fallback|eza|eza-fallback|ffmpeg|ffmpeg-fallback|ffmpegthumbnailer|ffmpegthumbnailer-fallback|file|file-fallback|glow|glow-fallback|gzip|gzip-fallback|kitten|kitten-fallback|native|pdftoppm|pdftoppm-fallback|tar|tar-fallback|unzip|unzip-fallback|xz|xz-fallback,,,,
session.start,cd|cp,0,none,0/0,
generation.start,ok,0|nonzero,none,0/0,
generation.publish,ok,0|nonzero,none,0/0,actor_queue_wait_us|candidate_count|local_us|path|transform_us|zoxide_*:policy+outcome
generation.discard,cancelled|error|stale|superseded,0|nonzero,none,0/0,actor_queue_wait_us|local_us|transform_us|zoxide_*:policy+outcome
fzf.start,ok,0,none,0/0,
fzf.exit,aborted|error|ok,0,none,0/0,
callback.event,en|es|fw|hm|ma|mi|sl|up,0,none,0/0,callback_ipc_us
callback.load,error|ok,0|nonzero,none,0/0,load_us
preview.dispatch,error|ok,0,required:@renderers,0/0,
preview.finished,error|ok,0,required:@renderers,native=0/0;non-native=0/0|1/0|1/1|2/0|2/1|3/0|3/1,
preview.cancel,cancelled,0,required:@renderers,0/0,
preview.exit,error|ok,0,required:@renderers,native=0/0;non-native=0/0|1/0|1/1|2/0|2/1|3/0|3/1,
session.close,aborted|accepted|error,0,none,0/0,
```

The listener binds `127.0.0.1:0`. Routes use the literal `RequestURI` `/v1/event`, `/v1/load`, or `/v1/preview`; `RawQuery` and `RawPath` must both be empty. Before constant-time token comparison, the server requires exactly one Authorization field with exactly one value in the grammar `Authorization: Bearer <token>`.

Only POST with `Content-Type: application/json` is admitted. JSON event, preview, and error bodies are limited to 64 KiB. Load `application/octet-stream` responses are limited to 64 MiB, and telemetry-empty client responses to 1 KiB. Decoding and draining use limit plus one bytes to distinguish overlimit input; every body is closed, and an overlimit response is not eligible for connection reuse. The server also disables keep-alives and bounds concurrent handlers.

Trace is newline-delimited JSON schema 1. Every record has `schema`, `time`, `session`, `event`, `generation`, `candidate_count`, `renderer`, `child_starts`, `max_live_children`, and `outcome`; optional fields are `path`, `zoxide_policy`, `zoxide_attempts`, `zoxide_starts`, `zoxide_exits`, `zoxide_processes`, `zoxide_live`, `zoxide_max_live`, `actor_queue_wait_us`, `callback_ipc_us`, `local_us`, `zoxide_us`, `zoxide_outcome`, `transform_us`, and `load_us`. Events are `session.start`, `generation.start`, `generation.publish`, `generation.discard`, `fzf.start`, `fzf.exit`, `callback.event`, `callback.load`, `preview.dispatch`, `preview.finished`, `preview.cancel`, `preview.exit`, and `session.close`. Session and path values are truncated SHA-256 redactions; paths and credentials are not emitted. Trace is diagnostic observation, not a performance guarantee, authorization record, or unredacted audit log.

The machine table is the validator contract. `@renderers` is the exact renderer set; every preview event requires one of those values, while every non-preview event requires an empty renderer. A generation may be nonzero only for generation start/publish/discard and callback load; zero remains validator-valid for those events. Candidate count and path apply only to generation publish. Generation timings apply only to publish/discard, callback IPC only to callback event, and load timing only to callback load. The `zoxide_*` bundle means policy plus outcome and may include zoxide counters/duration; it occurs only on generation publish/discard. Policy is cached/fresh, and zoxide outcome is ok, missing, process-error, malformed, timeout, cancelled, not-run, or cached. Attempts bound starts; exits and processes equal starts; live is zero and `zoxide_max_live` does not exceed starts. Child counters are always zero except on preview.finished and preview.exit; there child starts are 0..3, max live is 0..1 and never exceeds starts, and native remains 0/0.

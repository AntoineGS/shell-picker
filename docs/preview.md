# Preview pipeline

`KindVirtual` is not a filesystem path and is rejected before stat or tool work. Filesystem candidates are classified from metadata/prefix, then use category tool precedence: eza for directories, glow then bat for markdown, bat for text, kitten then chafa for images, archive tools for archives, and native renderers when tools are absent or produce no useful output.

Default limits are a 10-second deadline, 4 MiB output/internal/archive-decompression budget, 100 archive entries, and 64 MiB maximum accepted/cached artifacts. Cache cleanup is best-effort polling, not a hard transient disk cap. Tool paths are absolute and arguments prevent option injection. Unix inherited-group cancellation intentionally terminates the callback without fallback/final telemetry; Windows uses nested containment.

There is at most one simultaneously live converter/renderer and at most three sequential starts are allowed for converter plus kitten plus chafa. The native fallback starts none. Cache keys are SHA-256; artifacts require `Lstat` regular-file validation and exclusive temporary files. Publication is atomic no-replace (Unix link; Windows `MoveFileExW`); validated race winners are used. Renderer bytes are intentionally not a parity promise.

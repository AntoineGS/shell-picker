# Two-Line Picker Display Design

**Date:** 2026-07-30

## Goal

Give the current location and editable query separate lines so long paths no longer consume query space. Preserve the useful match count while hiding selection counts that do not help the current picker operation.

## Visible Contract

The current location is the first line and the input is the second line:

```text
··/long/path/to/current/
[I] query text                   7/42
```

The mode prompt is one of `[I] `, `[N] `, `[A] `, or `[A!] `. It has no `> ` suffix. The current path is never part of the prompt.

If the location is wider than the available main-pane content width, preserve its rightmost visible portion. Prefix a truncated location with `··` only when enough width remains for both the marker and path text. At extremely small widths, path-tail characters take priority over the marker.

The query retains fzf's native cursor-aware horizontal scrolling. Typing at the end hides the query's beginning as needed; moving the cursor toward the beginning scrolls that text back into view for editing.

Finder information follows these rules:

- CD always renders `matched/total`, regardless of its internal one-item selection state.
- CP renders `matched/total` when no item is selected.
- CP renders `matched/total (selected)` when one or more items are selected.

## fzf Layout

Launch fzf with a normal header and `--header-first`, keeping the existing full, reverse layout and preview. The initial header is the complete escaped current-location display. A startup transform replaces it with the width-aware rendering before interaction. The prompt contains only the mode token.

Keep fzf's native query renderer. Do not implement a second query buffer or force the final characters to remain visible when the cursor has moved elsewhere.

Bind resize to a typed shell-picker display transform. It recomputes the same header from the authoritative current session location, so resizing an open picker does not require a navigation or mode event to repair the display.

## State And Effects

The session continues to hold the authoritative `Location` and `Mode`. Prompt construction derives only the mode token. Header construction derives only `pathutil.PromptDisplay(Location)`.

Add an optional header field to `protocol.Effect`. Mode-only transitions update the prompt without unnecessarily changing the header. Navigation effects include the new full header alongside the existing reload, query, selection, mode, and cursor changes.

Action rendering keeps arbitrary path text in the final, colon-delimited `change-header` action. The mode prompt has a closed four-value vocabulary and can use a bounded parenthesized `change-prompt` action before the header. This preserves the existing rule that user-controlled display text cannot terminate its argument and inject another action.

The action order for navigation remains clear-selection, reload, clear-query, wait, first, prompt update, then terminal header update. Effects without a header retain the existing ordering.

## Display IPC

Add one authenticated, read-only display operation to session IPC. It accepts only its strict empty request shape and returns the current full escaped header. The backend reads the actor's current snapshot without applying a transition, changing generation, rebuilding candidates, or recording a navigation metric.

The typed fzf callback grammar gains a display command used only by startup and resize transforms. The callback:

1. Retrieves the current full header through display IPC.
2. Reads fzf's exported dimensions.
3. Computes the main-pane content width, accounting for the right-hand preview and full-style frame/padding.
4. Produces the left-truncated header.
5. Emits one terminal `change-header` action.

Ordinary navigation callbacks already receive the new full header in their event response and apply the same local width formatter without an additional display request.

## Width Handling

Read `FZF_COLUMNS` and `FZF_PREVIEW_COLUMNS` as positive decimal integers no greater than 1000. With the configured visible right preview, the main-pane content width is `FZF_COLUMNS - FZF_PREVIEW_COLUMNS - 8`: each full-style pane uses four columns for its borders and horizontal content padding. If fzf reports no preview width, use `FZF_COLUMNS - 4`. Reject a result that is not positive. The real-fzf test fixes this accounting against fzf 0.74.1.

Truncation operates on the escaped display string and never cuts within a UTF-8 encoding. Count ASCII graphic runes as one terminal cell and non-ASCII graphic runes conservatively as two. Overestimating width may omit extra prefix text but must not let fzf clip the retained path tail on the right. Roots, `Drives/`, Windows roots, trailing separators, escaped invalid bytes, and very small widths retain valid display text.

Missing, malformed, zero, negative, or unreasonably large dimensions produce no header action. fzf therefore retains the previous header; at startup, the complete initial header remains available as the fallback. A display IPC failure has the same no-action behavior and does not mutate session state.

## Finder Information

Use a fixed, typed `--info-command` routed through shell-picker's configured fzf shell. The command includes the picker kind in its validated grammar and performs no IPC.

The formatter reads only `FZF_MATCH_COUNT`, `FZF_TOTAL_COUNT`, and `FZF_SELECT_COUNT` as nonnegative decimal integers of at most 10 digits and no greater than 1,000,000,000. It emits the visible contract's exact formats. Missing or malformed counters emit an empty string successfully, preventing diagnostics from occupying the input line.

The info command contains no shell expression and does not evaluate fzf environment text. It is intentionally local because fzf invokes it synchronously during result and selection updates.

## Error And Security Behavior

- Display refresh is read-only and cannot trigger candidate generation or selection changes.
- Callback command kinds and picker values remain closed vocabularies.
- Header and prompt actions reject CR, LF, and NUL.
- Arbitrary path text remains the terminal action argument in every generated sequence.
- Numeric environment values are parsed with explicit bounds and never treated as commands.
- Header refresh failures preserve the previous display.
- Finder-info failures render no finder information rather than an error message.

No system-shell snippet, listener port, or externally writable state is introduced.

## Testing

Unit coverage includes:

- Exact mode-only prompts, including `[A!] `.
- CD and CP finder-info formatting with zero and nonzero selections.
- Missing, malformed, negative, overflowing, and over-limit dimension and counter values.
- Left truncation at normal, exact-fit, tiny, root, drive, escaped-byte, and Unicode boundaries.
- Effect rendering order and terminal header-action injection corpora.
- fzf options for header-first, startup/resize display transforms, and typed info commands.

Session IPC coverage includes:

- Strict display request and response decoding.
- Authentication, request-size, route, concurrency, cancellation, and shutdown behavior.
- Proof that display reads do not change generation or invoke candidate building.

Installed-fzf PTY coverage on fzf 0.74.1 verifies:

- A long initial path retains its final characters.
- Navigation updates the separate header without taking query width.
- Resizing recalculates the visible path tail.
- A long query shows its tail while typing and scrolls back to its beginning when the cursor moves there.
- CD never shows `(0)` or a nonzero selection count.
- CP omits `(0)` and shows `(1)` after selecting an item.
- Insert, Normal, Add, and Add-error modes retain the two-line layout.

Run the complete standard, race, security, cross-platform compile, and real-fzf suites after implementation.

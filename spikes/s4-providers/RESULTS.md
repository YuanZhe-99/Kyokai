# S4 — AI provider round trips (OpenAI + Claude)

Status (2026-10-16): **Claude verified live; OpenAI waits for a key.**
Nothing in this spike runs anything on a desktop.

| | Status |
|---|---|
| OpenAI field names checked against openai-python | done (`v3.32.0`, types last changed at `f348ec87b9`, 2026-09-09) |
| Claude shapes checked against the claude-api skill + computer-use-tool docs | done (fetched 2026-10-16) |
| Request builders, response parsers, action normalization | done, 12 offline tests pass (`cargo test`) |
| Live two-turn round trip, OpenAI | **waiting** for `OPENAI_API_KEY` |
| Live two-turn round trip, Claude | **done**: `claude-opus-5-5`, turn 1 HTTP 200 with three batched members (`left_click`, `wait`, `screenshot`), turn 2 HTTP 200 `end_turn` confirming the task; no fallback used |
| Live fixtures parse in the tests | **done** for Claude (`fixtures/live/claude`, `live_fixtures_parse` passes) |

## How to run

Offline (no network):

```bash
cargo test --manifest-path spikes/s4-providers/Cargo.toml
```

Live (the user runs this in their own terminal, with keys already in the environment; never paste a
key into a chat):

```bash
cargo run --manifest-path spikes/s4-providers/Cargo.toml -- --live
```

- A provider whose key is missing is skipped. `--provider openai|claude` runs only one.
- `--openai-model`, `--claude-model` (or `KYOKAI_OPENAI_MODEL` / `KYOKAI_CLAUDE_MODEL`) override
  the defaults `gpt-6.1-sol` / `claude-opus-5-5`.
- If OpenAI returns `pending_safety_checks`, the run stops. Re-run with `--ack-safety` to send
  them back as `acknowledged_safety_checks`. (In the product this only happens after the user approves.)
- `--openai-input-detail` (default `original`) sets `input_image.detail` on the first screenshot.
  `--openai-screenshot-detail D` adds `detail` to the `computer_screenshot` reply (see Differences).
- `--screens DIR` writes the two synthetic PNGs so you can look at them.
- Each request and response is saved, scrubbed, to `fixtures/live/<provider>/turn{1,2}.{request,response,error}.json`.
  Afterwards `cargo test` parses them. Then commit them as fixtures.

Keys are read from `OPENAI_API_KEY` and `ANTHROPIC_API_KEY`. For Claude, the spike also accepts
`ANTHROPIC_AUTH_TOKEN`, or as a last resort `ant auth print-credentials --access-token`, which it
sends as `Authorization: Bearer` with the extra beta `oauth-2025-04-20`. Keys only ever go into
request headers. They are never printed or logged, and the scrubber replaces any copy of a key
that shows up in a saved body with `<REDACTED>`.

## The scenario

Two generated 1280×720 PNGs ([src/screens.rs](src/screens.rs)): a "SAVE FILE" dialog with a blue
**SAVE** button at `[560,400,720,450]` (centre 640,425). In the second one the button is pressed
and a green "SAVED" line has appeared. The task is "Click the SAVE button in the dialog, then
confirm that the file was saved." Turn 1 sends screenshot 1. Every computer action is answered as
if it had worked, with screenshot 2 (or a crop of it for `zoom`), and turn 2's response is parsed.

## Request/response shapes used

### OpenAI — `POST https://api.openai.com/v1/responses`, `Authorization: Bearer`

Turn 1:

```json
{"model": "gpt-6.1-sol", "tools": [{"type": "computer"}],
 "input": [{"role": "user", "content": [
   {"type": "input_text", "text": "<task>"},
   {"type": "input_image", "image_url": "data:image/png;base64,<SCREENSHOT_1>", "detail": "original"}]}]}
```

Response items: `reasoning`, `message` (`output_text` / `refusal`), `computer_call`:
`{id, call_id, status, pending_safety_checks: [{id, code?, message?}], actions?: [...], action?: {...}}`.
Action variants (SDK `computer_action.py`): `click{button: left|right|wheel|back|forward, x, y, keys?}`,
`double_click{x, y, keys?}`, `drag{path: [{x,y}…], keys?}`, `keypress{keys}`, `move{x, y, keys?}`,
`screenshot`, `scroll{x, y, scroll_x, scroll_y, keys?}`, `type{text}`, `wait`.

Turn 2 (one item per `computer_call`; `tools` must be re-sent because they are not inherited):

```json
{"model": "gpt-6.1-sol", "tools": [{"type": "computer"}], "previous_response_id": "resp_…",
 "input": [{"type": "computer_call_output", "call_id": "call_…",
            "output": {"type": "computer_screenshot", "image_url": "data:image/png;base64,<SCREENSHOT_2>"},
            "acknowledged_safety_checks": [ /* only after approval */ ]}]}
```

### Claude — `POST https://api.anthropic.com/v1/messages`

Headers: `x-api-key`, `anthropic-version: 2023-06-01`,
`anthropic-beta: thinking-display-updates-2026-08-18,server-side-fallback-2026-07-01`. The computer
toolset itself is GA and needs no beta header.

```json
{"model": "claude-opus-5-5", "max_tokens": 16000,
 "thinking": {"type": "adaptive", "display": "updates"},
 "output_config": {"effort": "high"},
 "fallbacks": "default",
 "system": "…1280x720 desktop… End each batch with a screenshot.",
 "tools": [{"type": "computer_toolset_20260801"}],
 "messages": [{"role": "user", "content": [{"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "<SCREENSHOT_1>"}},
                                           {"type": "text", "text": "<task>"}]}]}
```

Response: `content` contains `thinking` blocks (non-empty means a progress update, otherwise empty
with a `signature`), `text`, `fallback{from,to}`, and member
`tool_use{id, name: <member>, toolset_name: "computer", input}`. `stop_reason` is one of
`tool_use | end_turn | refusal | max_tokens | …`. `stop_details{type, category, explanation}` is set
only on a refusal. A fallback was served when `usage.iterations` contains a `fallback_message` entry.

Turn 2 is the turn-1 body plus the assistant `content` unchanged, plus one `user` message with one
`tool_result` per `tool_use`, in order, each with `"toolset_name": "computer"`. `screenshot` and
`zoom` get an `image` block, `cursor_position` gets `X=…, Y=…`, and the rest get `"OK"`. The batch
stops at the first invalid action. That action gets `is_error: true` with the reason, and every
later one gets `is_error: true` with
`"Not executed: an earlier computer action in this turn failed."`.

## Kyokai action schema and normalization ([src/action.rs](src/action.rs))

`Screenshot · Zoom{region} · Move{to, held} · Click{at?, button, count 1/2/3, modifiers} ·
MouseDown/MouseUp{button} · Drag{path, modifiers} · Scroll{at?, dx, dy, modifiers} · Type{text} ·
KeyCombo{keys, repeat} · HoldKey{keys, duration_ms} · Wait{duration_ms} · CursorPosition`

| Concern | OpenAI | Claude | Kyokai |
|---|---|---|---|
| Coordinates | `x`, `y` | `coordinate: [x, y]` (optional on clicks/scroll = current cursor) | `Point`, `Option<Point>` |
| Buttons | `button` incl. `wheel`, `back`, `forward`; `double_click` = left ×2 | member name (`left/right/middle/double/triple_click`) | `Button` + `count` |
| Modifiers | `keys: [...]` on click/move/drag/scroll | `text: "ctrl+shift"` on click/drag/scroll (non-modifiers rejected) | `Vec<Key>` |
| Key names | uppercase DOM-ish (`CTRL`, `ENTER`, `ArrowLeft`, `CMD`) | xdotool (`Return`, `Page_Up`, `super`, `alt+Tab`) | lowercase canonical (`ctrl`, `enter`, `pageup`, `meta`, single chars); unknown names are errors |
| Combos | `keypress.keys` array (modifiers reordered first) | `+`-joined string (`ctrl++` / `plus` handled) + `repeat` 1–100 | `KeyCombo{keys, repeat}` |
| Scroll | `scroll_x/scroll_y` in **pixels** | `scroll_direction` + `scroll_amount` in **wheel clicks** | `WHEEL_DELTA` units (120 = 1 notch); OpenAI 100 px = 1 notch (assumption, see below) |
| Wait / hold | `wait` with no duration → 2000 ms | `duration` seconds (≤300, float) | ms |
| Drag | multi-point `path` | `start_coordinate` → `coordinate`; also `left_mouse_down/up` + `mouse_move` | `Drag{path}` / `MouseDown`/`MouseUp` |
| Zoom / cursor | none | `zoom{region}`, `cursor_position` | `Zoom`, `CursorPosition` |

`validate()` rejects coordinates outside the screenshot, an empty or inverted zoom region, drags with
fewer than 2 points, `repeat` outside 1–100 and durations over 300 s. In the Claude reply, a rejected
action becomes an `is_error` result and the rest of the batch is halted.

## Differences from the docs / things to watch in the live run

1. **OpenAI `computer_screenshot.detail`.** The computer-use guide's examples put
   `detail: "original"` on the `computer_screenshot` output, but the SDK's
   `ResponseComputerToolCallOutputScreenshotParam` only has `type`, `image_url` and `file_id`. The
   spike follows the SDK by default. `--openai-screenshot-detail original` tests the guide's form.
   *Live check:* is it accepted, and does it change coordinate accuracy?
2. **OpenAI `input_image.detail` is `Required`** in the SDK param type, with `original` as one of the
   values. The spike sends `original`.
3. **OpenAI chaining.** The SDK's own `ComputerToolset` runner (`lib/computer.py`) refuses
   `previous_response_id`. It replays the full history instead, with
   `include: ["reasoning.encrypted_content"]`, so the loop also works when response storage is off.
   This spike uses `previous_response_id` as the plan says. Phase 4 should support both (see below).
4. **OpenAI does not document the scroll unit or the key names** in the guide. Pixels and DOM-style
   uppercase names come from the reference drivers. The live run shows the real spelling, and the
   normalizer accepts both spellings either way.
5. **OpenAI `wait` has no duration.** 2 s is a Kyokai choice.
6. **Claude: no `display_width_px`/`name` on the toolset entry.** Coordinates are in the pixel space
   of the screenshots we send. Opus 5.5 rejects `computer_20251124` (400) and forced `tool_choice`
   (400), so the spike sends neither.
7. **Claude thinking blocks under `display: "updates"`.** Progress notes come back as `thinking`
   blocks with text. Reasoning blocks come back empty. All of them are echoed back unchanged
   (preserved thinking: editing history causes a 400 for accounts created after 2026-08-31).
8. **Spike build: TLS.** `reqwest` + rustls pulls in `ring`, which needs `clang` on Windows ARM64.
   The VS Clang component is installed (`…\VC\Tools\Llvm\ARM64\bin`) but is not on PATH in a plain
   shell. The spike uses `native-tls` (schannel / Security.framework) so it builds anywhere. The
   product build will need clang on PATH, or `CC_aarch64_pc_windows_msvc` set, for rustls+ring.

Fill in after the live run: the HTTP statuses, the actions each model chose, any
`pending_safety_checks`, whether turn 2 ended the task (`end_turn` / a `message`), token usage, and
anything the parsers rejected.

## Recommendations for the Phase 4 adapters

- **One trait, batch-oriented:** `fn next(&mut self, results) -> ProviderTurn { actions: Vec<(CallRef, Result<Action>)>, progress, text, stop: Stop }`.
  For OpenAI, `CallRef` is a `call_id` plus an index into `actions`. For Claude it is a `tool_use_id`.
  Run the actions in order with an approval check before each one, stop at the first failure, and
  answer the rest of the batch as the provider requires (OpenAI: one screenshot per `call_id`;
  Claude: one `tool_result` per block, halt text after a failure).
- **Keep provider JSON opaque for replay.** Store Claude's assistant `content` and OpenAI's output
  items exactly as received and send them back unchanged. Never rebuild them from parsed structs.
- **OpenAI:** support both `previous_response_id` (default, lowest bandwidth) and full replay with
  `reasoning.encrypted_content` (needed for `store: false` / ZDR-like setups). Show
  `pending_safety_checks` in the approval UI and echo them only after the user approves. Parse
  `actions` first, then fall back to `action`.
- **Claude:** dispatch on (`toolset_name`, `name`). Treat unknown members as `is_error`. Show
  non-empty `thinking` blocks as status lines in the AI panel. Handle `refusal` before reading
  content, and log `stop_details.category` and `usage.iterations` (fallback served). Keep history
  append-only. Bound it with server-side tool-result clearing or compaction, never by pruning it
  ourselves. Put `cache_control` on the toolset entry and system prompt.
- **Normalization lives in Kyokai, not in the executor.** The executor receives only canonical keys
  (`ctrl/shift/alt/meta`, lowercase names, single chars) and `WHEEL_DELTA` scroll units. On macOS,
  map `meta` → Command. Calibrate OpenAI's px→notch factor (100) during the Phase 4 live tests on
  real apps.
- **Screenshots:** send Claude ≤ 2576 px on the long edge (we send 1280×720 or 1366×768 and scale
  the coordinates back up). For OpenAI, keep the same downscale and coordinate mapping, and A/B test
  `detail: "original"`.
- **Secrets:** keys stay in `secrets.bin` / the OS store, reach `reqwest` only as headers, and never
  appear in logs. Keep the scrubber's literal-key redaction as a test helper for any captured
  traffic.

## Files

- [src/action.rs](src/action.rs): the action enum, key normalization and validation
- [src/openai.rs](src/openai.rs), [src/claude.rs](src/claude.rs): request builders and parsers
- [src/screens.rs](src/screens.rs): synthetic screenshots and zoom crops
- [src/scrub.rs](src/scrub.rs): fixture scrubbing
- [src/main.rs](src/main.rs): the CLI and the `--live` runner
- [fixtures/docs/](fixtures/docs/): hand-written fixtures from the SDK types and docs (all 17 Claude members; every OpenAI action type, the legacy `action` field, safety checks, refusal + fallback)
- `fixtures/live/`: written by `--live`
- [tests/fixtures.rs](tests/fixtures.rs): the offline tests

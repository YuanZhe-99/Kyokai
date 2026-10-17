# Third-party code

Kyokai is licensed under AGPL-3.0-only. This inventory lists every upstream it reuses or embeds,
what it uses, and what that obliges. `THIRD_PARTY_NOTICES.md` holds the notices themselves.

## Inventory

| Component | Repository and version | Used for | License | Modifications | Obligations |
|---|---|---|---|---|---|
| MyGo | `egoist/mygo` v0.5.5 | GUI framework, packaging, updater | MIT | none | keep the copyright notice; its embedded Kage compiler and MingCute icon are Apache-2.0 |
| Easytier-MyGo files | `YuanZhe-99/Easytier-MyGo` | `tools/checkdocs`, `internal/fsutil`, the `internal/version` pattern, the MyGo maintenance skill, the conventions | GPL-3.0, relicensed under AGPL-3.0 by its author | adapted names and comments | none beyond Kyokai's own license |
| EasyTier | `EasyTier/EasyTier` v2.7.0, commit `8b7f1f0` | P2P networking, embedded in the engine's core library | LGPL-3.0 | none; its `thunk-rs` build dependency is replaced by a no-op crate | ship the LGPL and GPL texts and the source location; Kyokai's own source being public lets users relink |
| arboard | `1Password/arboard` 3.7.0 | the clipboard (text and images, with the `image` crate) | MIT OR Apache-2.0; its Windows crates `clipboard-win` and `error-code` are BSL-1.0 | none | their notices ship in `RUST_LICENSES.txt` |
| RustDesk | `rustdesk/rustdesk` 1.5.0, commit `fada664` | Windows screen capture: `libs/scrap/src/dxgi/{mod,gdi,hdr}.rs` in `core/third_party/rustdesk-dxgi` (Magnification module left out, logging through `log`, scrap's types and libyuv's rotation in a shim, an upstream test disabled); video QoS: `src/server/video_qos.rs` and its tests in `core/third_party/rustdesk-qos` (its server-module and scrap items in a shim, unchanged otherwise); input logic and helper code to follow | AGPL-3.0 | each vendored file keeps its header and gains a "Modified by Kyokai" note | offer the corresponding source, also to remote users |

## Never used

`rustdesk/hbb_common` and `rustdesk-org/hwcodec` carry no license grant, RustDesk's FFmpeg build is
configured with `--enable-gpl`, and RustDesk's rendezvous (hbbs) and relay (hbbr) servers are not
part of Kyokai's design.

# Third-party code

Kyokai is licensed under AGPL-3.0-only. This inventory lists every upstream it reuses or embeds,
what it uses, and what that obliges. `THIRD_PARTY_NOTICES.md` holds the notices themselves.

## Inventory

| Component | Repository and version | Used for | License | Modifications | Obligations |
|---|---|---|---|---|---|
| MyGo | `egoist/mygo` v0.5.5 | GUI framework, packaging, updater | MIT | none | keep the copyright notice; its embedded Kage compiler and MingCute icon are Apache-2.0 |
| Easytier-MyGo files | `YuanZhe-99/Easytier-MyGo` | `tools/checkdocs`, `internal/fsutil`, the `internal/version` pattern, the MyGo maintenance skill, the conventions | GPL-3.0, relicensed under AGPL-3.0 by its author | adapted names and comments | none beyond Kyokai's own license |
| EasyTier | `EasyTier/EasyTier` v2.7.0, commit `8b7f1f0` | P2P networking, embedded in the engine (from Phase 2) | LGPL-3.0 | none planned | ship the LGPL and GPL texts and the source location; Kyokai's own source being public lets users relink |
| RustDesk | `rustdesk/rustdesk` 1.5.0, commit `fada664` | vendored Windows capture, input logic and helper code (from Phase 3) | AGPL-3.0 | each vendored file keeps its header and gains a "Modified by Kyokai" note | offer the corresponding source, also to remote users |

## Never used

`rustdesk/hbb_common` and `rustdesk-org/hwcodec` carry no license grant, RustDesk's FFmpeg build is
configured with `--enable-gpl`, and RustDesk's rendezvous (hbbs) and relay (hbbr) servers are not
part of Kyokai's design.

# Acceptance

The specification's mandatory test scenarios (§26) and its two-computer acceptance scenario (§27),
each with where it stands and the evidence. **Verified** means a real run, recorded in
[platform-notes.md](platform-notes.md); **Tested** means automated tests only; **In code** means
implemented and not yet run; **Open** means not done. OpenAI is the specification's provider;
Kyokai runs the same flow with any configured provider, and the live runs so far used Claude.

## Device identity

| Scenario | Status | Evidence |
|---|---|---|
| Device ID survives application restart | Verified (Windows) | platform notes: Secrets, App (ARM64 package) |
| Device ID survives EasyTier reconnection | Verified (Windows) | platform notes: Remote x64 VM (the session resumed after the node restarted) |
| Device ID unchanged when the network address changes | Verified (Windows) | platform notes: Listener rebinding; the ID derives from the device key only |
| Identity restored securely from backup | Verified (Windows) | platform notes: Identity backup; `a_backup_restores_the_same_identity_and_only_with_its_passphrase` |
| Cryptographic identity mismatch rejected | Tested | `a_changed_host_key_is_refused` |

## Authentication

| Scenario | Status | Evidence |
|---|---|---|
| Temporary password works | Verified (Windows) | platform notes: Sessions |
| Refreshing the temporary password invalidates the old one | Verified (Windows) | platform notes: Engine (`smoke.sh user` regenerates); the old record is replaced in memory |
| Permanent password works | Verified (Windows) | platform notes: Sessions |
| Temporary and permanent passwords independently enabled | Tested | the two OPAQUE slots; `unattended_grants_need_the_permanent_password_and_follow_whom_the_host_serves` |
| Invalid passwords rejected | Verified (Windows) | platform notes: Sessions; `refusals_are_named` |
| Repeated failures rate-limited | Tested | `an_address_is_blocked_after_five_failures_and_pinned_keys_skip_the_backoff` |
| Trusted devices can be revoked | Tested | `revoking_a_device_ends_its_sessions_and_its_resumes` |

## EasyTier networking

| Scenario | Status | Evidence |
|---|---|---|
| Two devices join a standard EasyTier network | Verified (Windows ↔ Windows) | platform notes: EasyTier in the engine, Remote x64 VM |
| Only standard EasyTier settings are needed | Verified | server address, network name and secret ([networking.md](networking.md)) |
| Device ID resolution without IP addresses | Verified | platform notes: Sessions, Remote x64 VM |
| No-TUN connectivity | Verified (Windows) | platform notes: S1 EasyTier spike, Firewall prompts |
| Dynamic addresses do not break identity | Verified (Windows) | platform notes: Listener rebinding |
| Network interruption handled cleanly | Verified (Windows) | platform notes: Remote x64 VM; `a_dropped_session_resumes_without_a_new_login` |

## Manual Computer Use

| Scenario | Status | Evidence |
|---|---|---|
| Remote screen viewing | Verified (Windows) | platform notes: The VM's real desktop, Screen video |
| Mouse control | Verified (Windows) | platform notes: The helper (a click on the UAC secure desktop) |
| Keyboard control | Verified (Windows) | platform notes: Typing into Windows 11 Notepad |
| Multi-monitor selection | Verified (Windows VM) | platform notes: Displays |
| DPI coordinate mapping | Tested | the input mapping tests over the virtual desktop; a mixed-DPI setup has not been tried |
| Disconnection releases resources | Tested | held keys and buttons released, capture and encoder dropped with the session |

## AI Computer Use (the specification's "OpenAI Computer Use")

| Scenario | Status | Evidence |
|---|---|---|
| A sends a task to B | Verified (Windows ↔ Windows VM) | platform notes: The VM's real desktop |
| B calls its provider with its own credentials | Verified (Claude); OpenAI waits for a key | platform notes: AI runtime |
| B captures its own desktop | Verified (Windows) | platform notes: AI runtime |
| B executes validated actions | Verified on synthetic screens; Tested | `claude_task_clicks_and_finishes` |
| A receives progress | Verified (Windows) | platform notes: AI from a controller |
| Invalid tool calls rejected | Tested | `openai_rejects_unknown_action_and_key`, `claude_rejects_bad_members` |
| Out-of-range coordinates rejected | Tested | `stale_and_out_of_range_actions_are_rejected` |
| Stale observations rejected | Tested | `stale_and_out_of_range_actions_are_rejected` |
| API timeouts do not deadlock | Tested | `one_task_at_a_time_and_cancel_stops_a_request` |
| Task completion verified | Verified on synthetic screens | the model took a screenshot and confirmed the result before finishing |

## Authorization and safety

| Scenario | Status | Evidence |
|---|---|---|
| Unauthorized devices cannot start AI tasks | Tested | `ai_requests_need_their_permissions` |
| VIEW_SCREEN does not imply RUN_AI_TASK | Tested | `confirmations_grant_only_what_was_asked_and_flag_new_keys`, `ai_requests_need_their_permissions` |
| A cannot retrieve B's API key | Tested | `settings_and_keys_never_expose_the_key`; no message carries it |
| Manual take-over stops AI actions | Tested | `the_stop_shortcut_cancels_and_a_take_over_pauses` |
| Emergency stop prevents further input | Tested | `the_stop_shortcut_cancels_and_a_take_over_pauses`, `a_response_after_a_pause_never_acts` |
| Connection loss pauses remote AI | Tested | `a_controllers_task_pauses_when_its_session_ends` |
| Reconnection does not repeat actions | Tested | commands are never re-sent; idempotent task start |
| Sensitive actions need approval | Tested | `every_action_policy_asks_and_a_decline_runs_nothing`, `openai_safety_check_waits_for_approval_and_is_acknowledged` |
| Revocation stops unauthorized execution | Tested | `revoking_a_device_ends_its_sessions_and_its_resumes` |

## Application lifecycle

| Scenario | Status | Evidence |
|---|---|---|
| No automatic startup by default | Verified (Windows) | platform notes: App (ARM64 package) |
| No privileged service installed by default | Verified (Windows) | the helper installs only when asked, with one administrator prompt |
| Exit stops networking | Verified (Windows) | platform notes: App (killing the GUI ends the engine) |
| Exit stops AI execution | Verified (Windows) | the engine, which runs AI tasks, ends with the app |
| No permanent networking daemon remains | Verified (Windows) | platform notes: The helper (uninstall leaves nothing) |
| Configuration paths displayed | Verified (Windows) | Settings → Storage and Diagnostics |

## Build and release

| Scenario | Status | Evidence |
|---|---|---|
| Each platform builds | Verified | local Windows ARM64 and x64; CI on macOS Apple silicon and Intel |
| Rust native libraries load | Verified | ABI smoke test on Windows and macOS CI |
| GUI works with the packaged core | Verified (Windows) | platform notes: App (ARM64 and x64 packages) |
| CI artifacts are real packages | Verified (Windows x64) | platform notes: Remote x64 VM (the CI installer) |
| Release checksums match | Open | no release yet |
| Missing signing reported | Verified | unsigned builds are labelled; the helper refuses unsigned updates without a prompt |
| README acknowledgements | Done | README, Acknowledgements |
| Third-party obligations | Done | [third-party.md](third-party.md), `THIRD_PARTY_NOTICES.md`, `RUST_LICENSES.txt` |

## Two-computer acceptance (§27)

| Step | Status | Evidence |
|---|---|---|
| 1 Network setup on a standard server | Verified (Windows ↔ Windows VM) | platform notes: Remote x64 VM |
| 2 B shows its Device ID and a password | Verified | Home |
| 3 A connects by Device ID and password, no IP address | Verified | platform notes: Remote x64 VM |
| 4 Manual mode | Verified | platform notes: The VM's real desktop |
| 5 AI mode on B's real desktop, steered from A | Verified with Claude (Windows ↔ Windows VM); OpenAI waits for a key | platform notes: The VM's real desktop (the task opened Notepad and typed, a flagged shortcut asked for approval) |
| 6 Exit ends sessions, networking and AI, releases resources | Verified (Windows) | platform notes: App |

macOS: the whole list waits for the Mac pass.

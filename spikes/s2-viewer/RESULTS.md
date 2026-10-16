# S2 — live frames from Rust into a MyGo window

Run on Windows 11 ARM64 (Snapdragon dev kit), 2026-10-16, Go 1.27.2, MyGo v0.5.5, Rust 1.99.1.

## Setup

- `frames/` (Rust cdylib, `+crt-static`): a producer thread draws a moving 1920×1080 RGBA pattern
  at 60 fps into four slots (free / drawing / ready / held; an unseen ready frame is dropped).
- `main.go`: loads the DLL with `syscall.LoadLibrary`, wraps each slot as an `*image.RGBA` without
  copying, and a goroutine hands the newest frame to `ui.Bitmap.Update` through `Window.Update`;
  the previous slot is released only after the next `Update` ran. Window 1400×820 with the picture
  (`Fit(Contain)`, about 0.54× of the frame, so MyGo's CPU mip path is not hit) next to a 500-row
  `ui.List`.

```bash
cargo build --release --manifest-path spikes/s2-viewer/frames/Cargo.toml
cd spikes/s2-viewer && go build -o ../../.dev/s2-viewer.exe . && cd ../..
./.dev/s2-viewer.exe -lib spikes/s2-viewer/frames/target/release/s2_frames.dll -seconds 15
```

## Results

| Check | Result |
|---|---|
| Frames shown | 59.0, 59.8, 59.7 fps (three 15 s runs); MyGo frame stats: 409 frames presented in ~7 s, ~16 ms each, vsync-bound present, build + layout ≈ 1 ms, ~500 allocations / 24 KB per frame |
| Process CPU | 30.8–36.7 % of one core, of which the test-pattern drawing is 9.2–11.4 % → viewer ≈ 21–25 % of one core |
| DLL dependencies | `dumpbin /dependents`: only `api-ms-win-core-synch-l1-2-0.dll`, `KERNEL32.dll`, `ntdll.dll` — no VC++ runtime |
| Input events | handler wired and logging; not exercised with real keys in this run (needs a person at the machine) |

Verdict: passes the S2 criterion (≥ 55 fps, ≤ 25 % of one core) at the limit on this machine. The
product viewer should deliver frames at the view's device-pixel size (less to upload than a full
1080p frame when the window is smaller) and keep the shader path as the next optimization.

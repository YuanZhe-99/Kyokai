//! Linux (doc/en-us/computer-use.md, "Linux"): on X11 the displays from
//! RandR, capture through MIT-SHM with Kyokai's own windows blacked out,
//! input through XTest that never reaches Kyokai's windows, the pointer,
//! the host user's own input through XInput 2 raw events, and the display's
//! power state through DPMS; on Wayland the same through the portals and
//! the input devices (`crate::wayland`). The public types pick by
//! `session()`.
//!
//! The X11 functions talk to the X server named by `$DISPLAY`; on Wayland
//! that server (Xwayland) shows only the X programs, so it is not used.

use crate::ai::desktop::{Desktop, Frame};
use crate::input::{Area, Event, Guard, LocalInput, Mods, PointerAction, lock_for};
use crate::video::{Display, Got, Grab, Take, order};
use std::collections::{BTreeSet, HashMap};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};
use x11rb::connection::{Connection, RequestConnection};
use x11rb::protocol::Event as XEvent;
use x11rb::protocol::dpms::{ConnectionExt as _, DPMSMode};
use x11rb::protocol::randr::ConnectionExt as _;
use x11rb::protocol::res::{ClientIdMask, ClientIdSpec, ConnectionExt as _};
use x11rb::protocol::shm::ConnectionExt as _;
use x11rb::protocol::xinput::{self, ConnectionExt as _};
use x11rb::protocol::xproto::{
    self, AtomEnum, ConnectionExt as _, ImageFormat, KeyButMask, MapState, Window,
};
use x11rb::protocol::xtest::ConnectionExt as _;
use x11rb::rust_connection::RustConnection;

/// The kind of graphical session this process runs in.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Session {
    X11,
    Wayland,
    /// No display server at all (a service, an SSH login).
    None,
}

/// The session this process belongs to, from its environment: Wayland when
/// `$WAYLAND_DISPLAY` is set or logind calls the session so, else X11 when
/// `$DISPLAY` is set.
pub fn session() -> Session {
    session_of(
        std::env::var("XDG_SESSION_TYPE").ok().as_deref(),
        std::env::var_os("WAYLAND_DISPLAY").is_some_and(|v| !v.is_empty()),
        std::env::var_os("DISPLAY").is_some_and(|v| !v.is_empty()),
    )
}

/// `session` from its three inputs.
fn session_of(kind: Option<&str>, wayland: bool, x11: bool) -> Session {
    if wayland || kind == Some("wayland") {
        Session::Wayland
    } else if x11 {
        Session::X11
    } else {
        Session::None
    }
}

/// A connection to the X server with the root window of its screen and the
/// atoms Kyokai reads.
struct X {
    conn: RustConnection,
    root: Window,
    width: u16,
    height: u16,
    stacking: u32,
    active: u32,
    wm_pid: u32,
    /// Whether the server has the X-Resource extension (a window's client
    /// process).
    res: bool,
}

impl X {
    /// Connects to `$DISPLAY` on an X11 session.
    fn open() -> Result<X, String> {
        if session() != Session::X11 {
            return Err("this is not an X11 session".into());
        }
        let (conn, screen) = x11rb::connect(None).map_err(|e| format!("X11: {e}"))?;
        let s = &conn.setup().roots[screen];
        let (root, width, height) = (s.root, s.width_in_pixels, s.height_in_pixels);
        let atom = |name: &[u8]| {
            conn.intern_atom(false, name)
                .ok()
                .and_then(|c| c.reply().ok())
                .map_or(0, |r| r.atom)
        };
        let (stacking, active, wm_pid) = (
            atom(b"_NET_CLIENT_LIST_STACKING"),
            atom(b"_NET_ACTIVE_WINDOW"),
            atom(b"_NET_WM_PID"),
        );
        let res = conn
            .extension_information(x11rb::protocol::res::X11_EXTENSION_NAME)
            .ok()
            .flatten()
            .is_some();
        Ok(X {
            conn,
            root,
            width,
            height,
            stacking,
            active,
            wm_pid,
            res,
        })
    }

    /// The monitors, the primary first and the others left to right; the
    /// whole screen as one when RandR names none.
    fn displays(&self) -> Vec<Display> {
        let monitors = self
            .conn
            .randr_get_monitors(self.root, true)
            .ok()
            .and_then(|c| c.reply().ok())
            .map(|r| r.monitors)
            .unwrap_or_default();
        let mut list: Vec<Display> = monitors
            .iter()
            .filter(|m| m.width > 0 && m.height > 0)
            .map(|m| Display {
                x: m.x.into(),
                y: m.y.into(),
                width: m.width.into(),
                height: m.height.into(),
                primary: m.primary,
            })
            .collect();
        if list.is_empty() {
            list.push(Display {
                x: 0,
                y: 0,
                width: self.width.into(),
                height: self.height.into(),
                primary: true,
            });
        }
        if !list.iter().any(|d| d.primary) {
            list.sort_by_key(|d| (d.x, d.y));
            list[0].primary = true;
        }
        order(&mut list);
        list
    }

    /// A 32-bit list property of a window.
    fn words(&self, w: Window, prop: u32) -> Vec<u32> {
        if prop == 0 {
            return Vec::new();
        }
        self.conn
            .get_property(false, w, prop, AtomEnum::ANY, 0, 4096)
            .ok()
            .and_then(|c| c.reply().ok())
            .and_then(|r| r.value32().map(|v| v.collect()))
            .unwrap_or_default()
    }

    /// The visible top-level windows, bottom to top, with their place and
    /// process.
    /// Notes: reads the window manager's stacking list; without one, the
    /// root's children. Requests go out together and the replies are read
    /// after, so a snapshot costs a few round trips, not one per window.
    fn windows(&self) -> Vec<Win> {
        let mut ids = self.words(self.root, self.stacking);
        if ids.is_empty() {
            ids = self
                .conn
                .query_tree(self.root)
                .ok()
                .and_then(|c| c.reply().ok())
                .map(|r| r.children)
                .unwrap_or_default();
        }
        let asked: Vec<_> = ids
            .iter()
            .map(|&w| {
                (
                    w,
                    self.conn.get_window_attributes(w).ok(),
                    self.conn.get_geometry(w).ok(),
                    self.conn.translate_coordinates(w, self.root, 0, 0).ok(),
                )
            })
            .collect();
        let mut out = Vec::with_capacity(ids.len());
        for (w, attrs, geo, at) in asked {
            let (Some(attrs), Some(geo), Some(at)) = (
                attrs.and_then(|c| c.reply().ok()),
                geo.and_then(|c| c.reply().ok()),
                at.and_then(|c| c.reply().ok()),
            ) else {
                continue;
            };
            if attrs.map_state != MapState::VIEWABLE
                || attrs.class == xproto::WindowClass::INPUT_ONLY
            {
                continue;
            }
            out.push(Win {
                id: w,
                rect: Rect {
                    x: at.dst_x.into(),
                    y: at.dst_y.into(),
                    w: geo.width.into(),
                    h: geo.height.into(),
                },
                pid: None,
            });
        }
        for win in &mut out {
            win.pid = self.pid(win.id);
        }
        out
    }

    /// The process of the client that made a window: X-Resource's answer,
    /// else the window's `_NET_WM_PID`.
    fn pid(&self, w: Window) -> Option<u32> {
        if self.res {
            let spec = ClientIdSpec {
                client: w,
                mask: ClientIdMask::LOCAL_CLIENT_PID,
            };
            if let Some(pid) = self
                .conn
                .res_query_client_ids(&[spec])
                .ok()
                .and_then(|c| c.reply().ok())
                .and_then(|r| r.ids.first().and_then(|v| v.value.first().copied()))
            {
                return Some(pid);
            }
        }
        self.words(w, self.wm_pid).first().copied()
    }

    /// The window that has the focus: the window manager's active window,
    /// else the server's input focus.
    fn focused(&self) -> Option<Window> {
        if let Some(&w) = self.words(self.root, self.active).first()
            && w != 0
        {
            return Some(w);
        }
        self.conn
            .get_input_focus()
            .ok()
            .and_then(|c| c.reply().ok())
            .map(|r| r.focus)
            .filter(|&w| w > 1)
    }

    /// Where the pointer is on the root window.
    fn pointer(&self) -> Option<(i32, i32, KeyButMask)> {
        let r = self.conn.query_pointer(self.root).ok()?.reply().ok()?;
        Some((r.root_x.into(), r.root_y.into(), r.mask))
    }
}

/// A rectangle of the root window, in pixels.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) struct Rect {
    pub x: i32,
    pub y: i32,
    pub w: i32,
    pub h: i32,
}

impl Rect {
    fn contains(&self, x: i32, y: i32) -> bool {
        x >= self.x && y >= self.y && x < self.x + self.w && y < self.y + self.h
    }

    fn intersect(&self, o: &Rect) -> Option<Rect> {
        let (x0, y0) = (self.x.max(o.x), self.y.max(o.y));
        let (x1, y1) = (
            (self.x + self.w).min(o.x + o.w),
            (self.y + self.h).min(o.y + o.h),
        );
        (x1 > x0 && y1 > y0).then_some(Rect {
            x: x0,
            y: y0,
            w: x1 - x0,
            h: y1 - y0,
        })
    }

    /// What is left of this rectangle without `o`: up to four rectangles.
    fn minus(&self, o: &Rect) -> Vec<Rect> {
        let Some(i) = self.intersect(o) else {
            return vec![*self];
        };
        let mut out = Vec::with_capacity(4);
        let push = |out: &mut Vec<Rect>, r: Rect| {
            if r.w > 0 && r.h > 0 {
                out.push(r);
            }
        };
        // Above and below the overlap, full width; left and right of it,
        // its height.
        push(
            &mut out,
            Rect {
                x: self.x,
                y: self.y,
                w: self.w,
                h: i.y - self.y,
            },
        );
        push(
            &mut out,
            Rect {
                x: self.x,
                y: i.y + i.h,
                w: self.w,
                h: self.y + self.h - (i.y + i.h),
            },
        );
        push(
            &mut out,
            Rect {
                x: self.x,
                y: i.y,
                w: i.x - self.x,
                h: i.h,
            },
        );
        push(
            &mut out,
            Rect {
                x: i.x + i.w,
                y: i.y,
                w: self.x + self.w - (i.x + i.w),
                h: i.h,
            },
        );
        out
    }
}

/// A visible top-level window.
#[derive(Debug, Clone)]
pub(crate) struct Win {
    id: Window,
    rect: Rect,
    pid: Option<u32>,
}

/// The parts of Kyokai's windows the screen shows, bottom to top windows
/// given with whether each is Kyokai's: each Kyokai window less the other
/// programs' windows above it.
pub(crate) fn kyokai_parts(windows: &[(Rect, bool)]) -> Vec<Rect> {
    let mut out = Vec::new();
    for (i, (rect, kyokai)) in windows.iter().enumerate() {
        if !kyokai {
            continue;
        }
        let mut parts = vec![*rect];
        for (above, theirs) in &windows[i + 1..] {
            if *theirs {
                continue;
            }
            parts = parts.iter().flat_map(|p| p.minus(above)).collect();
        }
        out.extend(parts);
    }
    out
}

/// Whether a process is one of Kyokai's (by its executable's name).
pub fn kyokai_process(pid: u32) -> bool {
    std::fs::read_link(format!("/proc/{pid}/exe"))
        .is_ok_and(|p| crate::input::is_kyokai(&p.to_string_lossy()))
}

/// Remembers which processes are Kyokai's, for a second each.
#[derive(Default)]
struct Owners(HashMap<u32, (bool, Instant)>);

impl Owners {
    fn kyokai(&mut self, pid: u32) -> bool {
        if let Some((k, at)) = self.0.get(&pid)
            && at.elapsed() < OWNER_TTL
        {
            return *k;
        }
        let k = kyokai_process(pid);
        self.0.insert(pid, (k, Instant::now()));
        k
    }
}

/// How long a process's answer, and a capture's window list, is reused.
const OWNER_TTL: Duration = Duration::from_secs(1);
const WINDOWS_TTL: Duration = Duration::from_millis(250);

/// The X server most calls use, opened once and opened again after it
/// failed.
static SHARED: Mutex<Option<X>> = Mutex::new(None);

/// Runs `f` on the shared connection.
fn with_x<T>(f: impl FnOnce(&X) -> Option<T>) -> Option<T> {
    let mut guard = SHARED.lock().unwrap_or_else(|e| e.into_inner());
    if guard.is_none() {
        *guard = X::open().ok();
    }
    let out = f(guard.as_ref()?);
    if out.is_none() && guard.as_ref().is_some_and(|x| x.conn.flush().is_err()) {
        *guard = None;
    }
    out
}

/// The displays: the primary first, the others left to right (on Wayland,
/// those the host user shares; empty without a graphical session).
pub fn displays() -> Vec<Display> {
    match session() {
        Session::X11 => with_x(|x| Some(x.displays())).unwrap_or_default(),
        Session::Wayland => crate::wayland::displays(),
        Session::None => Vec::new(),
    }
}

/// Whether DPMS has the display off (standby, suspend or off; X11 only).
pub fn display_off() -> bool {
    if session() != Session::X11 {
        return false;
    }
    with_x(|x| {
        let r = x.conn.dpms_info().ok()?.reply().ok()?;
        Some(r.state && r.power_level != DPMSMode::ON)
    })
    .unwrap_or(false)
}

/// Turns the display on, as input would (X11 only).
pub fn wake_display() {
    if session() != Session::X11 {
        return;
    }
    with_x(|x| {
        x.conn.dpms_force_level(DPMSMode::ON).ok()?;
        x.conn.force_screen_saver(xproto::ScreenSaver::RESET).ok()?;
        x.conn.flush().ok()
    });
}

/// Where the pointer is, as fractions of the primary display (outside 0–1
/// on another display).
pub fn pointer() -> Option<(f32, f32)> {
    pointer_in(main_area())
}

/// Where the pointer is, as fractions of `area` (X11 only: Wayland does
/// not tell, and its pictures carry the pointer drawn in).
pub fn pointer_in(area: Area) -> Option<(f32, f32)> {
    if session() != Session::X11 {
        return None;
    }
    let (x, y, _) = with_x(X::pointer)?;
    Some((
        (x - area.x) as f32 / area.width.max(1) as f32,
        (y - area.y) as f32 / area.height.max(1) as f32,
    ))
}

/// Whether Caps Lock and Num Lock are on (the Lock and Mod2 modifiers).
fn locks() -> (bool, bool) {
    with_x(X::pointer).map_or((false, false), |(_, _, m)| {
        (m.contains(KeyButMask::LOCK), m.contains(KeyButMask::MOD2))
    })
}

/// The primary display as an input area.
fn main_area() -> Area {
    let d = displays()
        .into_iter()
        .find(|d| d.primary)
        .unwrap_or(Display {
            x: 0,
            y: 0,
            width: 1,
            height: 1,
            primary: true,
        });
    Area {
        x: d.x,
        y: d.y,
        width: d.width,
        height: d.height,
    }
}

/// Where a fraction of `area` lies on the root window.
fn point_in(area: Area, fx: f32, fy: f32) -> (i16, i16) {
    let axis = |f: f32, start: i32, len: u32| {
        (start as f32 + f.clamp(0.0, 1.0) * (len.max(1) - 1) as f32).round() as i16
    };
    (axis(fx, area.x, area.width), axis(fy, area.y, area.height))
}

/// A System V shared-memory segment the X server writes pictures into.
struct Segment {
    id: i32,
    addr: *mut u8,
    len: usize,
}

impl Segment {
    /// Makes a private segment of `len` bytes, marked for removal once both
    /// sides let go of it.
    fn new(len: usize) -> Result<Segment, String> {
        // SAFETY: a plain System V call; the id is checked.
        let id = unsafe { libc::shmget(libc::IPC_PRIVATE, len, libc::IPC_CREAT | 0o600) };
        if id < 0 {
            return Err(format!("shmget: {}", std::io::Error::last_os_error()));
        }
        // SAFETY: attaches the segment just made; the address is checked.
        let addr = unsafe { libc::shmat(id, std::ptr::null(), 0) };
        if addr as isize == -1 {
            // SAFETY: removes the segment just made.
            unsafe { libc::shmctl(id, libc::IPC_RMID, std::ptr::null_mut()) };
            return Err(format!("shmat: {}", std::io::Error::last_os_error()));
        }
        Ok(Segment {
            id,
            addr: addr.cast(),
            len,
        })
    }

    /// Marks the segment for removal: it goes once the X server let go too.
    fn release_name(&self) {
        // SAFETY: a plain System V call on our own segment.
        unsafe { libc::shmctl(self.id, libc::IPC_RMID, std::ptr::null_mut()) };
    }

    fn bytes(&mut self) -> &mut [u8] {
        // SAFETY: `addr` maps `len` bytes for as long as `self` lives, and
        // only this thread touches them between the server's replies.
        unsafe { std::slice::from_raw_parts_mut(self.addr, self.len) }
    }
}

impl Drop for Segment {
    fn drop(&mut self) {
        // SAFETY: detaches the mapping made in `new`.
        unsafe { libc::shmdt(self.addr.cast()) };
        self.release_name();
    }
}

// SAFETY: the mapping is owned by the segment and used by one thread at a
// time.
unsafe impl Send for Segment {}

/// A display through MIT-SHM, without the pointer (the viewer draws it) and
/// without Kyokai's windows unless the host user lets capture see them.
pub struct XCapture {
    x: X,
    display: Display,
    seg: Segment,
    shmseg: u32,
    /// The last picture handed over, to leave out a still screen.
    last: Vec<u8>,
    /// The visible parts of Kyokai's windows, display-relative, and when they
    /// were read.
    hidden: (Vec<Rect>, Option<Instant>),
    owners: Owners,
}

/// How often a capture looks at the screen while it waits for a change.
const GRAB_EVERY: Duration = Duration::from_millis(1000 / crate::video::MAX_FPS as u64);

impl XCapture {
    /// Opens `display` on its own connection.
    fn start(display: Display) -> Result<XCapture, String> {
        let x = X::open()?;
        if x.conn
            .extension_information(x11rb::protocol::shm::X11_EXTENSION_NAME)
            .ok()
            .flatten()
            .is_none()
        {
            return Err("the X server has no MIT-SHM".into());
        }
        let len = display.width as usize * display.height as usize * 4;
        let seg = Segment::new(len)?;
        let shmseg = x.conn.generate_id().map_err(|e| e.to_string())?;
        x.conn
            .shm_attach(shmseg, seg.id as u32, false)
            .map_err(|e| e.to_string())?
            .check()
            .map_err(|e| format!("shm attach: {e}"))?;
        seg.release_name();
        Ok(XCapture {
            x,
            display,
            seg,
            shmseg,
            last: Vec::new(),
            hidden: (Vec::new(), None),
            owners: Owners::default(),
        })
    }

    /// Reads the display into the segment, Kyokai's windows blacked out.
    fn grab(&mut self) -> Result<(), String> {
        let d = self.display;
        let reply = self
            .x
            .conn
            .shm_get_image(
                self.x.root,
                d.x as i16,
                d.y as i16,
                d.width as u16,
                d.height as u16,
                !0,
                ImageFormat::Z_PIXMAP.into(),
                self.shmseg,
                0,
            )
            .map_err(|e| e.to_string())?
            .reply()
            .map_err(|e| e.to_string())?;
        if reply.depth != 24 && reply.depth != 32 {
            return Err(format!("a {}-bit screen cannot be captured", reply.depth));
        }
        if !crate::video::show_kyokai() {
            self.blackout();
        }
        Ok(())
    }

    /// Blacks out the visible parts of Kyokai's windows (the window list is
    /// read again every WINDOWS_TTL).
    fn blackout(&mut self) {
        if self.hidden.1.is_none_or(|t| t.elapsed() >= WINDOWS_TTL) {
            let d = self.display;
            let screen = Rect {
                x: d.x,
                y: d.y,
                w: d.width as i32,
                h: d.height as i32,
            };
            let wins: Vec<(Rect, bool)> = self
                .x
                .windows()
                .into_iter()
                .map(|w| (w.rect, w.pid.is_some_and(|p| self.owners.kyokai(p))))
                .collect();
            let parts = kyokai_parts(&wins)
                .iter()
                .filter_map(|r| r.intersect(&screen))
                .map(|r| Rect {
                    x: r.x - d.x,
                    y: r.y - d.y,
                    ..r
                })
                .collect();
            self.hidden = (parts, Some(Instant::now()));
        }
        let stride = self.display.width as usize * 4;
        let parts = std::mem::take(&mut self.hidden.0);
        let bytes = self.seg.bytes();
        for r in &parts {
            for row in r.y..r.y + r.h {
                let start = row as usize * stride + r.x as usize * 4;
                bytes[start..start + r.w as usize * 4].fill(0);
            }
        }
        self.hidden.0 = parts;
    }
}

impl Drop for XCapture {
    fn drop(&mut self) {
        let _ = self.x.conn.shm_detach(self.shmseg);
        let _ = self.x.conn.flush();
    }
}

impl Grab for XCapture {
    fn open(display: Display) -> Result<(Self, u32, u32), String> {
        // The display as it is now: a mode change since the list was read
        // gives the new size.
        let now = displays()
            .into_iter()
            .find(|d| (d.x, d.y) == (display.x, display.y))
            .ok_or("the display is gone")?;
        Ok((XCapture::start(now)?, now.width, now.height))
    }

    fn frame(&mut self, timeout: Duration, take: &mut Take<'_>) -> Got {
        let until = Instant::now() + timeout;
        loop {
            // A display that moved or changed its size is lost: the capture
            // loop opens it again.
            let d = self.display;
            let same = |n: &Display| (n.x, n.y, n.width, n.height) == (d.x, d.y, d.width, d.height);
            if !displays().iter().any(same) {
                return Got::Lost;
            }
            if self.grab().is_err() {
                return Got::Lost;
            }
            let (w, h) = (d.width as usize, d.height as usize);
            let bytes = self.seg.bytes();
            if self.last.as_slice() != &bytes[..] {
                self.last.clear();
                self.last.extend_from_slice(bytes);
                take(&self.last, w, h, w * 4);
                return Got::Picture;
            }
            if Instant::now() + GRAB_EVERY > until {
                return Got::Nothing;
            }
            std::thread::sleep(GRAB_EVERY);
        }
    }
}

/// The evdev code of a PC scan code (set 1, `0xE0..` extended), as the
/// viewer's Map mode sends them; the X keycode is this plus 8.
/// Returns: None for a scan code Linux gives no key.
pub fn scan_evdev(code: u32) -> Option<u8> {
    Some(match code {
        // Set 1's first block is evdev's numbering.
        0x01..=0x53 => code as u8,
        0x56 => 86,
        0x57 => 87,
        0x58 => 88,
        0x64..=0x6E => (code - 0x64 + 183) as u8, // F13–F23
        0x76 => 194,                              // F24
        0x70 => 93,                               // Katakana/Hiragana
        0x73 => 89,                               // Ro
        0x79 => 92,                               // Henkan
        0x7B => 94,                               // Muhenkan
        0x7D => 124,                              // Yen
        0xE01C => 96,
        0xE01D => 97,
        0xE035 => 98,
        0xE037 => 99,
        0xE038 => 100,
        0xE047 => 102,
        0xE048 => 103,
        0xE049 => 104,
        0xE04B => 105,
        0xE04D => 106,
        0xE04F => 107,
        0xE050 => 108,
        0xE051 => 109,
        0xE052 => 110,
        0xE053 => 111,
        0xE05B => 125,
        0xE05C => 126,
        0xE05D => 127,
        0xE11D => 119,
        0xE020 => 113, // mute
        0xE02E => 114, // volume down
        0xE030 => 115, // volume up
        0xE022 => 164, // play/pause
        0xE024 => 166, // stop
        0xE010 => 165, // previous
        0xE019 => 163, // next
        _ => return None,
    })
}

/// The evdev code of a key name (`input::vk`'s names), through the scan
/// code of the key at that place on a PC keyboard.
pub fn keycode(name: &str) -> Option<u8> {
    const LETTERS: [u32; 26] = [
        0x1E, 0x30, 0x2E, 0x20, 0x12, 0x21, 0x22, 0x23, 0x17, 0x24, 0x25, 0x26, 0x32, 0x31, 0x18,
        0x19, 0x10, 0x13, 0x1F, 0x14, 0x16, 0x2F, 0x11, 0x2D, 0x15, 0x2C,
    ];
    const FKEYS: [u32; 12] = [
        0x3B, 0x3C, 0x3D, 0x3E, 0x3F, 0x40, 0x41, 0x42, 0x43, 0x44, 0x57, 0x58,
    ];
    let b = name.as_bytes();
    let scan = if b.len() == 1 && b[0].is_ascii_lowercase() {
        LETTERS[(b[0] - b'a') as usize]
    } else if b.len() == 1 && b[0].is_ascii_digit() {
        // 1–9 are 0x02–0x0A, 0 is 0x0B.
        if b[0] == b'0' {
            0x0B
        } else {
            (b[0] - b'1') as u32 + 0x02
        }
    } else if let Some(n) = name.strip_prefix('f').and_then(|n| n.parse::<u32>().ok()) {
        match n {
            1..=12 => FKEYS[n as usize - 1],
            13..=23 => 0x64 + n - 13,
            24 => 0x76,
            _ => return None,
        }
    } else {
        match name {
            "enter" => 0x1C,
            "escape" => 0x01,
            "backspace" => 0x0E,
            "tab" => 0x0F,
            "space" => 0x39,
            "delete" => 0xE053,
            "insert" => 0xE052,
            "home" => 0xE047,
            "end" => 0xE04F,
            "page-up" => 0xE049,
            "page-down" => 0xE051,
            "left" => 0xE04B,
            "up" => 0xE048,
            "right" => 0xE04D,
            "down" => 0xE050,
            "minus" => 0x0C,
            "equal" => 0x0D,
            "comma" => 0x33,
            "period" => 0x34,
            "slash" => 0x35,
            "semicolon" => 0x27,
            "quote" => 0x28,
            "bracket-left" => 0x1A,
            "bracket-right" => 0x1B,
            "backslash" => 0x2B,
            "backquote" => 0x29,
            "context-menu" => 0xE05D,
            "caps-lock" => 0x3A,
            "print-screen" => 0xE037,
            "shift" => 0x2A,
            "ctrl" => 0x1D,
            "alt" => 0x38,
            "meta" => 0xE05B,
            _ => return None,
        }
    };
    scan_evdev(scan)
}

/// X keycodes are evdev codes moved by 8.
const XKB_OFFSET: u8 = 8;
/// Whether a modifier set asks for one modifier.
pub(crate) type Wanted = fn(&Mods) -> bool;

/// The modifier keys of a key event (evdev codes) and whether each is
/// wanted.
pub(crate) const MODIFIERS: [(u8, Wanted); 4] = [
    (42, |m| m.shift),
    (29, |m| m.ctrl),
    (56, |m| m.alt),
    (125, |m| m.meta),
];
/// Caps Lock and Num Lock (evdev).
const CAPS_LOCK: u8 = 58;
const NUM_LOCK: u8 = 69;
/// Escape (evdev): the emergency stop is Ctrl+Alt+Shift+Esc.
const ESCAPE: u8 = 1;
/// The X buttons of the protocol's buttons (left, right, middle) and of the
/// wheel (up, down, left, right).
const BUTTONS: [u8; 3] = [1, 3, 2];
const WHEEL: [u8; 4] = [4, 5, 6, 7];

/// The host's input on X11: events through XTest, nothing reaching Kyokai's
/// own windows, and whatever is held released at the end. Text is typed
/// through spare keycodes given the characters' keysyms, so it does not
/// depend on the host's layout.
pub struct XInjector {
    x: Option<X>,
    area: Option<Area>,
    /// Evdev codes held down.
    keys: BTreeSet<u8>,
    buttons: BTreeSet<u8>,
    scroll: (f32, f32),
    owners: Owners,
    /// The keycodes no key uses, for typing text, once found.
    spares: Option<Vec<u8>>,
}

impl Default for XInjector {
    fn default() -> Self {
        XInjector {
            x: X::open().ok(),
            area: None,
            keys: BTreeSet::new(),
            buttons: BTreeSet::new(),
            scroll: (0.0, 0.0),
            owners: Owners::default(),
            spares: None,
        }
    }
}

impl XInjector {
    /// Sets the display pointer positions refer to (None: the primary).
    pub fn set_area(&mut self, area: Option<Area>) {
        self.area = area;
    }

    /// Sends one XTest event.
    fn fake(&self, kind: u8, detail: u8, at: Option<(i16, i16)>) {
        let Some(x) = &self.x else {
            return;
        };
        let (rx, ry) = at.unwrap_or((0, 0));
        let root = if at.is_some() { x.root } else { x11rb::NONE };
        let _ = x
            .conn
            .xtest_fake_input(kind, detail, x11rb::CURRENT_TIME, root, rx, ry, 0);
    }

    /// A key (evdev code) going down or up.
    fn key(&mut self, code: u8, down: bool) {
        if down {
            self.keys.insert(code);
        } else {
            self.keys.remove(&code);
        }
        let kind = if down {
            xproto::KEY_PRESS_EVENT
        } else {
            xproto::KEY_RELEASE_EVENT
        };
        self.fake(kind, code + XKB_OFFSET, None);
    }

    /// Presses or releases modifiers so they match `mods`.
    fn sync_mods(&mut self, mods: &Mods) {
        for (code, wanted) in MODIFIERS {
            let held = self.keys.contains(&code);
            if wanted(mods) != held {
                self.key(code, !held);
            }
        }
    }

    fn button(&mut self, b: u8, down: bool) {
        let kind = if down {
            xproto::BUTTON_PRESS_EVENT
        } else {
            xproto::BUTTON_RELEASE_EVENT
        };
        self.fake(kind, b, None);
    }

    /// Whether the window `guard` names belongs to Kyokai (or none can be
    /// told).
    fn blocked(&mut self, guard: Guard) -> bool {
        let Some(x) = &self.x else {
            return true;
        };
        let pid = match guard {
            Guard::None => return false,
            Guard::At(fx, fy) => {
                let (px, py) = point_in(self.area.unwrap_or_else(main_area), fx, fy);
                window_at(&x.windows(), px.into(), py.into())
            }
            Guard::Pointer => match x.pointer() {
                Some((px, py, _)) => window_at(&x.windows(), px, py),
                None => return true,
            },
            Guard::Focus => x.focused().and_then(|w| x.pid(w)),
        };
        pid.is_some_and(|p| self.owners.kyokai(p))
    }

    /// The keycodes with no keysym, for typing text.
    fn spares(&mut self) -> Vec<u8> {
        if self.spares.is_none()
            && let Some(x) = &self.x
        {
            let setup = x.conn.setup();
            let (min, max) = (setup.min_keycode, setup.max_keycode);
            let map = x
                .conn
                .get_keyboard_mapping(min, max - min + 1)
                .ok()
                .and_then(|c| c.reply().ok());
            self.spares = map.map(|m| {
                let per = m.keysyms_per_keycode.max(1) as usize;
                m.keysyms
                    .chunks(per)
                    .enumerate()
                    .filter(|(_, syms)| syms.iter().all(|&s| s == 0))
                    .map(|(i, _)| min + i as u8)
                    .collect()
            });
        }
        self.spares.clone().unwrap_or_default()
    }

    /// Types `text` through the spare keycodes: each run of characters that
    /// fits them is mapped at once, typed, and given TEXT_SETTLE before the
    /// codes are mapped again.
    /// Notes: X programs read a changed mapping only when they next look a
    /// key up, maybe after it changed again; one code per character, never
    /// remapped while its key may wait, keeps each key its character
    /// (regression: through one code, "echo" came out "ccho" and the last
    /// characters went missing).
    fn type_text(&mut self, text: &str) {
        let spares = self.spares();
        let Some(x) = &self.x else {
            return;
        };
        if spares.is_empty() {
            return;
        }
        let chars: Vec<char> = text.chars().collect();
        for run in text_runs(&chars, spares.len()) {
            let mut codes: Vec<char> = Vec::new();
            for &c in run {
                if !codes.contains(&c) {
                    let code = spares[codes.len()];
                    let sym = keysym(c);
                    let _ = x.conn.change_keyboard_mapping(1, code, 2, &[sym, sym]);
                    codes.push(c);
                }
            }
            round_trip(&x.conn);
            std::thread::sleep(TEXT_SETTLE);
            for c in run {
                let code = spares[codes.iter().position(|d| d == c).unwrap_or(0)];
                for kind in [xproto::KEY_PRESS_EVENT, xproto::KEY_RELEASE_EVENT] {
                    let _ = x.conn.xtest_fake_input(
                        kind,
                        code,
                        x11rb::CURRENT_TIME,
                        x11rb::NONE,
                        0,
                        0,
                        0,
                    );
                }
            }
            round_trip(&x.conn);
            std::thread::sleep(TEXT_SETTLE);
        }
        for &code in spares.iter().take(chars.len()) {
            let _ = x.conn.change_keyboard_mapping(1, code, 2, &[0, 0]);
        }
        let _ = x.conn.flush();
    }

    /// Injects an event, unless it would reach a Kyokai window and
    /// `kyokai_ok` is false: a person's input while the host user lets it
    /// reach them (`input::kyokai_input`), never the AI's.
    /// Returns: whether it was injected.
    pub fn inject(&mut self, e: &Event, kyokai_ok: bool) -> bool {
        if self.x.is_none() || !e.valid() || (!kyokai_ok && self.blocked(e.guard())) {
            return false;
        }
        match e {
            Event::Pointer {
                action,
                button,
                x,
                y,
            } => {
                let at = point_in(self.area.unwrap_or_else(main_area), *x, *y);
                self.fake(xproto::MOTION_NOTIFY_EVENT, 0, Some(at));
                let b = BUTTONS[*button as usize];
                match action {
                    PointerAction::Move => {}
                    PointerAction::Down => {
                        self.button(b, true);
                        self.buttons.insert(*button);
                    }
                    PointerAction::Up => {
                        self.button(b, false);
                        self.buttons.remove(button);
                    }
                }
            }
            Event::Scroll { dx, dy } => {
                self.scroll.0 += dx;
                self.scroll.1 += dy;
                let (lx, ly) = (self.scroll.0.trunc(), self.scroll.1.trunc());
                self.scroll.0 -= lx;
                self.scroll.1 -= ly;
                // A positive dy turns the wheel toward the user (down).
                for (n, b) in [
                    (ly, if ly > 0.0 { WHEEL[1] } else { WHEEL[0] }),
                    (lx, if lx > 0.0 { WHEEL[3] } else { WHEEL[2] }),
                ] {
                    for _ in 0..(n.abs() as u32) {
                        self.button(b, true);
                        self.button(b, false);
                    }
                }
            }
            Event::Key { key, down, .. } => {
                let Some(code) = keycode(key) else {
                    return false;
                };
                let held = e.mods_held().unwrap_or_default();
                if *down {
                    self.sync_mods(&held);
                    self.key(code, true);
                } else {
                    self.key(code, false);
                    self.sync_mods(&held);
                }
            }
            Event::Text { text } => {
                self.sync_mods(&e.mods_held().unwrap_or_default());
                self.type_text(text);
            }
            Event::Scan {
                code,
                down,
                caps,
                num,
            } => {
                let Some(key) = scan_evdev(*code) else {
                    return false;
                };
                // The host's locks follow the controller's before a key
                // they change.
                if *down && let Some((lock, on)) = lock_for(*code, *caps, *num) {
                    let (caps_on, num_on) = locks();
                    let (k, now) = if lock == 0x3A {
                        (CAPS_LOCK, caps_on)
                    } else {
                        (NUM_LOCK, num_on)
                    };
                    if now != on {
                        self.key(k, true);
                        self.key(k, false);
                    }
                }
                self.key(key, *down);
            }
        }
        if let Some(x) = &self.x {
            let _ = x.conn.flush();
        }
        true
    }

    /// Releases every key and button this session holds.
    pub fn release_all(&mut self) {
        let keys: Vec<u8> = self.keys.iter().rev().copied().collect();
        for k in keys {
            self.key(k, false);
        }
        let buttons: Vec<u8> = std::mem::take(&mut self.buttons).into_iter().collect();
        for b in buttons {
            self.button(BUTTONS[b as usize], false);
        }
        if let Some(x) = &self.x {
            let _ = x.conn.flush();
        }
    }
}

impl Drop for XInjector {
    fn drop(&mut self) {
        self.release_all();
    }
}

/// Waits until the X server handled every request sent so far.
fn round_trip(conn: &RustConnection) {
    let _ = conn.get_input_focus().map(|c| c.reply());
}

/// How long programs get to read a changed key mapping, before its keys
/// and before it changes again.
const TEXT_SETTLE: Duration = Duration::from_millis(50);

/// Splits text into runs of at most `codes` distinct characters, in order.
fn text_runs(chars: &[char], codes: usize) -> Vec<&[char]> {
    let mut runs = Vec::new();
    let mut start = 0;
    let mut distinct: Vec<char> = Vec::new();
    for (i, c) in chars.iter().enumerate() {
        if !distinct.contains(c) {
            if distinct.len() == codes {
                runs.push(&chars[start..i]);
                start = i;
                distinct.clear();
            }
            distinct.push(*c);
        }
    }
    if start < chars.len() {
        runs.push(&chars[start..]);
    }
    runs
}

/// The keysym of a character: Latin-1 as itself, the rest as Unicode
/// keysyms; control characters as the keys that type them.
pub(crate) fn keysym(c: char) -> u32 {
    match c {
        '\n' | '\r' => 0xFF0D, // Return
        '\t' => 0xFF09,        // Tab
        '\u{8}' => 0xFF08,     // BackSpace
        c if (' '..='~').contains(&c) || ('\u{A0}'..='\u{FF}').contains(&c) => c as u32,
        c => 0x0100_0000 | c as u32,
    }
}

/// The process of the topmost window at a point of the root window.
fn window_at(windows: &[Win], x: i32, y: i32) -> Option<u32> {
    windows
        .iter()
        .rev()
        .find(|w| w.rect.contains(x, y))
        .and_then(|w| w.pid)
}

/// What the input watch's thread and its readers share.
#[derive(Default)]
struct Watched {
    press: Mutex<Option<Instant>>,
    moved: Mutex<Option<Instant>>,
    stop: AtomicBool,
    ended: AtomicBool,
}

/// Watches the host user's own keyboard and mouse through XInput 2 raw
/// events (XTest's virtual devices, Kyokai's input, left out), and the
/// emergency-stop shortcut Ctrl+Alt+Shift+Esc.
pub struct XWatch {
    watched: Arc<Watched>,
}

impl XWatch {
    /// Starts the watch on a thread of its own.
    /// Returns: an error without an X11 session or XInput 2.
    pub fn start() -> Result<XWatch, String> {
        let x = X::open()?;
        x.conn
            .xinput_xi_query_version(2, 0)
            .map_err(|e| e.to_string())?
            .reply()
            .map_err(|_| "the X server has no XInput 2")?;
        let xtest: BTreeSet<u16> = x
            .conn
            .xinput_xi_query_device(xinput::Device::ALL)
            .map_err(|e| e.to_string())?
            .reply()
            .map_err(|e| e.to_string())?
            .infos
            .iter()
            .filter(|d| String::from_utf8_lossy(&d.name).contains("XTEST"))
            .map(|d| d.deviceid)
            .collect();
        let mask = xinput::XIEventMask::RAW_KEY_PRESS
            | xinput::XIEventMask::RAW_KEY_RELEASE
            | xinput::XIEventMask::RAW_BUTTON_PRESS
            | xinput::XIEventMask::RAW_MOTION;
        x.conn
            .xinput_xi_select_events(
                x.root,
                &[xinput::EventMask {
                    deviceid: xinput::Device::ALL_MASTER.into(),
                    mask: vec![mask],
                }],
            )
            .map_err(|e| e.to_string())?
            .check()
            .map_err(|e| format!("XInput 2 raw events: {e:?}"))?;
        let watched = Arc::new(Watched::default());
        let shared = watched.clone();
        std::thread::spawn(move || watch(x, xtest, &shared));
        Ok(XWatch { watched })
    }

    /// When the host user last pressed something and moved the pointer.
    pub fn input(&self) -> LocalInput {
        LocalInput {
            press: self.watched.press.lock().ok().and_then(|p| *p),
            moved: self.watched.moved.lock().ok().and_then(|m| *m),
        }
    }

    /// Whether the stop shortcut was pressed since the last call.
    pub fn stop_requested(&self) -> bool {
        self.watched.stop.swap(false, Ordering::Relaxed)
    }
}

impl Drop for XWatch {
    fn drop(&mut self) {
        self.watched.ended.store(true, Ordering::Release);
    }
}

/// The watch's loop: notes the host user's input until the watch drops.
fn watch(x: X, xtest: BTreeSet<u16>, watched: &Watched) {
    // The modifier keys held (X keycodes), for the stop shortcut.
    let mut held: BTreeSet<u8> = BTreeSet::new();
    while !watched.ended.load(Ordering::Acquire) {
        let event = match x.conn.poll_for_event() {
            Ok(Some(e)) => e,
            Ok(None) => {
                std::thread::sleep(Duration::from_millis(10));
                continue;
            }
            Err(_) => return,
        };
        let (source, slot, key) = match &event {
            XEvent::XinputRawKeyPress(e) => {
                (e.sourceid, &watched.press, Some((e.detail as u8, true)))
            }
            XEvent::XinputRawKeyRelease(e) => {
                (e.sourceid, &watched.press, Some((e.detail as u8, false)))
            }
            XEvent::XinputRawButtonPress(e) => (e.sourceid, &watched.press, None),
            XEvent::XinputRawMotion(e) => (e.sourceid, &watched.moved, None),
            _ => continue,
        };
        if xtest.contains(&source) {
            continue;
        }
        if let Some((code, down)) = key {
            if stop_shortcut(&mut held, code, down) {
                watched.stop.store(true, Ordering::Relaxed);
            }
            if !down {
                continue;
            }
        }
        if let Ok(mut t) = slot.lock() {
            *t = Some(Instant::now());
        }
    }
}

/// Follows the modifiers held and tells whether this key completes
/// Ctrl+Alt+Shift+Esc (X keycodes).
pub(crate) fn stop_shortcut(held: &mut BTreeSet<u8>, code: u8, down: bool) -> bool {
    // Left and right Control, Alt and Shift (evdev 29/97, 56/100, 42/54).
    const CTRL: [u8; 2] = [29 + XKB_OFFSET, 97 + XKB_OFFSET];
    const ALT: [u8; 2] = [56 + XKB_OFFSET, 100 + XKB_OFFSET];
    const SHIFT: [u8; 2] = [42 + XKB_OFFSET, 54 + XKB_OFFSET];
    if CTRL.contains(&code) || ALT.contains(&code) || SHIFT.contains(&code) {
        if down {
            held.insert(code);
        } else {
            held.remove(&code);
        }
        return false;
    }
    let has = |set: [u8; 2]| set.iter().any(|k| held.contains(k));
    down && code == ESCAPE + XKB_OFFSET && has(CTRL) && has(ALT) && has(SHIFT)
}

/// A display capture on the session's own terms: MIT-SHM on X11, the
/// portal's PipeWire stream on Wayland.
pub enum Capture {
    X(Box<XCapture>),
    Wayland(crate::wayland::Capture),
}

impl Grab for Capture {
    fn open(display: Display) -> Result<(Self, u32, u32), String> {
        match session() {
            Session::X11 => {
                XCapture::open(display).map(|(c, w, h)| (Capture::X(Box::new(c)), w, h))
            }
            Session::Wayland => {
                crate::wayland::Capture::open(display).map(|(c, w, h)| (Capture::Wayland(c), w, h))
            }
            Session::None => Err("this session has no screen".into()),
        }
    }

    fn frame(&mut self, timeout: Duration, take: &mut Take<'_>) -> Got {
        match self {
            Capture::X(c) => c.frame(timeout, take),
            Capture::Wayland(c) => c.frame(timeout, take),
        }
    }
}

/// The host's input on the session's own terms: XTest on X11, the
/// RemoteDesktop portal on Wayland, nothing without a graphical session.
pub enum Injector {
    X(Box<XInjector>),
    Wayland(crate::wayland::Injector),
    None,
}

impl Default for Injector {
    fn default() -> Self {
        match session() {
            Session::X11 => Injector::X(Box::default()),
            Session::Wayland => Injector::Wayland(crate::wayland::Injector::default()),
            Session::None => Injector::None,
        }
    }
}

impl Injector {
    /// Sets the display pointer positions refer to (None: the primary).
    pub fn set_area(&mut self, area: Option<Area>) {
        match self {
            Injector::X(i) => i.set_area(area),
            Injector::Wayland(i) => i.set_area(area),
            Injector::None => {}
        }
    }

    /// Injects an event unless it would reach Kyokai's windows and
    /// `kyokai_ok` is false.
    /// Returns: whether it was injected.
    pub fn inject(&mut self, e: &Event, kyokai_ok: bool) -> bool {
        match self {
            Injector::X(i) => i.inject(e, kyokai_ok),
            Injector::Wayland(i) => i.inject(e, kyokai_ok),
            Injector::None => false,
        }
    }

    /// Releases every key and button this session holds.
    pub fn release_all(&mut self) {
        match self {
            Injector::X(i) => i.release_all(),
            Injector::Wayland(i) => i.release_all(),
            Injector::None => {}
        }
    }
}

/// The watch on the host user's own input: XInput 2 on X11, the input
/// devices on Wayland.
pub enum LocalWatch {
    X(XWatch),
    Evdev(crate::wayland::Evdev),
}

impl LocalWatch {
    /// Starts the watch.
    /// Returns: an error where nothing can watch.
    pub fn start() -> Result<LocalWatch, String> {
        match session() {
            Session::X11 => XWatch::start().map(LocalWatch::X),
            Session::Wayland => crate::wayland::Evdev::start().map(LocalWatch::Evdev),
            Session::None => Err("this session has no keyboard or mouse".into()),
        }
    }

    /// When the host user last pressed something and moved the pointer.
    pub fn input(&self) -> LocalInput {
        match self {
            LocalWatch::X(w) => w.input(),
            LocalWatch::Evdev(w) => w.input(),
        }
    }

    /// Whether the stop shortcut was pressed since the last call.
    pub fn stop_requested(&self) -> bool {
        match self {
            LocalWatch::X(w) => w.stop_requested(),
            LocalWatch::Evdev(w) => w.stop_requested(),
        }
    }
}

/// The real desktop for AI tasks: the primary display at full resolution,
/// the session's input, and a watch on local input (no AI task starts
/// where the host user's own input cannot be watched).
pub struct Screen {
    injector: Injector,
    watch: LocalWatch,
}

impl Screen {
    /// Starts watching for local input and opens the desktop.
    pub fn open() -> Result<Screen, String> {
        let watch = LocalWatch::start()?;
        Ok(Screen {
            injector: Injector::default(),
            watch,
        })
    }
}

/// The primary display.
fn primary() -> Result<Display, String> {
    displays()
        .into_iter()
        .find(|d| d.primary)
        .ok_or_else(|| "no display".into())
}

impl Desktop for Screen {
    fn size(&mut self) -> Result<(u32, u32), String> {
        primary().map(|d| (d.width, d.height))
    }

    fn capture(&mut self) -> Result<Frame, String> {
        let d = primary()?;
        if session() == Session::X11 {
            let mut cap = XCapture::start(d)?;
            cap.grab()?;
            return Ok(Frame {
                bgra: cap.seg.bytes().to_vec(),
                width: d.width,
                height: d.height,
            });
        }
        crate::wayland::Capture::open(d)?
            .0
            .grab(Duration::from_secs(2))
    }

    fn inject(&mut self, e: &Event) -> bool {
        // The AI never reaches Kyokai's windows, whatever the host user lets.
        self.injector.inject(e, false)
    }

    fn release(&mut self) {
        self.injector.release_all();
    }

    fn cursor(&mut self) -> Option<(f32, f32)> {
        if let Injector::Wayland(i) = &self.injector {
            return i.at;
        }
        let (x, y) = pointer()?;
        Some((x.clamp(0.0, 1.0), y.clamp(0.0, 1.0)))
    }

    fn local_input(&mut self) -> LocalInput {
        self.watch.input()
    }

    fn stop_requested(&mut self) -> bool {
        self.watch.stop_requested()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sessions_are_told_from_the_environment() {
        for (kind, wayland, x11, want) in [
            (Some("x11"), false, true, Session::X11),
            (Some("wayland"), true, true, Session::Wayland),
            // Xwayland sets DISPLAY too.
            (None, true, true, Session::Wayland),
            (Some("wayland"), false, true, Session::Wayland),
            (Some("tty"), false, false, Session::None),
            (None, false, true, Session::X11),
        ] {
            assert_eq!(
                session_of(kind, wayland, x11),
                want,
                "{kind:?} {wayland} {x11}"
            );
        }
    }

    fn r(x: i32, y: i32, w: i32, h: i32) -> Rect {
        Rect { x, y, w, h }
    }

    /// What capture blacks out: Kyokai's windows less what other programs'
    /// windows above them cover; nothing of a window above Kyokai's.
    #[test]
    fn kyokai_s_visible_parts_are_found() {
        let area = |v: &[Rect]| v.iter().map(|r| r.w * r.h).sum::<i32>();
        // Alone.
        assert_eq!(
            kyokai_parts(&[(r(0, 0, 10, 10), true)]),
            vec![r(0, 0, 10, 10)]
        );
        // Under another program's window covering its right half.
        let parts = kyokai_parts(&[(r(0, 0, 10, 10), true), (r(5, 0, 10, 10), false)]);
        assert_eq!(area(&parts), 50);
        assert!(parts.iter().all(|p| p.x + p.w <= 5));
        // Above the other program: whole.
        let parts = kyokai_parts(&[(r(5, 0, 10, 10), false), (r(0, 0, 10, 10), true)]);
        assert_eq!(parts, vec![r(0, 0, 10, 10)]);
        // Covered entirely: nothing.
        assert!(kyokai_parts(&[(r(2, 2, 4, 4), true), (r(0, 0, 10, 10), false)]).is_empty());
        // A hole in the middle leaves a frame of 100 - 16.
        let parts = kyokai_parts(&[(r(0, 0, 10, 10), true), (r(3, 3, 4, 4), false)]);
        assert_eq!(area(&parts), 84);
        // Another Kyokai window above does not uncover anything.
        let parts = kyokai_parts(&[(r(0, 0, 10, 10), true), (r(5, 5, 10, 10), true)]);
        assert_eq!(area(&parts), 200);
        // Programs not Kyokai's are never blacked out.
        assert!(kyokai_parts(&[(r(0, 0, 10, 10), false)]).is_empty());
    }

    /// The window a point reaches is the topmost one there.
    #[test]
    fn the_topmost_window_takes_the_point() {
        let w = |rect, pid| Win {
            id: 0,
            rect,
            pid: Some(pid),
        };
        let list = [w(r(0, 0, 100, 100), 1), w(r(50, 50, 100, 100), 2)];
        assert_eq!(window_at(&list, 10, 10), Some(1));
        assert_eq!(window_at(&list, 60, 60), Some(2));
        assert_eq!(window_at(&list, 200, 200), None);
    }

    /// Distinct scan codes reach distinct keys, and named keys reach the
    /// same evdev key as their scan code.
    #[test]
    fn scan_codes_names_and_evdev_codes_agree() {
        let mut seen = HashMap::new();
        for scan in (0x01..=0xFF).chain(0xE001..=0xE0FF).chain([0xE11D]) {
            if let Some(k) = scan_evdev(scan)
                && let Some(other) = seen.insert(k, scan)
            {
                panic!("{other:#x} and {scan:#x} both reach {k}");
            }
        }
        for (name, scan) in [
            ("a", 0x1E),
            ("z", 0x2C),
            ("q", 0x10),
            ("m", 0x32),
            ("1", 0x02),
            ("9", 0x0A),
            ("0", 0x0B),
            ("escape", 0x01),
            ("enter", 0x1C),
            ("space", 0x39),
            ("f1", 0x3B),
            ("f12", 0x58),
            ("f13", 0x64),
            ("f24", 0x76),
            ("left", 0xE04B),
            ("delete", 0xE053),
            ("meta", 0xE05B),
            ("print-screen", 0xE037),
        ] {
            assert_eq!(keycode(name), scan_evdev(scan), "{name}");
        }
        // Evdev's well-known codes.
        assert_eq!(keycode("a"), Some(30));
        assert_eq!(keycode("escape"), Some(1));
        assert_eq!(keycode("left"), Some(105));
        assert_eq!(keycode("meta"), Some(125));
        assert_eq!(scan_evdev(0xE11D), Some(119), "Pause");
        assert_eq!(keycode("f25"), None);
    }

    /// Text splits where a run would need more codes than there are, and
    /// repeats take no code of their own.
    #[test]
    fn text_splits_into_runs_that_fit_the_codes() {
        let chars: Vec<char> = "abcabd".chars().collect();
        let runs: Vec<String> = text_runs(&chars, 3)
            .iter()
            .map(|r| r.iter().collect())
            .collect();
        assert_eq!(runs, ["abcab", "d"]);
        let runs: Vec<String> = text_runs(&chars, 1)
            .iter()
            .map(|r| r.iter().collect())
            .collect();
        assert_eq!(runs, ["a", "b", "c", "a", "b", "d"]);
        assert!(text_runs(&[], 4).is_empty());
    }

    #[test]
    fn characters_have_keysyms() {
        assert_eq!(keysym('a'), 0x61);
        assert_eq!(keysym('é'), 0xE9);
        assert_eq!(keysym('鏡'), 0x0100_93E1);
        assert_eq!(keysym('\n'), 0xFF0D);
        assert_eq!(keysym('😀'), 0x0101_F600);
    }

    /// Ctrl+Alt+Shift+Esc stops, in any order of the modifiers, left or
    /// right; Esc alone or with fewer modifiers does not.
    #[test]
    fn the_stop_shortcut_needs_all_three_modifiers() {
        let esc = ESCAPE + XKB_OFFSET;
        let (ctrl, alt, shift) = (29 + XKB_OFFSET, 100 + XKB_OFFSET, 54 + XKB_OFFSET);
        let mut held = BTreeSet::new();
        assert!(!stop_shortcut(&mut held, esc, true));
        for k in [shift, ctrl] {
            stop_shortcut(&mut held, k, true);
        }
        assert!(!stop_shortcut(&mut held, esc, true));
        stop_shortcut(&mut held, alt, true);
        assert!(stop_shortcut(&mut held, esc, true));
        assert!(
            !stop_shortcut(&mut held, esc, false),
            "the release does not count"
        );
        stop_shortcut(&mut held, ctrl, false);
        assert!(!stop_shortcut(&mut held, esc, true));
    }

    #[test]
    fn kyokai_processes_are_told_by_their_executable() {
        // This test binary is kyokai_core-….
        assert!(kyokai_process(std::process::id()));
        assert!(!kyokai_process(1), "init");
        assert!(!kyokai_process(u32::MAX), "no such process");
    }

    /// On an X11 session: the displays are listed, the primary one captures
    /// a picture of its size, the pointer is somewhere and the input watch
    /// starts. Nothing is injected: the session may be a person's.
    #[test]
    fn an_x11_session_captures() {
        if session() != Session::X11 || X::open().is_err() {
            return;
        }
        let list = displays();
        assert!(!list.is_empty());
        assert!(list[0].primary);
        let (mut cap, w, h) = XCapture::open(list[0]).unwrap();
        assert_eq!((w, h), (list[0].width, list[0].height));
        let mut got = None;
        let r = cap.frame(
            Duration::from_secs(1),
            &mut |bgra, width, height, stride| {
                got = Some((bgra.len(), width, height, stride));
            },
        );
        assert_eq!(r, Got::Picture);
        let (len, width, height, stride) = got.unwrap();
        assert_eq!(
            (width, height, stride),
            (w as usize, h as usize, w as usize * 4)
        );
        assert_eq!(len, stride * height);
        assert!(pointer().is_some());
        // The watch on the host user's input starts (XInput 2) and has seen
        // nothing of its own.
        let watch = XWatch::start().unwrap();
        assert!(!watch.stop_requested());
    }
}

//! NanoKVM USB on Linux: the capture card through V4L2 (its MJPEG, read
//! from memory-mapped buffers), the serial port through termios, both found
//! in sysfs.

pub(super) use super::termios::Serial;
use super::{Picture, Port};
use crate::video::Got;
use std::fs;
use std::io;
use std::os::fd::{AsRawFd, OwnedFd};
use std::os::unix::fs::OpenOptionsExt;
use std::path::{Path, PathBuf};
use std::time::Duration;

/// The capture reads the card's own JPEG, so Raw is offered.
pub(super) const RAW: bool = true;

/// The USB vendor of the NanoKVM USB's capture chip (MacroSilicon).
const MACROSILICON: &str = "345f";
/// The NanoKVM USB's serial chip, a CH340 (QinHeng).
const CH340: (&str, &str) = ("1a86", "7523");

/// The USB device a sysfs device hangs under: the first directory up from
/// it with a USB vendor.
fn usb_device(dev: &Path) -> Option<PathBuf> {
    let mut p = fs::canonicalize(dev).ok()?;
    loop {
        if p.join("idVendor").exists() {
            return Some(p);
        }
        if !p.pop() {
            return None;
        }
    }
}

/// The hub a USB device (named by its port path, `1-2.4`) hangs on: its
/// path without the last port; empty for a device on a root port.
fn hub(usb: &Path) -> String {
    usb.file_name()
        .and_then(|n| n.to_str())
        .and_then(|n| n.rsplit_once('.'))
        .map(|(hub, _)| hub.to_owned())
        .unwrap_or_default()
}

/// A sysfs attribute, trimmed; empty when missing.
fn attr(dir: &Path, name: &str) -> String {
    fs::read_to_string(dir.join(name))
        .map(|s| s.trim().to_owned())
        .unwrap_or_default()
}

/// The capture cards: video nodes that capture (not a card's metadata
/// node).
pub(super) fn videos() -> Vec<Port> {
    let Ok(dir) = fs::read_dir("/sys/class/video4linux") else {
        return Vec::new();
    };
    let mut out: Vec<Port> = dir
        .flatten()
        .filter_map(|e| {
            let node = e.file_name().to_string_lossy().into_owned();
            let path = format!("/dev/{node}");
            let file = fs::File::open(&path).ok()?;
            let cap = querycap(&file).ok()?;
            if cap.device_caps & CAP_VIDEO_CAPTURE == 0 {
                return None;
            }
            let usb = usb_device(&e.path().join("device"));
            let (name, vendor, hub) = match &usb {
                Some(u) => (
                    format!("{} {}", attr(u, "manufacturer"), attr(u, "product"))
                        .trim()
                        .to_owned(),
                    attr(u, "idVendor"),
                    hub(u),
                ),
                None => (cstr(&cap.card), String::new(), String::new()),
            };
            Some(Port {
                name: format!("{name} ({path})"),
                id: path,
                nanokvm: vendor == MACROSILICON,
                hub,
            })
        })
        .collect();
    out.sort_by(|a, b| a.id.cmp(&b.id));
    out
}

/// The USB serial ports (ttyUSB, ttyACM).
pub(super) fn serials() -> Vec<Port> {
    let Ok(dir) = fs::read_dir("/sys/class/tty") else {
        return Vec::new();
    };
    let mut out: Vec<Port> = dir
        .flatten()
        .filter_map(|e| {
            let node = e.file_name().to_string_lossy().into_owned();
            if !(node.starts_with("ttyUSB") || node.starts_with("ttyACM")) {
                return None;
            }
            let path = format!("/dev/{node}");
            let usb = usb_device(&e.path().join("device"))?;
            let product = attr(&usb, "product");
            Some(Port {
                name: format!(
                    "{} ({path})",
                    if product.is_empty() {
                        "USB serial"
                    } else {
                        &product
                    }
                ),
                id: path,
                nanokvm: (
                    attr(&usb, "idVendor").as_str(),
                    attr(&usb, "idProduct").as_str(),
                ) == CH340,
                hub: hub(&usb),
            })
        })
        .collect();
    out.sort_by(|a, b| a.id.cmp(&b.id));
    out
}

/// A NUL-padded C string from a V4L2 structure.
fn cstr(b: &[u8]) -> String {
    let end = b.iter().position(|&c| c == 0).unwrap_or(b.len());
    String::from_utf8_lossy(&b[..end]).into_owned()
}

// V4L2, as linux/videodev2.h defines it on 64-bit systems (Kyokai's Linux
// builds are x86-64 and arm64).

const CAP_VIDEO_CAPTURE: u32 = 0x1;
const BUF_TYPE_VIDEO_CAPTURE: u32 = 1;
const MEMORY_MMAP: u32 = 1;
const FIELD_ANY: u32 = 0;
/// `V4L2_PIX_FMT_MJPEG`.
const MJPEG: u32 = u32::from_le_bytes(*b"MJPG");
/// Buffers the driver fills in turn.
const BUFFERS: u32 = 4;

#[repr(C)]
struct Capability {
    driver: [u8; 16],
    card: [u8; 32],
    bus_info: [u8; 32],
    version: u32,
    capabilities: u32,
    device_caps: u32,
    reserved: [u32; 3],
}

#[repr(C)]
#[derive(Clone, Copy)]
struct PixFormat {
    width: u32,
    height: u32,
    pixelformat: u32,
    field: u32,
    bytesperline: u32,
    sizeimage: u32,
    colorspace: u32,
    private: u32,
    flags: u32,
    ycbcr_enc: u32,
    quantization: u32,
    xfer_func: u32,
}

/// `struct v4l2_format`: its union is 200 bytes, aligned for the pointers
/// of its window member.
#[repr(C)]
struct Format {
    kind: u32,
    fmt: FormatUnion,
}

#[repr(C)]
union FormatUnion {
    pix: PixFormat,
    raw: [u8; 200],
    _align: [u64; 25],
}

#[repr(C)]
struct RequestBuffers {
    count: u32,
    kind: u32,
    memory: u32,
    capabilities: u32,
    flags: u8,
    reserved: [u8; 3],
}

#[repr(C)]
struct Timecode {
    kind: u32,
    flags: u32,
    frames: u8,
    seconds: u8,
    minutes: u8,
    hours: u8,
    userbits: [u8; 4],
}

#[repr(C)]
struct Buffer {
    index: u32,
    kind: u32,
    bytesused: u32,
    flags: u32,
    field: u32,
    timestamp: libc::timeval,
    timecode: Timecode,
    sequence: u32,
    memory: u32,
    /// The `m` union: the offset to map, for memory-mapped buffers.
    m: u64,
    length: u32,
    reserved2: u32,
    request_fd: u32,
}

#[repr(C)]
struct Fract {
    numerator: u32,
    denominator: u32,
}

#[repr(C)]
struct CaptureParm {
    capability: u32,
    capturemode: u32,
    timeperframe: Fract,
    extendedmode: u32,
    readbuffers: u32,
    reserved: [u32; 4],
}

#[repr(C)]
struct StreamParm {
    kind: u32,
    parm: StreamParmUnion,
}

#[repr(C)]
union StreamParmUnion {
    capture: std::mem::ManuallyDrop<CaptureParm>,
    raw: [u8; 200],
}

/// An ioctl request number: direction, size, type 'V' and number.
const fn ioc(dir: u64, nr: u64, size: usize) -> u64 {
    (dir << 30) | ((size as u64) << 16) | ((b'V' as u64) << 8) | nr
}
const READ: u64 = 2;
const WRITE: u64 = 1;
const QUERYCAP: u64 = ioc(READ, 0, size_of::<Capability>());
const S_FMT: u64 = ioc(READ | WRITE, 5, size_of::<Format>());
const REQBUFS: u64 = ioc(READ | WRITE, 8, size_of::<RequestBuffers>());
const QUERYBUF: u64 = ioc(READ | WRITE, 9, size_of::<Buffer>());
const QBUF: u64 = ioc(READ | WRITE, 15, size_of::<Buffer>());
const DQBUF: u64 = ioc(READ | WRITE, 17, size_of::<Buffer>());
const STREAMON: u64 = ioc(WRITE, 18, size_of::<i32>());
const STREAMOFF: u64 = ioc(WRITE, 19, size_of::<i32>());
const S_PARM: u64 = ioc(READ | WRITE, 22, size_of::<StreamParm>());

const _: () = assert!(size_of::<Format>() == 208);
const _: () = assert!(size_of::<Buffer>() == 88);
const _: () = assert!(size_of::<StreamParm>() == 204);
const _: () = assert!(size_of::<Capability>() == 104);

/// Calls an ioctl with a structure, again while it is interrupted.
/// Notes: `T` must be the structure the request number was made for.
fn ioctl<T>(fd: &impl AsRawFd, request: u64, arg: &mut T) -> io::Result<()> {
    loop {
        // SAFETY: the fd is open for the call, `arg` is a valid, exclusive
        // pointer to the #[repr(C)] structure the request names (its size
        // is part of the request number), which the kernel reads and
        // writes only within that size.
        let r = unsafe { libc::ioctl(fd.as_raw_fd(), request as _, arg as *mut T) };
        if r == 0 {
            return Ok(());
        }
        let e = io::Error::last_os_error();
        if e.kind() != io::ErrorKind::Interrupted {
            return Err(e);
        }
    }
}

/// What a video node is and can do.
fn querycap(file: &fs::File) -> io::Result<Capability> {
    // SAFETY: Capability is plain integers and bytes, valid all-zero.
    let mut cap: Capability = unsafe { std::mem::zeroed() };
    ioctl(file, QUERYCAP, &mut cap)?;
    Ok(cap)
}

/// One buffer the driver fills, mapped into this process.
struct Mapped {
    ptr: *mut libc::c_void,
    len: usize,
}

impl Drop for Mapped {
    fn drop(&mut self) {
        // SAFETY: ptr and len are what mmap returned for this buffer, and
        // nothing borrows the mapping once its owner drops.
        unsafe { libc::munmap(self.ptr, self.len) };
    }
}

/// The capture card, streaming MJPEG into memory-mapped buffers.
pub(super) struct Camera {
    fd: OwnedFd,
    buffers: Vec<Mapped>,
}

// SAFETY: the mappings belong to this Camera alone and are read only by
// the thread that owns it.
unsafe impl Send for Camera {}

impl Camera {
    /// Opens the card at `path` in MJPEG at 1920×1080 (or the size the
    /// driver gives instead) at its fastest rate, and starts it.
    /// Returns: the camera and its pictures' size.
    pub(super) fn open(path: &str) -> Result<(Camera, u32, u32), String> {
        let file = fs::OpenOptions::new()
            .read(true)
            .write(true)
            .custom_flags(libc::O_NONBLOCK | libc::O_CLOEXEC)
            .open(path)
            .map_err(|e| format!("{path}: {e}"))?;
        let fd: OwnedFd = file.into();
        // SAFETY: Format is plain integers and bytes, valid all-zero.
        let mut f: Format = unsafe { std::mem::zeroed() };
        f.kind = BUF_TYPE_VIDEO_CAPTURE;
        f.fmt.pix = PixFormat {
            width: 1920,
            height: 1080,
            pixelformat: MJPEG,
            field: FIELD_ANY,
            // SAFETY: as above.
            ..unsafe { std::mem::zeroed() }
        };
        ioctl(&fd, S_FMT, &mut f).map_err(|e| format!("{path}: the format: {e}"))?;
        // SAFETY: the driver filled the pix member for a capture format.
        let pix = unsafe { f.fmt.pix };
        if pix.pixelformat != MJPEG {
            return Err(format!("{path} gives no MJPEG"));
        }
        // The fastest rate it has: it picks the nearest to 1/60 s.
        // SAFETY: StreamParm is plain integers, valid all-zero.
        let mut p: StreamParm = unsafe { std::mem::zeroed() };
        p.kind = BUF_TYPE_VIDEO_CAPTURE;
        p.parm.capture = std::mem::ManuallyDrop::new(CaptureParm {
            capability: 0,
            capturemode: 0,
            timeperframe: Fract {
                numerator: 1,
                denominator: 60,
            },
            extendedmode: 0,
            readbuffers: 0,
            reserved: [0; 4],
        });
        let _ = ioctl(&fd, S_PARM, &mut p);
        let mut req = RequestBuffers {
            count: BUFFERS,
            kind: BUF_TYPE_VIDEO_CAPTURE,
            memory: MEMORY_MMAP,
            capabilities: 0,
            flags: 0,
            reserved: [0; 3],
        };
        ioctl(&fd, REQBUFS, &mut req).map_err(|e| format!("{path}: buffers: {e}"))?;
        let mut buffers = Vec::new();
        for index in 0..req.count {
            let mut b = buffer(index);
            ioctl(&fd, QUERYBUF, &mut b).map_err(|e| format!("{path}: a buffer: {e}"))?;
            // SAFETY: maps the driver's buffer at the offset and length it
            // gave, shared and readable, on a descriptor this function owns;
            // a failure is MAP_FAILED, checked below.
            let ptr = unsafe {
                libc::mmap(
                    std::ptr::null_mut(),
                    b.length as usize,
                    libc::PROT_READ,
                    libc::MAP_SHARED,
                    fd.as_raw_fd(),
                    b.m as libc::off_t,
                )
            };
            if ptr == libc::MAP_FAILED {
                return Err(format!(
                    "{path}: mapping a buffer: {}",
                    io::Error::last_os_error()
                ));
            }
            buffers.push(Mapped {
                ptr,
                len: b.length as usize,
            });
            ioctl(&fd, QBUF, &mut b).map_err(|e| format!("{path}: queueing: {e}"))?;
        }
        let mut on = BUF_TYPE_VIDEO_CAPTURE as i32;
        ioctl(&fd, STREAMON, &mut on).map_err(|e| format!("{path}: starting: {e}"))?;
        Ok((Camera { fd, buffers }, pix.width, pix.height))
    }

    /// Waits up to `timeout` for the card's next picture and hands the
    /// newest one ready to `take` (older ones waiting are skipped).
    pub(super) fn frame(&mut self, timeout: Duration, take: &mut dyn FnMut(Picture<'_>)) -> Got {
        let mut pfd = libc::pollfd {
            fd: self.fd.as_raw_fd(),
            events: libc::POLLIN,
            revents: 0,
        };
        // SAFETY: one valid pollfd for an open descriptor.
        let n = unsafe { libc::poll(&mut pfd, 1, timeout.as_millis() as i32) };
        if n == 0 {
            return Got::Nothing;
        }
        if n < 0 || pfd.revents & (libc::POLLERR | libc::POLLHUP | libc::POLLNVAL) != 0 {
            return if n < 0 && io::Error::last_os_error().kind() == io::ErrorKind::Interrupted {
                Got::Nothing
            } else {
                Got::Lost
            };
        }
        // Every buffer ready, the newest kept; the others go back at once.
        let mut newest: Option<Buffer> = None;
        loop {
            let mut b = buffer(0);
            match ioctl(&self.fd, DQBUF, &mut b) {
                Ok(()) => {
                    if let Some(mut old) = newest.replace(b) {
                        let _ = ioctl(&self.fd, QBUF, &mut old);
                    }
                }
                Err(e) if e.kind() == io::ErrorKind::WouldBlock => break,
                Err(_) => return Got::Lost,
            }
        }
        let Some(mut b) = newest else {
            return Got::Nothing;
        };
        if let Some(m) = self.buffers.get(b.index as usize) {
            let used = (b.bytesused as usize).min(m.len);
            // SAFETY: the buffer is dequeued, so the driver does not write
            // it until it is queued again below; `used` is within the
            // mapping.
            let jpeg = unsafe { std::slice::from_raw_parts(m.ptr as *const u8, used) };
            take(Picture::Jpeg(jpeg));
        }
        if ioctl(&self.fd, QBUF, &mut b).is_err() {
            return Got::Lost;
        }
        Got::Picture
    }
}

impl Drop for Camera {
    fn drop(&mut self) {
        let mut off = BUF_TYPE_VIDEO_CAPTURE as i32;
        let _ = ioctl(&self.fd, STREAMOFF, &mut off);
    }
}

/// An empty capture buffer of the memory-mapped kind.
fn buffer(index: u32) -> Buffer {
    // SAFETY: Buffer is plain integers (a timeval among them), valid
    // all-zero.
    let mut b: Buffer = unsafe { std::mem::zeroed() };
    b.index = index;
    b.kind = BUF_TYPE_VIDEO_CAPTURE;
    b.memory = MEMORY_MMAP;
    b
}

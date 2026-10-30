//! NanoKVM USB on Windows: the capture card through Media Foundation's
//! source reader in its own MJPEG, the serial port through the Win32 comm
//! functions; both found, and paired by the hub they hang on, through
//! SetupAPI and the device tree.

use super::{Picture, Port};
use crate::video::Got;
use std::io;
use std::time::{Duration, Instant};
use windows::Win32::Devices::Communication::{
    COMMTIMEOUTS, DCB, GetCommState, NOPARITY, ONESTOPBIT, PURGE_RXCLEAR, PURGE_TXCLEAR, PurgeComm,
    SetCommState, SetCommTimeouts,
};
use windows::Win32::Devices::DeviceAndDriverInstallation::{
    CM_Get_Device_IDW, CM_Get_Parent, CM_LOCATE_DEVNODE_NORMAL, CM_Locate_DevNodeW, CR_SUCCESS,
    DICS_FLAG_GLOBAL, DIGCF_PRESENT, DIREG_DEV, GUID_DEVCLASS_PORTS, MAX_DEVICE_ID_LEN,
    SP_DEVINFO_DATA, SPDRP_FRIENDLYNAME, SetupDiDestroyDeviceInfoList, SetupDiEnumDeviceInfo,
    SetupDiGetClassDevsW, SetupDiGetDeviceInstanceIdW, SetupDiGetDeviceRegistryPropertyW,
    SetupDiOpenDevRegKey,
};
use windows::Win32::Foundation::{CloseHandle, GENERIC_READ, GENERIC_WRITE, HANDLE};
use windows::Win32::Media::MediaFoundation::*;
use windows::Win32::Storage::FileSystem::{
    CreateFileW, FILE_FLAGS_AND_ATTRIBUTES, FILE_SHARE_NONE, OPEN_EXISTING, ReadFile, WriteFile,
};
use windows::Win32::System::Com::CoTaskMemFree;
use windows::Win32::System::Registry::{KEY_READ, RegCloseKey, RegQueryValueExW};
use windows::core::{GUID, HSTRING, PCWSTR, PWSTR};

/// The capture reads the card's own JPEG, so Raw is offered.
pub(super) const RAW: bool = true;

/// The NanoKVM USB's capture chip (MacroSilicon) and serial chip (CH340),
/// as their device IDs name them.
const MACROSILICON: &str = "VID_345F";
const CH340: &str = "VID_1A86&PID_7523";

/// A wide string up to its NUL.
fn wide(b: &[u16]) -> String {
    let end = b.iter().position(|&c| c == 0).unwrap_or(b.len());
    String::from_utf16_lossy(&b[..end])
}

/// A device node's ID.
fn device_id(node: u32) -> Option<String> {
    let mut buf = [0u16; MAX_DEVICE_ID_LEN as usize + 1];
    // SAFETY: the buffer is as long as the call is told.
    (unsafe { CM_Get_Device_IDW(node, &mut buf, 0) } == CR_SUCCESS).then(|| wide(&buf))
}

/// The hub a device hangs on: from its node, up past its interfaces to the
/// USB device itself (`USB\VID_…` without `&MI_`), then that device's
/// parent.
fn hub_of(mut node: u32) -> String {
    for _ in 0..4 {
        let Some(id) = device_id(node) else {
            return String::new();
        };
        let upper = id.to_ascii_uppercase();
        let mut parent = 0;
        // SAFETY: writes the parent's handle into a local.
        if unsafe { CM_Get_Parent(&mut parent, node, 0) } != CR_SUCCESS {
            return String::new();
        }
        if upper.starts_with("USB\\VID_") && !upper.contains("&MI_") {
            return device_id(parent).unwrap_or_default();
        }
        node = parent;
    }
    String::new()
}

/// A device node by its ID.
fn locate(id: &str) -> Option<u32> {
    let mut node = 0;
    let id = HSTRING::from(id);
    // SAFETY: a NUL-terminated ID and a local for the handle.
    (unsafe { CM_Locate_DevNodeW(&mut node, PCWSTR(id.as_ptr()), CM_LOCATE_DEVNODE_NORMAL) }
        == CR_SUCCESS)
        .then_some(node)
}

/// The device instance ID a capture card's symbolic link names
/// (`\\?\usb#vid_…&mi_00#7&…#{…}\global` → `usb\vid_…&mi_00\7&…`).
fn instance_of(link: &str) -> Option<String> {
    let rest = link.strip_prefix(r"\\?\")?;
    let end = rest.rfind("#{")?;
    Some(rest[..end].replace('#', "\\"))
}

/// The video capture devices' activation objects.
fn capture_devices() -> Result<Vec<IMFActivate>, String> {
    crate::mf::startup()?;
    // SAFETY: plain Media Foundation calls with locals; the array MF
    // allocates is taken over item by item and freed with CoTaskMemFree.
    unsafe {
        let mut attrs = None;
        MFCreateAttributes(&mut attrs, 1).map_err(|e| e.to_string())?;
        let attrs = attrs.ok_or("no attributes")?;
        attrs
            .SetGUID(
                &MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE,
                &MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE_VIDCAP_GUID,
            )
            .map_err(|e| e.to_string())?;
        let mut list: *mut Option<IMFActivate> = std::ptr::null_mut();
        let mut count = 0u32;
        MFEnumDeviceSources(&attrs, &mut list, &mut count).map_err(|e| e.to_string())?;
        let mut out = Vec::new();
        for i in 0..count as usize {
            if let Some(a) = (*list.add(i)).take() {
                out.push(a);
            }
        }
        CoTaskMemFree(Some(list as *const _));
        Ok(out)
    }
}

/// An activation object's string attribute.
fn string(a: &IMFActivate, key: &GUID) -> Option<String> {
    let mut p = PWSTR::null();
    let mut len = 0;
    // SAFETY: MF allocates the string, which is copied and freed here.
    unsafe {
        a.GetAllocatedString(key, &mut p, &mut len).ok()?;
        let s = p.to_string().ok();
        CoTaskMemFree(Some(p.0 as *const _));
        s
    }
}

/// The capture cards, by their symbolic links.
pub(super) fn videos() -> Vec<Port> {
    let Ok(list) = capture_devices() else {
        return Vec::new();
    };
    list.iter()
        .filter_map(|a| {
            let link = string(a, &MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE_VIDCAP_SYMBOLIC_LINK)?;
            let name = string(a, &MF_DEVSOURCE_ATTRIBUTE_FRIENDLY_NAME).unwrap_or_default();
            let hub = instance_of(&link)
                .and_then(|i| locate(&i))
                .map(hub_of)
                .unwrap_or_default();
            Some(Port {
                nanokvm: link.to_ascii_uppercase().contains(MACROSILICON),
                id: link,
                name,
                hub,
            })
        })
        .collect()
}

/// The serial ports (the Ports class), by their COM names.
pub(super) fn serials() -> Vec<Port> {
    let mut out = Vec::new();
    // SAFETY: SetupAPI calls on a device set this function owns and
    // destroys; every buffer passed is as long as the call is told.
    unsafe {
        let Ok(set) = SetupDiGetClassDevsW(
            Some(&GUID_DEVCLASS_PORTS),
            PCWSTR::null(),
            None,
            DIGCF_PRESENT,
        ) else {
            return out;
        };
        let mut i = 0;
        loop {
            let mut info = SP_DEVINFO_DATA {
                cbSize: size_of::<SP_DEVINFO_DATA>() as u32,
                ..Default::default()
            };
            if SetupDiEnumDeviceInfo(set, i, &mut info).is_err() {
                break;
            }
            i += 1;
            let mut idbuf = [0u16; MAX_DEVICE_ID_LEN as usize + 1];
            let id = SetupDiGetDeviceInstanceIdW(set, &info, Some(&mut idbuf), None)
                .map(|()| wide(&idbuf))
                .unwrap_or_default();
            let mut name = [0u8; 512];
            let name = SetupDiGetDeviceRegistryPropertyW(
                set,
                &info,
                SPDRP_FRIENDLYNAME,
                None,
                Some(&mut name),
                None,
            )
            .map(|()| {
                let w: Vec<u16> = name
                    .as_chunks::<2>()
                    .0
                    .iter()
                    .map(|c| u16::from_le_bytes(*c))
                    .collect();
                wide(&w)
            })
            .unwrap_or_default();
            let Ok(key) =
                SetupDiOpenDevRegKey(set, &info, DICS_FLAG_GLOBAL.0, 0, DIREG_DEV, KEY_READ.0)
            else {
                continue;
            };
            let mut port = [0u16; 64];
            let mut len = (port.len() * 2) as u32;
            let got = RegQueryValueExW(
                key,
                windows::core::w!("PortName"),
                None,
                None,
                Some(port.as_mut_ptr().cast()),
                Some(&mut len),
            );
            let _ = RegCloseKey(key);
            if got.is_err() {
                continue;
            }
            let com = wide(&port);
            if !com.starts_with("COM") {
                continue;
            }
            out.push(Port {
                nanokvm: id.to_ascii_uppercase().contains(CH340),
                name: if name.is_empty() { com.clone() } else { name },
                hub: hub_of(info.DevInst),
                id: com,
            });
        }
        let _ = SetupDiDestroyDeviceInfoList(set);
    }
    out.sort_by(|a, b| a.id.cmp(&b.id));
    out
}

/// The capture card through a source reader that gives its own MJPEG.
pub(super) struct Camera {
    reader: IMFSourceReader,
}

// SAFETY: the reader is made in the multithreaded apartment and used by
// the one thread that owns the Camera.
unsafe impl Send for Camera {}

impl Camera {
    /// Opens the capture card whose symbolic link is `link` in MJPEG at
    /// 1920×1080 when it has it (else its largest MJPEG size), at the
    /// fastest rate of that size.
    /// Returns: the camera and its pictures' size.
    pub(super) fn open(link: &str) -> Result<(Camera, u32, u32), String> {
        let a = capture_devices()?
            .into_iter()
            .find(|a| {
                string(a, &MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE_VIDCAP_SYMBOLIC_LINK).as_deref()
                    == Some(link)
            })
            .ok_or("the capture card is not there")?;
        // SAFETY: Media Foundation calls on objects this function owns;
        // native types are only read.
        unsafe {
            let source: IMFMediaSource = a
                .ActivateObject()
                .map_err(|e| format!("opening the card: {e}"))?;
            let mut attrs = None;
            MFCreateAttributes(&mut attrs, 1).map_err(|e| e.to_string())?;
            let attrs = attrs.ok_or("no attributes")?;
            attrs
                .SetUINT32(&MF_READWRITE_DISABLE_CONVERTERS, 1)
                .map_err(|e| e.to_string())?;
            let reader = MFCreateSourceReaderFromMediaSource(&source, &attrs)
                .map_err(|e| format!("a reader of the card: {e}"))?;
            let stream = MF_SOURCE_READER_FIRST_VIDEO_STREAM.0 as u32;
            // The best MJPEG type: 1920×1080 first, then the largest; the
            // fastest of the same size.
            let mut best: Option<(IMFMediaType, u32, u32, f64)> = None;
            let mut i = 0;
            while let Ok(t) = reader.GetNativeMediaType(stream, i) {
                i += 1;
                if t.GetGUID(&MF_MT_SUBTYPE).ok() != Some(MFVideoFormat_MJPG) {
                    continue;
                }
                let Ok(size) = t.GetUINT64(&MF_MT_FRAME_SIZE) else {
                    continue;
                };
                let (w, h) = ((size >> 32) as u32, size as u32);
                let rate = t
                    .GetUINT64(&MF_MT_FRAME_RATE)
                    .map_or(0.0, |r| (r >> 32) as f64 / (r as u32).max(1) as f64);
                let score = |w: u32, h: u32, rate: f64| {
                    ((w, h) == (1920, 1080), w * h, (rate * 100.0) as u64)
                };
                if best
                    .as_ref()
                    .is_none_or(|(_, bw, bh, br)| score(w, h, rate) > score(*bw, *bh, *br))
                {
                    best = Some((t, w, h, rate));
                }
            }
            let (t, w, h, _) = best.ok_or("the card gives no MJPEG")?;
            reader
                .SetCurrentMediaType(stream, None, &t)
                .map_err(|e| format!("the card's format: {e}"))?;
            Ok((Camera { reader }, w, h))
        }
    }

    /// Reads the card's next picture and hands it to `take`.
    /// Notes: the reader blocks until a picture comes (the card sends one
    /// every frame, a blank one without a signal); `_timeout` is not used.
    pub(super) fn frame(&mut self, _timeout: Duration, take: &mut dyn FnMut(Picture<'_>)) -> Got {
        let mut flags = 0u32;
        let mut sample = None;
        // SAFETY: a synchronous read into locals; the buffer is locked only
        // while `take` reads it.
        unsafe {
            if self
                .reader
                .ReadSample(
                    MF_SOURCE_READER_FIRST_VIDEO_STREAM.0 as u32,
                    0,
                    None,
                    Some(&mut flags),
                    None,
                    Some(&mut sample),
                )
                .is_err()
                || flags & (MF_SOURCE_READERF_ERROR.0 | MF_SOURCE_READERF_ENDOFSTREAM.0) as u32 != 0
            {
                return Got::Lost;
            }
            let Some(sample) = sample else {
                return Got::Nothing;
            };
            let Ok(buffer) = sample.ConvertToContiguousBuffer() else {
                return Got::Nothing;
            };
            let mut ptr = std::ptr::null_mut();
            let mut len = 0u32;
            if buffer.Lock(&mut ptr, None, Some(&mut len)).is_err() {
                return Got::Nothing;
            }
            take(Picture::Jpeg(std::slice::from_raw_parts(ptr, len as usize)));
            let _ = buffer.Unlock();
        }
        Got::Picture
    }
}

/// The serial port, at the given speed, 8N1, opened exclusively.
pub(super) struct Serial {
    handle: HANDLE,
}

// SAFETY: the handle belongs to this Serial alone.
unsafe impl Send for Serial {}

impl Serial {
    /// Opens `com` (`COM5`) at `baud`, 8 data bits, no parity, one stop
    /// bit; reads return what came within their timeout.
    pub(super) fn open(com: &str, baud: u32) -> Result<Serial, String> {
        let path = HSTRING::from(format!(r"\\.\{com}"));
        // SAFETY: opens a device by a NUL-terminated name; the DCB and the
        // timeouts are locals the calls read and write.
        unsafe {
            let handle = CreateFileW(
                &path,
                (GENERIC_READ | GENERIC_WRITE).0,
                FILE_SHARE_NONE,
                None,
                OPEN_EXISTING,
                FILE_FLAGS_AND_ATTRIBUTES(0),
                None,
            )
            .map_err(|e| format!("{com}: {e}"))?;
            let port = Serial { handle };
            let mut dcb = DCB {
                DCBlength: size_of::<DCB>() as u32,
                ..Default::default()
            };
            GetCommState(handle, &mut dcb).map_err(|e| format!("{com}: {e}"))?;
            dcb.BaudRate = baud;
            dcb.ByteSize = 8;
            dcb.Parity = NOPARITY;
            dcb.StopBits = ONESTOPBIT;
            // fBinary on; parity checks, flow control and the rest off.
            dcb._bitfield = 1;
            SetCommState(handle, &dcb).map_err(|e| format!("{com}: {e}"))?;
            let timeouts = COMMTIMEOUTS {
                ReadIntervalTimeout: u32::MAX,
                ReadTotalTimeoutMultiplier: 0,
                ReadTotalTimeoutConstant: 0,
                WriteTotalTimeoutMultiplier: 0,
                WriteTotalTimeoutConstant: 1000,
            };
            SetCommTimeouts(handle, &timeouts).map_err(|e| format!("{com}: {e}"))?;
            let _ = PurgeComm(handle, PURGE_RXCLEAR | PURGE_TXCLEAR);
            Ok(port)
        }
    }

    /// Writes all of `bytes`.
    pub(super) fn write(&mut self, mut bytes: &[u8]) -> io::Result<()> {
        while !bytes.is_empty() {
            let mut n = 0u32;
            // SAFETY: writes from a valid slice to an open handle.
            unsafe { WriteFile(self.handle, Some(bytes), Some(&mut n), None) }
                .map_err(|e| io::Error::other(e.to_string()))?;
            if n == 0 {
                return Err(io::ErrorKind::TimedOut.into());
            }
            bytes = &bytes[n as usize..];
        }
        Ok(())
    }

    /// Reads what arrives within `timeout` into `out`.
    pub(super) fn read(&mut self, timeout: Duration, out: &mut Vec<u8>) -> io::Result<()> {
        let until = Instant::now() + timeout;
        let mut buf = [0u8; 256];
        while Instant::now() < until {
            let mut n = 0u32;
            // SAFETY: reads into a valid buffer from an open handle; the
            // timeouts make it return at once with what is there.
            unsafe { ReadFile(self.handle, Some(&mut buf), Some(&mut n), None) }
                .map_err(|e| io::Error::other(e.to_string()))?;
            if n == 0 {
                std::thread::sleep(Duration::from_millis(5));
                continue;
            }
            out.extend_from_slice(&buf[..n as usize]);
        }
        Ok(())
    }
}

impl Drop for Serial {
    fn drop(&mut self) {
        // SAFETY: closes the handle this Serial owns, once.
        let _ = unsafe { CloseHandle(self.handle) };
    }
}

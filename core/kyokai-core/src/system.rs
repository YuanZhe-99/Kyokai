//! The host's own status for quiet status watchers (doc/en-us/computer-use.md,
//! "Host status"): the processor, memory, graphics, the system disk and its
//! reads and writes, the network's rates, the battery, the uptime, and which
//! of its SSH, Remote Desktop and VNC answer on loopback. Sampled on a thread
//! of its own every SAMPLE_EVERY while anyone watches, with the system's own
//! counters (no crate of its own).

use crate::access::Permission;
use crate::tunnel::Service;
use serde::{Deserialize, Serialize};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};
use tokio::sync::watch;

/// How often a watched host samples itself.
pub const SAMPLE_EVERY: Duration = Duration::from_secs(2);
/// How often a watcher's session looks which services listen.
pub const PROBE_EVERY: Duration = Duration::from_secs(30);
/// How long one look at a service's port may take.
const PROBE_WAIT: Duration = Duration::from_millis(300);

/// The host's status at one moment (the `system` message).
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct Sample {
    /// Unix milliseconds, the host's clock.
    pub at: u64,
    pub uptime_s: u64,
    pub cpu: Cpu,
    pub memory: Usage,
    /// None where the system reports no graphics load.
    pub gpu: Option<Load>,
    pub disk: Usage,
    /// The disks' reads and writes; None where the system reports none.
    #[serde(default)]
    pub disk_io: Option<DiskIo>,
    pub net: Rates,
    /// None without a battery.
    pub battery: Option<Battery>,
    /// The services answering on the host's loopback, on the ports its user
    /// set, filled in per watcher.
    #[serde(default)]
    pub services: Vec<Service>,
    /// What the watcher's password or key grants without asking, filled
    /// in per watcher (empty while the host asks for everything), so the
    /// controller offers only what will work.
    #[serde(default)]
    pub offers: Vec<Permission>,
    /// Whether the host's AI can start a task (a model chosen, its key
    /// present), filled in per watcher.
    #[serde(default)]
    pub ai: bool,
}

/// The processor's load, 0–100, over its logical cores: in all, and the
/// parts spent in programs (user) and in the system.
#[derive(Clone, Copy, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct Cpu {
    pub usage: f64,
    pub cores: u32,
    #[serde(default)]
    pub user: f64,
    #[serde(default)]
    pub system: f64,
}

/// A load, 0–100.
#[derive(Clone, Copy, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct Load {
    pub usage: f64,
}

/// Bytes used of a total.
#[derive(Clone, Copy, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct Usage {
    pub used: u64,
    pub total: u64,
}

/// Bytes per second received and sent, over the physical interfaces, and
/// the bytes their counters hold (since they came up).
#[derive(Clone, Copy, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct Rates {
    pub rx_bps: u64,
    pub tx_bps: u64,
    #[serde(default)]
    pub rx_total: u64,
    #[serde(default)]
    pub tx_total: u64,
}

/// Bytes per second read from and written to the disks.
#[derive(Clone, Copy, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct DiskIo {
    pub read_bps: u64,
    pub write_bps: u64,
}

/// The battery's charge and whether it charges.
#[derive(Clone, Copy, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct Battery {
    pub percent: u8,
    pub charging: bool,
}

/// A watcher's subscription to the samples: the newest, as they come. The
/// sampler runs while any subscription is held.
pub struct Watch {
    pub samples: watch::Receiver<Option<Arc<Sample>>>,
}

/// The running sampler: where it sends, how many watch, and what stops it.
struct Running {
    tx: watch::Sender<Option<Arc<Sample>>>,
    watchers: usize,
    stop: Arc<AtomicBool>,
}

static SAMPLER: Mutex<Option<Running>> = Mutex::new(None);

/// Subscribes to the host's samples.
/// Side effects: the first subscription starts the sampler's thread.
pub fn watch() -> Watch {
    let mut s = SAMPLER.lock().unwrap_or_else(|e| e.into_inner());
    if let Some(r) = s.as_mut() {
        r.watchers += 1;
        return Watch {
            samples: r.tx.subscribe(),
        };
    }
    let (tx, samples) = watch::channel(None);
    let stop = Arc::new(AtomicBool::new(false));
    let (sender, stopped) = (tx.clone(), stop.clone());
    std::thread::spawn(move || run(sender, stopped));
    *s = Some(Running {
        tx,
        watchers: 1,
        stop,
    });
    Watch { samples }
}

impl Drop for Watch {
    fn drop(&mut self) {
        let mut s = SAMPLER.lock().unwrap_or_else(|e| e.into_inner());
        if let Some(r) = s.as_mut() {
            r.watchers -= 1;
            if r.watchers == 0 {
                r.stop.store(true, Ordering::Relaxed);
                *s = None;
            }
        }
    }
}

/// The sampler's thread: a sample every SAMPLE_EVERY until stopped.
fn run(tx: watch::Sender<Option<Arc<Sample>>>, stop: Arc<AtomicBool>) {
    let mut probe = Probe::new();
    while !stop.load(Ordering::Relaxed) {
        tx.send_replace(Some(Arc::new(probe.sample())));
        let until = Instant::now() + SAMPLE_EVERY;
        while !stop.load(Ordering::Relaxed) && Instant::now() < until {
            std::thread::sleep(Duration::from_millis(100));
        }
    }
}

/// The services that accept a connection on this computer's loopback, on
/// the ports its user set (each looked at once, all at the same time).
pub async fn listening(ports: crate::access::ServicePorts) -> Vec<Service> {
    let looks = [Service::Ssh, Service::Rdp, Service::Vnc].map(|s| async move {
        let port = ports.port(s);
        for ip in [
            std::net::IpAddr::from([127, 0, 0, 1]),
            std::net::IpAddr::from(std::net::Ipv6Addr::LOCALHOST),
        ] {
            let look = tokio::net::TcpStream::connect((ip, port));
            if matches!(tokio::time::timeout(PROBE_WAIT, look).await, Ok(Ok(_))) {
                return Some(s);
            }
        }
        None
    });
    let [a, b, c] = looks;
    let (a, b, c) = tokio::join!(a, b, c);
    [a, b, c].into_iter().flatten().collect()
}

/// What the counters were at the last sample, to turn them into rates.
struct Probe {
    /// User, system and total processor time.
    cpu: Option<(u64, u64, u64)>,
    /// Bytes received and sent, and when.
    net: Option<(u64, u64, Instant)>,
    gpu: imp::Gpu,
    disk: imp::DiskCounters,
}

impl Probe {
    fn new() -> Probe {
        Probe {
            cpu: imp::cpu_times(),
            net: imp::net_bytes().map(|(rx, tx)| (rx, tx, Instant::now())),
            gpu: imp::Gpu::open(),
            disk: imp::DiskCounters::open(),
        }
    }

    /// Reads the counters now; rates and loads are over the time since
    /// the last sample (zero for the first).
    fn sample(&mut self) -> Sample {
        let cpu = imp::cpu_times();
        let (user, system) = match (self.cpu, cpu) {
            (Some((u0, s0, t0)), Some((u1, s1, t1))) if t1 > t0 => {
                let share = |a: u64, b: u64| {
                    (b.saturating_sub(a) as f64 * 100.0 / (t1 - t0) as f64).clamp(0.0, 100.0)
                };
                (share(u0, u1), share(s0, s1))
            }
            _ => (0.0, 0.0),
        };
        self.cpu = cpu.or(self.cpu);
        let now = Instant::now();
        let net = imp::net_bytes();
        let rates = match (self.net, net) {
            (Some((r0, t0, at)), Some((r1, t1))) => {
                let secs = now.duration_since(at).as_secs_f64().max(0.001);
                Rates {
                    rx_bps: (r1.saturating_sub(r0) as f64 / secs) as u64,
                    tx_bps: (t1.saturating_sub(t0) as f64 / secs) as u64,
                    rx_total: r1,
                    tx_total: t1,
                }
            }
            _ => Rates::default(),
        };
        self.net = net.map(|(rx, tx)| (rx, tx, now)).or(self.net);
        Sample {
            at: SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .map_or(0, |d| d.as_millis() as u64),
            uptime_s: imp::uptime(),
            cpu: Cpu {
                usage: (user + system).min(100.0),
                cores: std::thread::available_parallelism().map_or(1, |n| n.get() as u32),
                user,
                system,
            },
            memory: imp::memory(),
            gpu: self.gpu.load().map(|usage| Load { usage }),
            disk: imp::disk(),
            disk_io: self.disk.rates(),
            net: rates,
            battery: imp::battery(),
            services: Vec::new(),
            offers: Vec::new(),
            ai: false,
        }
    }
}

#[cfg(windows)]
mod imp {
    use super::{Battery, Usage};
    use windows::Win32::Foundation::FILETIME;
    use windows::Win32::NetworkManagement::IpHelper::{FreeMibTable, GetIfTable2, MIB_IF_TABLE2};
    use windows::Win32::NetworkManagement::Ndis::IfOperStatusUp;
    use windows::Win32::Storage::FileSystem::GetDiskFreeSpaceExW;
    use windows::Win32::System::Performance::{
        PDH_FMT_COUNTERVALUE, PDH_FMT_COUNTERVALUE_ITEM_W, PDH_FMT_DOUBLE, PDH_HCOUNTER,
        PDH_HQUERY, PDH_MORE_DATA, PdhAddEnglishCounterW, PdhCloseQuery, PdhCollectQueryData,
        PdhGetFormattedCounterArrayW, PdhGetFormattedCounterValue, PdhOpenQueryW,
    };
    use windows::Win32::System::Power::{GetSystemPowerStatus, SYSTEM_POWER_STATUS};
    use windows::Win32::System::SystemInformation::{
        GetTickCount64, GetWindowsDirectoryW, GlobalMemoryStatusEx, MEMORYSTATUSEX,
    };
    use windows::Win32::System::Threading::GetSystemTimes;
    use windows::core::{PCWSTR, w};

    fn ticks(f: FILETIME) -> u64 {
        (u64::from(f.dwHighDateTime) << 32) | u64::from(f.dwLowDateTime)
    }

    /// User, system and total processor time of every core, in 100 ns
    /// ticks.
    pub fn cpu_times() -> Option<(u64, u64, u64)> {
        let (mut idle, mut kernel, mut user) = Default::default();
        // SAFETY: three valid out pointers for the call.
        unsafe {
            GetSystemTimes(
                Some(&mut idle as *mut _),
                Some(&mut kernel as *mut _),
                Some(&mut user as *mut _),
            )
        }
        .ok()?;
        // Kernel time includes the idle time.
        let total = ticks(kernel) + ticks(user);
        Some((
            ticks(user),
            ticks(kernel).saturating_sub(ticks(idle)),
            total,
        ))
    }

    pub fn memory() -> Usage {
        let mut m = MEMORYSTATUSEX {
            dwLength: size_of::<MEMORYSTATUSEX>() as u32,
            ..Default::default()
        };
        // SAFETY: a valid structure with its length set.
        match unsafe { GlobalMemoryStatusEx(&mut m) } {
            Ok(()) => Usage {
                used: m.ullTotalPhys.saturating_sub(m.ullAvailPhys),
                total: m.ullTotalPhys,
            },
            Err(_) => Usage::default(),
        }
    }

    pub fn uptime() -> u64 {
        // SAFETY: no arguments; reads the system tick count.
        let ms = unsafe { GetTickCount64() };
        ms / 1000
    }

    /// The disk Windows is on.
    pub fn disk() -> Usage {
        let mut dir = [0u16; 260];
        // SAFETY: the buffer and its length.
        let n = unsafe { GetWindowsDirectoryW(Some(&mut dir)) } as usize;
        if !(3..dir.len()).contains(&n) {
            return Usage::default();
        }
        // "C:\" and its terminating zero.
        let root = [dir[0], dir[1], dir[2], 0];
        let (mut total, mut free) = (0u64, 0u64);
        // SAFETY: a zero-terminated path and two out pointers.
        let ok = unsafe {
            GetDiskFreeSpaceExW(
                PCWSTR(root.as_ptr()),
                None,
                Some(&mut total as *mut u64),
                Some(&mut free as *mut u64),
            )
        };
        match ok {
            Ok(()) => Usage {
                used: total.saturating_sub(free),
                total,
            },
            Err(_) => Usage::default(),
        }
    }

    /// Bytes received and sent by the hardware interfaces that are up
    /// (filter drivers, which repeat an interface's counters, left out).
    pub fn net_bytes() -> Option<(u64, u64)> {
        let mut table: *mut MIB_IF_TABLE2 = std::ptr::null_mut();
        // SAFETY: an out pointer; the table is freed below.
        if unsafe { GetIfTable2(&mut table) }.is_err() || table.is_null() {
            return None;
        }
        // SAFETY: the system filled NumEntries rows after the count.
        let rows = unsafe {
            std::slice::from_raw_parts((*table).Table.as_ptr(), (*table).NumEntries as usize)
        };
        let (mut rx, mut tx) = (0u64, 0u64);
        for r in rows {
            let flags = r.InterfaceAndOperStatusFlags._bitfield;
            let hardware = flags & 1 != 0 && flags & 2 == 0;
            if hardware && r.OperStatus == IfOperStatusUp {
                rx = rx.wrapping_add(r.InOctets);
                tx = tx.wrapping_add(r.OutOctets);
            }
        }
        // SAFETY: the table GetIfTable2 allocated, freed once.
        unsafe { FreeMibTable(table as *const _) };
        Some((rx, tx))
    }

    pub fn battery() -> Option<Battery> {
        let mut s = SYSTEM_POWER_STATUS::default();
        // SAFETY: a valid out structure.
        unsafe { GetSystemPowerStatus(&mut s) }.ok()?;
        // 128: no system battery; 255: unknown.
        if s.BatteryFlag & 128 != 0 || s.BatteryLifePercent > 100 {
            return None;
        }
        Some(Battery {
            percent: s.BatteryLifePercent,
            charging: s.BatteryFlag & 8 != 0,
        })
    }

    /// The graphics processors' 3D engines' load, through the performance
    /// counters Task Manager reads.
    pub struct Gpu(Option<(PDH_HQUERY, PDH_HCOUNTER)>);

    // SAFETY: the query is used and closed only on the sampler's thread.
    unsafe impl Send for Gpu {}

    impl Gpu {
        pub fn open() -> Gpu {
            let mut query = PDH_HQUERY::default();
            let mut counter = PDH_HCOUNTER::default();
            // SAFETY: out pointers for the query and the counter; the query
            // is closed on drop.
            let opened = unsafe {
                PdhOpenQueryW(PCWSTR::null(), 0, &mut query) == 0
                    && PdhAddEnglishCounterW(
                        query,
                        w!(r"\GPU Engine(*engtype_3D)\Utilization Percentage"),
                        0,
                        &mut counter,
                    ) == 0
                    && PdhCollectQueryData(query) == 0
            };
            if !opened {
                if !query.is_invalid() {
                    // SAFETY: the query opened above, closed once.
                    unsafe { PdhCloseQuery(query) };
                }
                return Gpu(None);
            }
            Gpu(Some((query, counter)))
        }

        /// The 3D engines' load since the last look: each process's share
        /// summed, as Task Manager shows it; None without a 3D engine.
        pub fn load(&mut self) -> Option<f64> {
            let (query, counter) = self.0?;
            // SAFETY: the open query.
            if unsafe { PdhCollectQueryData(query) } != 0 {
                return None;
            }
            let (mut size, mut count) = (0u32, 0u32);
            // SAFETY: asks for the buffer's size first.
            let r = unsafe {
                PdhGetFormattedCounterArrayW(counter, PDH_FMT_DOUBLE, &mut size, &mut count, None)
            };
            if r != PDH_MORE_DATA || count == 0 {
                return None;
            }
            let room = (size as usize).div_ceil(size_of::<PDH_FMT_COUNTERVALUE_ITEM_W>());
            let mut items = vec![PDH_FMT_COUNTERVALUE_ITEM_W::default(); room.max(count as usize)];
            // SAFETY: a buffer of at least `size` bytes, aligned for the items.
            let r = unsafe {
                PdhGetFormattedCounterArrayW(
                    counter,
                    PDH_FMT_DOUBLE,
                    &mut size,
                    &mut count,
                    Some(items.as_mut_ptr()),
                )
            };
            if r != 0 {
                return None;
            }
            let sum: f64 = items[..count as usize]
                .iter()
                .filter(|i| i.FmtValue.CStatus <= 1)
                // SAFETY: PDH_FMT_DOUBLE fills the double.
                .map(|i| unsafe { i.FmtValue.Anonymous.doubleValue })
                .sum();
            Some(sum.clamp(0.0, 100.0))
        }
    }

    impl Drop for Gpu {
        fn drop(&mut self) {
            if let Some((query, _)) = self.0 {
                // SAFETY: the query opened in `open`, closed once.
                unsafe { PdhCloseQuery(query) };
            }
        }
    }

    /// The physical disks' reads and writes, through the performance
    /// counters Task Manager reads.
    pub struct DiskCounters(Option<(PDH_HQUERY, PDH_HCOUNTER, PDH_HCOUNTER)>);

    // SAFETY: the query is used and closed only on the sampler's thread.
    unsafe impl Send for DiskCounters {}

    impl DiskCounters {
        pub fn open() -> DiskCounters {
            let mut query = PDH_HQUERY::default();
            let (mut read, mut write) = (PDH_HCOUNTER::default(), PDH_HCOUNTER::default());
            // SAFETY: out pointers for the query and the counters; the query
            // is closed on drop.
            let opened = unsafe {
                PdhOpenQueryW(PCWSTR::null(), 0, &mut query) == 0
                    && PdhAddEnglishCounterW(
                        query,
                        w!(r"\PhysicalDisk(_Total)\Disk Read Bytes/sec"),
                        0,
                        &mut read,
                    ) == 0
                    && PdhAddEnglishCounterW(
                        query,
                        w!(r"\PhysicalDisk(_Total)\Disk Write Bytes/sec"),
                        0,
                        &mut write,
                    ) == 0
                    && PdhCollectQueryData(query) == 0
            };
            if !opened {
                if !query.is_invalid() {
                    // SAFETY: the query opened above, closed once.
                    unsafe { PdhCloseQuery(query) };
                }
                return DiskCounters(None);
            }
            DiskCounters(Some((query, read, write)))
        }

        /// Bytes per second read and written since the last look.
        pub fn rates(&mut self) -> Option<super::DiskIo> {
            let (query, read, write) = self.0?;
            // SAFETY: the open query.
            if unsafe { PdhCollectQueryData(query) } != 0 {
                return None;
            }
            let value = |counter| {
                let mut v = PDH_FMT_COUNTERVALUE::default();
                // SAFETY: a counter of the open query and an out value.
                let r =
                    unsafe { PdhGetFormattedCounterValue(counter, PDH_FMT_DOUBLE, None, &mut v) };
                // SAFETY: PDH_FMT_DOUBLE fills the double.
                (r == 0 && v.CStatus <= 1)
                    .then(|| unsafe { v.Anonymous.doubleValue }.max(0.0) as u64)
            };
            Some(super::DiskIo {
                read_bps: value(read)?,
                write_bps: value(write)?,
            })
        }
    }

    impl Drop for DiskCounters {
        fn drop(&mut self) {
            if let Some((query, ..)) = self.0 {
                // SAFETY: the query opened in `open`, closed once.
                unsafe { PdhCloseQuery(query) };
            }
        }
    }
}

#[cfg(target_os = "macos")]
#[allow(deprecated)] // mach_host_self: the mach2 crate is not a dependency
mod imp {
    use super::{Battery, Usage};
    use objc2_core_foundation::CFString;
    use std::ffi::{CStr, c_char, c_void};

    /// A sysctl value of a fixed size.
    fn sysctl<T: Copy>(name: &CStr) -> Option<T> {
        let mut value = std::mem::MaybeUninit::<T>::uninit();
        let mut len = size_of::<T>();
        // SAFETY: a zero-terminated name and room for one T.
        let r = unsafe {
            libc::sysctlbyname(
                name.as_ptr(),
                value.as_mut_ptr().cast(),
                &mut len,
                std::ptr::null_mut(),
                0,
            )
        };
        // SAFETY: the call filled exactly a T.
        (r == 0 && len == size_of::<T>()).then(|| unsafe { value.assume_init() })
    }

    /// User (with nice), system and total processor ticks of every core.
    pub fn cpu_times() -> Option<(u64, u64, u64)> {
        // SAFETY: a plain structure of integers.
        let mut info: libc::host_cpu_load_info = unsafe { std::mem::zeroed() };
        let mut count = libc::HOST_CPU_LOAD_INFO_COUNT;
        // SAFETY: the host port, the flavor's structure and its size.
        let r = unsafe {
            libc::host_statistics(
                libc::mach_host_self(),
                libc::HOST_CPU_LOAD_INFO,
                (&mut info as *mut libc::host_cpu_load_info).cast(),
                &mut count,
            )
        };
        if r != libc::KERN_SUCCESS {
            return None;
        }
        let ticks = info.cpu_ticks.map(u64::from);
        let total: u64 = ticks.iter().sum();
        Some((
            ticks[libc::CPU_STATE_USER as usize] + ticks[libc::CPU_STATE_NICE as usize],
            ticks[libc::CPU_STATE_SYSTEM as usize],
            total,
        ))
    }

    /// Memory in use as Activity Monitor counts it: app memory, wired and
    /// compressed.
    pub fn memory() -> Usage {
        let total = sysctl::<u64>(c"hw.memsize").unwrap_or(0);
        let page = sysctl::<u64>(c"hw.pagesize").unwrap_or(4096);
        // SAFETY: a plain structure of integers.
        let mut vm: libc::vm_statistics64 = unsafe { std::mem::zeroed() };
        let mut count = libc::HOST_VM_INFO64_COUNT;
        // SAFETY: the host port, the flavor's structure and its size.
        let r = unsafe {
            libc::host_statistics64(
                libc::mach_host_self(),
                libc::HOST_VM_INFO64,
                (&mut vm as *mut libc::vm_statistics64).cast(),
                &mut count,
            )
        };
        if r != libc::KERN_SUCCESS {
            return Usage { used: 0, total };
        }
        let (internal, purgeable, wired, compressed) = (
            vm.internal_page_count,
            vm.purgeable_count,
            vm.wire_count,
            vm.compressor_page_count,
        );
        let pages = u64::from(internal.saturating_sub(purgeable))
            + u64::from(wired)
            + u64::from(compressed);
        Usage {
            used: (pages * page).min(total),
            total,
        }
    }

    pub fn uptime() -> u64 {
        let boot = sysctl::<libc::timeval>(c"kern.boottime").map_or(0, |t| t.tv_sec as u64);
        let now = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map_or(0, |d| d.as_secs());
        now.saturating_sub(boot)
    }

    /// The data volume (where the user's files are), else the root.
    pub fn disk() -> Usage {
        for path in [c"/System/Volumes/Data", c"/"] {
            // SAFETY: a plain structure filled by the call.
            let mut fs: libc::statfs = unsafe { std::mem::zeroed() };
            // SAFETY: a zero-terminated path and an out structure.
            if unsafe { libc::statfs(path.as_ptr(), &mut fs) } == 0 {
                let total = fs.f_blocks * u64::from(fs.f_bsize);
                let free = fs.f_bavail * u64::from(fs.f_bsize);
                return Usage {
                    used: total.saturating_sub(free),
                    total,
                };
            }
        }
        Usage::default()
    }

    /// The start of `struct if_data` (<net/if_var.h>), up to the byte
    /// counters; the rest is never read.
    #[repr(C)]
    struct IfData {
        kinds: [u8; 8],
        mtu: u32,
        metric: u32,
        baudrate: u32,
        ipackets: u32,
        ierrors: u32,
        opackets: u32,
        oerrors: u32,
        collisions: u32,
        ibytes: u32,
        obytes: u32,
    }

    /// Bytes received and sent by the interfaces that are up, loopback
    /// left out (32-bit counters: a wrap shows as one sample of zero).
    pub fn net_bytes() -> Option<(u64, u64)> {
        let mut first: *mut libc::ifaddrs = std::ptr::null_mut();
        // SAFETY: an out pointer; the list is freed below.
        if unsafe { libc::getifaddrs(&mut first) } != 0 {
            return None;
        }
        let (mut rx, mut tx) = (0u64, 0u64);
        let mut at = first;
        while !at.is_null() {
            // SAFETY: a node of the list getifaddrs made.
            let ifa = unsafe { &*at };
            let up = ifa.ifa_flags & libc::IFF_UP as u32 != 0
                && ifa.ifa_flags & libc::IFF_LOOPBACK as u32 == 0;
            // SAFETY: a non-null address of the node.
            let link = !ifa.ifa_addr.is_null()
                && i32::from(unsafe { (*ifa.ifa_addr).sa_family }) == libc::AF_LINK;
            if up && link && !ifa.ifa_data.is_null() {
                // SAFETY: AF_LINK entries carry the interface's if_data.
                let d = unsafe { &*(ifa.ifa_data as *const IfData) };
                rx += u64::from(d.ibytes);
                tx += u64::from(d.obytes);
            }
            at = ifa.ifa_next;
        }
        // SAFETY: the list getifaddrs made, freed once.
        unsafe { libc::freeifaddrs(first) };
        Some((rx, tx))
    }

    #[link(name = "IOKit", kind = "framework")]
    unsafe extern "C" {
        fn IOPSCopyPowerSourcesInfo() -> *const c_void;
        fn IOPSCopyPowerSourcesList(blob: *const c_void) -> *const c_void;
        fn IOPSGetPowerSourceDescription(
            blob: *const c_void,
            source: *const c_void,
        ) -> *const c_void;
        fn IOServiceMatching(name: *const c_char) -> *mut c_void;
        fn IOServiceGetMatchingServices(port: u32, matching: *mut c_void, found: *mut u32) -> i32;
        fn IOIteratorNext(iterator: u32) -> u32;
        fn IORegistryEntryCreateCFProperty(
            entry: u32,
            key: *const c_void,
            allocator: *const c_void,
            options: u32,
        ) -> *const c_void;
        fn IOObjectRelease(object: u32) -> i32;
    }

    #[link(name = "CoreFoundation", kind = "framework")]
    unsafe extern "C" {
        fn CFRelease(cf: *const c_void);
        fn CFArrayGetCount(array: *const c_void) -> isize;
        fn CFArrayGetValueAtIndex(array: *const c_void, index: isize) -> *const c_void;
        fn CFDictionaryGetValue(dict: *const c_void, key: *const c_void) -> *const c_void;
        fn CFNumberGetValue(number: *const c_void, kind: isize, out: *mut c_void) -> u8;
        fn CFBooleanGetValue(boolean: *const c_void) -> u8;
        fn CFGetTypeID(cf: *const c_void) -> usize;
        fn CFNumberGetTypeID() -> usize;
        fn CFBooleanGetTypeID() -> usize;
        fn CFDictionaryGetTypeID() -> usize;
    }

    /// kCFNumberSInt64Type.
    const SINT64: isize = 4;

    /// A dictionary's value for a key, borrowed from it.
    fn value(dict: *const c_void, key: &str) -> *const c_void {
        let key = CFString::from_str(key);
        // SAFETY: a live dictionary and a live key; the value is borrowed.
        unsafe { CFDictionaryGetValue(dict, (&*key as *const CFString).cast()) }
    }

    /// A number's value, when it is one.
    fn number(v: *const c_void) -> Option<i64> {
        // SAFETY: a live CF object (or null, checked first).
        if v.is_null() || unsafe { CFGetTypeID(v) != CFNumberGetTypeID() } {
            return None;
        }
        let mut n = 0i64;
        // SAFETY: a number and room for an SInt64.
        (unsafe { CFNumberGetValue(v, SINT64, (&mut n as *mut i64).cast()) } != 0).then_some(n)
    }

    /// The internal battery's charge, from the power sources.
    pub fn battery() -> Option<Battery> {
        // SAFETY: the info and the list are owned here and released below;
        // the descriptions are borrowed from them.
        unsafe {
            let blob = IOPSCopyPowerSourcesInfo();
            if blob.is_null() {
                return None;
            }
            let list = IOPSCopyPowerSourcesList(blob);
            let mut found = None;
            if !list.is_null() {
                for i in 0..CFArrayGetCount(list) {
                    let d = IOPSGetPowerSourceDescription(blob, CFArrayGetValueAtIndex(list, i));
                    if d.is_null() || CFGetTypeID(d) != CFDictionaryGetTypeID() {
                        continue;
                    }
                    let (Some(now), Some(max)) = (
                        number(value(d, "Current Capacity")),
                        number(value(d, "Max Capacity")),
                    ) else {
                        continue;
                    };
                    let charging = value(d, "Is Charging");
                    let charging = !charging.is_null()
                        && CFGetTypeID(charging) == CFBooleanGetTypeID()
                        && CFBooleanGetValue(charging) != 0;
                    found = Some(Battery {
                        percent: (now * 100 / max.max(1)).clamp(0, 100) as u8,
                        charging,
                    });
                    break;
                }
                CFRelease(list);
            }
            CFRelease(blob);
            found
        }
    }

    /// The graphics processors' load, as their drivers report it.
    pub struct Gpu;

    impl Gpu {
        pub fn open() -> Gpu {
            Gpu
        }

        /// The busiest accelerator's "Device Utilization %"; None when none
        /// reports one.
        pub fn load(&mut self) -> Option<f64> {
            let stats = CFString::from_static_str("PerformanceStatistics");
            // SAFETY: the matching dictionary is consumed by the call; the
            // iterator, its entries and the properties are released here.
            unsafe {
                let matching = IOServiceMatching(c"IOAccelerator".as_ptr());
                let mut found = 0u32;
                if matching.is_null() || IOServiceGetMatchingServices(0, matching, &mut found) != 0
                {
                    return None;
                }
                let mut busiest: Option<i64> = None;
                loop {
                    let entry = IOIteratorNext(found);
                    if entry == 0 {
                        break;
                    }
                    let p = IORegistryEntryCreateCFProperty(
                        entry,
                        (&*stats as *const CFString).cast(),
                        std::ptr::null(),
                        0,
                    );
                    if !p.is_null() {
                        if CFGetTypeID(p) == CFDictionaryGetTypeID()
                            && let Some(u) = number(value(p, "Device Utilization %"))
                        {
                            busiest = Some(busiest.map_or(u, |b| b.max(u)));
                        }
                        CFRelease(p);
                    }
                    IOObjectRelease(entry);
                }
                IOObjectRelease(found);
                busiest.map(|u| (u as f64).clamp(0.0, 100.0))
            }
        }
    }

    /// The disks' reads and writes: the block storage drivers' byte
    /// counters, turned into rates between looks.
    pub struct DiskCounters(Option<(u64, u64, std::time::Instant)>);

    impl DiskCounters {
        pub fn open() -> DiskCounters {
            DiskCounters(totals().map(|(r, w)| (r, w, std::time::Instant::now())))
        }

        /// Bytes per second read and written since the last look.
        pub fn rates(&mut self) -> Option<super::DiskIo> {
            let (r1, w1) = totals()?;
            let now = std::time::Instant::now();
            let last = self.0.replace((r1, w1, now));
            let (r0, w0, at) = last?;
            let secs = now.duration_since(at).as_secs_f64().max(0.001);
            Some(super::DiskIo {
                read_bps: (r1.saturating_sub(r0) as f64 / secs) as u64,
                write_bps: (w1.saturating_sub(w0) as f64 / secs) as u64,
            })
        }
    }

    /// Bytes read and written by every block storage driver.
    fn totals() -> Option<(u64, u64)> {
        let stats = CFString::from_static_str("Statistics");
        // SAFETY: the matching dictionary is consumed by the call; the
        // iterator, its entries and the properties are released here.
        unsafe {
            let matching = IOServiceMatching(c"IOBlockStorageDriver".as_ptr());
            let mut found = 0u32;
            if matching.is_null() || IOServiceGetMatchingServices(0, matching, &mut found) != 0 {
                return None;
            }
            let (mut read, mut written, mut any) = (0u64, 0u64, false);
            loop {
                let entry = IOIteratorNext(found);
                if entry == 0 {
                    break;
                }
                let p = IORegistryEntryCreateCFProperty(
                    entry,
                    (&*stats as *const CFString).cast(),
                    std::ptr::null(),
                    0,
                );
                if !p.is_null() {
                    if CFGetTypeID(p) == CFDictionaryGetTypeID() {
                        if let Some(n) = number(value(p, "Bytes (Read)")) {
                            read += n.max(0) as u64;
                            any = true;
                        }
                        if let Some(n) = number(value(p, "Bytes (Write)")) {
                            written += n.max(0) as u64;
                        }
                    }
                    CFRelease(p);
                }
                IOObjectRelease(entry);
            }
            IOObjectRelease(found);
            any.then_some((read, written))
        }
    }
}

#[cfg(not(any(windows, target_os = "macos")))]
mod imp {
    use super::{Battery, Usage};
    pub fn cpu_times() -> Option<(u64, u64, u64)> {
        None
    }
    pub fn memory() -> Usage {
        Usage::default()
    }
    pub fn uptime() -> u64 {
        0
    }
    pub fn disk() -> Usage {
        Usage::default()
    }
    pub fn net_bytes() -> Option<(u64, u64)> {
        None
    }
    pub fn battery() -> Option<Battery> {
        None
    }
    pub struct Gpu;
    impl Gpu {
        pub fn open() -> Gpu {
            Gpu
        }
        pub fn load(&mut self) -> Option<f64> {
            None
        }
    }
    pub struct DiskCounters;
    impl DiskCounters {
        pub fn open() -> DiskCounters {
            DiskCounters
        }
        pub fn rates(&mut self) -> Option<super::DiskIo> {
            None
        }
    }
}

#[cfg(all(test, any(windows, target_os = "macos")))]
mod tests {
    use super::*;

    /// The numbers a sample holds are this computer's and make sense.
    #[test]
    fn samples_are_plausible() {
        let mut probe = Probe::new();
        // Some work between two samples.
        let busy = Instant::now();
        let mut x = 0u64;
        while busy.elapsed() < Duration::from_millis(300) {
            x = x.wrapping_add(std::hint::black_box(1));
        }
        let s = probe.sample();
        assert!(x > 0);
        assert!((0.0..=100.0).contains(&s.cpu.usage), "{s:?}");
        assert!(s.cpu.cores >= 1);
        assert!(s.memory.total > 0 && s.memory.used > 0 && s.memory.used <= s.memory.total);
        assert!(s.disk.total > 0 && s.disk.used <= s.disk.total);
        assert!(s.uptime_s > 0);
        assert!(s.at > 1_700_000_000_000);
        if let Some(g) = s.gpu {
            assert!((0.0..=100.0).contains(&g.usage));
        }
        if let Some(b) = s.battery {
            assert!(b.percent <= 100);
        }
        // As the `system` message carries it.
        let v = serde_json::to_value(&s).unwrap();
        for key in [
            "at", "uptime_s", "cpu", "memory", "gpu", "disk", "net", "battery",
        ] {
            assert!(v.get(key).is_some(), "{key}");
        }
    }

    /// Samples come every SAMPLE_EVERY while someone watches, to every
    /// watcher.
    #[test]
    fn samples_come_while_watched() {
        let mut first = watch();
        let mut second = watch();
        let rt = tokio::runtime::Builder::new_current_thread()
            .enable_time()
            .build()
            .unwrap();
        rt.block_on(async {
            for w in [&mut first, &mut second] {
                for _ in 0..2 {
                    tokio::time::timeout(SAMPLE_EVERY * 3, w.samples.changed())
                        .await
                        .expect("a sample in time")
                        .unwrap();
                }
            }
        });
        let a = first.samples.borrow().clone().unwrap();
        let b = second.samples.borrow().clone().unwrap();
        assert!(a.at.abs_diff(b.at) <= SAMPLE_EVERY.as_millis() as u64 * 2);
    }

    /// The services that answer are looked for on the ports the host's user
    /// set (regression: only the standard ports were looked at).
    #[tokio::test]
    async fn services_answer_on_the_ports_set() {
        let open = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let closed = {
            let l = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
            l.local_addr().unwrap().port()
        };
        let ports = crate::access::ServicePorts {
            ssh: Some(closed),
            rdp: Some(closed),
            vnc: Some(open.local_addr().unwrap().port()),
        };
        assert_eq!(listening(ports).await, vec![Service::Vnc]);
    }
}

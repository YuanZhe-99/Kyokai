//! The NanoKVM USB's serial port on Linux and macOS, through termios.

use std::fs;
use std::io;
use std::os::fd::{AsRawFd, OwnedFd};
use std::os::unix::fs::OpenOptionsExt;
use std::time::{Duration, Instant};

/// The serial port, raw at the given speed, 8N1, held exclusively.
pub(super) struct Serial {
    fd: OwnedFd,
}

impl Serial {
    /// Opens `path` raw at `baud`, 8 data bits, no parity, one stop bit.
    pub(super) fn open(path: &str, baud: u32) -> Result<Serial, String> {
        let file = fs::OpenOptions::new()
            .read(true)
            .write(true)
            .custom_flags(libc::O_NONBLOCK | libc::O_CLOEXEC)
            .open(path)
            .map_err(|e| format!("{path}: {e}"))?;
        let fd: OwnedFd = file.into();
        let speed = match baud {
            57_600 => libc::B57600,
            115_200 => libc::B115200,
            9_600 => libc::B9600,
            _ => return Err(format!("no speed {baud}")),
        };
        // SAFETY: TIOCEXCL takes no argument; tcgetattr fills a zeroed
        // termios for an open terminal; cfmakeraw and cfset*speed change
        // only that structure; tcsetattr reads it.
        let ok = unsafe {
            libc::ioctl(fd.as_raw_fd(), libc::TIOCEXCL);
            let mut t: libc::termios = std::mem::zeroed();
            if libc::tcgetattr(fd.as_raw_fd(), &mut t) != 0 {
                false
            } else {
                libc::cfmakeraw(&mut t);
                t.c_cflag |= libc::CLOCAL | libc::CREAD;
                t.c_cflag &= !(libc::CSTOPB | libc::PARENB | libc::CRTSCTS);
                libc::cfsetispeed(&mut t, speed);
                libc::cfsetospeed(&mut t, speed);
                libc::tcsetattr(fd.as_raw_fd(), libc::TCSANOW, &t) == 0
            }
        };
        if !ok {
            return Err(format!("{path}: {}", io::Error::last_os_error()));
        }
        // SAFETY: discards what waits on an open terminal.
        unsafe { libc::tcflush(fd.as_raw_fd(), libc::TCIOFLUSH) };
        Ok(Serial { fd })
    }

    /// Writes all of `bytes`, waiting for room.
    pub(super) fn write(&mut self, mut bytes: &[u8]) -> io::Result<()> {
        while !bytes.is_empty() {
            // SAFETY: writes from a valid slice to an open descriptor.
            let n = unsafe { libc::write(self.fd.as_raw_fd(), bytes.as_ptr().cast(), bytes.len()) };
            if n < 0 {
                let e = io::Error::last_os_error();
                match e.kind() {
                    io::ErrorKind::Interrupted => continue,
                    io::ErrorKind::WouldBlock => {
                        self.wait(libc::POLLOUT, Duration::from_millis(100))?;
                        continue;
                    }
                    _ => return Err(e),
                }
            }
            bytes = &bytes[n as usize..];
        }
        Ok(())
    }

    /// Reads what arrives within `timeout` into `out`.
    pub(super) fn read(&mut self, timeout: Duration, out: &mut Vec<u8>) -> io::Result<()> {
        let until = Instant::now() + timeout;
        let mut buf = [0u8; 256];
        loop {
            let left = until.saturating_duration_since(Instant::now());
            if left.is_zero() || !self.wait(libc::POLLIN, left)? {
                return Ok(());
            }
            // SAFETY: reads into a valid buffer from an open descriptor.
            let n = unsafe { libc::read(self.fd.as_raw_fd(), buf.as_mut_ptr().cast(), buf.len()) };
            match n {
                0 => return Err(io::ErrorKind::UnexpectedEof.into()),
                n if n < 0 => {
                    let e = io::Error::last_os_error();
                    if !matches!(
                        e.kind(),
                        io::ErrorKind::Interrupted | io::ErrorKind::WouldBlock
                    ) {
                        return Err(e);
                    }
                }
                n => out.extend_from_slice(&buf[..n as usize]),
            }
        }
    }

    /// Waits up to `timeout` for the port to be ready for `events`.
    /// Returns: whether it is; an error once the device is gone.
    fn wait(&self, events: i16, timeout: Duration) -> io::Result<bool> {
        let mut pfd = libc::pollfd {
            fd: self.fd.as_raw_fd(),
            events,
            revents: 0,
        };
        // SAFETY: one valid pollfd for an open descriptor.
        let n = unsafe { libc::poll(&mut pfd, 1, timeout.as_millis().max(1) as i32) };
        if n < 0 {
            let e = io::Error::last_os_error();
            return if e.kind() == io::ErrorKind::Interrupted {
                Ok(false)
            } else {
                Err(e)
            };
        }
        if pfd.revents & (libc::POLLERR | libc::POLLHUP | libc::POLLNVAL) != 0 {
            return Err(io::Error::new(
                io::ErrorKind::BrokenPipe,
                "the serial port is gone",
            ));
        }
        Ok(n > 0)
    }
}

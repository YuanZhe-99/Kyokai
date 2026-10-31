//! What a device tells the other side of a session about itself once the
//! login succeeded: a friendly name, its maker and model, and its operating
//! system (doc/en-us/identity-and-access.md, "Friendly names").
//!
//! None of it authenticates anything: the Device ID and the pinned key do.
//! It never travels before the password check, and never in the EasyTier
//! hostname — except the name alone, in `host_hello`, while the user chose to
//! show it in their networks (`publish`).

use serde::{Deserialize, Serialize};
use std::path::{Path, PathBuf};

/// Longest friendly name kept, in characters.
pub const MAX_NAME: usize = 64;

/// Longest maker, model or system name kept, in characters.
const MAX_FIELD: usize = 64;

/// A device's description of itself.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct About {
    /// The name its user chose, or the computer's name.
    #[serde(default)]
    pub name: String,
    /// The hardware maker, as the firmware reports it ("LENOVO", "Apple").
    #[serde(default)]
    pub maker: String,
    /// The hardware model ("ThinkPad X1 Carbon", "MacBookPro18,3").
    #[serde(default)]
    pub model: String,
    /// "windows" or "macos".
    #[serde(default)]
    pub os: String,
}

impl About {
    /// Cleans a description a peer sent: control characters dropped,
    /// whitespace trimmed, each field cut to its length limit.
    pub fn clean(self) -> About {
        About {
            name: tidy(&self.name, MAX_NAME),
            maker: tidy(&self.maker, MAX_FIELD),
            model: tidy(&self.model, MAX_FIELD),
            os: tidy(&self.os, MAX_FIELD),
        }
    }
}

/// Removes control characters and bidirectional overrides (which could
/// show a name reversed), trims and cuts `s` to `max` characters.
fn tidy(s: &str, max: usize) -> String {
    let bidi = |c: char| matches!(c, '\u{200e}' | '\u{200f}' | '\u{202a}'..='\u{202e}' | '\u{2066}'..='\u{2069}');
    let s: String = s.chars().filter(|&c| !c.is_control() && !bidi(c)).collect();
    s.trim()
        .chars()
        .take(max)
        .collect::<String>()
        .trim_end()
        .to_owned()
}

/// This device's description and where the chosen name is kept.
pub struct Local {
    path: PathBuf,
    /// The name the user chose; empty follows the computer's name.
    chosen: String,
    /// The name shows to anyone who reaches this device, before any login
    /// (`published`).
    publish: bool,
    hardware: (String, String),
}

/// The file that keeps the chosen name and whether it shows before a login
/// (not a secret).
#[derive(Serialize, Deserialize, Default)]
struct Saved {
    #[serde(default)]
    name: String,
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    publish: bool,
}

impl Local {
    /// Reads the chosen name and whether it shows before a login from
    /// `state_dir`, and the hardware from the system (once).
    pub fn load(state_dir: &Path) -> Local {
        let path = state_dir.join("device.json");
        let saved = std::fs::read(&path)
            .ok()
            .and_then(|b| serde_json::from_slice::<Saved>(&b).ok())
            .unwrap_or_default();
        Local {
            path,
            chosen: tidy(&saved.name, MAX_NAME),
            publish: saved.publish,
            hardware: hardware(),
        }
    }

    /// The name the user chose ("" when it follows the computer's name).
    pub fn chosen(&self) -> &str {
        &self.chosen
    }

    /// Whether the name shows to anyone who reaches this device.
    pub fn publish(&self) -> bool {
        self.publish
    }

    /// Sets the friendly name (an empty one follows the computer's name)
    /// and whether it shows before a login; None keeps either as it is.
    /// Side effects: writes device.json.
    pub fn set(&mut self, name: Option<&str>, publish: Option<bool>) -> Result<(), String> {
        let name = name.map_or_else(|| self.chosen.clone(), |n| tidy(n, MAX_NAME));
        let publish = publish.unwrap_or(self.publish);
        let data = serde_json::to_vec(&Saved {
            name: name.clone(),
            publish,
        })
        .map_err(|e| e.to_string())?;
        crate::store::write_atomic(&self.path, &data)?;
        self.chosen = name;
        self.publish = publish;
        Ok(())
    }

    /// The name this device shows before any login: its friendly name while
    /// the user chose to show it in their networks, else None.
    pub fn published(&self) -> Option<String> {
        self.publish
            .then(|| self.about().name)
            .filter(|n| !n.is_empty())
    }

    /// What this device tells a peer now.
    pub fn about(&self) -> About {
        let name = if self.chosen.is_empty() {
            tidy(&computer_name(), MAX_NAME)
        } else {
            self.chosen.clone()
        };
        About {
            name,
            maker: tidy(&self.hardware.0, MAX_FIELD),
            model: tidy(&self.hardware.1, MAX_FIELD),
            os: OS.into(),
        }
    }
}

#[cfg(windows)]
const OS: &str = "windows";
#[cfg(target_os = "macos")]
const OS: &str = "macos";
#[cfg(not(any(windows, target_os = "macos")))]
const OS: &str = "";

/// The computer's name as the system shows it.
#[cfg(windows)]
fn computer_name() -> String {
    std::env::var("COMPUTERNAME").unwrap_or_default()
}

/// The computer's name as the system shows it (System Settings, Sharing).
#[cfg(target_os = "macos")]
fn computer_name() -> String {
    command("/usr/sbin/scutil", &["--get", "ComputerName"])
}

#[cfg(not(any(windows, target_os = "macos")))]
fn computer_name() -> String {
    String::new()
}

/// The hardware maker and model from the firmware tables (SMBIOS).
#[cfg(windows)]
fn hardware() -> (String, String) {
    use windows::Win32::System::Registry::{HKEY_LOCAL_MACHINE, RRF_RT_REG_SZ, RegGetValueW};
    use windows::core::HSTRING;
    let read = |value: &str| {
        let mut buf = [0u16; 256];
        let mut len = std::mem::size_of_val(&buf) as u32;
        // SAFETY: reads a string value into `buf`, whose size in bytes is
        // passed in `len`; the strings outlive the call.
        let ok = unsafe {
            RegGetValueW(
                HKEY_LOCAL_MACHINE,
                &HSTRING::from(r"HARDWARE\DESCRIPTION\System\BIOS"),
                &HSTRING::from(value),
                RRF_RT_REG_SZ,
                None,
                Some(buf.as_mut_ptr().cast()),
                Some(&mut len),
            )
        }
        .is_ok();
        let units = (len as usize / 2).min(buf.len());
        let s = if ok {
            String::from_utf16_lossy(&buf[..units])
        } else {
            String::new()
        };
        s.trim_end_matches('\0').to_owned()
    };
    (read("SystemManufacturer"), read("SystemProductName"))
}

/// The hardware maker and model.
#[cfg(target_os = "macos")]
fn hardware() -> (String, String) {
    (
        "Apple".into(),
        command("/usr/sbin/sysctl", &["-n", "hw.model"]),
    )
}

#[cfg(not(any(windows, target_os = "macos")))]
fn hardware() -> (String, String) {
    (String::new(), String::new())
}

/// The first line a system tool prints, or "" when it fails.
#[cfg(target_os = "macos")]
fn command(program: &str, args: &[&str]) -> String {
    std::process::Command::new(program)
        .args(args)
        .output()
        .ok()
        .filter(|o| o.status.success())
        .and_then(|o| String::from_utf8(o.stdout).ok())
        .map(|s| s.lines().next().unwrap_or_default().to_owned())
        .unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn peer_descriptions_are_cleaned() {
        let long = "x".repeat(200);
        let cases = [
            ("  Office PC  ", "Office PC"),
            ("bad\u{7}name\nhere", "badnamehere"),
            ("\u{202e}cod.exe", "cod.exe"),
            (long.as_str(), &long[..MAX_NAME]),
        ];
        for (sent, want) in cases {
            let a = About {
                name: sent.into(),
                ..About::default()
            }
            .clean();
            assert_eq!(a.name, want, "{sent:?}");
        }
    }

    #[test]
    fn the_chosen_name_is_kept_and_empty_follows_the_computer() {
        let dir = std::env::temp_dir().join(format!("kyokai-about-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let mut l = Local::load(&dir);
        assert_eq!(l.chosen(), "");
        l.set(Some("  Living room  "), None).unwrap();
        assert_eq!(Local::load(&dir).about().name, "Living room");
        l.set(Some(""), None).unwrap();
        assert_eq!(Local::load(&dir).chosen(), "");
        assert_eq!(
            Local::load(&dir).about().name,
            tidy(&computer_name(), MAX_NAME)
        );
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn the_name_shows_before_a_login_only_while_published() {
        let dir = std::env::temp_dir().join(format!("kyokai-publish-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let mut l = Local::load(&dir);
        l.set(Some("Living room"), None).unwrap();
        assert_eq!(l.published(), None, "off by default");
        l.set(None, Some(true)).unwrap();
        let again = Local::load(&dir);
        assert_eq!(again.chosen(), "Living room", "the name stays");
        assert_eq!(again.published().as_deref(), Some("Living room"));
        l.set(Some("Den"), None).unwrap();
        assert_eq!(
            Local::load(&dir).published().as_deref(),
            Some("Den"),
            "the choice stays"
        );
        l.set(None, Some(false)).unwrap();
        assert_eq!(Local::load(&dir).published(), None);
        let _ = std::fs::remove_dir_all(&dir);
    }
}

//! An official EasyTier core (`easytier-core`) run as a process of its own
//! for one joined network, instead of the embedded node
//! (doc/en-us/networking.md, "Official EasyTier core").
//!
//! The core runs without TUN. Kyokai reaches other devices through its
//! SOCKS5 portal on a loopback port, and other devices reach this one
//! through the core's own rewrite of its virtual address to loopback, on
//! KYOKAI_PORT (the only port its whitelist admits). What it knows of the
//! network comes from `easytier-cli -o json peer`, never `node`, whose
//! answer holds the network secret.

use std::net::{Ipv4Addr, SocketAddr, SocketAddrV4};
use std::path::{Path, PathBuf};
use std::process::Stdio;
use std::time::Duration;

use serde::Deserialize;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpStream;
use toml_edit::{DocumentMut, Item, Table};

/// The folder, under the state directory, that holds the downloaded cores
/// (`cores/<tag>/`, written by the engine) and the runtime files.
pub const CORES_DIR: &str = "cores";

/// The runtime files' folder under CORES_DIR.
const RUN_DIR: &str = "run";

/// How long one `easytier-cli` call may take.
const CLI_TIMEOUT: Duration = Duration::from_secs(3);

/// How long the core has to answer its RPC portal after it starts.
const START_TIMEOUT: Duration = Duration::from_secs(15);

/// The most output read from one `easytier-cli` call.
const CLI_MAX: usize = 1 << 20;

/// The keys Kyokai sets itself, refused in a network's TOML: its identity
/// in the network (`Kyokai-<id>`, the instance), the network's servers,
/// name and secret (the network's own fields), the portals Kyokai talks
/// through, and what keeps the core from opening this computer to the
/// network (no TUN, only KYOKAI_PORT, no proxy input, no foreign networks).
pub const OWNED: &[&str] = &[
    "instance_name",
    "instance_id",
    "hostname",
    "network_identity",
    "peer",
    "rpc_portal",
    "socks5_proxy",
    "tcp_whitelist",
    "udp_whitelist",
    "flags.no_tun",
    "flags.disable_quic_input",
    "flags.disable_kcp_input",
    "flags.private_mode",
];

/// Parses a network's TOML (the person's own options).
/// Returns: the document, or why it is not valid TOML.
pub fn parse(text: &str) -> Result<DocumentMut, String> {
    text.parse::<DocumentMut>()
        .map_err(|e| format!("the TOML is not valid: {}", e.message()))
}

/// The keys a network's TOML sets, as dotted paths of its tables (a table
/// array counts as one key): what switching back to Kyokai Network removes.
pub fn keys(doc: &DocumentMut) -> Vec<String> {
    fn walk(prefix: &str, table: &Table, out: &mut Vec<String>) {
        for (key, item) in table.iter() {
            let path = if prefix.is_empty() {
                key.to_owned()
            } else {
                format!("{prefix}.{key}")
            };
            match item {
                Item::Table(t) => walk(&path, t, out),
                Item::None => {}
                _ => out.push(path),
            }
        }
    }
    let mut out = Vec::new();
    walk("", doc.as_table(), &mut out);
    out
}

/// The keys of `doc` that Kyokai sets itself (OWNED), refused.
pub fn owned_in(doc: &DocumentMut) -> Vec<String> {
    keys(doc)
        .into_iter()
        .filter(|k| {
            OWNED
                .iter()
                .any(|o| k == o || k.starts_with(&format!("{o}.")))
        })
        .collect()
}

/// The core's configuration: Kyokai's own (`base`, as the embedded node's)
/// with the person's options over it, then the SOCKS5 portal on `socks`.
/// Notes: `person` holds no OWNED key (checked when it was set); a table
/// merges key by key, anything else replaces Kyokai's value.
pub fn merged(base: &str, person: &DocumentMut, socks: u16) -> Result<String, String> {
    fn merge(into: &mut Table, from: &Table) {
        for (key, item) in from.iter() {
            match (into.get_mut(key), item) {
                (Some(Item::Table(a)), Item::Table(b)) => merge(a, b),
                _ => {
                    into.insert(key, item.clone());
                }
            }
        }
    }
    let mut doc = parse(base)?;
    merge(doc.as_table_mut(), person.as_table());
    doc["socks5_proxy"] = toml_edit::value(format!("socks5://127.0.0.1:{socks}"));
    Ok(doc.to_string())
}

/// The core and CLI programs of a downloaded core, by its tag.
/// Returns: None when the tag does not name a folder under CORES_DIR or
/// either program is missing.
pub fn programs(state_dir: &Path, tag: &str) -> Option<(PathBuf, PathBuf)> {
    // A tag names one folder of CORES_DIR, never a path.
    if tag.is_empty()
        || tag == RUN_DIR
        || !tag
            .chars()
            .all(|c| c.is_ascii_alphanumeric() || ".-_".contains(c))
    {
        return None;
    }
    let dir = state_dir.join(CORES_DIR).join(tag);
    let exe = if cfg!(windows) { ".exe" } else { "" };
    let core = dir.join(format!("easytier-core{exe}"));
    let cli = dir.join(format!("easytier-cli{exe}"));
    (core.is_file() && cli.is_file()).then_some((core, cli))
}

/// The runtime file of a network's core: its whole configuration, secret
/// included, which exists only while the core starts.
pub fn run_file(state_dir: &Path, profile: &str) -> PathBuf {
    state_dir
        .join(CORES_DIR)
        .join(RUN_DIR)
        .join(format!("{profile}.toml"))
}

/// Removes runtime files a core left behind (a crash, a kill): they hold a
/// network secret.
pub fn clean(state_dir: &Path) {
    let _ = std::fs::remove_dir_all(state_dir.join(CORES_DIR).join(RUN_DIR));
}

/// A peer as `easytier-cli -o json peer` lists it (the same in 2.6 and 2.7).
#[derive(Clone, Debug, Default, Deserialize)]
pub struct Peer {
    /// Its virtual address, without the prefix length.
    #[serde(default)]
    pub ipv4: String,
    #[serde(default)]
    pub hostname: String,
    /// "Local" for this node, "p2p" for a direct path, else a relay.
    #[serde(default)]
    pub cost: String,
    /// Latency in milliseconds, "-" when not known.
    #[serde(default)]
    pub lat_ms: String,
    /// The NAT type's name, as EasyTier's `NatType`.
    #[serde(default)]
    pub nat_type: String,
}

impl Peer {
    /// Whether the row is this node.
    pub fn local(&self) -> bool {
        self.cost == "Local"
    }

    /// Whether the path to it is direct.
    pub fn direct(&self) -> bool {
        self.cost == "p2p"
    }

    /// Its virtual address, if it has one.
    pub fn ip(&self) -> Option<Ipv4Addr> {
        self.ipv4.parse().ok()
    }

    /// Its latency in whole milliseconds (0 when not known).
    pub fn latency(&self) -> i32 {
        self.lat_ms.parse::<f64>().map_or(0, |ms| ms.round() as i32)
    }
}

/// A running official core of one network.
pub struct External {
    child: tokio::sync::Mutex<tokio::process::Child>,
    cli: PathBuf,
    rpc: SocketAddr,
    socks: SocketAddr,
    /// The runtime file, removed once the core has read it.
    file: PathBuf,
}

impl External {
    /// Checks the configuration with the core's own `--check-config`, then
    /// starts the core on it and waits until its RPC portal answers.
    /// Inputs: `config` is the whole configuration, secret included
    /// (`merged`); `secret` is kept out of any error.
    /// Side effects: writes the runtime file (owner-only), removed again
    /// once the core answers or fails; starts a process without a window
    /// whose output goes nowhere (it prints its configuration).
    pub async fn start(
        state_dir: &Path,
        tag: &str,
        profile: &str,
        base: &str,
        person: &DocumentMut,
        secret: &str,
    ) -> Result<External, String> {
        let (core, cli) =
            programs(state_dir, tag).ok_or_else(|| format!("EasyTier {tag} is not downloaded"))?;
        let (socks, rpc) = (free_loopback()?, free_loopback()?);
        let config = merged(base, person, socks.port())?;
        let file = run_file(state_dir, profile);
        write_private(&file, &config).map_err(|e| format!("runtime file: {e}"))?;
        let started = async {
            let check = command(&core)
                .arg("--check-config")
                .arg("-c")
                .arg(&file)
                .stdout(Stdio::piped())
                .stderr(Stdio::piped())
                .output()
                .await
                .map_err(|e| format!("EasyTier {tag}: {e}"))?;
            if !check.status.success() {
                let said = String::from_utf8_lossy(&check.stderr).replace(secret, "…");
                let first = said
                    .lines()
                    .find(|l| !l.trim().is_empty())
                    .unwrap_or("")
                    .trim();
                return Err(format!("EasyTier refused the settings: {first}"));
            }
            let child = command(&core)
                .arg("-c")
                .arg(&file)
                .arg("--rpc-portal")
                .arg(rpc.to_string())
                .stdin(Stdio::null())
                .stdout(Stdio::null())
                .stderr(Stdio::null())
                .kill_on_drop(true)
                .spawn()
                .map_err(|e| format!("EasyTier {tag}: {e}"))?;
            let ext = External {
                child: tokio::sync::Mutex::new(child),
                cli,
                rpc,
                socks,
                file: file.clone(),
            };
            let deadline = tokio::time::Instant::now() + START_TIMEOUT;
            while ext.peers().await.is_none() {
                if !ext.alive().await {
                    return Err(format!("EasyTier {tag} stopped as it started"));
                }
                if tokio::time::Instant::now() > deadline {
                    ext.stop().await;
                    return Err(format!("EasyTier {tag} did not answer"));
                }
                tokio::time::sleep(Duration::from_millis(300)).await;
            }
            Ok(ext)
        }
        .await;
        let _ = std::fs::remove_file(&file);
        started
    }

    /// Whether the process still runs.
    pub async fn alive(&self) -> bool {
        matches!(self.child.lock().await.try_wait(), Ok(None))
    }

    /// The peers the core lists, this node among them; None when its RPC
    /// portal does not answer.
    pub async fn peers(&self) -> Option<Vec<Peer>> {
        let run = command(&self.cli)
            .arg("-p")
            .arg(self.rpc.to_string())
            .args(["-o", "json", "peer"])
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .kill_on_drop(true)
            .output();
        let out = tokio::time::timeout(CLI_TIMEOUT, run).await.ok()?.ok()?;
        if !out.status.success() || out.stdout.len() > CLI_MAX {
            return None;
        }
        serde_json::from_slice(&out.stdout).ok()
    }

    /// Opens a TCP connection to `ip:port` in the network through the core's
    /// SOCKS5 portal.
    pub async fn connect(&self, ip: Ipv4Addr, port: u16, timeout: Duration) -> Option<TcpStream> {
        tokio::time::timeout(timeout, socks5(self.socks, SocketAddrV4::new(ip, port)))
            .await
            .ok()?
            .ok()
    }

    /// Stops the core.
    pub async fn stop(&self) {
        let mut child = self.child.lock().await;
        let _ = child.kill().await;
        let _ = std::fs::remove_file(&self.file);
    }
}

/// A command for one of a core's programs, without a console window on
/// Windows.
fn command(program: &Path) -> tokio::process::Command {
    #[allow(unused_mut)]
    let mut cmd = tokio::process::Command::new(program);
    #[cfg(windows)]
    cmd.creation_flags(0x0800_0000); // CREATE_NO_WINDOW
    cmd
}

/// A loopback port free now, for a portal of the core.
fn free_loopback() -> Result<SocketAddr, String> {
    std::net::TcpListener::bind((Ipv4Addr::LOCALHOST, 0))
        .and_then(|l| l.local_addr())
        .map_err(|e| format!("no free port: {e}"))
}

/// Writes `text` to `file`, readable by this user only.
fn write_private(file: &Path, text: &str) -> std::io::Result<()> {
    if let Some(dir) = file.parent() {
        std::fs::create_dir_all(dir)?;
    }
    let mut opts = std::fs::OpenOptions::new();
    opts.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        opts.mode(0o600);
    }
    use std::io::Write;
    opts.open(file)?.write_all(text.as_bytes())
}

/// A SOCKS5 CONNECT (no authentication) to `to` through the portal at
/// `portal`.
async fn socks5(portal: SocketAddr, to: SocketAddrV4) -> std::io::Result<TcpStream> {
    let refused =
        |what: &str| std::io::Error::new(std::io::ErrorKind::ConnectionRefused, what.to_owned());
    let mut s = TcpStream::connect(portal).await?;
    s.set_nodelay(true)?;
    s.write_all(&[5, 1, 0]).await?;
    let mut hello = [0u8; 2];
    s.read_exact(&mut hello).await?;
    if hello != [5, 0] {
        return Err(refused("the SOCKS5 portal wants authentication"));
    }
    let mut req = vec![5, 1, 0, 1];
    req.extend_from_slice(&to.ip().octets());
    req.extend_from_slice(&to.port().to_be_bytes());
    s.write_all(&req).await?;
    let mut head = [0u8; 4];
    s.read_exact(&mut head).await?;
    if head[1] != 0 {
        return Err(refused("the device did not answer through EasyTier"));
    }
    // The bound address that follows: IPv4, a name or IPv6, then the port.
    let rest = match head[3] {
        1 => 4,
        4 => 16,
        3 => s.read_u8().await? as usize,
        _ => return Err(refused("a bad SOCKS5 reply")),
    } + 2;
    let mut skip = vec![0u8; rest];
    s.read_exact(&mut skip).await?;
    Ok(s)
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Kyokai's own keys are refused wherever they sit; the person's other
    /// keys are listed, tables key by key.
    #[test]
    fn owned_keys_are_found() {
        let doc = parse(
            "mtu = 1300\nhostname = \"x\"\n[flags]\nno_tun = false\nenable_kcp_proxy = true\n[[peer]]\nuri = \"tcp://a:1\"\n[network_identity]\nnetwork_secret = \"s\"\n",
        )
        .unwrap();
        assert_eq!(
            keys(&doc),
            [
                "mtu",
                "hostname",
                "flags.no_tun",
                "flags.enable_kcp_proxy",
                "peer",
                "network_identity.network_secret"
            ]
        );
        assert_eq!(
            owned_in(&doc),
            [
                "hostname",
                "flags.no_tun",
                "peer",
                "network_identity.network_secret"
            ]
        );
        assert!(keys(&parse("# only a comment\n").unwrap()).is_empty());
    }

    /// The person's options go over Kyokai's, tables key by key, and the
    /// SOCKS5 portal is Kyokai's.
    #[test]
    fn options_merge_over_kyokai_config() {
        let base = "hostname = \"Kyokai-1\"\nlisteners = []\n[flags]\nno_tun = true\nlatency_first = false\n";
        let person =
            parse("listeners = [\"tcp://0.0.0.0:11010\"]\n[flags]\nlatency_first = true\n")
                .unwrap();
        let out = parse(&merged(base, &person, 4242).unwrap()).unwrap();
        assert_eq!(out["hostname"].as_str(), Some("Kyokai-1"));
        assert_eq!(out["listeners"][0].as_str(), Some("tcp://0.0.0.0:11010"));
        assert_eq!(out["flags"]["no_tun"].as_bool(), Some(true));
        assert_eq!(out["flags"]["latency_first"].as_bool(), Some(true));
        assert_eq!(
            out["socks5_proxy"].as_str(),
            Some("socks5://127.0.0.1:4242")
        );
    }

    /// A tag names a folder of the cores, never a path.
    #[test]
    fn tags_are_folders() {
        let dir = std::env::temp_dir();
        for tag in ["", "..", "../x", "a/b", "run", "v2.7.0\\x"] {
            assert!(programs(&dir, tag).is_none(), "{tag}");
        }
    }

    /// The CLI's peer rows read the same from 2.6.4 and 2.7.0.
    #[test]
    fn peer_rows_parse() {
        let rows: Vec<Peer> = serde_json::from_str(
            r#"[{"cidr":"10.77.0.2/24","ipv4":"10.77.0.2","hostname":"Kyokai-2","cost":"Local","lat_ms":"-","nat_type":"Unknown","id":"1","version":"2.7.0"},
                {"cidr":"10.77.0.1/24","ipv4":"10.77.0.1","hostname":"Kyokai-1","cost":"p2p","lat_ms":"0.85","loss_rate":"0.0%","nat_type":"Symmetric","id":"2","version":"2.6.4"},
                {"ipv4":"10.77.0.3","hostname":"Kyokai-3","cost":"relay(2)","lat_ms":"41.6","nat_type":"FullCone"}]"#,
        )
        .unwrap();
        assert!(rows[0].local() && !rows[0].direct());
        assert_eq!(
            (rows[1].ip(), rows[1].direct(), rows[1].latency()),
            (Some(Ipv4Addr::new(10, 77, 0, 1)), true, 1)
        );
        assert_eq!((rows[2].direct(), rows[2].latency()), (false, 42));
    }
}

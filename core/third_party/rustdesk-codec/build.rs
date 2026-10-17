// Kyokai's build script for the vendored RustDesk codecs (AGPL-3.0, like
// RustDesk). It replaces scrap's build.rs: the bindings are generated ahead
// of time (generated/, see doc/en-us/development.md), so this only links
// the static libvpx and libaom that scripts/codecs.sh builds with vcpkg.

use std::env;
use std::path::PathBuf;

/// The vcpkg triplet for the target being built.
fn triplet() -> &'static str {
    let os = env::var("CARGO_CFG_TARGET_OS").unwrap_or_default();
    let arch = env::var("CARGO_CFG_TARGET_ARCH").unwrap_or_default();
    match (os.as_str(), arch.as_str()) {
        ("windows", "aarch64") => "arm64-windows-static",
        ("windows", "x86_64") => "x64-windows-static",
        ("macos", "aarch64") => "arm64-osx",
        ("macos", "x86_64") => "x64-osx",
        _ => panic!("no codec libraries for {arch}-{os}: Kyokai supports Windows and macOS"),
    }
}

/// Links the codec libraries from `KYOKAI_CODECS_DIR` (a vcpkg triplet's
/// install tree), by default `core/vcpkg_installed/<triplet>/<triplet>`.
fn main() {
    println!("cargo:rerun-if-env-changed=KYOKAI_CODECS_DIR");
    let triplet = triplet();
    let dir = match env::var_os("KYOKAI_CODECS_DIR") {
        Some(d) => PathBuf::from(d),
        None => PathBuf::from(env::var("CARGO_MANIFEST_DIR").unwrap())
            .join("../../vcpkg_installed")
            .join(triplet)
            .join(triplet),
    };
    let lib = dir.join("lib");
    let windows = env::var("CARGO_CFG_TARGET_OS").as_deref() == Ok("windows");
    for name in ["vpx", "aom"] {
        let file = if windows {
            lib.join(format!("{name}.lib"))
        } else {
            lib.join(format!("lib{name}.a"))
        };
        if !file.exists() {
            panic!(
                "{} is missing: run scripts/codecs.sh first (doc/en-us/development.md)",
                file.display()
            );
        }
        println!("cargo:rerun-if-changed={}", file.display());
        println!("cargo:rustc-link-lib=static={name}");
    }
    println!("cargo:rustc-link-search=native={}", lib.display());
}

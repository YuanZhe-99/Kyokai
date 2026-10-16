//! The Kyokai core: the native library that the GUI and the engine load with
//! purego (doc/en-us/architecture.md, "FFI").

/// Version of the C ABI. Hosts compare it with their own before any other
/// call; changing an export's signature or meaning bumps it.
pub const ABI_VERSION: u32 = 1;

/// Returns the C ABI version, the first call every host makes.
#[unsafe(no_mangle)]
pub extern "C" fn kyokai_abi_version() -> u32 {
    ABI_VERSION
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn abi_version_is_exported_unchanged() {
        assert_eq!(kyokai_abi_version(), ABI_VERSION);
    }
}

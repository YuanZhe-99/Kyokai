//! A no-op `thunk-rs`: EasyTier's build script calls `thunk::thunk()` on
//! x86/x64 Windows, and Kyokai keeps its static C runtime instead.

/// Does nothing: no download, no relink against msvcrt.dll.
pub fn thunk() {}

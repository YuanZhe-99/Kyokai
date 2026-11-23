# Native module androidhost

androidhost is a native module of MyGo: Go interfaces implemented in Swift, Kotlin,
Rust or Zig. Read https://mygo.dev/docs/native-modules (docs/native-modules.md)
before changing it.

- `androidhost.go` declares the interfaces, the types they use and the errors
  (`native.NewError`): it is the contract. `//mygo:native` marks an entry point,
  which gets a constructor, `NewT`.
- `androidhost_native.go` and each language's generated file
  (`swift/Sources/Androidhost/Androidhost.mygo.swift`, `kotlin/src/main/kotlin/.../Androidhost.mygo.kt`,
  `rust/src/mygo.rs`, `zig/src/mygo.zig`) are generated: never edit them; `mygo dev`
  regenerates them when the interfaces change.
- `swift/` is a Swift package, which macOS and iOS use when its
  `Package.swift` names them, for Apple's frameworks: a factory `makeT()`
  per entry point and classes conforming to the generated protocols. Add
  Swift packages and frameworks in `swift/Package.swift`.
- `kotlin/` is an Android library, which Android uses: a function
  `makeT()` per entry point and classes implementing the generated
  interfaces. Add dependencies in `kotlin/build.gradle.kts`.
- `rust/` is a Rust crate and `zig/` a Zig package, for every platform: a
  factory per entry point (`make_t` in Rust, `makeT` in Zig) and types
  implementing the generated traits or declarations.

Check the module without opening the app:

```sh
mygo native check            # generate, build and vet: every problem as file:line
mygo native call Greeter.Greet '{"name": "Ada"}'   # call a method, print its result
```

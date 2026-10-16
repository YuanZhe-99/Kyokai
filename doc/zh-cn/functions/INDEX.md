# 函数索引

每个 Go 包与 Rust crate 一行：职责，以及应该先读的声明。声明在文档注释中带有函数说明层（第一句写用途；非平凡的声明再写
输入、返回、副作用与注意事项）。

| 包 / crate | 职责 | 从这里读起 |
|---|---|---|
| `core/kyokai-core` | 原生核心库及其 C ABI | `kyokai_abi_version`、`ABI_VERSION` |
| `internal/version` | 产品版本 | `Version`、`Older` |
| `internal/fsutil` | 原子写入与 JSON 文档 | `WriteFileAtomic`、`WriteJSON`、`ReadJSON` |
| `tools/deps` | 上游版本固定 | `check`、`update` |
| `tools/checkdocs` | 文档镜像检查 | `main` |
| `tools/sign` | 特权助手 bundle 签名密钥 | `keygen` |

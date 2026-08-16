# Third-Party Notices and References

Anti Gravity NodeGuard is licensed under MIT. Its Go dependencies are:

| Dependency | License | Purpose |
| --- | --- | --- |
| [gorilla/websocket](https://github.com/gorilla/websocket) | BSD-2-Clause | Local CDP WebSocket transport |
| [go-yaml/yaml](https://github.com/yaml/go-yaml) | MIT and Apache-2.0 (file-specific) | YAML parsing and serialization |

The following projects and documentation informed independent compatibility behavior:

- [Clash Verge Rev](https://github.com/clash-verge-rev/clash-verge-rev), GPL-3.0: public Mihomo API behavior, green delay threshold, and profile validation/reload transaction boundaries were studied. No source code was copied into NodeGuard.
- [MetaCubeXD](https://github.com/MetaCubeX/metacubexd), MIT: selector and batch-delay interaction patterns.
- [Mihomo documentation](https://wiki.metacubex.one/): configuration and controller behavior.
- [Antigravity-Proxy Agent IP diagnosis](https://github.com/yuaotian/antigravity-proxy/blob/main/docs/agent-ip-diagnosis.md): diagnostic categories only; no code or prose was copied.

Google, Anti Gravity, Clash Verge Rev, Mihomo, MetaCubeXD, and the referenced projects retain all rights to their respective names, code, and documentation. Their mention does not imply endorsement.

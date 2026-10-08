# Changelog

## [3.10.0](https://github.com/gritzapp/gritz/compare/v3.9.4...v3.10.0) (2026-10-08)


### Features

* **driver:** add gritz driver --serve ([ae72f48](https://github.com/gritzapp/gritz/commit/ae72f48daa12ed61aafb35152d1e0ba763e8a2d1))
* **proto:** add driver.v1 service, first cut ([e70ba70](https://github.com/gritzapp/gritz/commit/e70ba700be7a45293384ce922c6cfa9a03efa209))
* **runrecord:** add the driver's run record ([4751326](https://github.com/gritzapp/gritz/commit/47513266a19a56d72515bea8e4e30e2bf5bf4897))


### Bug Fixes

* **webui:** always start the task sidebar collapsed on small screens ([c6aeb22](https://github.com/gritzapp/gritz/commit/c6aeb22ecee473ea061c3aedae6b87704b4ef12a))
* **webui:** show nav items as icons on mobile ([cfd31b2](https://github.com/gritzapp/gritz/commit/cfd31b21be8dc6c03e379ad15b645364d022e8a1))


### Miscellaneous

* add driver server proposal diagram and split diagrams into projects ([5c0fd44](https://github.com/gritzapp/gritz/commit/5c0fd44486ece4c9da3c079db99bbc225202234f))
* add motion canvas diagram of the lambda microvm start flow ([1108adf](https://github.com/gritzapp/gritz/commit/1108adffc5dae56e13a80484f3d539f969ec162c))
* add motion canvas diagram of the task creation flow ([86c7c59](https://github.com/gritzapp/gritz/commit/86c7c59f7d9281efafb1c3bd3ae73d91fca9b665))
* add motioncanvas mise task and skill ([3c44195](https://github.com/gritzapp/gritz/commit/3c44195fea26e9992cef89c2e62d59d462091f3c))
* **agent:** build dummy MCP server envs with envx.FromMap ([03547b7](https://github.com/gritzapp/gritz/commit/03547b770b8730c24bdbe76283db5dd53cba9781))
* **agent:** build run envs inline with slices.Concat ([17f7bba](https://github.com/gritzapp/gritz/commit/17f7bbaaff5f815f217435c8736ce901faf88369))
* **agent:** describe Driver.Env without naming its callers ([2f8f148](https://github.com/gritzapp/gritz/commit/2f8f14811a46164f1a1020945ae79547865b91e8))
* **agent:** drop the agent env tests in favor of envx.TestExpand ([4c0b28b](https://github.com/gritzapp/gritz/commit/4c0b28b36a56427dcfa45450977c775f65ec42dd))
* **agent:** drop the dummy MCP env comment ([b988507](https://github.com/gritzapp/gritz/commit/b98850753b9d59983ce269101581e7a989c87907))
* **agent:** drop the Options.Env comment ([cd68b4e](https://github.com/gritzapp/gritz/commit/cd68b4ed4842ff0f74567bd401dd498e3bc9adb7))
* **agent:** layer the run environment over the driver's ([a776c7a](https://github.com/gritzapp/gritz/commit/a776c7a587aac181f5f586584aec05faefd2d41a))
* **agent:** make the run env the complete child environment ([884ed9e](https://github.com/gritzapp/gritz/commit/884ed9e1bb65e3a171a690a2f7832a63b123dc01))
* **agent:** move env expansion into x/envx as Expand ([e3e4080](https://github.com/gritzapp/gritz/commit/e3e408060bf08941848ce0619ceb9fc807bd9f55))
* **agent:** thread a per-run environment through the driver ([5b789dd](https://github.com/gritzapp/gritz/commit/5b789ddd294d5f8cd07a33ad71010c2d1813ac7f))
* authenticate the runner to the driver with an optional public key ([3f40df8](https://github.com/gritzapp/gritz/commit/3f40df874cb001c0be51f64348e118894aa257af))
* **driver:** move serve and signal handling out of the command ([10106bb](https://github.com/gritzapp/gritz/commit/10106bb75b9a5f3c5db48d6ea7f2ff9a44d95105))
* phase the driver server rollout behind an experimental Docker backend ([d137351](https://github.com/gritzapp/gritz/commit/d1373513cfa107b64d2e040751b686f4993d41e3))
* point the orchestrator skill at the gritzapp org board ([5486a8e](https://github.com/gritzapp/gritz/commit/5486a8e0846ccff8db8d424a2ceaa3cb9f5222de))
* replace the driver outbox with a response log read from a runner offset ([ef878d5](https://github.com/gritzapp/gritz/commit/ef878d5e2273ad66881cd1d9769f0a95d9bc0f7c))
* route driver reports through the runner in the driver server proposal ([ebc2a05](https://github.com/gritzapp/gritz/commit/ebc2a05ebc3a76e9b0517b21647bbdae1ef223e5))

## [3.9.4](https://github.com/gritzapp/gritz/compare/v3.9.3...v3.9.4) (2026-10-04)


### Miscellaneous

* drop gritz- prefix from published image names ([ff815d4](https://github.com/gritzapp/gritz/commit/ff815d4aa4d6846bc7a7099b51fc5e0afc8898fb))
* hardcode published image names ([99ef2d6](https://github.com/gritzapp/gritz/commit/99ef2d60335cec96fd4a6044201b1afa063f52eb))
* move examples and workspace images to gritzapp/workspaces ([47f2e25](https://github.com/gritzapp/gritz/commit/47f2e25c64314681f22dc5e0fe20d69f88a82a68))
* move github app manifest to gritzapp/infra ([b2e3802](https://github.com/gritzapp/gritz/commit/b2e3802cd7e8d8d1cb16f166391688a6e7d669a2))

## [3.9.3](https://github.com/gritzapp/gritz/compare/v3.9.2...v3.9.3) (2026-10-04)


### Bug Fixes

* **webui:** align timeline rail markers with card icons ([f822644](https://github.com/gritzapp/gritz/commit/f822644894dd23c3c6cd34863bc4190f3d61e6ca)), closes [#1649](https://github.com/gritzapp/gritz/issues/1649)


### Miscellaneous

* move deployment config and secrets to gritzapp/infra ([fceaa44](https://github.com/gritzapp/gritz/commit/fceaa44bad82078a54b9fbac68704103e4c150fd))
* move repo references to gritzapp/gritz ([f1b702a](https://github.com/gritzapp/gritz/commit/f1b702a8f4d67d3c17f4f998aef79d87a84a0990))
* **secrets:** drop stale fly-era values from sops.env.yml ([ae5ba27](https://github.com/gritzapp/gritz/commit/ae5ba2763328fcbf84bdd68e80cc8245fa8d4d3c))
* **webui:** regenerate route tree ([a9aa5a5](https://github.com/gritzapp/gritz/commit/a9aa5a53defbceb6058c98a2d6520f88969d6680)), closes [#1655](https://github.com/gritzapp/gritz/issues/1655)

## [3.9.2](https://github.com/icholy/gritz/compare/v3.9.1...v3.9.2) (2026-10-04)


### Miscellaneous

* disable build cache for debian workspace image ([2e8dcd5](https://github.com/icholy/gritz/commit/2e8dcd590199482cbb6c0d9833c0a50d0a7c0def))
* **webui:** ignore tanstack router temp dir ([66bd141](https://github.com/icholy/gritz/commit/66bd1412af42c85a8da33cb7e76bd892b8a075e3))
* **webui:** regenerate route tree ([d487f5d](https://github.com/icholy/gritz/commit/d487f5d7a8abe686c12a56d446858ffca824e94f))

## [3.9.1](https://github.com/icholy/gritz/compare/v3.9.0...v3.9.1) (2026-10-04)


### Miscellaneous

* **deps:** update dependency pnpm to v11.28.0 ([f7b4ffb](https://github.com/icholy/gritz/commit/f7b4ffb066cce5819cfd3987941bfc633f92947f))
* disable build cache for mise workspace image ([745154d](https://github.com/icholy/gritz/commit/745154d2889e17ff913a1b0e198b940b962e2491))

## [3.9.0](https://github.com/icholy/gritz/compare/v3.8.0...v3.9.0) (2026-10-03)


### Features

* **webui:** drop status transition from sandbox exited timeline entry ([17c8754](https://github.com/icholy/gritz/commit/17c8754cdca19fd6f3c3e4ae7b4c691274c129e3))
* **webui:** move timeline card icons into card headers ([f9877fb](https://github.com/icholy/gritz/commit/f9877fb45d3deb4a81ab60adb15b0a1852b9086e))


### Bug Fixes

* **driver:** interrupt setup commands on cancellation ([3eda9e4](https://github.com/icholy/gritz/commit/3eda9e449709f7518daf79dad7d55cf92ca6b18c))
* **shell:** spawn non-login shell so image PATH is preserved ([089c6ac](https://github.com/icholy/gritz/commit/089c6ac2b8af4acfe3534367cd2d3020bf568b64))


### Miscellaneous

* **deps:** update dependency @bufbuild/buf to v1.73.0 ([4700d4b](https://github.com/icholy/gritz/commit/4700d4be1fa809f1cfebb2427c554f6371592961))
* **deps:** update dependency @bufbuild/protobuf to v2.15.0 ([24e5211](https://github.com/icholy/gritz/commit/24e521120aa58d6230c1a8932a60a9b25057c149))
* **deps:** update dependency @bufbuild/protoc-gen-es to v2.15.0 ([a967442](https://github.com/icholy/gritz/commit/a96744290263d254d91d117a1c5b4318d6b6b00c))
* **deps:** update dependency @types/node to v24.19.0 ([a1c3f62](https://github.com/icholy/gritz/commit/a1c3f621d5ae3b17cf6d4f35362e32c67c2c58c8))
* **deps:** update dependency prettier to v3.9.8 ([4fc47a1](https://github.com/icholy/gritz/commit/4fc47a178f03dbfdbc7b9b2a1423f567715817ad))
* **deps:** update dependency sops to v3.13.3 ([3af82bd](https://github.com/icholy/gritz/commit/3af82bd936ad9481147ae058abed40602fc8474a))
* **deps:** update tailwindcss monorepo to v4.3.3 ([863af03](https://github.com/icholy/gritz/commit/863af03fa76d43fe7e1df06f9831fa782b09dbec))
* **deps:** update tanstack-router monorepo ([42b1622](https://github.com/icholy/gritz/commit/42b1622fec849da27678c19473077e16ff96b0cf))
* reorganize internal packages under runner, server, and x ([ea7ad7a](https://github.com/icholy/gritz/commit/ea7ad7a981ba69e225363a2f4437336f3c345e77))

## [3.8.0](https://github.com/icholy/gritz/compare/v3.7.0...v3.8.0) (2026-09-29)


### Features

* **auth:** refresh expired cookie sessions with the refresh token ([c75e726](https://github.com/icholy/gritz/commit/c75e7265082669217a55ad6efc94ca8d4cbd4490))


### Miscellaneous

* **deps:** update dependency age to v1.3.2 ([f8001d6](https://github.com/icholy/gritz/commit/f8001d6ff05b6b1dbf918cda09b9360dc70becad))
* **deps:** update dependency eslint-plugin-react-refresh to v0.5.7 ([e5df6a2](https://github.com/icholy/gritz/commit/e5df6a26a170855f92fe1498028624760d9a8bbb))

## [3.7.0](https://github.com/icholy/gritz/compare/v3.6.0...v3.7.0) (2026-09-29)


### Features

* add a draft task status ([1e6b81e](https://github.com/icholy/gritz/commit/1e6b81e31bd4f0a6f0f660bbb3651935b0d05d2f))
* **webui:** create a draft task from the new task page ([18851a8](https://github.com/icholy/gritz/commit/18851a8a09673c03a35c3e523fc0393f288c00bd))


### Miscellaneous

* **deps:** update dependency pnpm to v11.27.1 ([540d166](https://github.com/icholy/gritz/commit/540d16607089921f278afb86b19bded8b4be79b6))
* **deps:** update radix-ui-primitives monorepo ([5b36f2e](https://github.com/icholy/gritz/commit/5b36f2e48209cb9d4c7aac752fe39de0c048a75c))

## [3.6.0](https://github.com/icholy/gritz/compare/v3.5.1...v3.6.0) (2026-09-29)


### Features

* **server:** add a draft flag to CreateTask ([c08eb63](https://github.com/icholy/gritz/commit/c08eb637c532f8da80a9384709694d0c50b2a4b8))


### Bug Fixes

* **deps:** update module github.com/zitadel/zitadel-go/v3 to v3.30.0 ([0aa90cf](https://github.com/icholy/gritz/commit/0aa90cfb104ce8a71da90110f396748d339adc8e))


### Miscellaneous

* **deps:** update dependency @types/node to v24.13.6 ([44e68b4](https://github.com/icholy/gritz/commit/44e68b485086800cb096c710dbea9239ee826f54))
* **deps:** update dependency vitest to v4.1.11 [security] ([7635b11](https://github.com/icholy/gritz/commit/7635b116328d5b8509d7bfdd7f2b3025bf7ddbd4))

## [3.5.1](https://github.com/icholy/gritz/compare/v3.5.0...v3.5.1) (2026-09-25)


### Bug Fixes

* **webui:** invalidate the edited schedule on save ([227d5c0](https://github.com/icholy/gritz/commit/227d5c0a50eda59f7261c9a40f76ae4ae1cfb7b3))
* **webui:** refresh schedules after an edit ([5af9d8b](https://github.com/icholy/gritz/commit/5af9d8bc1a429066a6d9c1a929d51ab3bfafe58f))
* **webui:** render the default namespace in the normal text color ([c04a00a](https://github.com/icholy/gritz/commit/c04a00aa2721762584cc0e99f9af8b1465a8067d)), closes [#1612](https://github.com/icholy/gritz/issues/1612)


### Miscellaneous

* **webui:** drop the schedule SSE invalidation test ([b2b550e](https://github.com/icholy/gritz/commit/b2b550eb6c25329abc044aa47973575f6775d696))

## [3.5.0](https://github.com/icholy/gritz/compare/v3.4.0...v3.5.0) (2026-09-22)


### Features

* **dev:** add example secret to dummy workspace ([80ab461](https://github.com/icholy/gritz/commit/80ab461bea658618dc38e6159aa509474e5eb4d3))
* **webui:** show the task namespace alongside runner and workspace ([092bf9f](https://github.com/icholy/gritz/commit/092bf9f1f887a747c2bccc86f3b6630748755c0b))


### Miscellaneous

* **logship:** hold the in-flight request instead of a chunk struct ([119081d](https://github.com/icholy/gritz/commit/119081d016a13d0fff106438ec5a2a92e13bbcff))

## [3.4.0](https://github.com/icholy/gritz/compare/v3.3.0...v3.4.0) (2026-09-19)


### Features

* **webui:** replace the icon with the new gritz logo ([66a3a2f](https://github.com/icholy/gritz/commit/66a3a2fdeeb21273b602edb92f3b466c66599d0b))

## [3.3.0](https://github.com/icholy/gritz/compare/v3.2.0...v3.3.0) (2026-09-19)


### Features

* **bytetrie:** add a byte-keyed trie with longest-prefix lookup ([459e508](https://github.com/icholy/gritz/commit/459e508cca989506e2791d56a92adad6429b4aa0))
* **driver:** mask declared secrets in the shipped log ([542c08e](https://github.com/icholy/gritz/commit/542c08e6bd4e9c9417af968ffd27f2d6650d1fe7))
* **redact:** mask known secret values in a byte stream ([d942136](https://github.com/icholy/gritz/commit/d9421362c440cb4a10964c6b6f6ed71eaf3961e0))
* **redactv2:** add a trie-backed masking Writer ([91b846f](https://github.com/icholy/gritz/commit/91b846f70bb59f28f9c36956b41d6393e40ff79e))
* **webui:** render the logs tab on a dark surface ([5d93a3d](https://github.com/icholy/gritz/commit/5d93a3de230c48aa29357a9ea14033b6615b15b0))
* **workspace:** declare secrets in the workspace config ([abd7c3e](https://github.com/icholy/gritz/commit/abd7c3e3fab3af5241f7a6766fde884c54e5674e))


### Bug Fixes

* **command/logs:** only follow when the -f flag is set ([50e9c0a](https://github.com/icholy/gritz/commit/50e9c0a9ea2dddd2075d234356c7a9229be5daa0))
* **logship:** let only the tick cut a partial chunk ([f861860](https://github.com/icholy/gritz/commit/f861860b871c5e46c87ff1678019ea2c29e3cf7d))
* **redact:** order mask rules by descending value length ([3e160c5](https://github.com/icholy/gritz/commit/3e160c53d6dc12f604da517ba44af9eab9625075))


### Miscellaneous

* **bytetrie:** move package under internal/x ([dae335f](https://github.com/icholy/gritz/commit/dae335fb93f0a69f3eaa1e8af69e423833d86073))
* **command/logs:** inline the log loops ([d88bd7c](https://github.com/icholy/gritz/commit/d88bd7cd7026caa20bb008eea3fb752d665c0b70))
* **gritzclient:** add ShippedLog to the client mock ([09fc807](https://github.com/icholy/gritz/commit/09fc807f02381184fd6d6191bcfd7b12109d5b74))
* **logship:** construct the shipper from an Options struct ([b27157f](https://github.com/icholy/gritz/commit/b27157fcf6a7ff79559591cb441ef4b3feecdada)), closes [#1593](https://github.com/icholy/gritz/issues/1593)
* **logship:** mask secrets in the shipper, not at the tee ([8c1ed33](https://github.com/icholy/gritz/commit/8c1ed3385b767861abff86a81a5addf52204c60e))
* **logship:** mask through a redactv2.Writer ([59adb17](https://github.com/icholy/gritz/commit/59adb176a0eda693f8a2af73d0e091cf266d3328))
* **logship:** move chunk cutting into the drain ([d2287e5](https://github.com/icholy/gritz/commit/d2287e57bdb72e9f17398c586f1fbc066875ddae)), closes [#1592](https://github.com/icholy/gritz/issues/1592)
* **logship:** use x/sync semaphore for sendSem ([9da3f3e](https://github.com/icholy/gritz/commit/9da3f3ea58b0882cfcc60ae85053765e9fd921b0))
* mark secret-hygiene-for-shipped-driver-logs as implemented ([cb44d5a](https://github.com/icholy/gritz/commit/cb44d5a7d285c41eb10129a5391fece19e161ddb))
* **redact:** drop prefix rules, add a String entry point ([3b10bb7](https://github.com/icholy/gritz/commit/3b10bb7b217958f7c6c59e7dd1c964b8b2dedd23))
* **redact:** implement String with the Transformer ([0775039](https://github.com/icholy/gritz/commit/0775039c7d33bd5d77010698dbe2d8f11934527f))
* **redact:** retire the transformer chain, rename redactv2 ([bc6e972](https://github.com/icholy/gritz/commit/bc6e9721457bdebd7503902acadd16134d9ea58d))
* **secrets:** document the workspace secrets: map ([ae089b3](https://github.com/icholy/gritz/commit/ae089b309e6474a3d3465eef00bb1a6c87667e69))
* **secrets:** drop the example-config load test ([0ab76a8](https://github.com/icholy/gritz/commit/0ab76a83ffacbbc2a1dd57e0b1f1dd11e7989579))
* **secrets:** drop the explanatory comments from the examples ([1db5622](https://github.com/icholy/gritz/commit/1db56225149061e8efff48b2d0d11cd2c7d78dc9))
* **workspace:** drop secret validation and its tests ([c105463](https://github.com/icholy/gritz/commit/c105463217b0108ae1daddfe74208a92e7a1e806))
* **workspace:** trim secret validation to reserved names ([c49c2eb](https://github.com/icholy/gritz/commit/c49c2ebac487f0dbaf0b16b9d97825b1526dbec6))

## [3.2.0](https://github.com/icholy/gritz/compare/v3.1.0...v3.2.0) (2026-09-06)


### Features

* **agent:** add the driver log shipper ([4a22c70](https://github.com/icholy/gritz/commit/4a22c70d7276fbd404b23cb84161464cb36ae47b))
* **cli:** read gritz logs from the server ([0325f83](https://github.com/icholy/gritz/commit/0325f83385af55b637595671138710805821888c))
* **driver:** wire the log shipper into the driver ([4b9bdb6](https://github.com/icholy/gritz/commit/4b9bdb67a4a328cc32284d6fc1e6621381dba960))
* **server:** add log chunk append and list RPCs ([4de7da0](https://github.com/icholy/gritz/commit/4de7da0a0f0cdde1541e70cdf7cdf77076138fc9))
* **store:** add log chunk queries and store methods ([a8fc72b](https://github.com/icholy/gritz/commit/a8fc72bd8eb39a449726eb45443c0cca6d9a41bc))
* **store:** add log_chunks table migration ([f7833c9](https://github.com/icholy/gritz/commit/f7833c98f8de10cded69b65073ac708bdee0781b))
* **webui:** add a logs tab to the task page ([7abae8d](https://github.com/icholy/gritz/commit/7abae8d902e00720a8eb6ce08cfa81abed1ea948))


### Bug Fixes

* **webui:** keep tailing the logs tab after a task goes terminal ([6af920d](https://github.com/icholy/gritz/commit/6af920d17e344daa9cb28ff3633bc363e7704be1))


### Miscellaneous

* **agent:** move the log shipper into internal/logship ([00ef9b8](https://github.com/icholy/gritz/commit/00ef9b8d4f7e726f49a4a9af6ad60012b2d81670))
* correct the log chunk list response in the proposal ([18c854f](https://github.com/icholy/gritz/commit/18c854fc8fa74183da1d9755e0ddaafd683263c3))
* **gritzclient:** add a log chunk iterator ([b81d348](https://github.com/icholy/gritz/commit/b81d348c21772c849ea7127d325115b9ee8cf408))
* mark ship-driver-logs-to-server as implemented ([0e1e67d](https://github.com/icholy/gritz/commit/0e1e67deb682456bb110ffaa30a1c521a0d9a941))

## [3.1.0](https://github.com/icholy/gritz/compare/v3.0.1...v3.1.0) (2026-09-05)


### Features

* **cli:** support --version on the root command ([78f4678](https://github.com/icholy/gritz/commit/78f46783f0f4106e83cb40de0ac623fd94f3d291))


### Bug Fixes

* **runner:** make the restart handler idempotent ([6f058ac](https://github.com/icholy/gritz/commit/6f058ac50d78082834c4c7b6a720e0ce275b9728)), closes [#1520](https://github.com/icholy/gritz/issues/1520)


### Miscellaneous

* add an ssh task for the dokku host ([c2eab54](https://github.com/icholy/gritz/commit/c2eab548978d32a022e8bc164ff5defe8a43d93a))
* **taskstate:** use shared atomicio.WriteFile ([2c0bd69](https://github.com/icholy/gritz/commit/2c0bd69d78eb7674c9c59683f390092003cdd0f7))
* update stale xagent references to gritz ([12310d9](https://github.com/icholy/gritz/commit/12310d92c41694af27d184442171e3d96762f63f))
* update the web ui screenshot ([0b9a0a8](https://github.com/icholy/gritz/commit/0b9a0a87e1596592d8993b1be885f4a151cf9c50))

## [3.0.1](https://github.com/icholy/gritz/compare/v3.0.0...v3.0.1) (2026-08-30)


### Bug Fixes

* **otel:** drop peer attributes from otelconnect RPC metrics ([973906b](https://github.com/icholy/gritz/commit/973906b2e92c0b0c7c6d2145e08e23a05842a54a)), closes [#1530](https://github.com/icholy/gritz/issues/1530)
* **server:** keep idle SSE streams alive behind proxies ([0c17b8f](https://github.com/icholy/gritz/commit/0c17b8f186b5290de7bda6c34377932887faa887))


### Miscellaneous

* correct the release description now that fly is gone ([3b71ac3](https://github.com/icholy/gritz/commit/3b71ac355c840cbc4eb85e285a15ba8c2b23a9b2))
* **deps:** update dependency pnpm to v11.16.0 ([48acb6c](https://github.com/icholy/gritz/commit/48acb6c90a86f08a1236e0800907de7d8e34dba2))
* **deps:** update radix-ui-primitives monorepo ([a6725ca](https://github.com/icholy/gritz/commit/a6725ca4feca6c7c8373810aaa02b3782b90d019))
* **gritzclient:** capture the keep-alive log through io.Pipe ([011bc03](https://github.com/icholy/gritz/commit/011bc03223513a5852f308ba10dbd9e350080c0d))
* **gritzclient:** drop the keep-alive test ([db4de97](https://github.com/icholy/gritz/commit/db4de97848c5273b444682e613acd680473379b5))
* **otel:** inline the otelconnect metric view ([20ae356](https://github.com/icholy/gritz/commit/20ae3564ee6b57158b580d5211a21c3c54737be0))
* remove fly.io and point the tooling at the dokku host ([da5340e](https://github.com/icholy/gritz/commit/da5340e8313070d2a8c89731d9666d91914d95c8))
* restore the deploy workflow, targeting the dokku host ([b93fb57](https://github.com/icholy/gritz/commit/b93fb5798362f659e25cb2cb9b43940c3aecd035))
* **server:** drop the keep-alive payload ([663793f](https://github.com/icholy/gritz/commit/663793ffad160fcc842ecea0c1f6f5508307b3b5))
* **server:** hard-code the SSE keep-alive interval ([aec4ccb](https://github.com/icholy/gritz/commit/aec4ccbc2fee3b1261e5304f86374929921ff0f8))
* **server:** rename the SSE keep-alive event to keepalive ([5d2b06c](https://github.com/icholy/gritz/commit/5d2b06c33c82890a057492cb24ab949533344728))
* **server:** send the SSE keep-alive as a real event ([53be81f](https://github.com/icholy/gritz/commit/53be81f8199b5da0f0f9ae4283c8bd0ea30e8fb5))
* **server:** use cmp.Or for the SSE keep-alive default ([4d7db27](https://github.com/icholy/gritz/commit/4d7db27e66326ce043d81c965a13c8863e0c8371))
* **sse:** restore the reader to its original form ([55103e1](https://github.com/icholy/gritz/commit/55103e1c2ca48644d0a3f1581be23dedd66d64a5))

## [3.0.0](https://github.com/icholy/gritz/compare/v2.18.0...v3.0.0) (2026-08-30)


### ⚠ BREAKING CHANGES

* The Go module path (github.com/icholy/gritz), protobuf package (gritz.v1), environment variable prefix (GRITZ_), config directory (~/.config/gritz), MCP server name, container name prefix (gritz-<task-id>), Docker image names, Fly app name, and public domain (gritz.dev) all change. Operational migration steps are required; see the PR description.

### Bug Fixes

* **deploy:** set HOME and make the bootstrap script resumable ([f006efc](https://github.com/icholy/gritz/commit/f006efcbacbf6d4fd8a3038901a5b59d789a8627))


### Miscellaneous

* **deploy:** add cloud-init for a dokku host on digitalocean ([ad5430b](https://github.com/icholy/gritz/commit/ad5430b7a766d6352b15a20d9e04fafce561f562))
* **deploy:** fill in the data migration from the live fly setup ([def8e95](https://github.com/icholy/gritz/commit/def8e953a111b13e6b1a5af96d69fc5e060f8b94))
* **deploy:** fix the deploy/import ordering and image requirement ([5665796](https://github.com/icholy/gritz/commit/5665796ba72ebf6503ffd5de5c83089f5bf2d526))
* **deps:** update dependency flyctl to v0.4.72 ([892d0b4](https://github.com/icholy/gritz/commit/892d0b46be3f5f1e393635da5f2a221187ae068c))
* **deps:** update dependency pnpm to v11.15.0 ([17a6096](https://github.com/icholy/gritz/commit/17a6096be27ee2d1e2db1987d3efc56e63462699))
* **deps:** update dependency pnpm to v11.15.1 ([916059a](https://github.com/icholy/gritz/commit/916059a7aead35b94369a7bca167eecd82c97877))
* **deps:** update radix-ui-primitives monorepo ([113c5f6](https://github.com/icholy/gritz/commit/113c5f67717e5ce7e563a14770a31edbf0129874))
* fix stale proposals/accepted links to proposals/implemented ([5421683](https://github.com/icholy/gritz/commit/542168381165ca53de4ceb46875e6f6ba3d4cf71))
* move implemented proposals from accepted to implemented ([d92beda](https://github.com/icholy/gritz/commit/d92bedaf74283c94f93ce89ca5189a7671e35477))
* remove the fly.io deployment workflow ([13fd05b](https://github.com/icholy/gritz/commit/13fd05b18dba0862f04d6feda1bd0f79d4a6f80e))
* rename project from xagent to gritz ([df210b4](https://github.com/icholy/gritz/commit/df210b4e39787b11ff5ec9bcadf2f29bc77bc15d))
* **secrets:** restore decryptability of sops.env.yml ([aa76b7d](https://github.com/icholy/gritz/commit/aa76b7da943b14f88b2741ec9f772c8cd8695148))
* **webui:** fix prettier formatting after rename ([3188be8](https://github.com/icholy/gritz/commit/3188be8392cdfc0226c404cceb6013997fc65202))

## [2.18.0](https://github.com/icholy/gritz/compare/v2.17.0...v2.18.0) (2026-07-25)


### Features

* **webui:** add task quick-switch menu ([079b66f](https://github.com/icholy/gritz/commit/079b66f90b44bd27d0093c3e93f77cfb536a6b48)), closes [#1510](https://github.com/icholy/gritz/issues/1510)


### Bug Fixes

* **deps:** update aws-sdk-go-v2 monorepo ([e29d5ea](https://github.com/icholy/gritz/commit/e29d5eab33166019fa019e4618ab9dbabd3113c2))
* **store:** stop schedule tests leaking rows into the due set ([e9cc543](https://github.com/icholy/gritz/commit/e9cc543bad7f66a2c7216398809fa1ce107799f5))


### Miscellaneous

* **deps:** update dependency @types/node to v24.13.3 ([c338133](https://github.com/icholy/gritz/commit/c338133f2684fccdb5e9f561d026319c17cc659a))
* **deps:** update dependency flyctl to v0.4.70 ([1c9e378](https://github.com/icholy/gritz/commit/1c9e3782d9fafb7218790ac69a86db577fcda596))
* **deps:** update dependency flyctl to v0.4.71 ([2729fc4](https://github.com/icholy/gritz/commit/2729fc436b4200aaa0e401d935124cd75ddc411b))
* **deps:** update dependency go to v1.26.5 ([478f7b2](https://github.com/icholy/gritz/commit/478f7b27d48843ac623ac22c7133bd01857302a8))
* **deps:** update dependency pnpm to v11.13.1 ([3109b96](https://github.com/icholy/gritz/commit/3109b96f62e59b59a8d379da6a1c198d3ac601fb))
* **deps:** update dependency vite to v8.1.5 ([033e36f](https://github.com/icholy/gritz/commit/033e36f9683824eb74c8f84ab51c8897225f7980))
* **deps:** update dependency vitest to v4.1.10 ([b7e39ef](https://github.com/icholy/gritz/commit/b7e39efba878b198dd083eb912ced142cc516e42))
* **deps:** update radix-ui-primitives monorepo ([d9a24ce](https://github.com/icholy/gritz/commit/d9a24ce705034300a9ccf916ea2fcc28bb233052))

## [2.17.0](https://github.com/icholy/gritz/compare/v2.16.0...v2.17.0) (2026-07-19)


### Features

* **schedule:** add RunSchedule RPC for manual one-off runs ([8a841ee](https://github.com/icholy/gritz/commit/8a841ee26accf0645e9ff9dda5511a8ac2670f72))
* **webui:** add Run now button to schedules list ([92b4dee](https://github.com/icholy/gritz/commit/92b4dee856e3a8d98e6562c976139d41811d3cce))


### Bug Fixes

* install tzdata in server image so non-UTC schedule timezones resolve ([327441a](https://github.com/icholy/gritz/commit/327441a8810a537f3b5c90b48356cc1d7583c7b0)), closes [#1480](https://github.com/icholy/gritz/issues/1480)


### Miscellaneous

* **schedule:** compare fired events with cmpx.OnlyFields ([b670e9f](https://github.com/icholy/gritz/commit/b670e9fb8a09b4ec27e508d35c3ac1dec05a9a54))
* **scheduler:** extract shared Fire from Scheduler.fire ([2c534fb](https://github.com/icholy/gritz/commit/2c534fbacca7f7de6c33b922220994d3fd3f8d55))
* **scheduler:** inline firedAt with new(now.UTC()) ([c2ca157](https://github.com/icholy/gritz/commit/c2ca15730c53d48108143b7bd17461f31b232192))
* **webui:** promote task sidebar actions to top-level ([fcede8b](https://github.com/icholy/gritz/commit/fcede8bc763aee3bbaed60195bcd5399642588fc))

## [2.16.0](https://github.com/icholy/gritz/compare/v2.15.0...v2.16.0) (2026-07-18)


### Features

* **webui:** add schedules list and create/edit views ([57cda24](https://github.com/icholy/gritz/commit/57cda2402b22aa72f9ef53bb10cf7e00a055660c))
* **webui:** remove cron preset buttons from the schedule form ([3df1828](https://github.com/icholy/gritz/commit/3df1828ada30b83684a3be206802edfe85248da3))
* **webui:** split the events page into full-width tabs ([3bc50f7](https://github.com/icholy/gritz/commit/3bc50f7c23941d1f7254b244a96e8ee0f3ab4e8e))


### Bug Fixes

* **server:** only flag instruction events as wakes when the task actually starts ([7a8f66c](https://github.com/icholy/gritz/commit/7a8f66c647f0c28d0aa3540692512a302e9f4acf))
* **webui:** make top nav span the full viewport width ([fcdf3ad](https://github.com/icholy/gritz/commit/fcdf3adc2338f2f99f55d4827e6be050578b61ae))
* **webui:** show archived badge next to status in task detail view ([07e2034](https://github.com/icholy/gritz/commit/07e2034eaa7f51d1039f8aa6a15460d2683fcd95))


### Miscellaneous

* **server:** inline instruction wake condition ([541ab80](https://github.com/icholy/gritz/commit/541ab80d69b0f49d24596235fcd1c71644bec2c6))
* **webui:** make tasks, workspaces, and members pages full width ([babb8d0](https://github.com/icholy/gritz/commit/babb8d015e902ff21f6b580e9b5a444527149217))

## [2.15.0](https://github.com/icholy/gritz/compare/v2.14.1...v2.15.0) (2026-07-17)


### Features

* **scheduler:** add scheduler worker that fires due schedules ([c176a18](https://github.com/icholy/gritz/commit/c176a181d561c9cdd71f75f6072b18e6054885b1))
* **server:** add schedule proto and RPC handlers ([073f573](https://github.com/icholy/gritz/commit/073f5738dc39458a607e70cc9d7156925e9ba3bd))
* **store:** add schedule model and store layer ([c1f1fdf](https://github.com/icholy/gritz/commit/c1f1fdf2417cdc648553e979a7c3bd4767981176))


### Bug Fixes

* **deps:** update aws-sdk-go-v2 monorepo ([1aff12b](https://github.com/icholy/gritz/commit/1aff12ba178870aa0c2a1c423c08790fdfb0eb4d))
* **server:** gate schedule delete/enable on task-write ([5eeb953](https://github.com/icholy/gritz/commit/5eeb9532a75efca86ec4ffe641f0286884316533))
* **webui:** reposition task sidebar status and toggle button ([31e413d](https://github.com/icholy/gritz/commit/31e413d54f8b39e3aca2cce03626898666799e11))


### Miscellaneous

* **deps:** update dependency lucide-react to v1.24.0 ([2af465c](https://github.com/icholy/gritz/commit/2af465c1ac5c603ab45a3f536fe5ffbb965d63c3))
* **deps:** update dependency vite to v8.1.4 ([1f6f61a](https://github.com/icholy/gritz/commit/1f6f61af52807d279e961fd0abeafb91ef04216c))
* **model:** assert Schedule.Task/Events with DeepEqual ([7061e84](https://github.com/icholy/gritz/commit/7061e84eb0826e05fd1bebbdc089a0c43fd5493f))
* **model:** move schedule instruction proto conversion into model ([dbf885a](https://github.com/icholy/gritz/commit/dbf885ac506e6107a0bb0b768237d165a5f1c61f))
* **model:** rename Instruction to ScheduleInstruction ([612c46e](https://github.com/icholy/gritz/commit/612c46e90b5ef628782c00e95e0fc3b197d320a7))
* **scheduler:** collapse fire's disable/advance into one if/else ([4c80955](https://github.com/icholy/gritz/commit/4c809556b581c418d07e99c1419d6bb5a9d5ee4f))
* **scheduler:** drive the tick tests through a moq'd Store ([246ee63](https://github.com/icholy/gritz/commit/246ee637d5b6bb9a929f9a58077747374ac4fecc))
* **scheduler:** inline task seeding and drop invalid-fire report event ([d1f0a20](https://github.com/icholy/gritz/commit/d1f0a20e3adedf5086b8d5adc6b9aa82b1044f71))
* **scheduler:** inline the tick test helpers at their call sites ([c435868](https://github.com/icholy/gritz/commit/c43586838dd28fc189a611a4db64e970ba4df322))
* **scheduler:** move schedule task/event mapping onto the model ([a8566a1](https://github.com/icholy/gritz/commit/a8566a1d889af0eed59a0338e3b48558d16cad3e))
* **scheduler:** use errgroup in the concurrent tick test ([1abbad9](https://github.com/icholy/gritz/commit/1abbad92d1659795118118cd122f72ee20a13f8b))
* **scheduler:** use wg.Go in the concurrent tick test ([9925e1c](https://github.com/icholy/gritz/commit/9925e1c120af7e8e6f1fcd888044da4571e9ac81))
* **schedules:** drop the unused version column ([fe69097](https://github.com/icholy/gritz/commit/fe69097a41e386657ab5e5f637e50aa787ec497b))
* **server:** always recompute next_run_at on enabled update ([78fdebf](https://github.com/icholy/gritz/commit/78fdebf743b384334898b269e21a2536d2f85ef5))
* **server:** clarify CreateSchedule enabled is explicit ([09c060e](https://github.com/icholy/gritz/commit/09c060efbc802fac125f312981f543b4a7d5acd7))
* **server:** inline schedule change notifications ([8e69d34](https://github.com/icholy/gritz/commit/8e69d346c99867643bb9b3bff0192fca5ffa26b2))
* **server:** use plain bool for CreateSchedule enabled ([f5142c6](https://github.com/icholy/gritz/commit/f5142c62b7135d6dc545f11f813732556c6a6769))
* **store:** add schedules table migration ([3a48965](https://github.com/icholy/gritz/commit/3a48965d180c39e20f800e9a0e56135211657dcd))
* **store:** group AdvanceSchedule params into a struct ([9993943](https://github.com/icholy/gritz/commit/99939433279b7f05b0883bfcf78b8defd77d4a51))
* **store:** use BIGSERIAL for schedules.id ([ee713b7](https://github.com/icholy/gritz/commit/ee713b7cb6a9381f5baf27e9f34d8a84bd253571))

## [2.14.1](https://github.com/icholy/gritz/compare/v2.14.0...v2.14.1) (2026-07-17)


### Miscellaneous

* **webui:** let task timeline fill the main pane width ([43f527e](https://github.com/icholy/gritz/commit/43f527e03363528822e010d41d7ed803a8859a6b))

## [2.14.0](https://github.com/icholy/gritz/compare/v2.13.1...v2.14.0) (2026-07-17)


### Features

* **webui:** redesign task page with collapsible sidebar layout ([71eb3aa](https://github.com/icholy/gritz/commit/71eb3aaff57d44d04d91d5a556c32dae76487d01))


### Bug Fixes

* **server:** rename task_logs SSE notification to task_events ([790b011](https://github.com/icholy/gritz/commit/790b011bc576399dedca5bfabe394879b0131994))


### Miscellaneous

* **deps:** update dependency prettier to v3.9.5 ([a928cb7](https://github.com/icholy/gritz/commit/a928cb76623bc4cd72830923257068e8fe7539b2))

## [2.13.1](https://github.com/icholy/gritz/compare/v2.13.0...v2.13.1) (2026-07-13)


### Miscellaneous

* **agent:** make name-setting line conditional in first-run brief ([bdd7453](https://github.com/icholy/gritz/commit/bdd74534a11e5e69f9ca8c0423b541ac416395e8))
* **agent:** render workspace prompt in how-to-work section ([d811a63](https://github.com/icholy/gritz/commit/d811a63dbae4cc721487edb36cc5f4593da9bb10))
* **agent:** render workspace prompt on first run only ([98f7bd0](https://github.com/icholy/gritz/commit/98f7bd05725a3cacc9d1689bcd95bced5cea0c65))
* regenerate taskstatus_string.go ([398d52c](https://github.com/icholy/gritz/commit/398d52c26af2875197210ea8ae628ce6b66a7884)), closes [#1443](https://github.com/icholy/gritz/issues/1443)

## [2.13.0](https://github.com/icholy/gritz/compare/v2.12.0...v2.13.0) (2026-07-13)


### Features

* **agent:** add NextEventToken config field ([8ae008f](https://github.com/icholy/gritz/commit/8ae008f97d5cc29b0e33381beb7da320c913b605))
* **agent:** add RenderBrief and Options.TaskDetails ([d28483a](https://github.com/icholy/gritz/commit/d28483ad8a016fe592bd00b9a7bbf498df280efe))
* **agent:** advance the event cursor after a successful run ([547f6f8](https://github.com/icholy/gritz/commit/547f6f8b437d1dd665ab92c1e39565524efc4b04))
* **agent:** driver fetches and injects first-run task brief ([23276c5](https://github.com/icholy/gritz/commit/23276c5ef4866719f058073dd2045f833bf3a1ca))
* **agent:** filter wake events server-side via the types RPC filter ([181ff41](https://github.com/icholy/gritz/commit/181ff4146c919ae105ff8ff2ae4eedc9fbadd63a))
* **agent:** inject wake events into the prompt ([a9625ce](https://github.com/icholy/gritz/commit/a9625ce912fb3526fb77c66f25dbf11cef64043d))
* **agent:** render task brief in first-run prompt branch ([05f00ae](https://github.com/icholy/gritz/commit/05f00aee53bd1f67aeab5f7923163f4536eed19e))
* **apiserver:** add types filter to ListEventsByTask RPC ([92cd8a8](https://github.com/icholy/gritz/commit/92cd8a8bb8db2b8f16a3d5de8bc7d88eb5f9c12a))
* **apiserver:** expose task namespace through proto and API ([ff943a7](https://github.com/icholy/gritz/commit/ff943a708cd5c0e0097c50fca1cce06c402e2105))
* **eventrouter:** partition rule matching and create by namespace ([7bcf481](https://github.com/icholy/gritz/commit/7bcf4816aa1139871551ba95f9afc9b3ceeb9617)), closes [#1317](https://github.com/icholy/gritz/issues/1317)
* **events:** persist source/type on ExternalPayload ([fef18f1](https://github.com/icholy/gritz/commit/fef18f14dcd8f1ddf3bb118bbe47aafc3ac106c4)), closes [#1410](https://github.com/icholy/gritz/issues/1410)
* **pagination:** expose whether more pages remain (More bool) ([5272387](https://github.com/icholy/gritz/commit/5272387c3d9f58a49052cc18e0ce2462f2e9dcba)), closes [#1389](https://github.com/icholy/gritz/issues/1389)
* **store:** surface task namespace on subscribed links ([1f447f3](https://github.com/icholy/gritz/commit/1f447f339ce249a24632ba7dea5485d2ab42e52d))
* **store:** thread task namespace through the store ([cc84fd4](https://github.com/icholy/gritz/commit/cc84fd497e178bc88e695d3fb0906caee5c02f51))
* **webui:** add task namespace field and badge ([afa24be](https://github.com/icholy/gritz/commit/afa24bebd24769fd644df87251fe036bf1e96f09))


### Bug Fixes

* **deps:** update aws-sdk-go-v2 monorepo ([d727f6b](https://github.com/icholy/gritz/commit/d727f6b505a0f0a39bf27aa020d4c233841bfbe4))
* **eventrouter:** include task_logs in no-wake notification ([1334088](https://github.com/icholy/gritz/commit/13340887cb04ecdf43c688b2006f0e9bfa38d758))


### Performance Improvements

* **store:** re-add partial keyset index for active tasks ([bab2111](https://github.com/icholy/gritz/commit/bab21110b9c4ee5323db0ab0f64d58abc1c5d564))


### Miscellaneous

* **agent:** add hybrid markdown event renderer ([b905abf](https://github.com/icholy/gritz/commit/b905abfa2224f0f84f8161d6e2148cc2da7384e9))
* **agent:** build event block with strings.Builder ([c0ce42e](https://github.com/icholy/gritz/commit/c0ce42ec6cc7b6f2280eb49f522bf344f088909d))
* **agent:** converge init/wake prompt into one skeleton ([a502c77](https://github.com/icholy/gritz/commit/a502c77862bbaca84d0fac03f3bb2f29b07f6df0))
* **agent:** drop blank line after external event header ([dc925cb](https://github.com/icholy/gritz/commit/dc925cb23d31f411b9e55d423968683d163b8fdd))
* **agent:** drop GetTaskDetails from driver first-run brief ([82cafb2](https://github.com/icholy/gritz/commit/82cafb2c06a7846fb374d21764ada23f2df4ce31))
* **agent:** extract prompt rendering into agentprompt package ([5b01e84](https://github.com/icholy/gritz/commit/5b01e84cd83e932cb5f2cdf9f5720beb4f03afd4))
* **agent:** flatten Options and share one event loop across init/wake prompts ([8a9ca00](https://github.com/icholy/gritz/commit/8a9ca00010714b7cb02389520e41b1d8b7c5b8aa))
* **agent:** inline external Type line with fmt.Sprintf ([fe65712](https://github.com/icholy/gritz/commit/fe65712c5b24098239170e9025f4127cb2c676b6))
* **agent:** inline the event types filter and stabilize RenderEvent ([358a5a4](https://github.com/icholy/gritz/commit/358a5a4fcadcc1f960947df06ddb3d3c0f3acc82))
* **agent:** marshal wake events inside the template ([6983de7](https://github.com/icholy/gritz/commit/6983de7fc6648c9e5dc2ef45ff23f2000a4c96ae))
* **agentmcp:** make get_my_task event-native ([89779e2](https://github.com/icholy/gritz/commit/89779e27d2d366e1aa40c196fb3aeba7e6d3fc24))
* **agent:** move brief header and framing into the template ([865e93d](https://github.com/icholy/gritz/commit/865e93dced3e50d1aa316bf39f312796d0c3d3ca))
* **agent:** polish first-run brief and drop dead bootstrap arm ([f006c46](https://github.com/icholy/gritz/commit/f006c46e1e436f8e63fc8894176f57a89e8efef0))
* **agent:** remove first-run brief driver tests ([7e14787](https://github.com/icholy/gritz/commit/7e14787ad647ac4862a8c1c3381b4115ed167f4c))
* **agent:** render brief via renderEvent + header/links helpers ([50c20db](https://github.com/icholy/gritz/commit/50c20dbd22cdf0e2dd84561127e4e1ea88a4b877))
* **agent:** render event stream as a flat list ([a7b398b](https://github.com/icholy/gritz/commit/a7b398be8d9fa0446742970a8ad9b6a228e01d5d))
* **agent:** render one event per template func call ([7bf3065](https://github.com/icholy/gritz/commit/7bf306522f637fe2d2ebeb7731cc3724fd3ae6ad))
* **agent:** render wake events via renderEvent markdown ([f4c090d](https://github.com/icholy/gritz/commit/f4c090de71f6924949f47bbd6ac1f554ee48f955))
* **agent:** reshape external event rendering into labeled fields ([b200b06](https://github.com/icholy/gritz/commit/b200b06aa74cc03b27db51509890530e783197f8))
* **agent:** restore blank line after external event header ([2a849c7](https://github.com/icholy/gritz/commit/2a849c77ef485ed616f4ecab81486ae36dd1c15d))
* **agent:** snapshot renderEvent output with goldens ([5b68d9e](https://github.com/icholy/gritz/commit/5b68d9ec83eb44b0976eeeb484036885f4f8f9df))
* **agent:** snapshot the rendered wake prompt with golden files ([0754bf2](https://github.com/icholy/gritz/commit/0754bf2dc09f75bcb5b4538ae4227ad29e9f5e7b))
* **agent:** take Render inputs via an Options struct ([0ba93f0](https://github.com/icholy/gritz/commit/0ba93f065f1c3d4408477d58a77c1ab14cd9fae3))
* **agent:** use external source verbatim in label ([aa5064e](https://github.com/icholy/gritz/commit/aa5064ee765f175430dae4bed711626c0315e879))
* **cli:** render task list header-only from ListTasks ([6adcc43](https://github.com/icholy/gritz/commit/6adcc43120531a4deda5b2613e8fdc49348e20f3))
* **eventrouter:** keep first-match-per-org for namespace routing ([f46157a](https://github.com/icholy/gritz/commit/f46157a14545df8e632e0d3209b8d5d7aeff26f3))
* fold gritz-implement into gritz-orchestrator skill ([76b8a26](https://github.com/icholy/gritz/commit/76b8a26430e2c73876d18372ef4e5d4d224fd4fa))
* **mcpserver:** make getTask event-native ([655ffb4](https://github.com/icholy/gritz/commit/655ffb4b741cbffad26fcfee755aa194ad124dd1))
* **n8n:** compose task details from primitives ([52cea8e](https://github.com/icholy/gritz/commit/52cea8ea90706ff35c6744f4a68b64bff9ff403c))
* pin pnpm to 11.11.0 to avoid broken 11.12.0 ([40fabec](https://github.com/icholy/gritz/commit/40fabecd7b4f9071f6f487f716a9749a21aadbe1))
* **proto:** remove GetTaskDetails RPC ([db0d66c](https://github.com/icholy/gritz/commit/db0d66c2ffe574c9fe671af4e4f2e35ce99a1a33))
* rewrite gritz-orchestrator to cover the full lifecycle ([114d10a](https://github.com/icholy/gritz/commit/114d10a385784f79f3ff7055eda3915f5f8b4eef))
* **runner:** update get_my_task RPC expectations ([ccbd0e8](https://github.com/icholy/gritz/commit/ccbd0e815c3adb8c2f23e6c5f7efce1e70742356))
* **store:** add tasks.namespace column ([68354ae](https://github.com/icholy/gritz/commit/68354aeaa00f20f32270d077ce8c46379fe8f058))
* track orchestrator work on a GitHub Project board ([19a9d67](https://github.com/icholy/gritz/commit/19a9d67fb9471c332b9ec55af5e90b1ff9587b92))
* **webui:** derive timeline source from persisted field ([e3902ef](https://github.com/icholy/gritz/commit/e3902efb7d59282a4d9682d466beb9ed3cbb15f7))
* **webui:** move task detail off GetTaskDetails to GetTask + ListLinks ([2278c3b](https://github.com/icholy/gritz/commit/2278c3bd16442b2d0f124599287ba014d35f7141))

## [2.12.0](https://github.com/icholy/gritz/compare/v2.11.0...v2.12.0) (2026-07-12)


### Features

* add generic details map to ExternalPayload ([a1fd80f](https://github.com/icholy/gritz/commit/a1fd80fccaecb57b7c2599168376a61b17644293))
* **apiserver:** gate TestEvent stub on OpOrgRead scope ([0cd1b61](https://github.com/icholy/gritz/commit/0cd1b61251c09ad67ad7a4b75adb79559dad895a))
* **apiserver:** implement TestEvent dry-run handler ([3d8d305](https://github.com/icholy/gritz/commit/3d8d30535c60e8eeaf6afd70c7d3de8c10c5d205))
* **apiserver:** pass archived filter through ListTasks handler ([410d4c8](https://github.com/icholy/gritz/commit/410d4c80469b184e83601f6f3641970a70d13c9e))
* **apiserver:** route fired test events for real ([23eb047](https://github.com/icholy/gritz/commit/23eb0474e86ec230ff4f07a9abbb994b5ca8e60c))
* **driver:** stamp run version on runner events ([d5f8066](https://github.com/icholy/gritz/commit/d5f8066abc529ae8668af65ad47887eeddb6db40))
* enrich PR review-comment events with code location ([5d2a921](https://github.com/icholy/gritz/commit/5d2a921c358839a7975d25d50cc874323f2ffb0e))
* **eventrouter:** add user attr to event schemas ([9ef5f9d](https://github.com/icholy/gritz/commit/9ef5f9d25d190edf2a291a4bcc158f7403ce2e70))
* **model:** align task version with run counter ([2d02fcf](https://github.com/icholy/gritz/commit/2d02fcf16244d1f9f7b8bb2969be1d643978d2ed))
* **pagination:** add generic keyset pagination package ([d130240](https://github.com/icholy/gritz/commit/d130240ef965674c144b20237e1db37dc99a80e8))
* **pagination:** add page fields to ListEventsByTask proto ([9f5d4fa](https://github.com/icholy/gritz/commit/9f5d4fa31b6816c1be02a0b5b579c9df316b41ba))
* **pagination:** add paged event store queries and ListEventsByTaskPage ([2d7d632](https://github.com/icholy/gritz/commit/2d7d632965c2ad15693d70d42fac08142e164f24))
* **pagination:** make List/Source/Page bidirectional ([1003166](https://github.com/icholy/gritz/commit/1003166cf81602d86ef8c956dca08a2c385384c3))
* **pagination:** page ListEventsByTask handler ([f559561](https://github.com/icholy/gritz/commit/f5595617fbf939b1a32caf75ca6420bf64dc04e5))
* **pagination:** paginate ListTasks server-side ([fb20a39](https://github.com/icholy/gritz/commit/fb20a399249d6495d6b62d25084810996731deb0))
* plumb InputEvent.Details through the event router ([5b4f26c](https://github.com/icholy/gritz/commit/5b4f26c94519880062b67750e9b49edef1e21e33))
* **proto:** add archived field to ListTasksRequest ([5804de0](https://github.com/icholy/gritz/commit/5804de07867f69e14567f7ad1250580a0682f98a))
* **proto:** add TestEvent RPC and messages ([1e7c76f](https://github.com/icholy/gritz/commit/1e7c76f6d97efe13c2902f47b0f15e69a4f737d1))
* **runner:** version-scope backstop failed events ([62be8d4](https://github.com/icholy/gritz/commit/62be8d4c561a9b7f54e1e698a0f0e9c0c50bac9b))
* stop registering default routing rules in producers ([7bd87af](https://github.com/icholy/gritz/commit/7bd87af50dd10bdeea2315443a0d0bf57b795a92))
* **store:** include archived tasks in ListTasksPage ([9ceb1f5](https://github.com/icholy/gritz/commit/9ceb1f5a9263ac3366cb1f2ae64d16f2780734d0))
* tee driver logs to /gritz/log in the sandbox ([a905d3e](https://github.com/icholy/gritz/commit/a905d3ec35757026583cf7acbafcb14907667d13))
* **webui:** add "Show archived" toggle to task list ([b96a46c](https://github.com/icholy/gritz/commit/b96a46cf51f532fe19af981f58e93847d0261748))
* **webui:** add first-page button and page number to pagination ([c82af7e](https://github.com/icholy/gritz/commit/c82af7ebd834b51f9340e94c5604b5493ed6b635))
* **webui:** add per-page size selector persisted to localstorage ([0c89eb9](https://github.com/icholy/gritz/commit/0c89eb9fa40920243da86c1ec7c3ea8231257651))
* **webui:** add TestEventForm for testing routing rules ([0868732](https://github.com/icholy/gritz/commit/0868732c00f673960c81d61d2a10b1129da9c782))
* **webui:** add useOrgLocalStorageBoolean hook ([033f4d8](https://github.com/icholy/gritz/commit/033f4d83069c30387b198720aa3076802f061732))
* **webui:** chat-style task timeline ([694c9be](https://github.com/icholy/gritz/commit/694c9becb7f7391576243429a493fac95fae6952))
* **webui:** follow task timeline with a bidirectional infinite query ([65726ef](https://github.com/icholy/gritz/commit/65726efd485d6c30e759dff4b394b505094d9815))
* **webui:** move label to the left of the archived toggle ([8d4c991](https://github.com/icholy/gritz/commit/8d4c991aef5ee58998d261264dbc37593d7b5796))
* **webui:** paginate task list with load more ([35af767](https://github.com/icholy/gritz/commit/35af767c75282b730446c07a98ce29015b362200))
* **webui:** render code location details on external events ([508abbd](https://github.com/icholy/gritz/commit/508abbd5eb87a56fc75be9ecd55cde7f9e8819f8))


### Bug Fixes

* **deps:** update aws-sdk-go-v2 monorepo ([221d990](https://github.com/icholy/gritz/commit/221d99096ffe82e06c4d2c935a489dcf49e7e906))
* use slog.DiscardHandler for DiscardDriverLog ([9225d88](https://github.com/icholy/gritz/commit/9225d88facffc7d1d055ac5fa896c1e5e4007758))
* **webui:** add unarchive button and move archived badge to status column ([9c79361](https://github.com/icholy/gritz/commit/9c793618f11bfd2bba31c72ef1592f6e1bcbcc51))
* **webui:** don't refetch timeline pages on SSE reconnect ([4f6e556](https://github.com/icholy/gritz/commit/4f6e556922719989fb0efdddae4fa4803885c559))
* **webui:** poll live-follow as a backstop for missed SSE signals ([7d20a71](https://github.com/icholy/gritz/commit/7d20a710c3ab671cfda3b9cfbcbc6246baa6adb2))
* **webui:** rename test-event route to avoid vitest glob collision ([ed50f35](https://github.com/icholy/gritz/commit/ed50f35c72a98a11e41e02f0c5c8e216cddaf5b1))
* **webui:** satisfy prettier and infinite-query input types ([d1b2f72](https://github.com/icholy/gritz/commit/d1b2f72e6642498409a7bd17cabb8d20088a807d))


### Miscellaneous

* add DriverLog.Stdout/Stderr tee methods ([c852b8d](https://github.com/icholy/gritz/commit/c852b8d38b826b5344765e736f8ec675585e81a4))
* **agent:** add ConfigStore type with delegating globals ([c765f6f](https://github.com/icholy/gritz/commit/c765f6f7431cf57afcca9c0cc5c4497e294cb3b2))
* **agent:** drive config IO through the Driver's ConfigStore ([290f873](https://github.com/icholy/gritz/commit/290f87309c3cb2feb613916fdf2a76b08dea69ad))
* **agent:** remove the ConfigDir global and dead Tar method ([655cb11](https://github.com/icholy/gritz/commit/655cb118ff2ea81c86b83fc085f43c128ea6a9a3))
* **apiserver:** append TestEvent matches into response directly ([71a8ff8](https://github.com/icholy/gritz/commit/71a8ff88e6d8f7550b4033106d90300dba88de0d))
* **apiserver:** collapse TestEvent tests into one sanity test ([43ef53f](https://github.com/icholy/gritz/commit/43ef53f181aa51caff2e165642281272fcdeb124))
* **apiserver:** preinit TestEvent attrs map ([609290b](https://github.com/icholy/gritz/commit/609290b997f2f67b3affe79fe3548aa98d7ea58b))
* **apiserver:** simplify TestEvent dry-run handler ([a3e135e](https://github.com/icholy/gritz/commit/a3e135e1c735ef44cf3a7160723790eb880d9986))
* assert full external+lifecycle payload slice in wake test ([554831c](https://github.com/icholy/gritz/commit/554831c5aae88ee1a9a473d65e1d18d9b013be6d))
* **atomicio:** add a perm parameter to WriteFile ([eca6355](https://github.com/icholy/gritz/commit/eca63554d6551b98c74088e7989f30b0e67f2bc0))
* bundle driver logger and sink into DriverLog ([458f6a2](https://github.com/icholy/gritz/commit/458f6a27eb0a43cb2c706cb5edbc2091e5d1e2fb))
* de-generify model.FilterPayloads to filter by type discriminator ([64f4d9b](https://github.com/icholy/gritz/commit/64f4d9b8e43bf8e01024829585d7cde2b2ae0f22))
* **deps:** update dependency flyctl to v0.4.64 ([2ed5d64](https://github.com/icholy/gritz/commit/2ed5d647b1e78888ead5f37fc4939c4385f7cbff))
* **deps:** update dependency lucide-react to v1.23.0 ([0658e64](https://github.com/icholy/gritz/commit/0658e64c5f7a56f937b787d42e9b6565d06e02ae))
* **deps:** update dependency npm:pnpm to v11.9.0 ([fd8adf7](https://github.com/icholy/gritz/commit/fd8adf70555985f7d9194f53ac73fe7f8c4aeab0))
* **deps:** update dependency prettier to v3.9.4 ([32651c8](https://github.com/icholy/gritz/commit/32651c89754cc601284ced6c919fdbd2bc59e3a6))
* **deps:** update dependency sops to v3.13.2 ([d92ab39](https://github.com/icholy/gritz/commit/d92ab3979f27a0cc20084b7eabe9d66fead4910f))
* **deps:** update dependency vite to v8.1.3 ([70047e7](https://github.com/icholy/gritz/commit/70047e7427ffed0238a38514b1457a3815aaa786))
* **deps:** update radix-ui-primitives monorepo ([e31d08b](https://github.com/icholy/gritz/commit/e31d08b88f044d0e4b7274a1d69ca0d43c6b4578))
* **deps:** update webui to TypeScript 7.0 ([6971265](https://github.com/icholy/gritz/commit/6971265edb7ebfb82a49d649cadfbadd87970b36))
* **driver:** assert runner events via mock extension ([3233e87](https://github.com/icholy/gritz/commit/3233e87b88ebd1175c11a0223205efb0e9123190))
* **driver:** pass task instead of response to run ([f248b1c](https://github.com/icholy/gritz/commit/f248b1c9e9d4c937be6e1346ec0856037872d9f8))
* drop ExternalPayload round-trip tests per review ([d179788](https://github.com/icholy/gritz/commit/d1797881615dc37cede209d352041aea3bcedcc3))
* enrich SubmitRunnerEvents log line for desync diagnosis ([a748071](https://github.com/icholy/gritz/commit/a7480719ca83a291d0943cb10421ac885c5f003a))
* **eventrouter:** add RouteMatch.ProtoRuleIndex helper ([c8dd5a2](https://github.com/icholy/gritz/commit/c8dd5a236367722bec8f0d57244480e1806f6449))
* **eventrouter:** drop orgFilter from Plan ([325a1a5](https://github.com/icholy/gritz/commit/325a1a5329a38eb7a179e83b8941ae00f21a5551))
* **eventrouter:** extract Apply from Route ([b716ef6](https://github.com/icholy/gritz/commit/b716ef628356a21bbd55ba735afcb65391c0888f))
* **eventrouter:** extract Plan from Route ([ed5a7d5](https://github.com/icholy/gritz/commit/ed5a7d5e96af054011f8caa98626a60dd1dc0bcd))
* **eventrouter:** flag default rule with RuleDefault bool ([961c509](https://github.com/icholy/gritz/commit/961c509fa1305d533a53ad8a7609d9d4889d68d9))
* **eventrouter:** remove dead default-rules registry API ([8687f3f](https://github.com/icholy/gritz/commit/8687f3f007034213120168254e0839221ba252f7))
* **eventrouter:** remove ruleless-org default fallback ([69e250e](https://github.com/icholy/gritz/commit/69e250e96a14c8753d7bcc48a5a81df68fedc4db))
* **eventrouter:** return full TestEventMatch from RouteMatch.Proto ([66047ed](https://github.com/icholy/gritz/commit/66047ed4d338edd2007bc6d58f8870caa02a336f))
* **eventrouter:** split RouteMatch literal across lines ([f4db997](https://github.com/icholy/gritz/commit/f4db9976a94e7a87bd4c6a54ca80390fc7ccbee1))
* **eventrouter:** use match fields directly in Route apply loop ([108f695](https://github.com/icholy/gritz/commit/108f695ea2d560114d2fa9aca8c4ac649ccc14a6))
* inline FilterPayloads calls into DeepEqual assertions ([4f30f97](https://github.com/icholy/gritz/commit/4f30f97ce8f96be4404faea5a2ad6f501107aad5))
* inline withLogSink helper into the two callers ([c02969b](https://github.com/icholy/gritz/commit/c02969bb8cd93b6f1e9ec8384a5d40e19f75762d))
* log tasks via slog.LogValuer instead of flat fields ([cf3790a](https://github.com/icholy/gritz/commit/cf3790a2a3d357de81475792d4341388a5fc5282))
* make DriverLog embed slog.Logger and require Log ([c309f52](https://github.com/icholy/gritz/commit/c309f52a913b6405b8a409f2f465daf5e93abcae))
* make FilterPayloads accept types as a variadic ([901edb1](https://github.com/icholy/gritz/commit/901edb1e5c7806d6aafb30ac4952289d1acce14c))
* **model:** split run-version scenarios into separate tests ([bfea3c1](https://github.com/icholy/gritz/commit/bfea3c1c3628790c696b03a10ed23a37e515a0ea))
* move config store and runner events proposals to implemented ([ba2a4c1](https://github.com/icholy/gritz/commit/ba2a4c136d00f19e3ef3d58c004d793e46de1a1c))
* move pr review-comment code location proposal to implemented ([93323d2](https://github.com/icholy/gritz/commit/93323d22917185790d0c744f4d3b1dd5d4c74da9))
* **pagination:** drop redundant pagination tests ([096a454](https://github.com/icholy/gritz/commit/096a4543ba63122f975608be3d2e85bfb71d3116))
* **pagination:** generate Source mock with moq, trim tests ([a556ffa](https://github.com/icholy/gritz/commit/a556ffafcdc78d18214cf436ce4b18cfdfc836db))
* **pagination:** keep NewMockSource name, inline row fixture ([767a38c](https://github.com/icholy/gritz/commit/767a38ce37deb8058d0979ca1654bd5af7fc8712))
* **pagination:** make Encode/Decode exported and Token-typed ([5e81cc5](https://github.com/icholy/gritz/commit/5e81cc54d4f6589a5409b7f2b1a9d4d58e729d76))
* **pagination:** migrate List to an options-struct API ([c388566](https://github.com/icholy/gritz/commit/c388566e1ee308ecd3a711066ac6c3d34c971d80))
* **pagination:** move mock builder to extension, use int cursor ([9f37bf6](https://github.com/icholy/gritz/commit/9f37bf699aa33c556e21a0a232af14d7aab8062a))
* **pagination:** pass an exported Token to Source.Query ([653f2ef](https://github.com/icholy/gritz/commit/653f2eff031ab4b9047519fbc9070997a122e8d8))
* **pagination:** rename Page tokens to Next/Prev, tidy tests ([6367b05](https://github.com/icholy/gritz/commit/6367b0562891f0239b345b2eb6185c6bf0eb4128))
* **pagination:** simplify ListTasks pagination test ([88559a8](https://github.com/icholy/gritz/commit/88559a81502e334b7e0fbb3529d4fb3fafc8c4a3))
* **pagination:** use testx.ExtractField in event handler tests ([d56a328](https://github.com/icholy/gritz/commit/d56a328ec1a5dd65b96901cabf49c7e03c2986d6))
* pass DriverLog through agent Options ([cfcf607](https://github.com/icholy/gritz/commit/cfcf607dcc3f037129b8adfed3a7fc2750c20d57))
* **proposals:** reject run-scoped-runner-events draft ([11c37be](https://github.com/icholy/gritz/commit/11c37be94d5c4fc249aa871cce2d4db99ce1ee49))
* rename task snapshot var to original ([fd21e44](https://github.com/icholy/gritz/commit/fd21e44babb16c28c6e8556a656a2fe70da63f5b))
* reuse call-log projections via moq extensions ([397eb32](https://github.com/icholy/gritz/commit/397eb324d2597115c0ebe61a6f630b02a7456726))
* run tests through gotestsum for readable output ([0eb104d](https://github.com/icholy/gritz/commit/0eb104d8aa7f415e43070072536f22d970089746))
* **runner:** name the sandbox config path via ConfigStore ([8863753](https://github.com/icholy/gritz/commit/8863753e92e6d202a619534a1c6b8a200c9c2555))
* **runner:** tighten TestRunnerLoad_VersionScopedBackstop doc comment ([4140315](https://github.com/icholy/gritz/commit/4140315b34ab2309b36c0a67370e4632b010d19b))
* **skills:** add generated-mock extension file convention ([eca0fb4](https://github.com/icholy/gritz/commit/eca0fb49ef1740a1f993774ee46b28806b3cddc0))
* **skills:** mock extensions must be general, not test-specific ([acb513b](https://github.com/icholy/gritz/commit/acb513b7aa86b455b8a50e34e355f5474a1fe8b8))
* skip backend tests on frontend-only PRs ([cf6474d](https://github.com/icholy/gritz/commit/cf6474d733bb318cbffb66c7541aa28d718be5f7))
* snapshot task via Clone instead of per-field locals ([dfb7019](https://github.com/icholy/gritz/commit/dfb7019003ede060ff82fa3a772c3c4ff649db45))
* **store:** drop partial predicate from keyset index ([e3e744c](https://github.com/icholy/gritz/commit/e3e744c82295efa2860fb823cd9ba905a2750b32))
* **store:** inline event test helpers, use testx.ExtractField ([7894d4d](https://github.com/icholy/gritz/commit/7894d4d15cb4c34b9e07a3493ea73f07443fe5c9))
* **store:** inline event types coercion into Query ([87d7dae](https://github.com/icholy/gritz/commit/87d7dae5d9cc4bbc28608b194062180a97a78f4f))
* **store:** inline task-creation helper, use testx.ExtractField ([f3e7036](https://github.com/icholy/gritz/commit/f3e7036cac093872830f68c4bb726cc2b1dfdd7e))
* **store:** repeat task-creation inline in each test ([e419a12](https://github.com/icholy/gritz/commit/e419a12cd76bf5bd5ec33c725d170f0403af76f3))
* **taskstate:** fold version round-trip into TestWriteRead ([9aab3ac](https://github.com/icholy/gritz/commit/9aab3ac17ab29e40c3fe97ef45bca9224c016d1d))
* thread DriverLog into agent implementations ([4bb1387](https://github.com/icholy/gritz/commit/4bb1387e52d15b660446ad82cc50f4e0e8a93e82))
* tighten FilterPayloads test assertions ([28e98ae](https://github.com/icholy/gritz/commit/28e98aefb525f568c039edf32f3728f4ca9b210f))
* track gotestsum as a go tool instead of go install ([4abbfc1](https://github.com/icholy/gritz/commit/4abbfc1c289fc7b230d35c172fff8bdc8f318e35))
* use cmp.Or for review-comment line fallback ([9868cb8](https://github.com/icholy/gritz/commit/9868cb839592ea8ddbbda315f518994cc391ab35))
* **webui:** back live-follow poll with usehooks-ts useInterval ([dd1e5f9](https://github.com/icholy/gritz/commit/dd1e5f96838445ddd91cae94447fd24ffcb3cbc6))
* **webui:** exclude typescript 7.0.2 from minimumReleaseAge policy ([7f6c37f](https://github.com/icholy/gritz/commit/7f6c37f81508c119c886bc56447533d322e9cc75))
* **webui:** fold matched rule into test-event results ([e21ff31](https://github.com/icholy/gritz/commit/e21ff31f6e1ae673bc837eb03396e2ab40238aa1))
* **webui:** make timeline followers an injected service ([98f28c5](https://github.com/icholy/gritz/commit/98f28c57e1f00b71c97984beb839d35a9f7675f7))
* **webui:** move event details below the event body ([2347c9a](https://github.com/icholy/gritz/commit/2347c9a183f341398312e3c53bc288a02809fa0e))
* **webui:** raise chunk size warning limit to 1500 kB ([1bd02ac](https://github.com/icholy/gritz/commit/1bd02ac8ca63ca37d9eb164b5f42cb7de6907207))
* **webui:** reformat with prettier 3.9 ([b341869](https://github.com/icholy/gritz/commit/b341869db4413508c90996dfa1b987c0c5973b39))
* **webui:** render event details as generic key/value pairs ([c993f80](https://github.com/icholy/gritz/commit/c993f80bde817b59a99b0bd5c0685b75c71da1d3))
* **webui:** resync reconnect by allow-list instead of exclusion ([934d4ae](https://github.com/icholy/gritz/commit/934d4aee281e9c2bb229e839cd577046d488f5f6))
* **webui:** run prettier on tasks.index.tsx ([a49c427](https://github.com/icholy/gritz/commit/a49c42717093c4c76e76e1f10b3d037beef66786))
* **webui:** use prev/next pagination instead of load more ([98f578d](https://github.com/icholy/gritz/commit/98f578d73ecb57a64647a0664d801e5b03ce39f3))

## [2.11.0](https://github.com/icholy/gritz/compare/v2.10.0...v2.11.0) (2026-07-08)


### Features

* add ListOrgIDsByGitHubInstallation store method ([7b685bc](https://github.com/icholy/gritz/commit/7b685bc98256d0cf930286a92464966116df63e8))
* add public flag to RoutingRule ([a4da159](https://github.com/icholy/gritz/commit/a4da159dc7321cb01b39e0f912ce753402dc4f4e))
* add public toggle to the routing rule editor ([098a3c6](https://github.com/icholy/gritz/commit/098a3c68f16598d2a480c0444aa65bb9c313038b))
* populate InputEvent.Orgs from GitHub App installation ([a34ee40](https://github.com/icholy/gritz/commit/a34ee4051db3d24ca14952179cfff3e7c4d6c6f4))
* populate InputEvent.Orgs from the Atlassian webhook org ([b68644e](https://github.com/icholy/gritz/commit/b68644e6ba9bbaba6f2cc76abdd9e1738f1e9036))
* replace ListRoutingRulesForUser with ListRoutingRulesForEvent ([d46b07b](https://github.com/icholy/gritz/commit/d46b07b795fb9612a301eb7a20cea4898166c4df))
* route non-member events via InputEvent.Orgs and Public rules ([ca0c2a2](https://github.com/icholy/gritz/commit/ca0c2a24482d46fd5b165de85925480166f64d9b))


### Bug Fixes

* **deps:** update aws-sdk-go-v2 monorepo ([24f6f45](https://github.com/icholy/gritz/commit/24f6f452c73c00f72f8ed8889da882922d8d59c4))
* **deps:** update module github.com/urfave/cli/v3 to v3.10.1 ([4d2d779](https://github.com/icholy/gritz/commit/4d2d779f264488d41c42512b528326c3e353883a))


### Miscellaneous

* **deps:** update dependency eslint to v10.6.0 ([5222048](https://github.com/icholy/gritz/commit/52220488e1329830e5a232da33e57545216fe55f))
* **deps:** update dependency flyctl to v0.4.62 ([2c40c99](https://github.com/icholy/gritz/commit/2c40c99221b2f567c18fe651d73458d6050706df))
* **deps:** update dependency flyctl to v0.4.63 ([8434e24](https://github.com/icholy/gritz/commit/8434e24872f6b2a6b27ed8f74ccae3d0d6db13f0))
* **deps:** update dependency globals to v17.7.0 ([fe2555f](https://github.com/icholy/gritz/commit/fe2555fd846d1b4f2934cadcc790f3255662181b))
* **deps:** update dependency lucide-react to v1.22.0 ([1b45068](https://github.com/icholy/gritz/commit/1b4506881308c9b9dbe9b1cd723a58ad9d014598))
* **deps:** update dependency typescript-eslint to v8.62.1 ([22a81fd](https://github.com/icholy/gritz/commit/22a81fdaf5e131729cae3b4298a571141a8e8037))
* **deps:** update tailwindcss monorepo to v4.3.2 ([6801ddb](https://github.com/icholy/gritz/commit/6801ddb9359e969a35d5494447aeda552a4bb147))
* **deps:** update tanstack-query monorepo to v5.101.2 ([61495c5](https://github.com/icholy/gritz/commit/61495c501aafbcf410721b5509c5ca35565d1e97))
* **eventrouter:** declare schema attributes inline per event type ([5c3ea94](https://github.com/icholy/gritz/commit/5c3ea94f093b57273cb7f05c97af4d64e0fca705))
* **githubserver:** inline wakeOnGritzPrefix into each schema ([b3a5109](https://github.com/icholy/gritz/commit/b3a5109e579e836c9dee56d99ea5cdae94b8591d))
* range over rule value instead of index in Route ([163a129](https://github.com/icholy/gritz/commit/163a1298d38893670a6384c7595ccddb089a6565))
* replace findOrgRules helper with testx.FindFunc ([354fbdf](https://github.com/icholy/gritz/commit/354fbdf821c6bc18a9ea3b089cc633783158d928))

## [2.10.0](https://github.com/icholy/gritz/compare/v2.9.0...v2.10.0) (2026-07-05)


### Features

* **routing:** self-describing attribute schema (AttrDef) ([cd74ed6](https://github.com/icholy/gritz/commit/cd74ed64236e97ec2d32c7744167b97e69595eff))


### Miscellaneous

* **eventrouter:** drop projection/factory helpers from routing-rule tests ([dcd7cd1](https://github.com/icholy/gritz/commit/dcd7cd1afea2e22fc2445f9dd00b1f2b9afe0008))
* **eventrouter:** remove legacy routing-rule translator ([b696b22](https://github.com/icholy/gritz/commit/b696b22d309ead03efd875a49f44e862d8835f12))
* **eventrouter:** trim redundant routing-rule tests ([798eb0f](https://github.com/icholy/gritz/commit/798eb0f1ccfa0130ea46d559d7560b52156fd1ee))
* move attribute-based event matching proposal to implemented ([efe1486](https://github.com/icholy/gritz/commit/efe148694386b48798358b538dd866eb50ed9378))

## [2.9.0](https://github.com/icholy/gritz/compare/v2.8.0...v2.9.0) (2026-07-05)


### Features

* **eventrouter2:** add attribute-based core matcher ([ccb5050](https://github.com/icholy/gritz/commit/ccb505092b272104a6e036cacf25902e8e51618f))
* **eventrouter2:** add event-type registry and rule validation ([ba62549](https://github.com/icholy/gritz/commit/ba625491f076051af5a9dc752aa2bf13bd74eeb0))
* **eventrouter2:** require registered event type and add default rules ([8531804](https://github.com/icholy/gritz/commit/85318048840f4e84314533aec512892ef83a23c2))
* **eventrouter2:** translate legacy rules to attribute conditions ([62fb223](https://github.com/icholy/gritz/commit/62fb223fcc707d9d51771de839b313dfb84598e9))
* **eventrouter:** populate inert event attrs from webhooks ([c81680a](https://github.com/icholy/gritz/commit/c81680a97722f34b9968b94310259da68bad1680))
* **eventrouter:** route via attribute-based matcher (translate-on-read) ([07185b4](https://github.com/icholy/gritz/commit/07185b42562af82e40677a5d61f5145dd1b85136))
* **observability:** attach org and task ids to traces and logs ([26b126e](https://github.com/icholy/gritz/commit/26b126ed5c6d64a67d07e6f9cb5d1b6827678878)), closes [#1053](https://github.com/icholy/gritz/issues/1053)
* **routing:** switch rules to attribute conditions (backend) ([934e583](https://github.com/icholy/gritz/commit/934e583dc3ee86b7998bac21dce915cf360fecbc))
* **server:** add GetEventTypes RPC exposing the event-type registry ([3457727](https://github.com/icholy/gritz/commit/345772790d4e34e715c6512790d06af131e521ca))
* **shell:** log rendezvous leg connect / established / teardown at info ([1434e50](https://github.com/icholy/gritz/commit/1434e50ca75c753c2aca5b7d010dd7cbdd83242c))
* **webui:** attribute-condition editor for routing rules ([46f8a4f](https://github.com/icholy/gritz/commit/46f8a4fa8043faf6d6eada782be6b113c3169cd0))
* **webui:** show full task name on hover via title attribute ([9304660](https://github.com/icholy/gritz/commit/9304660c416b421c76f0ef65180a8c36321c0f0c))
* **webui:** surface archive as an icon button beside the task menu ([37667bd](https://github.com/icholy/gritz/commit/37667bdda7036a1b65779e272e8a4b026a259b8e))


### Bug Fixes

* **otel:** enable honeycomb metrics ingestion ([330fc28](https://github.com/icholy/gritz/commit/330fc28bc10aaed7cb429b9fbe48ce42bdd614ce))
* **webui:** keep task header controls right-aligned on overflow ([5fc8262](https://github.com/icholy/gritz/commit/5fc8262b1f29453f2fb63bbbeaf923a76299a11a)), closes [#1230](https://github.com/icholy/gritz/issues/1230)
* **webui:** truncate task title on wide screens to keep header one line ([e7a5143](https://github.com/icholy/gritz/commit/e7a5143d413a52aac3a01166e9deff61a0d84430))
* **webui:** use pointer cursor on tab triggers ([8c663f6](https://github.com/icholy/gritz/commit/8c663f698991255b61e4cc1bc226b15ae3c3b12d))


### Miscellaneous

* **eventrouter2:** accumulate defaultRules during registration ([9b79d17](https://github.com/icholy/gritz/commit/9b79d17ac441d5e481a958e8c64af91a7070791d))
* **eventrouter2:** drop tautological registry tests ([a90e5ba](https://github.com/icholy/gritz/commit/a90e5baa6e96edf2b5bde4d9453d4adac4694ceb))
* **eventrouter2:** format registry entries multi-line ([3f1afb6](https://github.com/icholy/gritz/commit/3f1afb6496456b0f3429521c50ac978736cf31e3))
* **eventrouter2:** group registry vars in a single block ([28d96f8](https://github.com/icholy/gritz/commit/28d96f892531043884fcaaab606fd6fbce60cb08))
* **eventrouter2:** index registry by key and simplify Validate ([73c0ffe](https://github.com/icholy/gritz/commit/73c0ffec008a8e8f7de9fc82a59a00f8304caece))
* **eventrouter2:** init index map in NewSchemaRegistry ([634d6df](https://github.com/icholy/gritz/commit/634d6df2f0af2ee24cef2c16a2dd466f8c536cda))
* **eventrouter2:** make condition matching a method on Condition ([6c0a988](https://github.com/icholy/gritz/commit/6c0a9882e679108bbb2bb74315828937c72031b3))
* **eventrouter2:** make MatchRule a method on RoutingRule ([f734d7f](https://github.com/icholy/gritz/commit/f734d7f475e518703d4e34252c021d7c68cd6420))
* **eventrouter2:** operate on model.RoutingRule, drop wrapper types ([4d9f290](https://github.com/icholy/gritz/commit/4d9f290e63a2346d37340629b2d02914f320c328))
* **eventrouter2:** producers register via registerSchemas(reg) ([ca3139a](https://github.com/icholy/gritz/commit/ca3139ab935feda3c5f317a55d3bd13e70cc5904))
* **eventrouter2:** register event-type schemas from producers ([578cbc6](https://github.com/icholy/gritz/commit/578cbc635b2c0d1c00775d09bc30f4209859037e))
* **eventrouter2:** register fixtures per test, drop helper ([4ad9540](https://github.com/icholy/gritz/commit/4ad95401146f8002dea4315a327d06298de04fe7))
* **eventrouter2:** registry as an explicit SchemaRegistry type ([ef2e9c6](https://github.com/icholy/gritz/commit/ef2e9c6a26175d4d5f3597d2889d229428a39acc))
* **eventrouter2:** rename RegisteredEventTypes to EventTypes ([f2cdfcf](https://github.com/icholy/gritz/commit/f2cdfcf50dd4cbcae8e4e1948f3045817dbe888a))
* **eventrouter2:** rename registeredEventTypes var to eventTypes ([998cd21](https://github.com/icholy/gritz/commit/998cd21d0e23ad5f748c54183b8358cc9918cbb6))
* **eventrouter2:** use gotest.tools assert in schema tests ([3b5afd0](https://github.com/icholy/gritz/commit/3b5afd06f267c09f80c0aa8a3465ae99a7e82f9e))
* **eventrouter:** inline mention attr at construction sites ([6446de3](https://github.com/icholy/gritz/commit/6446de3aa9e0957ff59b5cffe6b0ffbe5463470b))
* **eventrouter:** merge eventrouter2 into eventrouter ([7cc98bc](https://github.com/icholy/gritz/commit/7cc98bc9eb0109c2998b565ff439ae1154914e08))
* **eventrouter:** move mention extraction into domain packages ([9630501](https://github.com/icholy/gritz/commit/9630501babc1b09526b934a39ee7f61671bdb260))
* **observability:** drop context logger from driver and runner ([6a53d2c](https://github.com/icholy/gritz/commit/6a53d2c82c674f920bb0aa6e536dad22f510a3e5))
* **observability:** drop logctx handler test ([ea5c7fa](https://github.com/icholy/gritz/commit/ea5c7fadc0057962304d18e8857a99728714c8c8))
* **store:** consolidate routing-rule decode tests into one table-driven test ([eb6650b](https://github.com/icholy/gritz/commit/eb6650b2d21c8288067fa7c93fe26fe4e3a6465b))

## [2.8.0](https://github.com/icholy/gritz/compare/v2.7.0...v2.8.0) (2026-07-05)


### Features

* **outbox:** adopt durable outbox for runner events ([#1205](https://github.com/icholy/gritz/issues/1205)) ([28970c1](https://github.com/icholy/gritz/commit/28970c194116ad4bef4a924ef4a9a97713b6b2e7))
* **webui:** add overflow actions menu to task page ([ebc6180](https://github.com/icholy/gritz/commit/ebc6180f13522b44e511f924072ee84240033a75))
* **webui:** deep-link task page panels via ?tab= search param ([#1210](https://github.com/icholy/gritz/issues/1210)) ([e367979](https://github.com/icholy/gritz/commit/e3679790f8ede9be2d84bf9fdb6761e5b28f3eca))
* **webui:** move cancel action into task overflow menu ([24e8ff6](https://github.com/icholy/gritz/commit/24e8ff6206d0d5c3650f89920ec2d8099959898a))
* **webui:** move restart action into task overflow menu ([aab0a8c](https://github.com/icholy/gritz/commit/aab0a8c6c87ca764e62dc4b43778f816d23b3cb2))
* **webui:** move task tabs into header beside actions menu ([2b29574](https://github.com/icholy/gritz/commit/2b29574024ad2ef84b8c8f0500b4e86744ed31c4)), closes [#1215](https://github.com/icholy/gritz/issues/1215)


### Bug Fixes

* **runner:** crash on durable store write failure ([e6dd3ff](https://github.com/icholy/gritz/commit/e6dd3ffbe9d98a639dcb0521ccf266fbf68a01a1))
* **webui:** align add instruction send button with composer input ([bba7eb7](https://github.com/icholy/gritz/commit/bba7eb77f62d0f5d3e04167e8d0ed4c6439ac666)), closes [#1005](https://github.com/icholy/gritz/issues/1005)
* **webui:** tidy task overflow menu layout ([e90f562](https://github.com/icholy/gritz/commit/e90f56230f08207729e6faa737debc66e20f65cb))


### Miscellaneous

* adopt testx.ExtractField for single-field call-log asserts ([#1211](https://github.com/icholy/gritz/issues/1211)) ([3d46e19](https://github.com/icholy/gritz/commit/3d46e198a0bb5a68e5394bf0a6b777a2c8720075))
* **eventrouter:** extract FilterPayloads results into variables ([ea8f1f3](https://github.com/icholy/gritz/commit/ea8f1f30c0ae269ee671981f0688ef4ea816561a))
* **eventrouter:** use FilterPayloads for external/restarted tally ([88e8991](https://github.com/icholy/gritz/commit/88e899122775232ac627dccd83b7315f33d77477))
* **model:** add FilterPayloads and adopt it in eventrouter tests ([a2ccd2a](https://github.com/icholy/gritz/commit/a2ccd2a3c0e79cab7c7f556588456dd9397d303c))
* **proposal:** reuse shipped generic outbox in server-side-taskstate ([72dc231](https://github.com/icholy/gritz/commit/72dc2317e9f416e1b3f95b85f00226856e400391))
* **proposals:** tidy up draft directory ([#1213](https://github.com/icholy/gritz/issues/1213)) ([9f8d02e](https://github.com/icholy/gritz/commit/9f8d02e423524b37bf1512f1ee77240f01bbd7a5))
* **runner:** default Fatal to no-op so die calls it directly ([951a6bd](https://github.com/icholy/gritz/commit/951a6bdbbdbe20a892594ba62352d25278e5f595))
* **runner:** inline event literal at enqueue sites ([ef68828](https://github.com/icholy/gritz/commit/ef688282e2dd5bf5704ade2e3f8d5361d6c761a8))
* **runner:** rename cancelCause to fatal ([1d5dcc0](https://github.com/icholy/gritz/commit/1d5dcc0be025337612c20155e8010743a8882127))
* **runner:** rename fatal cancel func to cancel ([53a2ece](https://github.com/icholy/gritz/commit/53a2eced53c63cda1028fd178824c78ba3cccb09))
* **runner:** rename FatalStoreError to FatalError ([9bb562d](https://github.com/icholy/gritz/commit/9bb562d4327eece6ad07dac507cebb9fb89ca8ea))
* **runner:** use moq StoreMock for outbox store failure injection ([1456402](https://github.com/icholy/gritz/commit/14564024377b4a53f7a85d321360f5151fb8ea34))
* **testx:** add ExtractField helper for moq call-log arguments ([#1206](https://github.com/icholy/gritz/issues/1206)) ([2ec87be](https://github.com/icholy/gritz/commit/2ec87bef3c22b4d6a8f591559aa1d2c88d73e5d0))
* **webui:** format dropdown-menu with prettier ([47f8259](https://github.com/icholy/gritz/commit/47f825990ff90deb2bc83c7879523671118ff746))

## [2.7.0](https://github.com/icholy/gritz/compare/v2.6.0...v2.7.0) (2026-07-04)


### Features

* **cmpx:** add OnlyFields inverse of cmpopts.IgnoreFields ([#1178](https://github.com/icholy/gritz/issues/1178)) ([e69a5e1](https://github.com/icholy/gritz/commit/e69a5e18bd00660590d1332eb2b4176e78ebc50f))
* **dev:** add dummy-error workspace to dev runner ([#1183](https://github.com/icholy/gritz/issues/1183)) ([c463e4e](https://github.com/icholy/gritz/commit/c463e4e1e7be9ea170d28cfb85ac211484f568c7))
* **events:** carry failure reason on sandbox lifecycle events ([#1172](https://github.com/icholy/gritz/issues/1172)) ([56a033c](https://github.com/icholy/gritz/commit/56a033cee14d4bfee50f832d048e695de2ae9d13))
* in-browser task shell on the task detail page ([#1154](https://github.com/icholy/gritz/issues/1154)) ([fd8349e](https://github.com/icholy/gritz/commit/fd8349e6857a86a495633c47700d02adc00a08b0))
* **mcpbridge:** add "all" option to channel_mute ([#1175](https://github.com/icholy/gritz/issues/1175)) ([705eec7](https://github.com/icholy/gritz/commit/705eec79f5f4bbd7eead643812a6a91eb0376253))
* **mcpbridge:** channel_mute / channel_unmute / channel_muted tools ([#1162](https://github.com/icholy/gritz/issues/1162)) ([bda33bb](https://github.com/icholy/gritz/commit/bda33bba23ae3678b2d7579c0ca94b80d6849af4))
* **moqassert:** add package for asserting on moq call logs ([#1177](https://github.com/icholy/gritz/issues/1177)) ([c3faa71](https://github.com/icholy/gritz/commit/c3faa7120d47deecef5187386e64dcec31a58ac1))
* **outbox:** add durable outbox store interface and filesystem implementation ([#1188](https://github.com/icholy/gritz/issues/1188)) ([674214b](https://github.com/icholy/gritz/commit/674214b8ad152fa3dd5e5b5c37ebf35c3f3a3fc4))
* **outbox:** add generic outbox delivery engine ([#1200](https://github.com/icholy/gritz/issues/1200)) ([f63399d](https://github.com/icholy/gritz/commit/f63399d2fa915a68104036e356d12f7e85e5377c))
* **shellserver:** add active shell sessions metric ([#1168](https://github.com/icholy/gritz/issues/1168)) ([02c86b9](https://github.com/icholy/gritz/commit/02c86b934c3cbd2c14a44cb429f4f5201ea09f16))
* **shellserver:** add idle timeout to debug shell relay ([#1191](https://github.com/icholy/gritz/issues/1191)) ([d1227b0](https://github.com/icholy/gritz/commit/d1227b0554f1bba72050b0c9cb53747c5f8d3750))
* **webui:** add read-only Links tab to task detail page ([#1195](https://github.com/icholy/gritz/issues/1195)) ([89f2530](https://github.com/icholy/gritz/commit/89f253069adb55c095e34bf1ecbe5d0d99e586b0))
* **webui:** make task shell an in-page tab ([#1184](https://github.com/icholy/gritz/issues/1184)) ([3fb99a3](https://github.com/icholy/gritz/commit/3fb99a39a7ba6726ead4c7d6a92f658dcc90e4ce))
* **gritzclient:** add retry with backoff to gRPC client ([#1201](https://github.com/icholy/gritz/issues/1201)) ([4312bbe](https://github.com/icholy/gritz/commit/4312bbeb270bcb34e9b48931124b41b9e60c7315))


### Bug Fixes

* close the operator shell leg with CloseNow to avoid an exit hang ([#1149](https://github.com/icholy/gritz/issues/1149)) ([212760a](https://github.com/icholy/gritz/commit/212760a4b3982e1ebcc8b7535e50e7dd2b2fa883)), closes [#1144](https://github.com/icholy/gritz/issues/1144)
* **server:** return 404 on wrong-org shell attach ([#1190](https://github.com/icholy/gritz/issues/1190)) ([4c4c377](https://github.com/icholy/gritz/commit/4c4c377b1dcd10c1754bcfea8ed4e19fcbbd8bd7))
* **shell:** reap sandbox when operator disconnects from reverse shell ([#1182](https://github.com/icholy/gritz/issues/1182)) ([9928f09](https://github.com/icholy/gritz/commit/9928f091cf1773b5f2909a9aa8abf29b46c7cf53))
* **shell:** tear down reverse shell on driver SIGTERM ([#1164](https://github.com/icholy/gritz/issues/1164)) ([b893f85](https://github.com/icholy/gritz/commit/b893f85218b9fc44208bee0f8505ebae0812abb1))


### Miscellaneous

* add dummy runner to local dev stack ([#1155](https://github.com/icholy/gritz/issues/1155)) ([d60d5bc](https://github.com/icholy/gritz/commit/d60d5bcbff600ea82ad904bdb148b6e5a078f03c))
* add Implementation Plan section to proposal skill ([#1176](https://github.com/icholy/gritz/issues/1176)) ([3099f66](https://github.com/icholy/gritz/commit/3099f661f72b99e6d4b52cd6a20eaca06ff1e5d9))
* add internal/x/mcpx for shared MCP result helpers ([#1192](https://github.com/icholy/gritz/issues/1192)) ([f5bf7cf](https://github.com/icholy/gritz/commit/f5bf7cf80d5d9f8bd5ff7ecf91c8d875e3bea133))
* add mcptest package and use it in mcpbridge tests ([#1189](https://github.com/icholy/gritz/issues/1189)) ([6a6725c](https://github.com/icholy/gritz/commit/6a6725c517963a4487c30bcad8df3481f1f71cc0))
* add moq and no-helper rules to testing skill ([#1163](https://github.com/icholy/gritz/issues/1163)) ([d8c14a3](https://github.com/icholy/gritz/commit/d8c14a333f0024cddb4d498d309d64eb1e5c2107))
* add gritz-implement skill ([#1186](https://github.com/icholy/gritz/issues/1186)) ([1bffff8](https://github.com/icholy/gritz/commit/1bffff8b072ef225614e8beaff67820466e076cc))
* collapse field-by-field named-struct asserts to whole-value DeepEqual ([#1203](https://github.com/icholy/gritz/issues/1203)) ([125d277](https://github.com/icholy/gritz/commit/125d27742a0512c58fc74233bd20bad04b3d74d7))
* enable standard golangci-lint linters and fix findings ([#1173](https://github.com/icholy/gritz/issues/1173)) ([99ada0b](https://github.com/icholy/gritz/commit/99ada0b9d7faf3607a40878d308ff8313c581210))
* enforce slog "err" key with sloglint ([#1171](https://github.com/icholy/gritz/issues/1171)) ([bffa878](https://github.com/icholy/gritz/commit/bffa8780a1ad322d9db73328696f7d3a2983b924))
* experiment — whole-value DeepEqual call-log assertions ([#1198](https://github.com/icholy/gritz/issues/1198)) ([01e1225](https://github.com/icholy/gritz/commit/01e1225d424464727e216378456bcdf076b86909))
* **mcp:** adopt mcptest helpers and testing conventions ([#1194](https://github.com/icholy/gritz/issues/1194)) ([2abc045](https://github.com/icholy/gritz/commit/2abc0456088ed75c2169d55f04e38238dec1e18b))
* **mcpbridge:** use moqassert and cmpx for channel-sender assertions ([#1181](https://github.com/icholy/gritz/issues/1181)) ([7c049b3](https://github.com/icholy/gritz/commit/7c049b365b8bbd7d58d437ee4905da9012abc95a))
* migrate moq call-count assertions to cmp.Len ([#1197](https://github.com/icholy/gritz/issues/1197)) ([2b9947e](https://github.com/icholy/gritz/commit/2b9947e581e278c26746a62064186667ceea326e))
* relay PR feedback to the task in gritz-implement skill ([#1193](https://github.com/icholy/gritz/issues/1193)) ([99dc956](https://github.com/icholy/gritz/commit/99dc956bb13210ec71d43067d4ecdb5a44f1b3ac))
* return provisioned user from Provision ([#1169](https://github.com/icholy/gritz/issues/1169)) ([163e3e1](https://github.com/icholy/gritz/commit/163e3e1b55b09293691b3794d9e4035fb3f2fa64))
* **shell:** tear down reverse shell via Cmd.Cancel/WaitDelay ([#1167](https://github.com/icholy/gritz/issues/1167)) ([b33e9df](https://github.com/icholy/gritz/commit/b33e9df6923bf0b0185a09fd347b95136e29eaad))
* **testx:** replace moqassert with generic testx.At helper ([#1185](https://github.com/icholy/gritz/issues/1185)) ([8bf2a61](https://github.com/icholy/gritz/commit/8bf2a61cd00feaeacd304791317574641016ceaf))

## [2.6.0](https://github.com/icholy/gritz/compare/v2.5.1...v2.6.0) (2026-07-03)


### Features

* add C2 shell rendezvous relay ([#1118](https://github.com/icholy/gritz/issues/1118)) ([df17327](https://github.com/icholy/gritz/commit/df17327ca0b2382e9748f6ce20a34a3c320e3681))
* add driver-side debug shell (runShell) ([#1123](https://github.com/icholy/gritz/issues/1123)) ([4fb3229](https://github.com/icholy/gritz/commit/4fb3229a3ca0ce359651469e3eecc286f0a4da54))
* add OpenShell RPC for debug shells ([#1124](https://github.com/icholy/gritz/issues/1124)) ([1c5b6ac](https://github.com/icholy/gritz/commit/1c5b6acf007d14605468bb7cee3fe323eb990c9b))
* add shell_session field to Task ([#1114](https://github.com/icholy/gritz/issues/1114)) ([5f48cc9](https://github.com/icholy/gritz/commit/5f48cc9d26fce57781511ece624ab473610a0ee4))
* clear shell_session when the shell rendezvous ends ([#1138](https://github.com/icholy/gritz/issues/1138)) ([e97eb3b](https://github.com/icholy/gritz/commit/e97eb3bde85e134c7ce63c5cf426e49c29444665))
* fork driver into debug shell on shell_session ([#1125](https://github.com/icholy/gritz/issues/1125)) ([ead1e9d](https://github.com/icholy/gritz/commit/ead1e9db7b517f5026651f8144b8a767ceeafb7b))
* reimplement gritz shell against the server ([#1127](https://github.com/icholy/gritz/issues/1127)) ([4d4350d](https://github.com/icholy/gritz/commit/4d4350d9a1803f24fee396319f3fd8a66a425474))


### Bug Fixes

* bind the driver reverse-shell leg to the session's task ([#1140](https://github.com/icholy/gritz/issues/1140)) ([05c428e](https://github.com/icholy/gritz/commit/05c428e4a549cd3f8b1f8ca86e60a0256af61c38))
* raise websocket read limit on all debug-shell legs ([#1134](https://github.com/icholy/gritz/issues/1134)) ([e1e612e](https://github.com/icholy/gritz/commit/e1e612e0bac4097c4c916cb546aa35f781fcf532))
* **webui:** remove redundant auto-archive deadline text ([#1117](https://github.com/icholy/gritz/issues/1117)) ([bf8842e](https://github.com/icholy/gritz/commit/bf8842e3f145bad0683f6c6a85f8ec4d8b5dbf1d))


### Miscellaneous

* carry shell session id as a query param ([#1137](https://github.com/icholy/gritz/issues/1137)) ([c31259e](https://github.com/icholy/gritz/commit/c31259eff44fc0601586c146764626233b6cc4dc))
* consolidate debug shell core into internal/shell ([#1131](https://github.com/icholy/gritz/issues/1131)) ([7bb9032](https://github.com/icholy/gritz/commit/7bb903277bdddad6032c0659516233c0b7e1474b))
* correct internal/proto note (checked in, not gitignored) ([#1116](https://github.com/icholy/gritz/issues/1116)) ([1c282bd](https://github.com/icholy/gritz/commit/1c282bd37674bc27b8565890abec17461fe6dfbb))
* **deps:** update dependency @bufbuild/protobuf to v2.12.1 ([#1129](https://github.com/icholy/gritz/issues/1129)) ([84d1252](https://github.com/icholy/gritz/commit/84d12524fecc6ac654cc8e96a955a7e62665a13d))
* **deps:** update dependency @bufbuild/protoc-gen-es to v2.12.1 ([#1130](https://github.com/icholy/gritz/issues/1130)) ([e9a8da0](https://github.com/icholy/gritz/commit/e9a8da035aa5fd3e604333b5cb9d63aa72838180))
* **deps:** update dependency @vitejs/plugin-react to v6.0.3 ([#1132](https://github.com/icholy/gritz/issues/1132)) ([40187f9](https://github.com/icholy/gritz/commit/40187f97fe5dd9fe1bcede9c3a3b951942156d45))
* **deps:** update dependency flyctl to v0.4.61 ([#1135](https://github.com/icholy/gritz/issues/1135)) ([05bddf6](https://github.com/icholy/gritz/commit/05bddf6804a4662b6f501d0221c44b9c3840ceea))
* **deps:** update module golang.org/x/tools to v0.47.0 ([#1128](https://github.com/icholy/gritz/issues/1128)) ([978e2a9](https://github.com/icholy/gritz/commit/978e2a97acdad960b3a7bda5da897d1fa4ade866))
* **lambdamicrovm:** correct the create-microvm-image recipe ([5fc3a09](https://github.com/icholy/gritz/commit/5fc3a091c68b9f800b3398d7dfea64ca35afe182))
* move shellwire under internal/shell ([#1133](https://github.com/icholy/gritz/issues/1133)) ([0b5d42a](https://github.com/icholy/gritz/commit/0b5d42a1872fe260b2e8d4f8a58066e192dee809))
* replace C2/botnet analogy with neutral control-plane terms ([#1122](https://github.com/icholy/gritz/issues/1122)) ([433de88](https://github.com/icholy/gritz/commit/433de88a7038ebbf9039adfbfb23c2ae4ef2e8b6))
* split shellrelay into a leg-agnostic Session and server-owned registry ([#1136](https://github.com/icholy/gritz/issues/1136)) ([8eced10](https://github.com/icholy/gritz/commit/8eced10e833ff6b43dc2529855ef22054bb23765))
* use options structs for shell and shellserver APIs ([#1139](https://github.com/icholy/gritz/issues/1139)) ([89185f8](https://github.com/icholy/gritz/commit/89185f83f4b3549460b2a50f398005015f341fd2))

## [2.5.1](https://github.com/icholy/gritz/compare/v2.5.0...v2.5.1) (2026-07-01)


### Bug Fixes

* **runner:** keep supervise on the runner's root context ([#1103](https://github.com/icholy/gritz/issues/1103)) ([78afecd](https://github.com/icholy/gritz/commit/78afecd6a1a3d83f42337ebd08ec61673e8ab846))

## [2.5.0](https://github.com/icholy/gritz/compare/v2.4.0...v2.5.0) (2026-07-01)


### Features

* add notify command for system notifications ([#1026](https://github.com/icholy/gritz/issues/1026)) ([5536efb](https://github.com/icholy/gritz/commit/5536efb2309f8d5a2d5731798fcaeaf10b386748))
* **awsmicrovm:** add CreateMicrovmAuthToken and proxy request helper ([#1082](https://github.com/icholy/gritz/issues/1082)) ([4c28245](https://github.com/icholy/gritz/commit/4c28245fa139b1b0bc1ad4391df5c43567948ba7))
* **awsmicrovm:** add general-purpose AWS Lambda MicroVMs package ([#1079](https://github.com/icholy/gritz/issues/1079)) ([dbcaa39](https://github.com/icholy/gritz/commit/dbcaa39e0f204af00bf805820dcebb750ad7e10d))
* **awsmicrovm:** add SuspendMicrovm and ResumeMicrovm ([#1084](https://github.com/icholy/gritz/issues/1084)) ([c9d8d3b](https://github.com/icholy/gritz/commit/c9d8d3ba3f1431b6e40279ce77afcbd2410b364f))
* **awsmicrovm:** add typed APIError and IsNotFound ([#1080](https://github.com/icholy/gritz/issues/1080)) ([b57dd98](https://github.com/icholy/gritz/commit/b57dd98ba8de05a0c4ade9eefef6b06aa9537950))
* **runner:** add lambda-microvm backend with runner-driven SSE lifecycle ([#1087](https://github.com/icholy/gritz/issues/1087)) ([640bf5d](https://github.com/icholy/gritz/commit/640bf5d4f5046c72624725b1661cdf20574facb8))
* **runner:** add taskstate store package ([#1077](https://github.com/icholy/gritz/issues/1077)) ([25fa9a9](https://github.com/icholy/gritz/commit/25fa9a98b539b931fa0a2874b51fbee52864d2a8))
* **runner:** per-handle Backend.Wait for task-oriented exit observation ([#1098](https://github.com/icholy/gritz/issues/1098)) ([4d6206c](https://github.com/icholy/gritz/commit/4d6206c553971178a725e4b39163605321ce7e69))
* **webui:** view and edit auto_archive on the task page ([#1102](https://github.com/icholy/gritz/issues/1102)) ([5a2f40a](https://github.com/icholy/gritz/commit/5a2f40a42219ec7988ed8452ad80596902e4b059)), closes [#1095](https://github.com/icholy/gritz/issues/1095)


### Bug Fixes

* **agentmcp:** remove auto_archive control from update_my_task ([#1096](https://github.com/icholy/gritz/issues/1096)) ([aeb23b7](https://github.com/icholy/gritz/commit/aeb23b7aee7b1772d60173b314ea58bb70745d22)), closes [#1094](https://github.com/icholy/gritz/issues/1094)
* **archiver:** pin auto-archive deadline comparison to UTC ([#1093](https://github.com/icholy/gritz/issues/1093)) ([0eeb3f7](https://github.com/icholy/gritz/commit/0eeb3f7af5d0a1f481ac2173e5f912e4d8af2150))
* **awsmicrovm:** add /ready + /validate build hooks and correct the image recipe ([#1101](https://github.com/icholy/gritz/issues/1101)) ([a8c42bb](https://github.com/icholy/gritz/commit/a8c42bb88b8a9093502dbece1111846d63d2e92a))
* **awsmicrovm:** align client with the real lambda-microvms API ([4038f6d](https://github.com/icholy/gritz/commit/4038f6d1423883ccfb1029b48d6b366245c68437))
* **lambdamicrovm:** launch with an ingress connector so the shim is reachable ([#1099](https://github.com/icholy/gritz/issues/1099)) ([512434d](https://github.com/icholy/gritz/commit/512434d07997d40458805d5f58d087d2ab0f2b01))
* **microvmshim:** serve AWS hooks and the gritz control surface on separate ports ([#1100](https://github.com/icholy/gritz/issues/1100)) ([565c747](https://github.com/icholy/gritz/commit/565c747c15e4c20fa160ce380e2bd4e3b98bb294))


### Miscellaneous

* **deps:** update dependency @tanstack/react-router to v1.170.16 ([#1085](https://github.com/icholy/gritz/issues/1085)) ([fef52db](https://github.com/icholy/gritz/commit/fef52dbbc7170f6c020956bb218e3638dc50b0bb))
* **deps:** update dependency typescript-eslint to v8.62.0 ([#1090](https://github.com/icholy/gritz/issues/1090)) ([2d55903](https://github.com/icholy/gritz/commit/2d55903d89a03965cee9885911abf31e7e263b88))
* **deps:** update tanstack-query monorepo to v5.101.0 ([#1086](https://github.com/icholy/gritz/issues/1086)) ([eab29d2](https://github.com/icholy/gritz/commit/eab29d2695426404d84bc5958cf79bf7e3248628))
* **runner:** make taskstate the source of truth for sandbox handles ([#1078](https://github.com/icholy/gritz/issues/1078)) ([52c8646](https://github.com/icholy/gritz/commit/52c8646e41abceac614d6b77700c43dd519b5785))

## [2.4.0](https://github.com/icholy/gritz/compare/v2.3.2...v2.4.0) (2026-06-28)


### Features

* share a GitHub App installation across gritz orgs ([#1043](https://github.com/icholy/gritz/issues/1043)) ([304a2ee](https://github.com/icholy/gritz/commit/304a2ee5f7bb68581c9e3fe34ee7fc71f17ff12e))
* **webui:** link existing GitHub installation by ID ([#1073](https://github.com/icholy/gritz/issues/1073)) ([3307010](https://github.com/icholy/gritz/commit/3307010d668d1287df1b5e6c0c6b5889a147eaf5))


### Bug Fixes

* **deps:** update module github.com/bradleyfalzon/ghinstallation/v2 to v2.19.0 ([#1069](https://github.com/icholy/gritz/issues/1069)) ([851e93b](https://github.com/icholy/gritz/commit/851e93b2a748b6b798aee10901d3a0b7bee02823))
* **deps:** update module github.com/jackc/pgx/v5 to v5.10.0 ([#1070](https://github.com/icholy/gritz/issues/1070)) ([3a28657](https://github.com/icholy/gritz/commit/3a28657f25d7dd7b1c7ab160bb9086f26cd0d137))
* **deps:** update module github.com/urfave/cli/v3 to v3.10.0 ([#1071](https://github.com/icholy/gritz/issues/1071)) ([b33bcba](https://github.com/icholy/gritz/commit/b33bcbae833e05f6ec7052f35c7155a9cd728319))
* **deps:** update module github.com/zitadel/zitadel-go/v3 to v3.29.1 ([#1061](https://github.com/icholy/gritz/issues/1061)) ([bd3b1f9](https://github.com/icholy/gritz/commit/bd3b1f9238443ee25c726873e2a745948aa036c2))


### Miscellaneous

* **deps:** update actions/checkout action to v7 ([#1072](https://github.com/icholy/gritz/issues/1072)) ([1c9211f](https://github.com/icholy/gritz/commit/1c9211fc5c7a6562f7093eb7f07614fd88f22a28))
* **deps:** update alpine docker tag to v3.24 ([#1062](https://github.com/icholy/gritz/issues/1062)) ([4e8571c](https://github.com/icholy/gritz/commit/4e8571ce7637e9bbaeb2b55a3d5fa75bd636c120))
* **deps:** update dependency @bufbuild/buf to v1.71.0 ([#1063](https://github.com/icholy/gritz/issues/1063)) ([2e12c5a](https://github.com/icholy/gritz/commit/2e12c5a2faddac7ffbc0df7104b7974d6213ef3c))
* **deps:** update dependency @connectrpc/connect to v2.1.2 ([#1038](https://github.com/icholy/gritz/issues/1038)) ([52109e1](https://github.com/icholy/gritz/commit/52109e16a481d00a26203e46b690d22b858aa69e))
* **deps:** update dependency @connectrpc/connect-web to v2.1.2 ([#1039](https://github.com/icholy/gritz/issues/1039)) ([e7ae8a4](https://github.com/icholy/gritz/commit/e7ae8a4b3157d2aba7d8b6d884929c1525b9bece))
* **deps:** update dependency @types/node to v24.13.2 ([#1064](https://github.com/icholy/gritz/issues/1064)) ([de48071](https://github.com/icholy/gritz/commit/de4807193a680353c06f6d66778a6237b54b2b86))
* **deps:** update dependency eslint to v10.5.0 ([#1065](https://github.com/icholy/gritz/issues/1065)) ([a4cd2c6](https://github.com/icholy/gritz/commit/a4cd2c6028631169b950c9caf2ff4c4f02e7167e))
* **deps:** update dependency eslint-plugin-react-refresh to v0.5.3 ([#1041](https://github.com/icholy/gritz/issues/1041)) ([7911646](https://github.com/icholy/gritz/commit/791164626457c978bf78f708b13087d49c48bec6))
* **deps:** update dependency lucide-react to v1.21.0 ([#1066](https://github.com/icholy/gritz/issues/1066)) ([91f3430](https://github.com/icholy/gritz/commit/91f3430e19f53da3a6c10ad3cbb9bbcd1c9b94dd))
* **deps:** update dependency npm:pnpm to v11.6.0 ([#1035](https://github.com/icholy/gritz/issues/1035)) ([d9bc1e6](https://github.com/icholy/gritz/commit/d9bc1e699f44de74a9f766db88ff883271bd0789))
* **deps:** update dependency npm:pnpm to v11.7.0 ([#1045](https://github.com/icholy/gritz/issues/1045)) ([c5465d1](https://github.com/icholy/gritz/commit/c5465d1fd8eea6cac2f641caeb3a800b73ace5a8))
* **deps:** update dependency npm:pnpm to v11.8.0 ([#1058](https://github.com/icholy/gritz/issues/1058)) ([8d949c6](https://github.com/icholy/gritz/commit/8d949c6f3a3e5a16fc4469578c206e1dd03656af))
* **deps:** update dependency prettier to v3.8.4 ([#1042](https://github.com/icholy/gritz/issues/1042)) ([ab28b1b](https://github.com/icholy/gritz/commit/ab28b1bc44f24c886451c647e6825f5db3d9791a))
* **deps:** update dependency vitest to v4.1.9 ([#1059](https://github.com/icholy/gritz/issues/1059)) ([dae6e68](https://github.com/icholy/gritz/commit/dae6e68b961281aef13531c4b99580ef38fbf5d0))
* **deps:** update happy-dom monorepo ([#1047](https://github.com/icholy/gritz/issues/1047)) ([1101749](https://github.com/icholy/gritz/commit/11017490d05cd723ba9f996b2046ae63b7b253b0))
* **deps:** update happy-dom monorepo ([#1050](https://github.com/icholy/gritz/issues/1050)) ([89abd7f](https://github.com/icholy/gritz/commit/89abd7f8c0771aef6930217b53347745e32b19a4))
* **deps:** update happy-dom monorepo ([#1055](https://github.com/icholy/gritz/issues/1055)) ([6160db1](https://github.com/icholy/gritz/commit/6160db1bdbe8b50cdc42cf72ad8461e4c96b875a))
* **deps:** update happy-dom monorepo to v20.10.3 ([#1044](https://github.com/icholy/gritz/issues/1044)) ([8e4274d](https://github.com/icholy/gritz/commit/8e4274d0f12db42a32b04bc577f7456c252c2416))
* **deps:** update module github.com/bufbuild/buf to v1.71.0 ([#1067](https://github.com/icholy/gritz/issues/1067)) ([c0b158b](https://github.com/icholy/gritz/commit/c0b158b8b246c8ea251ef4fb7a19a79822c4890d))
* **deps:** update module golang.org/x/tools to v0.46.0 ([#1068](https://github.com/icholy/gritz/issues/1068)) ([88bdca4](https://github.com/icholy/gritz/commit/88bdca4f83c68e8901672f5045435822f80f4c6b))
* **deps:** update radix-ui-primitives monorepo ([#1056](https://github.com/icholy/gritz/issues/1056)) ([645b474](https://github.com/icholy/gritz/commit/645b47498ab17cdc8b5cc4bd39718f42bedbf08e))
* **deps:** update react monorepo ([#1057](https://github.com/icholy/gritz/issues/1057)) ([363a741](https://github.com/icholy/gritz/commit/363a741263c9247d3bd6c5bb475436d1708c8a8d))
* **deps:** update tailwindcss monorepo to v4.3.1 ([#1060](https://github.com/icholy/gritz/issues/1060)) ([b26553c](https://github.com/icholy/gritz/commit/b26553ca9b9bf7dba26149f88b085ef6f401937a))
* **deps:** update typescript-eslint monorepo ([#1046](https://github.com/icholy/gritz/issues/1046)) ([344539a](https://github.com/icholy/gritz/commit/344539a0b80eaa9a2449aef9b68bbaac8f249ccb))
* **deps:** update webui to TypeScript 7.0 RC ([#1037](https://github.com/icholy/gritz/issues/1037)) ([b5e7606](https://github.com/icholy/gritz/commit/b5e7606f31e7e9a8bde38d912fd5f2bb4db6404d))

## [2.3.2](https://github.com/icholy/gritz/compare/v2.3.1...v2.3.2) (2026-06-19)


### Bug Fixes

* **webui:** expand agent output by default in timeline ([#1032](https://github.com/icholy/gritz/issues/1032)) ([c956bc3](https://github.com/icholy/gritz/commit/c956bc30df6deedf1d57241c0121857c790122ac))

## [2.3.1](https://github.com/icholy/gritz/compare/v2.3.0...v2.3.1) (2026-06-18)


### Bug Fixes

* **eventrouter:** drop redundant RESTARTED lifecycle event on event wake ([#1024](https://github.com/icholy/gritz/issues/1024)) ([4b1da8a](https://github.com/icholy/gritz/commit/4b1da8a1e9af9907e122cbcf2d522d568ba454a7))


### Miscellaneous

* **deps:** update dependency npm:pnpm to v11.5.3 ([#1018](https://github.com/icholy/gritz/issues/1018)) ([1ab8514](https://github.com/icholy/gritz/commit/1ab8514abfb01baaceae303f8949b00e63c933da))

## [2.3.0](https://github.com/icholy/gritz/compare/v2.2.1...v2.3.0) (2026-06-17)


### Features

* **github:** add routing rule trigger for PR created ([#1021](https://github.com/icholy/gritz/issues/1021)) ([3379d24](https://github.com/icholy/gritz/commit/3379d24ca584082d21635d0da9a6a0a368f67c17))


### Miscellaneous

* **deps:** update dependency @tailwindcss/typography to v0.5.20 ([#1016](https://github.com/icholy/gritz/issues/1016)) ([94adef9](https://github.com/icholy/gritz/commit/94adef9f526209b69b2070f7761530882f74ffde))
* **deps:** update dependency flyctl to v0.4.59 ([#1015](https://github.com/icholy/gritz/issues/1015)) ([b18b84c](https://github.com/icholy/gritz/commit/b18b84c50c90472c72990b5ab731c54b5c04ab4b))
* **deps:** update dependency go to v1.26.4 ([#1017](https://github.com/icholy/gritz/issues/1017)) ([bcc342b](https://github.com/icholy/gritz/commit/bcc342b72b0f4a50310a87f83fa5903a5cfd9418))
* **deps:** update dependency vite to v8.0.16 [security] ([#1013](https://github.com/icholy/gritz/issues/1013)) ([237ed72](https://github.com/icholy/gritz/commit/237ed72a8ca69396d0ff9d338298729dd67c1792))
* **deps:** update typescript-eslint monorepo to v8.61.0 ([#1010](https://github.com/icholy/gritz/issues/1010)) ([ad52f62](https://github.com/icholy/gritz/commit/ad52f625e9fb3997e55f1852fe3c7a90b1b2b295))

## [2.2.1](https://github.com/icholy/gritz/compare/v2.2.0...v2.2.1) (2026-06-15)


### Bug Fixes

* surface routing-rule trigger as leading external event ([#1008](https://github.com/icholy/gritz/issues/1008)) ([d464413](https://github.com/icholy/gritz/commit/d464413d8d0c5bb6421e982373329ee6b82868ca))

## [2.2.0](https://github.com/icholy/gritz/compare/v2.1.0...v2.2.0) (2026-06-15)


### Features

* **webui:** support GitHub Flavored Markdown in rendered prose ([#1001](https://github.com/icholy/gritz/issues/1001)) ([c4e8c75](https://github.com/icholy/gritz/commit/c4e8c75a22f7bd5348b1ad6e9bc8bcdf651bb4ec))


### Miscellaneous

* update schema diagram in README ([#1004](https://github.com/icholy/gritz/issues/1004)) ([d86ac1d](https://github.com/icholy/gritz/commit/d86ac1da9483c444c8c2bd34e9b6751f3585c011))

## [2.1.0](https://github.com/icholy/gritz/compare/v2.0.2...v2.1.0) (2026-06-15)


### Features

* **webui:** streamline task page layout and composer ([5d6f3f7](https://github.com/icholy/gritz/commit/5d6f3f77d8537da1f253e488506439bbe711f87d))

## [2.0.2](https://github.com/icholy/gritz/compare/v2.0.1...v2.0.2) (2026-06-15)


### Bug Fixes

* emit link event before instruction in eventrouter created tasks ([#992](https://github.com/icholy/gritz/issues/992)) ([b47cd4d](https://github.com/icholy/gritz/commit/b47cd4d5202de7037243725bec4d80a4b3356b9d))
* show routing rule as actor on router-created lifecycle events ([#996](https://github.com/icholy/gritz/issues/996)) ([aeca52d](https://github.com/icholy/gritz/commit/aeca52d9a812ac3eaa5440081c1bd868b8573dc5))

## [2.0.1](https://github.com/icholy/gritz/compare/v2.0.0...v2.0.1) (2026-06-15)


### Bug Fixes

* emit Created lifecycle event before instruction events ([#988](https://github.com/icholy/gritz/issues/988)) ([fd89605](https://github.com/icholy/gritz/commit/fd8960538afe698760b42090d961eff0bb319d37))
* include changed fields in updated lifecycle event ([#989](https://github.com/icholy/gritz/issues/989)) ([fde159a](https://github.com/icholy/gritz/commit/fde159a972c4455128a901df6cc1c1d856ca63a1))
* remove separate links section from task UI ([#987](https://github.com/icholy/gritz/issues/987)) ([c469b36](https://github.com/icholy/gritz/commit/c469b36dbb834d9ebceef984fed2538055af63ff))


### Miscellaneous

* **deps:** update dependency flyctl to v0.4.58 ([#982](https://github.com/icholy/gritz/issues/982)) ([c35d099](https://github.com/icholy/gritz/commit/c35d099978a2b917890b1a67a326e0952f8fc448))
* **deps:** update tanstack-router monorepo ([#979](https://github.com/icholy/gritz/issues/979)) ([abc7c45](https://github.com/icholy/gritz/commit/abc7c45b5eb8bb78c55ea8b7cd61d10535b26f1a))
* **deps:** update typescript native preview to v7.0.0-dev.20260607.1 ([#980](https://github.com/icholy/gritz/issues/980)) ([fa790d6](https://github.com/icholy/gritz/commit/fa790d6d0e89c78bd5861a2c373d859e7cc02951))
* remove logs table from schema diagram ([#985](https://github.com/icholy/gritz/issues/985)) ([4f7aaf2](https://github.com/icholy/gritz/commit/4f7aaf2f56b57419abc2700d9795c1789f69e026))

## [2.0.0](https://github.com/icholy/gritz/compare/v1.4.0...v2.0.0) (2026-06-14)


### ⚠ BREAKING CHANGES

* remove orphaned CreateEvent RPC and its UI ([#974](https://github.com/icholy/gritz/issues/974))
* parameterize ListEvents store filter, rename RPC to ListExternalEvents ([#972](https://github.com/icholy/gritz/issues/972))
* lifecycle events replace audit/info/error logs, drop logs table ([#966](https://github.com/icholy/gritz/issues/966))
* instruction events replace tasks.instructions ([#959](https://github.com/icholy/gritz/issues/959))
* the Event proto message and the events table are incompatible with the prior flat shape; existing events are not migrated.
* removes the AddEventTask, RemoveEventTask, and ListEventTasks RPCs and the event_tasks table.
* removes the ListChildTasks RPC, the child_tasks token capability, and the Task.parent / GetTaskDetailsResponse.children fields.

### Features

* **webui:** make the task timeline the single activity view ([#975](https://github.com/icholy/gritz/issues/975)) ([633e29b](https://github.com/icholy/gritz/commit/633e29b55b34bfbe0996ce6b819759a2d5b6c998))


### Bug Fixes

* passthrough any connect error code in task/runner handlers ([#970](https://github.com/icholy/gritz/issues/970)) ([4fa2e47](https://github.com/icholy/gritz/commit/4fa2e4709ffb74d50976acb50bdc8c7b87f0e46c))
* render activity timeline oldest-first ([#976](https://github.com/icholy/gritz/issues/976)) ([4e27370](https://github.com/icholy/gritz/commit/4e27370941a858484d7010ca8cf4e7870479613a))
* **webui:** render each report as its own timeline entry ([#977](https://github.com/icholy/gritz/issues/977)) ([d716bb5](https://github.com/icholy/gritz/commit/d716bb5b8f854ea7bfddd70d637412ebb8248bbc))


### Miscellaneous

* inline NewLifecycleEvent, add TaskStatus.Label ([#967](https://github.com/icholy/gritz/issues/967)) ([92269d2](https://github.com/icholy/gritz/commit/92269d26396195602975234929a0bbb86cfb2961))
* instruction events replace tasks.instructions ([#959](https://github.com/icholy/gritz/issues/959)) ([c1672bf](https://github.com/icholy/gritz/commit/c1672bf233aacef2150d5aaa4da569707de0e0f1))
* lifecycle events replace audit/info/error logs, drop logs table ([#966](https://github.com/icholy/gritz/issues/966)) ([65ff0b6](https://github.com/icholy/gritz/commit/65ff0b67faf2639cd54eec64a5a54c189c2176b1))
* link events as timeline source of truth, task_links the projection ([#961](https://github.com/icholy/gritz/issues/961)) ([819dcc5](https://github.com/icholy/gritz/commit/819dcc5b8ca0132c6af188760f4b7a8af19e0410))
* make events task-scoped ([#956](https://github.com/icholy/gritz/issues/956)) ([52a49b5](https://github.com/icholy/gritz/commit/52a49b530616a55d5764d2e61ffb5049a63e8071))
* make runnerLifecycleEvent a RunnerEvent method ([#969](https://github.com/icholy/gritz/issues/969)) ([d7b01d3](https://github.com/icholy/gritz/commit/d7b01d37b79a786536066b6b13335ad991a652d6))
* parameterize ListEvents store filter, rename RPC to ListExternalEvents ([#972](https://github.com/icholy/gritz/issues/972)) ([fedccce](https://github.com/icholy/gritz/commit/fedccce1a1325af819a0f75abfc9d59f232a0cdc))
* remove child tasks ([#954](https://github.com/icholy/gritz/issues/954)) ([6602667](https://github.com/icholy/gritz/commit/6602667804b22404561f07ba448ba586345d155a))
* remove orphaned CreateEvent RPC and its UI ([#974](https://github.com/icholy/gritz/issues/974)) ([03c913f](https://github.com/icholy/gritz/commit/03c913f47d10bece0d7b339ba1888fa6c0c2e5f1))
* report tool writes a report event ([#964](https://github.com/icholy/gritz/issues/964)) ([c601e89](https://github.com/icholy/gritz/commit/c601e895141480308edab3809583f24247fee6e7))
* type the Event payload as a oneof ([#957](https://github.com/icholy/gritz/issues/957)) ([8f1acb9](https://github.com/icholy/gritz/commit/8f1acb99c39b69dc33bbcb1cb76160efbf6ff657))

## [1.4.0](https://github.com/icholy/gritz/compare/v1.3.0...v1.4.0) (2026-06-10)


### Features

* **runner:** abstract the sandbox runtime behind a backend interface ([#937](https://github.com/icholy/gritz/issues/937)) ([75bb3bf](https://github.com/icholy/gritz/commit/75bb3bf1ed04006be9c7cce151ac8111d87a2ec9))


### Miscellaneous

* **deps:** update typescript-eslint monorepo to v8.60.1 ([#931](https://github.com/icholy/gritz/issues/931)) ([004a064](https://github.com/icholy/gritz/commit/004a064ed07c1c0258c291886d0ddfbfb5712c29))

## [1.3.0](https://github.com/icholy/gritz/compare/v1.2.0...v1.3.0) (2026-06-10)


### Features

* move ownership of task lifecycle events into the driver ([#934](https://github.com/icholy/gritz/issues/934)) ([e87c84f](https://github.com/icholy/gritz/commit/e87c84f7a4d5996dd3fa6739e9ece1ac7dbe25b5))


### Miscellaneous

* add MCP server and GitHub/Jira webhooks to architecture diagram ([08037f9](https://github.com/icholy/gritz/commit/08037f94b17ce351690900014778c592ac37ccbb))
* **deps:** update tanstack-router monorepo ([#929](https://github.com/icholy/gritz/issues/929)) ([989158d](https://github.com/icholy/gritz/commit/989158d5d051299174c565711b3eee2b475f317c))
* **deps:** update typescript native preview to v7.0.0-dev.20260527.2 ([#930](https://github.com/icholy/gritz/issues/930)) ([111e291](https://github.com/icholy/gritz/commit/111e291a8687059c15321a86017803555970df90))
* move eliminate-runner-socket-proxy proposal to implemented ([86ab487](https://github.com/icholy/gritz/commit/86ab487bec28fcb75b89489580083bec3ec91b67))
* update architecture diagram for direct C2 connection ([67e7aa4](https://github.com/icholy/gritz/commit/67e7aa40c3606f0f451d46a6f5af1c45f5458b2a))
* **webui:** add Vitest harness with first test case ([#927](https://github.com/icholy/gritz/issues/927)) ([bedf362](https://github.com/icholy/gritz/commit/bedf36289c1595a9a8a8e69c736e2e064827338b))
* **webui:** test AuthTransport with MSW ([#928](https://github.com/icholy/gritz/issues/928)) ([d9c3978](https://github.com/icholy/gritz/commit/d9c39784189226322836978b9fe4bd762c93151c))

## [1.2.0](https://github.com/icholy/gritz/compare/v1.1.1...v1.2.0) (2026-06-07)


### Features

* **agent:** summarize tool-call inputs in logs ([#896](https://github.com/icholy/gritz/issues/896)) ([6974425](https://github.com/icholy/gritz/commit/6974425ec9fd82d602ad191cb25d6b0100049ffb))
* **auth:** add CreateTaskToken issuance RPC ([#909](https://github.com/icholy/gritz/issues/909)) ([f05c529](https://github.com/icholy/gritz/commit/f05c529aa346fbd5a7cdd436327d79827036a57d))
* **auth:** carry authscope scopes on xat_ API keys ([#905](https://github.com/icholy/gritz/issues/905)) ([746c833](https://github.com/icholy/gritz/commit/746c8339bcf304708ce2beb01a50c1ac37948204))
* **auth:** carry scopes on the authenticated caller ([#900](https://github.com/icholy/gritz/issues/900)) ([9aa16ad](https://github.com/icholy/gritz/commit/9aa16ad9615208107ee3a3700341bbd67cf26ef5))
* **mcp:** expose task archive_after in local create_task ([#904](https://github.com/icholy/gritz/issues/904)) ([ecd9569](https://github.com/icholy/gritz/commit/ecd9569079c6fa2aac3beaa7c9c4773636c0ddbc))
* **scope:** add generic scope-matching engine ([#897](https://github.com/icholy/gritz/issues/897)) ([54810f3](https://github.com/icholy/gritz/commit/54810f3f7331a8e9afa956e53cc6455c4562a468))


### Bug Fixes

* **deps:** update opentelemetry ([#885](https://github.com/icholy/gritz/issues/885)) ([53ffc74](https://github.com/icholy/gritz/commit/53ffc7492febbd7ecab90e0c14be1ca777dbf730))
* emit clean org query params in task deep links ([#901](https://github.com/icholy/gritz/issues/901)) ([94a85e2](https://github.com/icholy/gritz/commit/94a85e2149566fc43fbf41e773a4d904595da770))
* **github:** remove confused face reaction on unwoken events ([#888](https://github.com/icholy/gritz/issues/888)) ([99fdbf1](https://github.com/icholy/gritz/commit/99fdbf1f10b22b0d0deee6c355f76b03fc6f9fde))
* **model:** json-quote org query param in TaskURL deep links ([#898](https://github.com/icholy/gritz/issues/898)) ([42805dc](https://github.com/icholy/gritz/commit/42805dcd040479e3b8b7c55e936a95e5f3d81e1e))
* **webui:** shorten routing rules button label to "Rule" ([#873](https://github.com/icholy/gritz/issues/873)) ([bada7cf](https://github.com/icholy/gritz/commit/bada7cf13f24d0a2781ef76dcd2338d0669c5d11))


### Miscellaneous

* **auth:** convert AgentFilter to the scope engine ([#902](https://github.com/icholy/gritz/issues/902)) ([678f52c](https://github.com/icholy/gritz/commit/678f52ce75b85fcb04449ff2f7caca955ac76ba7))
* **auth:** enforce per-task scopes in apiserver handlers ([#911](https://github.com/icholy/gritz/issues/911)) ([ed1f633](https://github.com/icholy/gritz/commit/ed1f633e240670e7ddf4b75447685f9896ee1244))
* **deps:** update dependency @bufbuild/buf to v1.70.0 ([#877](https://github.com/icholy/gritz/issues/877)) ([82ad5a4](https://github.com/icholy/gritz/commit/82ad5a48424a82997575e8ada93685ffe3cb1aa1))
* **deps:** update dependency date-fns to v4.4.0 ([#889](https://github.com/icholy/gritz/issues/889)) ([d13156e](https://github.com/icholy/gritz/commit/d13156ee3c7db5c78d6c127f3fb2201e8a549dac))
* **deps:** update dependency dbmate to v2.33.0 ([#881](https://github.com/icholy/gritz/issues/881)) ([a96b6a8](https://github.com/icholy/gritz/commit/a96b6a849e47579376ebee4c33630896697b8eca))
* **deps:** update dependency flyctl to v0.4.56 ([#875](https://github.com/icholy/gritz/issues/875)) ([b95da6a](https://github.com/icholy/gritz/commit/b95da6aaacb2653df4e98862a746c808341fc09c))
* **deps:** update dependency flyctl to v0.4.57 ([#876](https://github.com/icholy/gritz/issues/876)) ([c00df3f](https://github.com/icholy/gritz/commit/c00df3f09b81ccc6f8907752cee0ec6509603b94))
* **deps:** update dependency sops to v3.13.1 ([#882](https://github.com/icholy/gritz/issues/882)) ([111d193](https://github.com/icholy/gritz/commit/111d193fa348a9b6e98553f5d2b909db2c46c0a0))
* **deps:** update module github.com/bufbuild/buf to v1.70.0 ([#883](https://github.com/icholy/gritz/issues/883)) ([bb34266](https://github.com/icholy/gritz/commit/bb34266ae9605d72465d8f70cba8a3b86d0794ee))
* **deps:** update node.js to v25.9.0 ([#884](https://github.com/icholy/gritz/issues/884)) ([51e614b](https://github.com/icholy/gritz/commit/51e614bf507f4d1ad73229b37fa18739ed0fb910))
* rename "archive after" to "auto archive" ([#910](https://github.com/icholy/gritz/issues/910)) ([4670a45](https://github.com/icholy/gritz/commit/4670a45f1e71bb4ad9825db4706093307033bae4))
* **runner:** connect agents directly to the C2 ([#912](https://github.com/icholy/gritz/issues/912)) ([698c070](https://github.com/icholy/gritz/commit/698c070182ff1a76091ff3bf1f2ec2699ed8fd10))

## [1.1.1](https://github.com/icholy/gritz/compare/v1.1.0...v1.1.1) (2026-06-02)


### Bug Fixes

* **webui:** hide routing rules Filters and Action columns on small screens ([#870](https://github.com/icholy/gritz/issues/870)) ([43da2c8](https://github.com/icholy/gritz/commit/43da2c84bc2b70ef510cdac770dfaa6635101aa9))

## [1.1.0](https://github.com/icholy/gritz/compare/v1.0.0...v1.1.0) (2026-06-02)


### Features

* **webui:** add Atlassian icon to settings page ([#859](https://github.com/icholy/gritz/issues/859)) ([9e8729a](https://github.com/icholy/gritz/commit/9e8729a84e1bddeac8202a1582cb82096a841d86))
* **webui:** show auto-archive countdown on archive button ([#868](https://github.com/icholy/gritz/issues/868)) ([2018c26](https://github.com/icholy/gritz/commit/2018c26afe66529a78cf0e41bf68178ab04382f5)), closes [#865](https://github.com/icholy/gritz/issues/865)


### Bug Fixes

* don't react 😕 for matched-only events on non-waking rules ([#867](https://github.com/icholy/gritz/issues/867)) ([6f4875a](https://github.com/icholy/gritz/commit/6f4875a71dec7f0c71b171b25bf76e6f3ded802a))
* mount postgres volume at /var/lib/postgresql for pg18 ([#869](https://github.com/icholy/gritz/issues/869)) ([d37b2ba](https://github.com/icholy/gritz/commit/d37b2ba3ae3769a773a663b0daedff7283568c76))
* show both wake and create badges in routing rule action column ([#858](https://github.com/icholy/gritz/issues/858)) ([dcd972e](https://github.com/icholy/gritz/commit/dcd972ef588d9cd2376c69486069ace5166642f1))
* **webui:** rename routing rules Labels column to Filters ([#862](https://github.com/icholy/gritz/issues/862)) ([2a13d08](https://github.com/icholy/gritz/commit/2a13d082a20879d80b2d695196b8d93fbdd194a9))

## [1.0.0](https://github.com/icholy/gritz/compare/v0.24.1...v1.0.0) (2026-06-02)


### ⚠ BREAKING CHANGES

* existing persisted rules deserialize with the bool zero value (false) and stop waking until re-saved. This is intended. The in-code defaultRules fallback sets Wakeup: true explicitly so default routing keeps waking. UI-created rules default the toggle to checked.

### Features

* add link routing_url schema, model, and proto ([#848](https://github.com/icholy/gritz/issues/848)) ([128f7ee](https://github.com/icholy/gritz/commit/128f7eee522d7a97bc8e068719e6b86a84c8458d))
* add RoutingRule Wakeup struct to opt out of waking linked tasks ([#855](https://github.com/icholy/gritz/issues/855)) ([ae22ab5](https://github.com/icholy/gritz/commit/ae22ab5a2109df6cf228b4a063286055977edbb0))
* **atlassian:** emit label_added events from issue_updated webhooks ([#840](https://github.com/icholy/gritz/issues/840)) ([4478c8b](https://github.com/icholy/gritz/commit/4478c8b49ace904aa13687056f69cebf3eeea751))
* **atlassian:** parse label changes from issue_updated webhooks ([#838](https://github.com/icholy/gritz/issues/838)) ([809d4cc](https://github.com/icholy/gritz/commit/809d4cc1bf521a4552127ad4b146c6326c9eb2a2))
* **eventrouter:** add generic Value membership routing match ([#846](https://github.com/icholy/gritz/issues/846)) ([9de90ef](https://github.com/icholy/gritz/commit/9de90ef58f64b54f4934d85ef7cdbe181051ded6))
* github pull_request_closed routing event ([#854](https://github.com/icholy/gritz/issues/854)) ([8d7568f](https://github.com/icholy/gritz/commit/8d7568f62bb9c78ac2eb5787a4b561f440fd4bef))
* **githubserver:** add label_added event routing ([#847](https://github.com/icholy/gritz/issues/847)) ([bf3dd00](https://github.com/icholy/gritz/commit/bf3dd00707baa4e6a9326cbfb6454eca8313457b))
* **model:** add RoutingURL function for event routing ([#839](https://github.com/icholy/gritz/issues/839)) ([23e44bb](https://github.com/icholy/gritz/commit/23e44bb91af64761a6c8d224c2ab1805794f38e9))
* route links by derived routing key ([#850](https://github.com/icholy/gritz/issues/850)) ([d36e02d](https://github.com/icholy/gritz/commit/d36e02dd12169444d01e41528e39c5285971910d))
* webhooks emit expressive trigger URL ([#851](https://github.com/icholy/gritz/issues/851)) ([72a041d](https://github.com/icholy/gritz/commit/72a041d30fab1bf2366ac8516cd54ed2900bde7d))
* **webui:** add atlassian:label_added event type ([#845](https://github.com/icholy/gritz/issues/845)) ([01e80c5](https://github.com/icholy/gritz/commit/01e80c50492898fafad38b08048a8222975430ee))


### Miscellaneous

* **deps:** update dependency flyctl to v0.4.55 ([#841](https://github.com/icholy/gritz/issues/841)) ([5b7a024](https://github.com/icholy/gritz/commit/5b7a024006a62baabcd4e8b2ba92753609a6d764))
* **deps:** update dependency typescript-eslint to v8.60.0 ([#842](https://github.com/icholy/gritz/issues/842)) ([4e360ff](https://github.com/icholy/gritz/commit/4e360ffcc86ff5799ee754971ad9b20ff054a30d))
* drop routing_key from CreateLink RPC ([#852](https://github.com/icholy/gritz/issues/852)) ([c8ce76e](https://github.com/icholy/gritz/commit/c8ce76ef8bbbe6d5e266cfc8e6942e6d8e1f4efd))
* enable the archiver ([5c0cf5b](https://github.com/icholy/gritz/commit/5c0cf5b9dce5b29d5d735cddc9be9b895567f85c))
* **fly:** pin min_machines_running to 1 for in-process pubsub ([76b1ce7](https://github.com/icholy/gritz/commit/76b1ce73136f72d4b07a0023559a5d849d90f027))
* generalize README event wording from comments to events ([03b16cc](https://github.com/icholy/gritz/commit/03b16cc0b006ceedad615da0088071f014a9be04))
* **githubserver:** react via GraphQL for all event types ([#834](https://github.com/icholy/gritz/issues/834)) ([4284be3](https://github.com/icholy/gritz/commit/4284be3f7574a07aca7685fdbd78c71269c21f86))
* **githubserver:** remove redundant comments in react ([#837](https://github.com/icholy/gritz/issues/837)) ([0ce157f](https://github.com/icholy/gritz/commit/0ce157f242aac277cf81776a1c1168bdad152d4a))
* move atlassian label routing proposal to implemented ([81e2d8d](https://github.com/icholy/gritz/commit/81e2d8d6de557df6f4d088ec45fb9ef1461321fb))
* move link routing url proposal to implemented ([ee31e6d](https://github.com/icholy/gritz/commit/ee31e6d11abd6822f38f347e38ee8207c9466e7b))
* rename RoutingURL to RoutingKey ([#849](https://github.com/icholy/gritz/issues/849)) ([9d0ef1c](https://github.com/icholy/gritz/commit/9d0ef1ca51efa3db581640377353d117d6c8a811))
* stub server CreateGitHubToken as unimplemented ([#853](https://github.com/icholy/gritz/issues/853)) ([6c2e6e8](https://github.com/icholy/gritz/commit/6c2e6e8f9f2dbd42ea1b45a00885d69ac9e758bc))

## [0.24.1](https://github.com/icholy/gritz/compare/v0.24.0...v0.24.1) (2026-06-01)


### Bug Fixes

* **githubserver:** react to PR review summaries via GraphQL ([#832](https://github.com/icholy/gritz/issues/832)) ([d918951](https://github.com/icholy/gritz/commit/d918951ac9c1c29c58104c78f7db4bd8aaa70c5c))
* **webui:** restore routing rules table with separate labels column ([#830](https://github.com/icholy/gritz/issues/830)) ([e14edeb](https://github.com/icholy/gritz/commit/e14edeba3602f795f3d6d224d21afd8531a04f24))

## [0.24.0](https://github.com/icholy/gritz/compare/v0.23.1...v0.24.0) (2026-05-31)


### Features

* **githubserver:** react to issue and PR assignment events ([#816](https://github.com/icholy/gritz/issues/816)) ([fb7ad79](https://github.com/icholy/gritz/commit/fb7ad79d2e94fa62f0ff315cf72b54d22abab160)), closes [#811](https://github.com/icholy/gritz/issues/811)


### Bug Fixes

* **eventrouter:** custom routing-rule prompt replaces default preamble ([#823](https://github.com/icholy/gritz/issues/823)) ([6b15a2d](https://github.com/icholy/gritz/commit/6b15a2d89225cd3b86a52d99391dda70a193a0f1))
* install postgresql-client-18 to match the v18 server ([#819](https://github.com/icholy/gritz/issues/819)) ([442cc51](https://github.com/icholy/gritz/commit/442cc51e81ef9e090ffd4972bfdb7acc50a432aa))
* use single-line inline table for dbmate task env ([#812](https://github.com/icholy/gritz/issues/812)) ([7b3a31a](https://github.com/icholy/gritz/commit/7b3a31ab29ec3f8af6fdded7f3c59f18c5abade5))
* **webui:** make routing rules list compact on small screens ([#820](https://github.com/icholy/gritz/issues/820)) ([addb199](https://github.com/icholy/gritz/commit/addb19936c09abdebc7fac4cefe84cf948b47460))


### Miscellaneous

* **deps:** update dependency flyctl to v0.4.54 ([#814](https://github.com/icholy/gritz/issues/814)) ([5797645](https://github.com/icholy/gritz/commit/5797645b923c65e5820692cc7d8c12b47832fca8))
* **githubx:** return *http.Client from AppTokenCache ([#828](https://github.com/icholy/gritz/issues/828)) ([973d321](https://github.com/icholy/gritz/commit/973d32105b48b2f7112c06c54f309bab4474a715))
* move mcpswap package under internal/x/ ([#815](https://github.com/icholy/gritz/issues/815)) ([b87af55](https://github.com/icholy/gritz/commit/b87af55d1acd303b670ce35f18f211139a95bc25))

## [0.23.1](https://github.com/icholy/gritz/compare/v0.23.0...v0.23.1) (2026-05-31)


### Bug Fixes

* **githubx:** build installation transports from pre-parsed private key ([#804](https://github.com/icholy/gritz/issues/804)) ([16b79fe](https://github.com/icholy/gritz/commit/16b79fe4914257e52e73863f0a35d927bec44536))


### Miscellaneous

* move github comment reactions proposal to implemented ([2cb722d](https://github.com/icholy/gritz/commit/2cb722d23cb1334c1b433412229b644d4150538a))

## [0.23.0](https://github.com/icholy/gritz/compare/v0.22.2...v0.23.0) (2026-05-31)


### Features

* add archive-after option to routing-rule create-task config ([#786](https://github.com/icholy/gritz/issues/786)) ([cfb0e89](https://github.com/icholy/gritz/commit/cfb0e8915655fc0e0132ab13bc950a4b4ce21a65))
* add OnRouteOutcome callback to eventrouter ([#791](https://github.com/icholy/gritz/issues/791)) ([519d7d7](https://github.com/icholy/gritz/commit/519d7d7030fef073409b57431fddfa1e7094695f))
* add reaction-target coordinates to GitHubMeta ([#792](https://github.com/icholy/gritz/issues/792)) ([964f419](https://github.com/icholy/gritz/commit/964f4193171a71d1443b90298c638fe7cdf8975d))
* add reusable githubx.AppTokenCache ([#785](https://github.com/icholy/gritz/issues/785)) ([7573021](https://github.com/icholy/gritz/commit/757302149ddb1ddb4acc9e94a6b9e54d4dddd305))
* explicit per-task channel subscriptions in mcp bridge ([#798](https://github.com/icholy/gritz/issues/798)) ([f4130aa](https://github.com/icholy/gritz/commit/f4130aa3945fc1ce6e1b4b142d69d6b173e27ffc))
* react to matched GitHub comments with 👀 ([#796](https://github.com/icholy/gritz/issues/796)) ([0346ae5](https://github.com/icholy/gritz/commit/0346ae5ec3759e7468a24ed047b14f32eecae0a6))


### Bug Fixes

* **deps:** update dependency lucide-react to v1 ([#790](https://github.com/icholy/gritz/issues/790)) ([9e33461](https://github.com/icholy/gritz/commit/9e33461f841965663545aadfbce2a0e5d61dc572))
* **deps:** update module github.com/golang-jwt/jwt/v4 to v5 ([#794](https://github.com/icholy/gritz/issues/794)) ([bd6f4b9](https://github.com/icholy/gritz/commit/bd6f4b95745cd3f1b8f3df881c821ccf2251cb84))
* **deps:** update module github.com/golang-jwt/jwt/v4 to v5 ([#797](https://github.com/icholy/gritz/issues/797)) ([4c15568](https://github.com/icholy/gritz/commit/4c1556871ffb433e8fc0fa551e06d70df2c74001))
* **deps:** update module github.com/golang-jwt/jwt/v4 to v5 ([#799](https://github.com/icholy/gritz/issues/799)) ([dfaca44](https://github.com/icholy/gritz/commit/dfaca445a055636c92abcc3386c4f4c383e73e44))
* **deps:** update module github.com/google/go-github/v68 to v88 ([#800](https://github.com/icholy/gritz/issues/800)) ([f9f891e](https://github.com/icholy/gritz/commit/f9f891ee31dce177750e14c25b3f7d93150f6c65))
* ignore non-create/edit GitHub comment webhook actions ([#774](https://github.com/icholy/gritz/issues/774)) ([567588b](https://github.com/icholy/gritz/commit/567588ba3769d42beda90803a79dff1a75c6d9d5))


### Reverts

* back out per-task channel subscription watch tools and skill docs ([856f721](https://github.com/icholy/gritz/commit/856f721a94108deb94f643011cb4a17a11b99a6c))


### Miscellaneous

* build per-installation transports from raw materials in AppTokenCache ([#801](https://github.com/icholy/gritz/issues/801)) ([7e88a8d](https://github.com/icholy/gritz/commit/7e88a8d0539473412ed5119e1aa79690271e9a49)), closes [#787](https://github.com/icholy/gritz/issues/787)
* build release images for linux/amd64 only ([3096dca](https://github.com/icholy/gritz/commit/3096dca61d2b74ba49b0fcdcfc6dcda08b69fec7))
* **deps:** update eslint monorepo to v10 ([#772](https://github.com/icholy/gritz/issues/772)) ([8c874bc](https://github.com/icholy/gritz/commit/8c874bcd3a920886f9a5619eb2f392b2023b5e09))
* **deps:** update googleapis/release-please-action action to v5 ([#778](https://github.com/icholy/gritz/issues/778)) ([b8007c0](https://github.com/icholy/gritz/commit/b8007c07bd85e401f09311ef4e5c9c5e534e816e))
* **deps:** update pnpm/action-setup action to v6 ([#779](https://github.com/icholy/gritz/issues/779)) ([d1ed0e6](https://github.com/icholy/gritz/commit/d1ed0e693ccc82125f314e66820c330bda8dfdf7))
* **deps:** update postgres docker tag to v18 ([#783](https://github.com/icholy/gritz/issues/783)) ([cf81cf1](https://github.com/icholy/gritz/commit/cf81cf14e819c94302956665b1eee41d6238cf2d))
* **deps:** update softprops/action-gh-release action to v3 ([#784](https://github.com/icholy/gritz/issues/784)) ([2a9cd03](https://github.com/icholy/gritz/commit/2a9cd03e473f8e9715bbbbaa5d47cd293fc0c43a))
* dissolve webhookserver into source packages ([#780](https://github.com/icholy/gritz/issues/780)) ([9d308a4](https://github.com/icholy/gritz/commit/9d308a4279e2cade8abe910e605cf702c26c56b5))
* flatten webhook Meta structs (drop GithubUser/AtlassianUser) ([#777](https://github.com/icholy/gritz/issues/777)) ([161c452](https://github.com/icholy/gritz/commit/161c45273a6ed6f0da5601d176df0b60398fb03e))
* move implemented proposals out of draft ([748b729](https://github.com/icholy/gritz/commit/748b7297d4e686952ddeb978de90c4820113710d))
* parse Atlassian webhooks into InputEvent, rename extractors ([#776](https://github.com/icholy/gritz/issues/776)) ([966dad1](https://github.com/icholy/gritz/commit/966dad1090df3279f85a543244e6fd13df8f2956))
* parse GitHub webhooks directly into InputEvent ([#775](https://github.com/icholy/gritz/issues/775)) ([7216541](https://github.com/icholy/gritz/commit/7216541185f14eb55e6571927938df0ed70bbbab))
* promote Atlassian event-type string to a named constant ([#789](https://github.com/icholy/gritz/issues/789)) ([5e39020](https://github.com/icholy/gritz/commit/5e3902071c2329b5e332d6b6cd8a2ae247ecc8e8))
* promote github webhook event-type strings to named constants ([#788](https://github.com/icholy/gritz/issues/788)) ([753727a](https://github.com/icholy/gritz/commit/753727a31dc800c96bef3c04d9b5eea91f247cb5))
* **renovate:** rewrite Go import paths on major module updates ([f81879e](https://github.com/icholy/gritz/commit/f81879e3aea38db4b0d6a804e83b4b061809643a))
* **skills:** document mute-by-default channel notifications and watch tools ([b8686c8](https://github.com/icholy/gritz/commit/b8686c88e58ef7eaa7315239d220d9da559ac1b5))

## [0.22.2](https://github.com/icholy/gritz/compare/v0.22.1...v0.22.2) (2026-05-31)


### Bug Fixes

* copy pnpm-workspace.yaml before webui install in server image ([155117b](https://github.com/icholy/gritz/commit/155117b7ba7b50d7921260cdda1a349a7c081878))


### Miscellaneous

* **deps:** update docker/setup-buildx-action action to v4 ([#764](https://github.com/icholy/gritz/issues/764)) ([acc1d80](https://github.com/icholy/gritz/commit/acc1d8016bef00b37f201ba5db1c568792c2ed77))
* **deps:** update docker/setup-qemu-action action to v4 ([#767](https://github.com/icholy/gritz/issues/767)) ([e9de22f](https://github.com/icholy/gritz/commit/e9de22f001d7bc31ff0c22f749ddef0cd52f03bb))

## [0.22.1](https://github.com/icholy/gritz/compare/v0.22.0...v0.22.1) (2026-05-31)


### Bug Fixes

* install postgresql-client-17 from Debian trixie main ([0437292](https://github.com/icholy/gritz/commit/043729212aaf2600762223288275d86b34ebab60))

## [0.22.0](https://github.com/icholy/gritz/compare/v0.21.0...v0.22.0) (2026-05-31)


### Features

* add assignment-triggered routing rules for GitHub ([#761](https://github.com/icholy/gritz/issues/761)) ([4478868](https://github.com/icholy/gritz/commit/4478868642ce0c55acf50763580b783031a189a0))
* add URL-prefix matching to routing rules ([#763](https://github.com/icholy/gritz/issues/763)) ([cf68f57](https://github.com/icholy/gritz/commit/cf68f57ebe3687dd390263e3dc420ddc5330776d)), closes [#742](https://github.com/icholy/gritz/issues/742)
* **mcp:** add archive_task tool to user-facing MCP server ([#744](https://github.com/icholy/gritz/issues/744)) ([e8c333f](https://github.com/icholy/gritz/commit/e8c333f2e441d739d981ba68701cbe6127d2658c))
* **server:** notify agent channel when task is archived ([#747](https://github.com/icholy/gritz/issues/747)) ([3e663f0](https://github.com/icholy/gritz/commit/3e663f04f8a36d5a45989eebfb5e7eba9eafc05c))
* support routing rules that create tasks ([#746](https://github.com/icholy/gritz/issues/746)) ([8098eb7](https://github.com/icholy/gritz/commit/8098eb7707643232c8e9b658d065a5791bcdbf35))
* **webui:** allow reordering routing rules ([#754](https://github.com/icholy/gritz/issues/754)) ([b27297b](https://github.com/icholy/gritz/commit/b27297b9f72e34460b359b5b7be99ae64db799ef)), closes [#717](https://github.com/icholy/gritz/issues/717)
* **webui:** wire CreateTaskAction into routing-rule editor ([#749](https://github.com/icholy/gritz/issues/749)) ([b0cd154](https://github.com/icholy/gritz/commit/b0cd154bb4424a968c30e4f1a6401630322465e7)), closes [#717](https://github.com/icholy/gritz/issues/717)


### Bug Fixes

* **deps:** update module github.com/xsam/otelsql to v0.42.0 ([#726](https://github.com/icholy/gritz/issues/726)) ([33c0645](https://github.com/icholy/gritz/commit/33c064592e5433b6920b395430cffc6ae109bdfa))
* **deps:** update module github.com/zitadel/oidc/v3 to v3.47.5 ([#728](https://github.com/icholy/gritz/issues/728)) ([c528474](https://github.com/icholy/gritz/commit/c52847455b0ab23e8edf2b28fb17744e8ff4fb14))
* **eventrouter:** suppress wake notifications when no transition ([#748](https://github.com/icholy/gritz/issues/748)) ([caabd69](https://github.com/icholy/gritz/commit/caabd69414d8af8c9430ce96ca259716958707db))
* **mcp:** marshal channel meta as empty object when nil ([5ad5eb7](https://github.com/icholy/gritz/commit/5ad5eb702a03cdf5b8d92b66016fe55bffea7c6d))
* resume setup from failed command ([#762](https://github.com/icholy/gritz/issues/762)) ([5cc04b9](https://github.com/icholy/gritz/commit/5cc04b903981feb8ce3603a27c9f3593fcef41ee))
* **server:** there's always a url ([61577cb](https://github.com/icholy/gritz/commit/61577cbb4e06bd5bf6baaa82da17e4aa6cd0043f))
* switch mise pnpm backend to npm and bump Dockerfile to v11 ([#755](https://github.com/icholy/gritz/issues/755)) ([f290563](https://github.com/icholy/gritz/commit/f29056304add99054863b0d244acae8efc1ccd0f))
* **webui:** fold routing-rules hint into card description ([#759](https://github.com/icholy/gritz/issues/759)) ([23c69bb](https://github.com/icholy/gritz/commit/23c69bbe8cc8562c981ed9417cf9f88883cb5bc1))


### Miscellaneous

* add notification-handling guidance to orchestrator skill ([c3fcde6](https://github.com/icholy/gritz/commit/c3fcde6d1af3bf23ccedddca1cc606f4c6eabaab))
* add Task.IsTerminal() helper ([#756](https://github.com/icholy/gritz/issues/756)) ([40cbb12](https://github.com/icholy/gritz/commit/40cbb12f4d1545697223a566391ebc37de3a307a))
* **deps:** update debian docker tag to v13 ([#731](https://github.com/icholy/gritz/issues/731)) ([709af15](https://github.com/icholy/gritz/commit/709af156c8bbef6aaa9d0065cd0c6d57b7a8aac6))
* **deps:** update dependency @vitejs/plugin-react to v6 ([#732](https://github.com/icholy/gritz/issues/732)) ([bb7865b](https://github.com/icholy/gritz/commit/bb7865b11080a82b1a7c2728705a7c93e1eeccee))
* **deps:** update dependency globals to v17 ([#734](https://github.com/icholy/gritz/issues/734)) ([1eeb5a6](https://github.com/icholy/gritz/commit/1eeb5a6fed2f9aceb22ad035d7eb9a2b0d502d64))
* **deps:** update dependency node to v24 ([#735](https://github.com/icholy/gritz/issues/735)) ([1872a96](https://github.com/icholy/gritz/commit/1872a963ae103c3b70218ec37a533e0fd595d004))
* **deps:** update dependency pnpm to v11 ([#740](https://github.com/icholy/gritz/issues/740)) ([c3a0e77](https://github.com/icholy/gritz/commit/c3a0e7752517934e20704fa9bb741ff820f7d539))
* **deps:** update dependency typescript to v6 ([#750](https://github.com/icholy/gritz/issues/750)) ([c4e59c7](https://github.com/icholy/gritz/commit/c4e59c7647646ac7adbbea41aa9ede10dae2c97c))
* **deps:** update docker/build-push-action action to v7 ([#752](https://github.com/icholy/gritz/issues/752)) ([73df7ee](https://github.com/icholy/gritz/commit/73df7ee6165fa2204dd5d6d3dce86c02618eab40))
* **deps:** update docker/login-action action to v4 ([#757](https://github.com/icholy/gritz/issues/757)) ([96d3a8a](https://github.com/icholy/gritz/commit/96d3a8ab1204a56a418ec3a9a6de55582f08e147))
* **deps:** update docker/metadata-action action to v6 ([#758](https://github.com/icholy/gritz/issues/758)) ([66234ff](https://github.com/icholy/gritz/commit/66234ffda4c95fb50c22470bc9c868f52cb918cd))
* gate notification publishing on Notification.Ignore ([#753](https://github.com/icholy/gritz/issues/753)) ([64af1ee](https://github.com/icholy/gritz/commit/64af1ee5b22288f90a8c775573a5978a4b1398ef))
* move implemented proposals out of draft ([4d78311](https://github.com/icholy/gritz/commit/4d78311c104655de55543ecfe4075c1ca2b32fe2))
* **webui:** make routing-rule form source-aware ([#737](https://github.com/icholy/gritz/issues/737)) ([d92122d](https://github.com/icholy/gritz/commit/d92122d508f4f3c505c48a914cdbd9865b859035))
* **webui:** move routing rule editor to dedicated routes ([#730](https://github.com/icholy/gritz/issues/730)) ([8c48b68](https://github.com/icholy/gritz/commit/8c48b683746e338f6a706935f621e2f93b0e52e7))
* **webui:** promote Events to top-level nav page ([#733](https://github.com/icholy/gritz/issues/733)) ([6d831c9](https://github.com/icholy/gritz/commit/6d831c9852598b13f8f7a2116e050a5d51423dd2))

## [0.21.0](https://github.com/icholy/gritz/compare/v0.20.0...v0.21.0) (2026-05-30)


### Features

* **mcp:** channel-message-gated notifications ([#725](https://github.com/icholy/gritz/issues/725)) ([88720f0](https://github.com/icholy/gritz/commit/88720f0860448433c3d5e309f8f0b18b2f5dccce))


### Bug Fixes

* **deps:** update module connectrpc.com/connect to v1.20.0 ([#723](https://github.com/icholy/gritz/issues/723)) ([ceccde5](https://github.com/icholy/gritz/commit/ceccde558a84d583fc680d6a30d30153043a8d10))
* **deps:** update module github.com/modelcontextprotocol/go-sdk to v1.6.1 ([#724](https://github.com/icholy/gritz/issues/724)) ([9648682](https://github.com/icholy/gritz/commit/9648682ef47b6c1dbace485c9bc6d52b524c6e65))
* **mcp:** suppress self-notifications in the bridge ([#719](https://github.com/icholy/gritz/issues/719)) ([ed23ade](https://github.com/icholy/gritz/commit/ed23aded75537056256537bb1addc9f3dfed9880))


### Miscellaneous

* **deps:** update tanstack-query monorepo to v5.100.14 ([#715](https://github.com/icholy/gritz/issues/715)) ([1a45dd8](https://github.com/icholy/gritz/commit/1a45dd84e0b3ef02c0a44bb3fda8d8f05bc3ba5d))
* keep orchestrator task instructions lean ([6b320a5](https://github.com/icholy/gritz/commit/6b320a58ccf43cb84c4cbf12d9f3955d0c269967))
* throttle tanstack renovate updates to monthly ([#721](https://github.com/icholy/gritz/issues/721)) ([e372f49](https://github.com/icholy/gritz/commit/e372f49d721e87106dba4f0d6215d054e60a58b4))

## [0.20.0](https://github.com/icholy/gritz/compare/v0.19.1...v0.20.0) (2026-05-30)


### Features

* add gritz mcp local stdio command ([#708](https://github.com/icholy/gritz/issues/708)) ([5e34cc0](https://github.com/icholy/gritz/commit/5e34cc0261dab439dd9209bfe81abb89e87e785b))
* **mcp:** push task changes as Claude Code channel events ([#713](https://github.com/icholy/gritz/issues/713)) ([2b74513](https://github.com/icholy/gritz/commit/2b7451306d12d3ed777ff66e7e982d93f38bd58c))
* **webui:** move events into a settings tab ([#710](https://github.com/icholy/gritz/issues/710)) ([346835f](https://github.com/icholy/gritz/commit/346835f1ddcee8c3965c7579d19df60131f5d740))


### Bug Fixes

* **runner:** wake main loop when a concurrency slot frees ([#711](https://github.com/icholy/gritz/issues/711)) ([b3c9be3](https://github.com/icholy/gritz/commit/b3c9be374b57e670c9ce4d6b757359c215cb0e8b))
* **webui:** remove Recent Events card description ([b701b62](https://github.com/icholy/gritz/commit/b701b622154f6ece7707d9d265ec064473df72f6))


### Miscellaneous

* add gritz-orchestrator skill ([ec4c2ac](https://github.com/icholy/gritz/commit/ec4c2acd988112a12848c7f91df78eec17af013d))
* extract shared SSE notification subscriber into gritzclient ([#714](https://github.com/icholy/gritz/issues/714)) ([361109a](https://github.com/icholy/gritz/commit/361109a28c2dbcdfec9bae149b5c2dd58d9f34ca))
* extract wakeup.Chan coalescing wake-up signal ([#712](https://github.com/icholy/gritz/issues/712)) ([b11174d](https://github.com/icholy/gritz/commit/b11174d48dc639a1694fe972303f5a05f9306920))
* move agent mcp server under tool subcommand ([#705](https://github.com/icholy/gritz/issues/705)) ([12a4fdb](https://github.com/icholy/gritz/commit/12a4fdb09c62d3881ff08f078e67d2a2356d4672))
* organize the proposals ([e90ef2f](https://github.com/icholy/gritz/commit/e90ef2f81d69edc4982dd397bc1e327b7c6fee03))
* remove redundant TestRunnerWakeChannel ([a7a8cc8](https://github.com/icholy/gritz/commit/a7a8cc892cfd8c78641e1b8b544fc16544747497))
* rework channels proposal around local mcp bridge ([#706](https://github.com/icholy/gritz/issues/706)) ([bed15d1](https://github.com/icholy/gritz/commit/bed15d1f1ef2b5577c8bccb314337f8332d02052))

## [0.19.1](https://github.com/icholy/gritz/compare/v0.19.0...v0.19.1) (2026-05-30)


### Miscellaneous

* **deps:** update dependency @types/react to v19.2.15 ([#699](https://github.com/icholy/gritz/issues/699)) ([5f79162](https://github.com/icholy/gritz/commit/5f79162ecf6cfa9a1272c80b05e6de31f85618b0))
* **deps:** update dependency date-fns to v4.2.1 ([#700](https://github.com/icholy/gritz/issues/700)) ([d3ce37a](https://github.com/icholy/gritz/commit/d3ce37a847e40678a63d34d6214744bca37ea690))
* **deps:** update dependency date-fns to v4.3.0 ([#702](https://github.com/icholy/gritz/issues/702)) ([d858aba](https://github.com/icholy/gritz/commit/d858ababbb5c2dcb87e3b2d454d2e254da2580b1))
* **deps:** update dependency typescript-eslint to v8.59.4 ([#695](https://github.com/icholy/gritz/issues/695)) ([d857057](https://github.com/icholy/gritz/commit/d8570577de79d141c3bb86381aa867063b18d104))
* **deps:** update tanstack-query monorepo to v5.100.11 ([#696](https://github.com/icholy/gritz/issues/696)) ([1b0943a](https://github.com/icholy/gritz/commit/1b0943a47f3757ac508b4467f13196b5867bdb3c))
* **deps:** update tanstack-query monorepo to v5.100.13 ([#701](https://github.com/icholy/gritz/issues/701)) ([81fd1ac](https://github.com/icholy/gritz/commit/81fd1ac0ea0c34f1f93933ec16baac25496ed2f3))
* **deps:** update tanstack-router monorepo ([#689](https://github.com/icholy/gritz/issues/689)) ([994c5ea](https://github.com/icholy/gritz/commit/994c5ea6f6f59536f90dee0af248e9cb7f35cdfe))
* **deps:** update tanstack-router monorepo ([#697](https://github.com/icholy/gritz/issues/697)) ([47c9fd8](https://github.com/icholy/gritz/commit/47c9fd8e6ba603ff6b621f2305b2869c899a9bd2))
* **deps:** update tanstack-router monorepo ([#698](https://github.com/icholy/gritz/issues/698)) ([bc36594](https://github.com/icholy/gritz/commit/bc36594c3f93e303f0f6757b78d5c8cf9ac9992f))

## [0.19.0](https://github.com/icholy/gritz/compare/v0.18.0...v0.19.0) (2026-05-24)


### Features

* **runner:** wake on SSE task notifications ([#680](https://github.com/icholy/gritz/issues/680)) ([ec664dd](https://github.com/icholy/gritz/commit/ec664ddc2699994a6b1aaf7a67c5a6c06667dc02))
* **server:** route actionable task notifications to runners ([#677](https://github.com/icholy/gritz/issues/677)) ([31bcecf](https://github.com/icholy/gritz/commit/31bcecf257f4036c6697976c254bdbdbfc8b6d19))


### Bug Fixes

* **runner:** bind-mount socket parent dir so it survives runner restart ([#686](https://github.com/icholy/gritz/issues/686)) ([e00b186](https://github.com/icholy/gritz/commit/e00b18697ec886d76106ea395eaa8f8a189946b1))
* **store:** write timestamps in UTC ([#679](https://github.com/icholy/gritz/issues/679)) ([5db29d5](https://github.com/icholy/gritz/commit/5db29d59eda509f7dea562580c31e6e8d5e61b93))


### Miscellaneous

* move runner-sse-events proposal to accepted ([e5a2f7c](https://github.com/icholy/gritz/commit/e5a2f7c852dec139b29eb69f9dddabc0c8de406a))
* **notifyserver:** exercise bearer and cookie auth paths for /events ([186880f](https://github.com/icholy/gritz/commit/186880f4ca6c7303e9b15b84d676d0d202d375b7))
* **server:** move store-backed auth adapters into server package ([#685](https://github.com/icholy/gritz/issues/685)) ([6466efc](https://github.com/icholy/gritz/commit/6466efcaa45cd35a8cf12fbac74c17ff02e16a5f)), closes [#683](https://github.com/icholy/gritz/issues/683)

## [0.18.0](https://github.com/icholy/gritz/compare/v0.17.0...v0.18.0) (2026-05-23)


### Features

* allow accessing different org in different tabs ([#658](https://github.com/icholy/gritz/issues/658)) ([e1ef73d](https://github.com/icholy/gritz/commit/e1ef73d513e59dd25009ee991d26f933f1e7af55))
* auto-archive tasks after configurable timeout ([#655](https://github.com/icholy/gritz/issues/655)) ([93fc393](https://github.com/icholy/gritz/commit/93fc3934da035953e5717ed72c201a16e5931355))
* **runner:** auto-repair stale container network attachments ([#666](https://github.com/icholy/gritz/issues/666)) ([6a894c4](https://github.com/icholy/gritz/commit/6a894c466c0bc83b42fccf9af2d4cc50fd10d79b))
* **runner:** gate child task operations behind workspace scopes ([#672](https://github.com/icholy/gritz/issues/672)) ([204b5dc](https://github.com/icholy/gritz/commit/204b5dc071f10a921e80def79f718916923ae37c))
* **runner:** gate github token issuance behind workspace scopes ([#669](https://github.com/icholy/gritz/issues/669)) ([38521f1](https://github.com/icholy/gritz/commit/38521f1f0a76ea9840a192368bf085d7a861ec54))


### Bug Fixes

* **agent:** only mention child task tools when scope is granted ([#674](https://github.com/icholy/gritz/issues/674)) ([9b1e086](https://github.com/icholy/gritz/commit/9b1e086c20b2252bbe4091d615f2502b02c8cef8))
* explicitly pass org query parameter in all links ([4169031](https://github.com/icholy/gritz/commit/41690317537c2051e8839f85cb39cc965c441b49))
* guard nil github server in CreateGitHubToken ([#667](https://github.com/icholy/gritz/issues/667)) ([a7183ec](https://github.com/icholy/gritz/commit/a7183ec0b3f2c1e5f78fda0e5812d8fdd2803b0c))
* normalize escaped newlines in GitHub App private key ([#665](https://github.com/icholy/gritz/issues/665)) ([163015d](https://github.com/icholy/gritz/commit/163015def2a6dd932c54d01991f610ce9c9aea10))
* parse github private key in githubserver.New ([#663](https://github.com/icholy/gritz/issues/663)) ([64ff92d](https://github.com/icholy/gritz/commit/64ff92d13f020486e158aad9357092389b231eab)), closes [#660](https://github.com/icholy/gritz/issues/660)
* pass org query param in navigate/redirect calls ([#664](https://github.com/icholy/gritz/issues/664)) ([f9a645b](https://github.com/icholy/gritz/commit/f9a645b788940f759e75b1ce43e7b320c7c0db36))
* replace dotenv output format with jq expression to fix newline issue ([0ab9366](https://github.com/icholy/gritz/commit/0ab936688a43632efc6b75eb2b23ba9164a6d589))


### Miscellaneous

* add playwrite mcp dir to .gitignore ([247ac84](https://github.com/icholy/gritz/commit/247ac842b173e4c7d8f70ba765568340353778bd))
* **apiauth:** inline validateAppToken into authenticate ([#676](https://github.com/icholy/gritz/issues/676)) ([62d1b7f](https://github.com/icholy/gritz/commit/62d1b7f03420cbede1983e63557ca51469015050))
* **deps:** update dependency @tanstack/router-vite-plugin to v1.167.5 ([#671](https://github.com/icholy/gritz/issues/671)) ([ff93dc8](https://github.com/icholy/gritz/commit/ff93dc8f825d45f9f7ad04655cfea00a4aa96d78))
* **deps:** update tanstack-router monorepo ([#659](https://github.com/icholy/gritz/issues/659)) ([95324ca](https://github.com/icholy/gritz/commit/95324caadced5c382e817b691997c4b2e75d535b))
* drop X-Auth-Type header dispatch ([#673](https://github.com/icholy/gritz/issues/673)) ([a5ba15e](https://github.com/icholy/gritz/commit/a5ba15ef55aa4aaae4f006e10f235f27b4ad654d))
* move implemented proposals to accepted ([6efb5ab](https://github.com/icholy/gritz/commit/6efb5ab422a5f640ed9d2859f62e24f2292e7524))
* move simplify-bearer-auth-dispatch proposal to accepted ([#675](https://github.com/icholy/gritz/issues/675)) ([0260c86](https://github.com/icholy/gritz/commit/0260c862775218fa935becd503f1fd98d517b595))
* remove debug log ([1b2c72a](https://github.com/icholy/gritz/commit/1b2c72aa67306fabdf997991170016f9a0e184c4))
* remove debug log ([cd8aabf](https://github.com/icholy/gritz/commit/cd8aabf60378417e27c24d14b0f3cce5b386b210))
* remove gritz setup command and device flow ([#668](https://github.com/icholy/gritz/issues/668)) ([ec53ce2](https://github.com/icholy/gritz/commit/ec53ce2008ee06f8e85a28bf47eaeb706fc62e1c))
* remove zitadel bearer middleware from apiauth ([#670](https://github.com/icholy/gritz/issues/670)) ([d4e6bea](https://github.com/icholy/gritz/commit/d4e6beae63feaa9e44f82d3adf2e21d551efdd75))
* run formatter ([6efc665](https://github.com/icholy/gritz/commit/6efc6652cf62eb7f92712ae3393029b92af1cf3b))

## [0.17.0](https://github.com/icholy/gritz/compare/v0.16.0...v0.17.0) (2026-05-23)


### Features

* add get_github_token agent MCP tool ([#651](https://github.com/icholy/gritz/issues/651)) ([7f73c36](https://github.com/icholy/gritz/commit/7f73c365f72e8ba5c37c5da09d6bb904bfd658b7))
* add gritz tool github-mcp subcommand ([#653](https://github.com/icholy/gritz/issues/653)) ([06a9d3a](https://github.com/icholy/gritz/commit/06a9d3a658af85b883f5ccd5aba94ab36fb35a27))


### Bug Fixes

* **deps:** update module github.com/urfave/cli/v3 to v3.9.0 ([#648](https://github.com/icholy/gritz/issues/648)) ([cc74fb9](https://github.com/icholy/gritz/commit/cc74fb9e06317d92ea4508096ddeaf7e5abaec91))
* inject release tag as version at build time ([48df1dd](https://github.com/icholy/gritz/commit/48df1dd37f9aab1762bf00a1a77aa5f16ffce211))
* use the transport onOrgChange to update the org query ([63596c2](https://github.com/icholy/gritz/commit/63596c27ea05eba62a30976a716dbd80c904150b))


### Miscellaneous

* **deps:** update tanstack-router monorepo ([#650](https://github.com/icholy/gritz/issues/650)) ([33959d7](https://github.com/icholy/gritz/commit/33959d7d4aecb862851bb7bbcaf81e0efc61dc79))
* group in-container helpers under a tool namespace ([8d128d0](https://github.com/icholy/gritz/commit/8d128d0ae596ed5536ff5e12401ce40b57dbd25d))
* use gotest.tools assert in mcpswap tests ([#652](https://github.com/icholy/gritz/issues/652)) ([5c4cea1](https://github.com/icholy/gritz/commit/5c4cea1cebff1aea3d67ff3d5f9c5d199c69b060))
* vendor mcpswap single-upstream MCP adapter ([14b5be6](https://github.com/icholy/gritz/commit/14b5be6533b2797302918f7e269107465f63e480))

## [0.16.0](https://github.com/icholy/gritz/compare/v0.15.4...v0.16.0) (2026-05-23)


### Features

* add ?org= query param for deep-link org routing ([#643](https://github.com/icholy/gritz/issues/643)) ([8b1bb2a](https://github.com/icholy/gritz/commit/8b1bb2a1bbcce172a4793407cd07633336aa342d))
* show server version in settings page footer ([#646](https://github.com/icholy/gritz/issues/646)) ([44eb88e](https://github.com/icholy/gritz/commit/44eb88ea39294b612492fa5d3bb592aeb2a71974))


### Bug Fixes

* **deps:** update module github.com/docker/cli to v29.5.0+incompatible ([#625](https://github.com/icholy/gritz/issues/625)) ([807045e](https://github.com/icholy/gritz/commit/807045eee8c68786d1c82214c87777c66bb504af))


### Miscellaneous

* **deps:** update module github.com/matryer/moq to v0.7.1 ([#630](https://github.com/icholy/gritz/issues/630)) ([de997e4](https://github.com/icholy/gritz/commit/de997e4c38f1476d58bfe9ae9d5431ef1b393ef6))
* **deps:** update module github.com/sqlc-dev/sqlc to v1.31.1 ([#631](https://github.com/icholy/gritz/issues/631)) ([7c7b543](https://github.com/icholy/gritz/commit/7c7b543c05dcb9d5db7886f7ffa6fbf3fb55026e))
* **deps:** update module golang.org/x/tools to v0.45.0 ([#632](https://github.com/icholy/gritz/issues/632)) ([d14c826](https://github.com/icholy/gritz/commit/d14c82696dde684c66886f257c7717c27ea4a0f4))
* **deps:** update tailwindcss monorepo to v4.3.0 ([#640](https://github.com/icholy/gritz/issues/640)) ([fe3432d](https://github.com/icholy/gritz/commit/fe3432d1314ce3b6edf538d20d22eeca48fb051a))
* **deps:** update tanstack-query monorepo to v5.100.10 ([#642](https://github.com/icholy/gritz/issues/642)) ([bfd5568](https://github.com/icholy/gritz/commit/bfd55687218a6439d6682c162098a4bd57a6dce4))
* **deps:** update tanstack-router monorepo ([#644](https://github.com/icholy/gritz/issues/644)) ([607b2bf](https://github.com/icholy/gritz/commit/607b2bffed25dcd56873a1f27cbea14216fe6c8e))
* fix linting errors ([08d90dd](https://github.com/icholy/gritz/commit/08d90dd6723a6214d339e6ebad185db3c8d0cbbf))
* remind agents to run pnpm lint on webui changes ([7ec88e2](https://github.com/icholy/gritz/commit/7ec88e2d2c62034f853563ed287ad7e39ffde69a))
* run eslint on webui PRs ([74d79bf](https://github.com/icholy/gritz/commit/74d79bf052b9d59262a2cc347f2018afdfa46812))
* schedule docker/cli updates weekly ([#641](https://github.com/icholy/gritz/issues/641)) ([6bd5ad9](https://github.com/icholy/gritz/commit/6bd5ad92ba6249c90d99cb762734d6f9dd71a27f))
* throttle @typescript/native-preview to weekly updates ([#628](https://github.com/icholy/gritz/issues/628)) ([f205845](https://github.com/icholy/gritz/commit/f20584523651067bcbfe878134a449df711db3ed))
* **webui:** add prettier ([#645](https://github.com/icholy/gritz/issues/645)) ([08b3ab9](https://github.com/icholy/gritz/commit/08b3ab943e8689332e961eb5a172f1250f84a28a))

## [0.15.4](https://github.com/icholy/gritz/compare/v0.15.3...v0.15.4) (2026-05-21)


### Bug Fixes

* return 401 for invalid Bearer tokens ([#624](https://github.com/icholy/gritz/issues/624)) ([de01986](https://github.com/icholy/gritz/commit/de019863e56d5a73da3f44d0b192f991238ab593))


### Miscellaneous

* **deps:** update dependency @typescript/native-preview to v7.0.0-dev.20260514.1 ([#620](https://github.com/icholy/gritz/issues/620)) ([98e6611](https://github.com/icholy/gritz/commit/98e6611ba0965ba347843725e165939a348d8014))

## [0.15.3](https://github.com/icholy/gritz/compare/v0.15.2...v0.15.3) (2026-05-20)


### Bug Fixes

* remove MCP hello middleware that broke n8n clients ([#619](https://github.com/icholy/gritz/issues/619)) ([359bd77](https://github.com/icholy/gritz/commit/359bd77a9ceeea8148840c42c811eaf2469d35fe))
* remove nested code tags from MCP landing page pre blocks ([#617](https://github.com/icholy/gritz/issues/617)) ([bf9c49b](https://github.com/icholy/gritz/commit/bf9c49ba6a54a3225fde8184a3fd9cbb2aabc713))

## [0.15.2](https://github.com/icholy/gritz/compare/v0.15.1...v0.15.2) (2026-05-20)


### Bug Fixes

* scope runner docker operations by runner id ([#615](https://github.com/icholy/gritz/issues/615)) ([63b0f28](https://github.com/icholy/gritz/commit/63b0f289f860d1e296a1ffbae59ceb966ef77945))

## [0.15.1](https://github.com/icholy/gritz/compare/v0.15.0...v0.15.1) (2026-05-20)


### Bug Fixes

* **deps:** update module github.com/docker/cli to v29.4.3+incompatible ([#611](https://github.com/icholy/gritz/issues/611)) ([7f8c513](https://github.com/icholy/gritz/commit/7f8c513c5d83060a9cfeee6c871e9b886b331e70))
* move agent unix socket to /gritz.sock ([#614](https://github.com/icholy/gritz/issues/614)) ([f90ad22](https://github.com/icholy/gritz/commit/f90ad22df40db5146855d0de47814288f8b79d20))


### Miscellaneous

* **deps:** update dependency @types/node to v24.12.4 ([#609](https://github.com/icholy/gritz/issues/609)) ([236f973](https://github.com/icholy/gritz/commit/236f97305dea3da404a9fa9be37b52ebe833d87e))
* **deps:** update dependency @typescript/native-preview to v7.0.0-dev.20260512.1 ([#612](https://github.com/icholy/gritz/issues/612)) ([5762cc5](https://github.com/icholy/gritz/commit/5762cc56ffd4d8a9cee583fb7883f84d7216977b))
* **deps:** update dependency @typescript/native-preview to v7.0.0-dev.20260513.1 ([#613](https://github.com/icholy/gritz/issues/613)) ([00d81b1](https://github.com/icholy/gritz/commit/00d81b11b77a744f14e775195f9b52afcf7354d0))
* **deps:** update dependency tailwind-merge to v3.6.0 ([#606](https://github.com/icholy/gritz/issues/606)) ([58ba3f4](https://github.com/icholy/gritz/commit/58ba3f43e6f73418cd1cccc9a461df72932fb5e9))
* **deps:** update dependency typescript-eslint to v8.59.3 ([#608](https://github.com/icholy/gritz/issues/608)) ([e6cbdfe](https://github.com/icholy/gritz/commit/e6cbdfe17752ce14610554f997519985d7d55798))
* **deps:** update module github.com/bufbuild/buf to v1.69.0 ([#610](https://github.com/icholy/gritz/issues/610)) ([1f0160f](https://github.com/icholy/gritz/commit/1f0160f24a3d25a5901222793eca5f464f581fd9))

## [0.15.0](https://github.com/icholy/gritz/compare/v0.14.1...v0.15.0) (2026-05-18)


### Features

* add CreateGitHubToken RPC for GitHub App installation tokens ([#594](https://github.com/icholy/gritz/issues/594)) ([b8b9c3d](https://github.com/icholy/gritz/commit/b8b9c3dda544f080f09b3dfc734a146111fe148a))
* add git-credential subcommand for GitHub App installation tokens ([#603](https://github.com/icholy/gritz/issues/603)) ([2e0fc6a](https://github.com/icholy/gritz/commit/2e0fc6abb51484e96488f25a210205b836bc922f))


### Bug Fixes

* wire git-credential helper through agent filter and env token ([#605](https://github.com/icholy/gritz/issues/605)) ([dc3369e](https://github.com/icholy/gritz/commit/dc3369e2ef05ebe7eac6932261b79968a9464cc3))


### Miscellaneous

* **deps:** update dependency @vitejs/plugin-react to v5.2.0 ([#599](https://github.com/icholy/gritz/issues/599)) ([595fb8d](https://github.com/icholy/gritz/commit/595fb8d324431324e7e27f0d80ff1cca0ba73e6d))
* **deps:** update dependency eslint-plugin-react-refresh to ^0.5.0 ([#604](https://github.com/icholy/gritz/issues/604)) ([8e6e05e](https://github.com/icholy/gritz/commit/8e6e05e6551fdc16fe68ff484667e7d5a879e792))

## [0.14.1](https://github.com/icholy/gritz/compare/v0.14.0...v0.14.1) (2026-05-18)


### Bug Fixes

* change GitHub App install label to 'Installed' in org settings ([#598](https://github.com/icholy/gritz/issues/598)) ([8d09553](https://github.com/icholy/gritz/commit/8d09553e4daf99691336b9ffbc43b090520db4c5))


### Miscellaneous

* **deps:** update dependency @bufbuild/protoc-gen-es to v2.12.0 ([#592](https://github.com/icholy/gritz/issues/592)) ([021651c](https://github.com/icholy/gritz/commit/021651c28ff811cc198ff5e3efd11876d65a923c))
* **deps:** update dependency @types/node to v24.12.3 ([#595](https://github.com/icholy/gritz/issues/595)) ([f2625ab](https://github.com/icholy/gritz/commit/f2625abd89b62eefece80971a00eeaf102bb02a2))
* **deps:** update dependency @typescript/native-preview to v7.0.0-dev.20260511.1 ([#596](https://github.com/icholy/gritz/issues/596)) ([98d7caf](https://github.com/icholy/gritz/commit/98d7caf14af37e4e86c1e9c6055e88ead7b47671))

## [0.14.0](https://github.com/icholy/gritz/compare/v0.13.2...v0.14.0) (2026-05-17)


### Features

* show GitHub App install state in org settings ([#593](https://github.com/icholy/gritz/issues/593)) ([1489532](https://github.com/icholy/gritz/commit/148953258282b1ea28f54841be1d0f7517b73321))


### Bug Fixes

* check out tag in deploy job so flyctl sees fly.toml ([28abffb](https://github.com/icholy/gritz/commit/28abffbadfb29230cc3ddbb4861589099057b8a0))


### Miscellaneous

* **deps:** update dependency @bufbuild/protobuf to v2.12.0 ([#591](https://github.com/icholy/gritz/issues/591)) ([4209bad](https://github.com/icholy/gritz/commit/4209bad2cb810337cfb4e15765466c46e7ddd401))
* extract driver logic into agent package ([#590](https://github.com/icholy/gritz/issues/590)) ([68d6b37](https://github.com/icholy/gritz/commit/68d6b37f1299b559d7541a3a77e25f645dcb3e27))
* keep failed deploys visible as failures in deployment history ([2f12982](https://github.com/icholy/gritz/commit/2f12982e0251a95ec867b6532909eaba48ebf2f3))
* simplify github setup confirmation screen ([62e58ed](https://github.com/icholy/gritz/commit/62e58edbbf96088485b99a1246c0e55e2f8ea170))

## [0.13.2](https://github.com/icholy/gritz/compare/v0.13.1...v0.13.2) (2026-05-17)


### Bug Fixes

* bump dockerfile go to 1.26 and node to 25 ([c915954](https://github.com/icholy/gritz/commit/c9159541ea5f66acbb6e1c65f9eeaf19d2ba3b30))

## [0.13.1](https://github.com/icholy/gritz/compare/v0.13.0...v0.13.1) (2026-05-17)


### Miscellaneous

* allow any scope in conform conventional commit policy ([a7a57fd](https://github.com/icholy/gritz/commit/a7a57fd6e75e1a008722083dc14ea36e5c18b0c3))
* combine release-please into release.yml, split deploy ([5b38cc4](https://github.com/icholy/gritz/commit/5b38cc4666207005231cd3e9ab12e342902289ff))
* **deps:** update alpine docker tag to v3.23 ([#585](https://github.com/icholy/gritz/issues/585)) ([ff44217](https://github.com/icholy/gritz/commit/ff442173fe1d4d8258dd210fade2e7644543117d))
* **deps:** update dependency @bufbuild/buf to v1.69.0 ([#586](https://github.com/icholy/gritz/issues/586)) ([e97ecb7](https://github.com/icholy/gritz/commit/e97ecb73f721cfb90639ed7296766ebe50193446))
* **deps:** update dependency @typescript/native-preview to v7.0.0-dev.20260510.1 ([#583](https://github.com/icholy/gritz/issues/583)) ([694c435](https://github.com/icholy/gritz/commit/694c43556dea8b2524b9230ddf9c131f51642aad))
* **deps:** update react monorepo ([#584](https://github.com/icholy/gritz/issues/584)) ([eda2420](https://github.com/icholy/gritz/commit/eda2420abc0403ecc51f93998d498de7fd10ebc4))
* drop installation from manifest default_events ([df0a90a](https://github.com/icholy/gritz/commit/df0a90ae9868f35520575b9da9f14807bceac88b))
* group non-release commit types under Miscellaneous in changelog ([4886bee](https://github.com/icholy/gritz/commit/4886bee411be974b2be192682987a5e8db8fedf6))
* let renovate open PRs anytime, limit to 3 concurrent ([27062b7](https://github.com/icholy/gritz/commit/27062b7f64459d49729bfd62c518101626711300))

## [0.13.0](https://github.com/icholy/gritz/compare/v0.12.1...v0.13.0) (2026-05-17)


### Features

* allow driver to submit own runner events via AgentFilter ([#582](https://github.com/icholy/gritz/issues/582)) ([96e41df](https://github.com/icholy/gritz/commit/96e41df372069e3d850be60de85c0f814034b525))
* store GitHub App installation id on org ([#579](https://github.com/icholy/gritz/issues/579)) ([8b1ae59](https://github.com/icholy/gritz/commit/8b1ae59ee171a7673bfc16b407014d7ec6aa7626))


### Bug Fixes

* add sslmode=disable to GRITZ_DATABASE_URL ([5b5116a](https://github.com/icholy/gritz/commit/5b5116a642c39b2bf810cbf21598edee00764a3f))

# Changelog

## [v0.9.0](https://gitlab.com/phpboyscout/go/config/-/releases/v0.9.0)

[Compare to previous version](https://gitlab.com/phpboyscout/go/config/-/compare/v0.8.0...v0.9.0)

### Features

- add the PollIntervalHinter optional interface ([493c56a](https://gitlab.com/phpboyscout/go/config/-/commit/493c56a70032d794136ce053ed70278e5b9c9a13))

## [v0.8.0](https://gitlab.com/phpboyscout/go/config/-/releases/v0.8.0)

[Compare to previous version](https://gitlab.com/phpboyscout/go/config/-/compare/v0.7.0...v0.8.0)

### Features

- add the ErrReadOnlyFS sentinel ([60ab581](https://gitlab.com/phpboyscout/go/config/-/commit/60ab581c17ffaf1ff50e7b9edd98c720bf19c9fe))

## [v0.7.0](https://gitlab.com/phpboyscout/go/config/-/releases/v0.7.0)

[Compare to previous version](https://gitlab.com/phpboyscout/go/config/-/compare/v0.6.0...v0.7.0)

### Features

- **backendconformance**: a sensitive read-only backend refuses the routed-beneath write ([0f98f10](https://gitlab.com/phpboyscout/go/config/-/commit/0f98f10e86d23954edce3b0b9256239c10e35a10))
- export the core YAML codec as YAMLCodec ([3cbe793](https://gitlab.com/phpboyscout/go/config/-/commit/3cbe793ba2d25074a71e7e19d6179ec6364da1d3))

## [v0.6.0](https://gitlab.com/phpboyscout/go/config/-/releases/v0.6.0)

[Compare to previous version](https://gitlab.com/phpboyscout/go/config/-/compare/v0.5.0...v0.6.0)

### Features

- **backendconformance**: add the shared backend-conformance suite ([828bdca](https://gitlab.com/phpboyscout/go/config/-/commit/828bdca6f4c03a164057efc1504e530640467a9c))
- **plan**: enforce Sensitive at the write path with ErrSensitiveLeak ([547ca9d](https://gitlab.com/phpboyscout/go/config/-/commit/547ca9d33dc4635aee6342ad5e761995acb30b37))

### Bug Fixes

- **deps**: bump golang.org/x/text to v0.40.0 ([94466bf](https://gitlab.com/phpboyscout/go/config/-/commit/94466bf43d157ab80c7e92ca1f27570f7bf27880))

## [v0.5.0](https://gitlab.com/phpboyscout/go/config/-/releases/v0.5.0)

[Compare to previous version](https://gitlab.com/phpboyscout/go/config/-/compare/v0.4.0...v0.5.0)

### Features

- **conformance**: add the exported conformance suite for codec adapters ([92e94e2](https://gitlab.com/phpboyscout/go/config/-/commit/92e94e263b73d0472270c1a83328fee559a97ddc))

## [v0.4.0](https://gitlab.com/phpboyscout/go/config/-/releases/v0.4.0)

[Compare to previous version](https://gitlab.com/phpboyscout/go/config/-/compare/v0.3.1...v0.4.0)

### Features

- **validate**: accept a Reader in ValidateStruct rather than a *View ([efb698f](https://gitlab.com/phpboyscout/go/config/-/commit/efb698f26fa8fac1e387a7215ead2af3b71dad7e))

## [v0.3.1](https://gitlab.com/phpboyscout/go/config/-/releases/v0.3.1)

[Compare to previous version](https://gitlab.com/phpboyscout/go/config/-/compare/v0.3.0...v0.3.1)

### Bug Fixes

- stop config.Dir leaking a descriptor per call ([a5fc5b3](https://gitlab.com/phpboyscout/go/config/-/commit/a5fc5b3a84b33756486962db4d9557ffb0088f73))

## [v0.3.0](https://gitlab.com/phpboyscout/go/config/-/releases/v0.3.0)

[Compare to previous version](https://gitlab.com/phpboyscout/go/config/-/compare/v0.2.0...v0.3.0)

### Features

- replace afero.Fs with a config.FS interface the module owns ([520dc49](https://gitlab.com/phpboyscout/go/config/-/commit/520dc4901e0205cc92881c2ed089fdac8da1b1e5))
- **watch**: coalesce foreign changes, completing D8 ([093cff4](https://gitlab.com/phpboyscout/go/config/-/commit/093cff438484b39c3b71dfc357f771519b9bfdd0))
- complete the typed read surface ([89d3f26](https://gitlab.com/phpboyscout/go/config/-/commit/89d3f26f55bf8e28c188d3bb6ebd55995a016c4a))
- **sections**: make Initial meaningful via a caller-triggered delivery ([81da208](https://gitlab.com/phpboyscout/go/config/-/commit/81da208a606310403f19fe70356adc19946450c0))
- **backend**: build the Watchable seam D11 names ([d69aaaf](https://gitlab.com/phpboyscout/go/config/-/commit/d69aaafd721f04efca24ab50fc7afb4109c852cb))
- **store**: add AddLayer, and reconcile four criteria with the code ([832d152](https://gitlab.com/phpboyscout/go/config/-/commit/832d152e53223eb792b1542166df0c9600a6489e))
- **test**: add the godog layer and fix two defects the audit found ([3d646ed](https://gitlab.com/phpboyscout/go/config/-/commit/3d646ed9517632f2e76acec89b46d3fd6620fd90))
- refuse configuration writes from inside an observer ([647853b](https://gitlab.com/phpboyscout/go/config/-/commit/647853b12441d0a2f78c9dbfc919e699dfe08bd1))
- **store**: add watching, with a polling fallback that always works ([fddcb11](https://gitlab.com/phpboyscout/go/config/-/commit/fddcb113fffbbc037f3bef9940683c5c0317d070))
- replace the viper-backed container with the Store ([1f5129c](https://gitlab.com/phpboyscout/go/config/-/commit/1f5129cbd52d2b9904c3c98ca449a79793b4bcd8))
- **store**: add environment and flag layers ([0771519](https://gitlab.com/phpboyscout/go/config/-/commit/0771519f5f55486d511a907838ce4a971bda4257))
- **store**: add the typed View over a snapshot ([69ae75e](https://gitlab.com/phpboyscout/go/config/-/commit/69ae75e0b2e03ecf4696ba7e7660b76b23be40a3))
- **store**: add Apply — comment-safe, layer-correct writes ([a10aced](https://gitlab.com/phpboyscout/go/config/-/commit/a10acedbb9386c5c488995f720d3e849937e6fa2))
- **store**: add write routing and the inspectable plan ([5696aec](https://gitlab.com/phpboyscout/go/config/-/commit/5696aec2b6610d1c139698ebb35f0915ac8e7ef4))
- **store**: add the Store and the file backend ([4895ff6](https://gitlab.com/phpboyscout/go/config/-/commit/4895ff69b53b1913ebab619e199d7a7c1763e127))
- **store**: add the immutable Snapshot ([6a2c0e6](https://gitlab.com/phpboyscout/go/config/-/commit/6a2c0e679d277837002bf6421f0698b9f00f9b0f))
- **store**: add layers, deep merge and per-key provenance ([ccaeb68](https://gitlab.com/phpboyscout/go/config/-/commit/ccaeb68af554b4421dd5021426c90bcef97af323))

### Bug Fixes

- render the document index for any multi-document source ([bc0735d](https://gitlab.com/phpboyscout/go/config/-/commit/bc0735d5f6eb4a436937455e0783e2aa462e24e9))
- order notification, and keep user values out of the value layer ([d29f461](https://gitlab.com/phpboyscout/go/config/-/commit/d29f4617a0babaaf1a09e77af075ee7b23d5a238))
- the last of the review findings ([7096bc8](https://gitlab.com/phpboyscout/go/config/-/commit/7096bc81d50b3f762d718e78099c8c83dd5d223d))
- **unmarshal**: one owner for struct-tag precedence ([d107484](https://gitlab.com/phpboyscout/go/config/-/commit/d107484060058e454a9df7fccb87e661a0ebd6f4))
- **sections**: reject a stale delivery, and settle atomically ([c2a6ae9](https://gitlab.com/phpboyscout/go/config/-/commit/c2a6ae9877ae55c79ef2caf99514866f6e47e0b7))
- **watch**: group paths by filesystem, and detect same-length edits ([880999f](https://gitlab.com/phpboyscout/go/config/-/commit/880999fe56629d511c5bcf39b49637acb381470c))
- withdraw only the failing layer, and report in-memory sources honestly ([95436b0](https://gitlab.com/phpboyscout/go/config/-/commit/95436b0a2cd09aec38cc0092d666bd8cd389dcd4))
- five more defects, including writes destroying symlinks ([911d475](https://gitlab.com/phpboyscout/go/config/-/commit/911d47554010dd6736c6b7d50d3b8a7cab19693d))
- five more defects from the recovered review candidates ([59559dc](https://gitlab.com/phpboyscout/go/config/-/commit/59559dc4393e620f9e9c60fad06db81ba9d6c50b))
- four defects the review raised but never adjudicated ([85b915d](https://gitlab.com/phpboyscout/go/config/-/commit/85b915d9ac450c1cdee1b669d4ff288fd16d040b))
- six defects found by code review ([2bcc69a](https://gitlab.com/phpboyscout/go/config/-/commit/2bcc69a2b68fe68d01dfb7455a60ea22773371f4))
- **validate**: accept typed values supplied as strings ([9d1f6b1](https://gitlab.com/phpboyscout/go/config/-/commit/9d1f6b13e5886360bfb65d2a76cc80505e9bfe44))
- **plan**: report shadowing for a target that does not exist yet ([8d1e77e](https://gitlab.com/phpboyscout/go/config/-/commit/8d1e77e59136fc644db5f3d19de03b0f0df56333))
- **store**: re-derive key-aware backends when building a candidate ([f6eba9e](https://gitlab.com/phpboyscout/go/config/-/commit/f6eba9e3ff6cadafe6ad095149b073c9e6e70cf4))
- **watch**: cover paths the operating system cannot watch ([afa46be](https://gitlab.com/phpboyscout/go/config/-/commit/afa46be41436c3d9e516f6e9d105266bef072526))
- **write**: restore the Store on rollback, and create files as block YAML ([dfda77b](https://gitlab.com/phpboyscout/go/config/-/commit/dfda77b3430be157ec041c128114fb77a6e06aeb))
- **watch**: keep native notification for rooted filesystems ([6bae1f3](https://gitlab.com/phpboyscout/go/config/-/commit/6bae1f3e5640503c466aa4fd5dcd6ab0cfa1e37d))

## [v0.2.0](https://gitlab.com/phpboyscout/go/config/-/releases/v0.2.0)

### Features

- **mocks**: rename the configmock package to mocks

## [v0.1.0](https://gitlab.com/phpboyscout/go/config/-/releases/v0.1.0)

### Features

- extract the configuration container from go-tool-base

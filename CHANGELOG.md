# Changelog

## [0.1.0](https://github.com/quebragalho-dev/quebragalho-cli/compare/v0.0.5...v0.1.0) (2026-10-28)


### Features

* **browser:** add cross-platform URL opener ([0b10cf5](https://github.com/quebragalho-dev/quebragalho-cli/commit/0b10cf529ea7c542e3749a6935b6d2e375d77dc1))
* **cli:** add qg logout and share logout core with auth logout qg ([83f5889](https://github.com/quebragalho-dev/quebragalho-cli/commit/83f5889ccf030e801ffb426fe1a0787181797ad8))
* **cli:** open browser in qg login --device; add --no-browser ([50d676d](https://github.com/quebragalho-dev/quebragalho-cli/commit/50d676d2676cb3981fdd1483fd0166f3b5c6c6b8))
* **cli:** require a QG credential to skip first-run login ([6d3ae79](https://github.com/quebragalho-dev/quebragalho-cli/commit/6d3ae7940cfc643e1e49a7b341272f595ad7ba29))
* **tui:** add /logout ([fa1e0a3](https://github.com/quebragalho-dev/quebragalho-cli/commit/fa1e0a3b1850bbe55ee46796ff05a4999cea6b3a))
* **tui:** browser device login on the first-run credentials screen ([cb1e722](https://github.com/quebragalho-dev/quebragalho-cli/commit/cb1e72291bec0f0936d692d0b6564e3512b30d87))


### Bug Fixes

* bump Go to 1.26.10 for stdlib vulnerabilities; stabilize device login tests; assert hosted runners ([d6d9264](https://github.com/quebragalho-dev/quebragalho-cli/commit/d6d926433f781eb95f1141b6fd39973309b96a32))
* **cli:** final review fixes for login/logout ([7251ba3](https://github.com/quebragalho-dev/quebragalho-cli/commit/7251ba325b23a1f1546903da804bcdca71af7dab))
* **cli:** keep legacy auth logout json fields and make qg logout atomic ([abf071b](https://github.com/quebragalho-dev/quebragalho-cli/commit/abf071be07364396fcc58af5cfd25d6581aa7441))
* **cli:** logout no longer depends on config validation ([248c5c1](https://github.com/quebragalho-dev/quebragalho-cli/commit/248c5c1ecbe90547164950965b2c8052f997eb24))
* **cli:** switch keyless active provider to QG at launch ([41ab5d4](https://github.com/quebragalho-dev/quebragalho-cli/commit/41ab5d40b4aa1e93f3213fd9f55bd2f9b893492f))
* **cli:** validate qg api key during setup before saving ([3814281](https://github.com/quebragalho-dev/quebragalho-cli/commit/3814281a250d718e75eabbee579655ae2d7aa891))
* **config:** clear stale inline key on stored-key save ([5b24994](https://github.com/quebragalho-dev/quebragalho-cli/commit/5b24994c4da76ad06acfc54b2390c02710b6c254))
* **qgaccount:** read device-flow key from access_token ([e8c655e](https://github.com/quebragalho-dev/quebragalho-cli/commit/e8c655e8a735c0f1937e9098248c4cd1ddbf848c))
* **qgaccount:** send grant_type in device token poll ([6a8774c](https://github.com/quebragalho-dev/quebragalho-cli/commit/6a8774c88463e1e4888df89714004842abc3b13b))
* **qgaccount:** use injected clock for device code expiry ([06549f7](https://github.com/quebragalho-dev/quebragalho-cli/commit/06549f76515c8ccf397b4b1d464ad637c28cf540))
* **tui:** end transcript drag on release before modal routes ([a1a72eb](https://github.com/quebragalho-dev/quebragalho-cli/commit/a1a72ebe05d7edde9570d3d9cc5f9415074b7659))
* **tui:** extend release pre-route to composer and pet drags ([0862552](https://github.com/quebragalho-dev/quebragalho-cli/commit/0862552cc6f21a1328d860b8c392ab17cbb24b84))
* **tui:** ignore buttonless motion in composer selection ([ae8ce53](https://github.com/quebragalho-dev/quebragalho-cli/commit/ae8ce533471621f090c798e39882e4ef6701801e))
* **tui:** ignore buttonless motion in pet drag ([e0c2a81](https://github.com/quebragalho-dev/quebragalho-cli/commit/e0c2a810517acfb35809d9611f67d6ea91817c27))
* **tui:** ignore buttonless motion in transcript selection ([f52a5d7](https://github.com/quebragalho-dev/quebragalho-cli/commit/f52a5d7a06ca189ff66816475701e5f0210b7457))
* **tui:** never truncate the device login URL; paste switches to key mode ([3961f40](https://github.com/quebragalho-dev/quebragalho-cli/commit/3961f4054e968bd0203050be787c63a86b6d1ff6))
* **tui:** print /logout farewell after exit and refuse while running ([24fca54](https://github.com/quebragalho-dev/quebragalho-cli/commit/24fca5424ee586adf54524f043d4ca7725612dfd))
* **tui:** warm model discovery after first-run setup ([d681f6b](https://github.com/quebragalho-dev/quebragalho-cli/commit/d681f6b4f581ff9791678ab1e7fc50d236b18ad1))

## Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
aims to follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html) once the first release is
tagged. Until then, source builds report the version `dev`.

History from the upstream Zero project lives in that repository; the Quebra
Galho lineage starts fresh at 0.0.1.

# Third-party notices

hls-monitor is built with, and its images carry, the work of others. Each
is under its own licence; the texts are in the projects named.

## In the server and the agent (Go)

| Project | Licence |
|---|---|
| [Eyevinn/hls-m3u8](https://github.com/Eyevinn/hls-m3u8) | BSD 3-Clause |
| [Eyevinn/mp4ff](https://github.com/Eyevinn/mp4ff) | MIT |
| [asticode/go-astits](https://github.com/asticode/go-astits) | MIT |
| [gin-gonic/gin](https://github.com/gin-gonic/gin) | MIT |
| [pressly/goose](https://github.com/pressly/goose) | MIT |
| [swaggo/swag](https://github.com/swaggo/swag), [gin-swagger](https://github.com/swaggo/gin-swagger), [files](https://github.com/swaggo/files) (with Swagger UI, Apache 2.0) | MIT |
| [golang.org/x/crypto](https://pkg.go.dev/golang.org/x/crypto), [net](https://pkg.go.dev/golang.org/x/net), [sys](https://pkg.go.dev/golang.org/x/sys) | BSD 3-Clause |
| [modernc.org/sqlite](https://gitlab.com/cznic/sqlite) (with SQLite, public domain) | BSD 3-Clause |

The modules these depend on are listed in `go.sum`.

## In the web interface

| Project | Licence |
|---|---|
| [hls.js](https://github.com/video-dev/hls.js) | Apache 2.0 |
| [uPlot](https://github.com/leeoniya/uPlot) | MIT |
| [Vue](https://github.com/vuejs/core), [Vue Router](https://github.com/vuejs/router), [Pinia](https://github.com/vuejs/pinia), [Vue I18n](https://github.com/intlify/vue-i18n) | MIT |
| [Manrope](https://github.com/sharanda/manrope) (font, through Fontsource) | SIL Open Font License 1.1 |
| [JetBrains Mono](https://github.com/JetBrains/JetBrainsMono) (font, through Fontsource) | SIL Open Font License 1.1 |

The packages these depend on are listed in `web/package-lock.json`.

## In the images

The images are built on [Alpine Linux](https://alpinelinux.org) and carry
its package of [FFmpeg](https://ffmpeg.org), which is under the GNU GPL as
Alpine builds it. FFmpeg is not linked into hls-monitor: it is run as a
program of its own, to decode a picture and sound.

The compose file runs [VictoriaMetrics](https://github.com/VictoriaMetrics/VictoriaMetrics)
(Apache 2.0) from its own image.

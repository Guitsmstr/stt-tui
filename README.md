# stt-tui

TUI mínima para probar en vivo el servidor STT que corre en el teléfono
(`stt-remote/1`). Es un cliente **receive-only**: se conecta al WebSocket,
muestra el estado del servicio y, cuando activas el test de audio, va
imprimiendo todo lo que el teléfono detecta y entrega.

```
┌──────────────────────────────────────────────────────────────┐
│                    STT  (wordmark 3 líneas)                  │
├───────────────────────────────┬──────────────────────────────┤
│ INFO & OPTIONS                │ OUTPUT · LIVE FEED           │
│ ● connected                   │ 22:10:02 FINAL   hola mundo  │
│ 127.0.0.1:4719 · adb          │ 22:10:03 PARTIAL cómo estás  │
│ ...                           │ ...                          │
│ ▸ Health check             h  │                              │
│   Start audio test          a │                              │
├───────────────────────────────┴──────────────────────────────┤
│ ❯ type a command… (listCommands)                             │
└──────────────────────────────────────────────────────────────┘
```

Las columnas, el campo de texto y el marco comparten bordes (un solo bloque,
con divisores internos). Todos los bordes son aguamarina diluido; el wordmark
hace un degradado aguamarina → fucsia.

## Requisitos

- Go 1.24+ (el `go.mod` fija 1.24.2; compila con toolchains más nuevos).
- `adb` con el teléfono conectado y depuración USB habilitada.
- En el teléfono: `sttd`/`stt-serve.sh` corriendo, por ejemplo:

  ```sh
  ./scripts/stt-serve.sh --engine sherpa
  ```

El servidor solo expone dos cosas (contrato `stt-remote/1`):

| Endpoint | Uso |
|---|---|
| `GET /healthz` | estado + `session` activa |
| `WS /v1/feed` | eventos `stt-events/1` en vivo (token Bearer) |

No hay endpoint para arrancar/detener la sesión: el ciclo lo controla el
teléfono. Si `hello` llega con `session:null`, la app queda esperando un
`start`; hay que revisar la captura/micrófono en el teléfono.

## Compilar y ejecutar

```sh
go build -o stt-tui .
./stt-tui
```

o:

```sh
make run
```

## Plug and play: cómo resuelve la conexión

Al arrancar, la app resuelve los datos en este orden:

1. `--config`, `./.stt-tui.json`, `stt-tui.json` o
   `~/.config/stt-tui/config.json`.
2. Variables de entorno.
3. Flags de línea de comandos.
4. **adb**: elige el dispositivo conectado, crea
   `adb forward tcp:4719 tcp:4719` (o el siguiente puerto libre) y lee el
   token del rootfs Alpine del teléfono con `adb shell su -c`.
5. Archivos locales de token (`.stt-token`, `var/etc/stt-token`, etc.).

El token del teléfono está en:

```
/data/data/com.termux/files/usr/var/lib/proot-distro/containers/alpine/rootfs/root/Documents/tools/stt/var/etc/stt-token
```

Se lee con `su` (Magisk/root). **Nunca se imprime**; en la UI solo se muestra
`64 hex (adb su)` o similar. Si adb no puede leerlo, se puede pasar a mano o
crear un config local.

Para ver qué resolvió sin abrir la TUI:

```sh
./stt-tui --print-config
```

Para una prueba de humo de health + feed (con el token real, sin mostrarlo):

```sh
./stt-tui --probe
```

## Uso de la TUI

- `Tab` alterna entre la barra de comandos y las acciones de la izquierda.
- En la izquierda: `↑`/`↓` + `Enter`, o directamente `h`, `a`, `t`, `c`, `r`.
- La barra de abajo acepta comandos; `Enter` los ejecuta.
- La columna derecha se desplaza con `PgUp`/`PgDn` o la rueda del ratón.
  Al hacer scroll hacia arriba se pausa el autoscroll; al volver al fondo se
  reanuda.

### Comandos

| Comando | Efecto |
|---|---|
| `listCommands` / `help` / `?` | muestra la lista de comandos |
| `clear` | limpia la columna de resultados |
| `onlyTranscript [on/off]` | deja solo `final`/`partial` del servidor |
| `raw [on/off]` | muestra el JSON crudo de cada evento |
| `audio [on/off]` | arranca/para el test de audio |
| `health` | consulta `/healthz` ahora |
| `connect` / `disconnect` | gestiona el WebSocket |
| `reconnect` | fuerza una conexión nueva |
| `url <ws-url>` | cambia endpoint (`host:puerto` también vale) |
| `token <token>` | cambia el Bearer token |
| `save` | guarda URL/token en `./.stt-tui.json` (0600) |
| `status` | muestra la configuración resuelta y contadores |
| `quit` / `exit` | sale |

Atajos: `Ctrl+C` salir, `Ctrl+R` health, `Ctrl+S` audio, `Ctrl+T`
onlyTranscript, `Ctrl+L` clear.

### Test de audio

`audio` se suscribe al feed y empieza a volcar eventos en la columna derecha.
Mientras esté apagado, la app mantiene la conexión y el keepalive, y actualiza
la sesión/estado, pero **no registra** `final`/`partial`. Al activarlo verás:

- `hello`: versión del servidor y sesión (`id`, engine, partials).
- `start`: formato de audio.
- `final` / `partial`: texto, tiempos `t0`/`t1` y `rtf` cuando vienen.
- `eof`, `error` y eventos desconocidos.

El servidor no tiene historial: al reconectar solo llega `start` y el texto
posterior. Si el teléfono cierra el feed tras `eof`, la app reconecta sola con
backoff (1 s → 10 s) para escuchar la siguiente sesión.

## Configuración

`./.stt-tui.json`:

```json
{
  "url": "ws://127.0.0.1:4719/v1/feed",
  "http_url": "http://127.0.0.1:4719/healthz",
  "token": "…64 hex…",
  "device": "A22_SERIAL"
}
```

`save` crea ese archivo con permisos `0600`. Está en `.gitignore`; no lo
commitees.

Variables de entorno:

| Variable | Uso |
|---|---|
| `STT_WS_URL` | endpoint WebSocket (acepta `host:puerto`) |
| `STT_HTTP_URL` | endpoint de health (si no, se deriva) |
| `STT_TOKEN` | Bearer token |
| `STT_TOKEN_FILE` | archivo local con el token |
| `STT_DEVICE` | serial de adb |
| `STT_NO_ADB=1` | desactiva autodiscovery por adb |

Flags principales: `--url`, `--token`, `--http-url`, `--device`, `--config`,
`--no-adb`, `--print-config`, `--probe`, `--version`.

## Seguridad

- El token no se loguea ni se muestra en `status`/`--print-config`; si usas el
  comando `token <token>`, el texto se ve mientras lo escribes (prefiere
  `STT_TOKEN` o el config si te preocupa el scrollback).
- `save` escribe el token en disco (`0600`); si no quieres eso, no lo uses.
- v1 del servidor no tiene TLS. Para LAN no confiable, usa `adb forward` o un
  túnel SSH; esta app usa `adb forward` por defecto.
- La app es receive-only: no envía audio ni comandos al servidor; la barra de
  texto es para comandos locales de la TUI.

## Estructura

```
main.go                     flags, --print-config, --probe, arranque Bubble Tea
internal/settings/          descubrimiento (config/env/flags/adb/token)
internal/remote/            health HTTP + dial WebSocket + parseo de eventos
internal/tui/model.go       estado, Update, comandos, reconexión, filtros
internal/tui/view.go        layout, marco unificado, colores, wordmark, formato
internal/tui/textutil.go    wrapping/padding ANSI-aware
```

## Tests

```sh
go test ./...
```

Los tests cubren la forma del layout (80×24, bordes, wordmark), el filtro
`onlyTranscript`, el modo raw, comandos, wrapping y normalización de URLs.

## Notas

- `onlyTranscript` deja pasar también la respuesta explícita de comandos
  (`listCommands`, `status`, errores de uso) para que la ayuda no quede oculta.
- La sesión la controla el teléfono. Si `health` dice `session:false`, la TUI
  no puede arrancarla; hay que iniciar `stt-serve.sh` en el chroot.
- Después de reiniciar el teléfono, el comando `save` + adb vuelve a crear el
  forward automáticamente porque la URL es loopback.

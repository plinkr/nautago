# NautaGo

[English](README.md) | Español

Cliente y herramienta de línea de comandos para el portal cautivo Nauta de
ETECSA, escrito en Go.

NautaGo es una reimplementación de [NautaPy](https://github.com/plinkr/nautapy).
Conserva los mismos comandos, las mismas opciones y el mismo comportamiento
observable, y se distribuye como un único binario estático sin dependencias en
tiempo de ejecución. La salida de la CLI está en español, como en NautaPy.

## Características

- Inicio y cierre de sesión en el portal Nauta. La sesión se guarda en disco,
  así que `nauta down` puede cerrar una sesión abierta por otro proceso.
- Varias cuentas guardadas, con usuario predeterminado y búsqueda por prefijo
  (`nauta up plinkr` encuentra `plinkr@nauta.com.cu`).
- Contraseñas cifradas en disco con AES-GCM y una clave local de 256 bits.
- Historial de conexiones del mes actual, del mes anterior o completo, y
  resumen de horas por usuario y mes.
- Consulta del tiempo restante y del crédito de la cuenta.
- `run-connected`: ejecuta un comando con la sesión abierta y la cierra al
  terminar.
- Proxy HTTP/HTTPS bajo demanda que comparte una sesión entre clientes
  simultáneos y la cierra en el límite de la unidad de facturación.
- Detecta OpenVPN y ProtonVPN, que se quedan con la tabla de rutas, y los
  detiene antes de hablar con el portal.
- Importación única de los usuarios, el usuario predeterminado, el historial y
  la sesión abierta de NautaPy.

## Requisitos

- Go 1.26 o superior para compilar.
- Sin `cgo` ni bibliotecas del sistema: SQLite lo aporta un driver en Go puro.
- Linux, macOS o Windows.
- Opcional: `sudo` y `killall` (Unix) o `taskkill` (Windows) para cerrar
  OpenVPN; `nmcli` y la CLI `protonvpn` para detectar y cerrar ProtonVPN.

## Instalación

Cada [release de GitHub](https://github.com/plinkr/nautago/releases) incluye
binarios precompilados:

| SO      | Sufijo del archivo                                     |
|---------|--------------------------------------------------------|
| Linux   | `linux_amd64`, `linux_arm64`, `linux_armv7`, `linux_armv6` |
| Windows | `windows_amd64` (`.zip`)                               |
| macOS   | `darwin_arm64` (Apple Silicon), `darwin_amd64` (Intel) |

En una Raspberry Pi use `linux_arm64` con un SO de 64 bits, `linux_armv7` para
Pi 2/3/4 con SO de 32 bits y `linux_armv6` para Pi Zero/1. Verifique las descargas
con `checksums.txt`, o con `gh attestation verify <archivo> -R plinkr/nautago`.

Con Go:

```sh
go install github.com/plinkr/nautago/cmd/nauta@latest
```

Desde el repositorio:

```sh
git clone https://github.com/plinkr/nautago.git
cd nautago
go build -o nauta ./cmd/nauta
```

## Inicio rápido

```sh
nauta users add plinkr@nauta.com.cu   # pide la contraseña
nauta up                                 # abre la sesión; Ctrl+C la cierra
```

Desde otra terminal:

```sh
nauta down
```

## Modo de uso

```text
nauta [-h] [-d] [-lc] [-lm] [-ac] [-rc] [--version] COMANDO ...
```

| Comando | Descripción |
| --- | --- |
| `up` | Abre una sesión y la mantiene hasta que se interrumpa. |
| `down` | Cierra la sesión activa. |
| `info` | Muestra la cuenta y su tiempo restante. |
| `is-online` | Indica si hay conexión a internet. |
| `is-logged-in` | Indica si hay una sesión Nauta abierta. |
| `run-connected` | Ejecuta un comando con la sesión abierta y después la cierra. |
| `users` | Gestiona las cuentas guardadas. |
| `proxy` | Proxy que abre una sesión bajo demanda. |

Todos los comandos salvo `run-connected` aceptan `-h`. Las opciones pueden ir
antes o después de los argumentos posicionales, así que
`nauta up plinkr --no-log` funciona.

### up

```text
nauta up [-t TIEMPO] [-b] [-nl] [usuario] [contraseña]
```

| Opción | Descripción |
| --- | --- |
| `-t`, `--session-time` | Cierra la sesión pasado ese tiempo. Segundos por defecto; admite los sufijos `s`, `m` y `h` (`90`, `90s`, `30m`, `2h`). |
| `-b`, `--batch` | Abre la sesión, informa el tiempo restante y termina. La sesión queda abierta hasta un `nauta down`. |
| `-nl`, `--no-log` | No registra esta conexión en el historial. |

Sin usuario explícito se usa el predeterminado y, si no hay ninguno marcado, el
primero guardado.

```sh
nauta up plinkr
nauta up -t 1h
nauta up -b plinkr
```

Sin `-b` se muestra el tiempo de conexión en la terminal; la sesión se cierra
con `Ctrl+C` o con `nauta down` desde otra terminal.

### down, is-online, is-logged-in

```sh
nauta down
nauta is-online       # Online: No
nauta is-logged-in    # Sesión activa: No
```

`is-online` es el único comando que nunca cierra un VPN, porque no habla con el
portal. Con un VPN conectado, `Online: Sí` quiere decir que hay internet y que
llega por el VPN.

### info

```text
nauta info [-c] [usuario] [contraseña]
```

```text
$ nauta info plinkr
Usuario Nauta: plinkr@nauta.com.cu
Tiempo restante: 02:14:24
```

`-c`, `--credit` añade el crédito de la cuenta:

```text
$ nauta info --credit
Usuario Nauta: plinkr@nauta.com.cu
Tiempo restante: 02:14:24
Crédito: 1.12 CUC
```

El portal solo sirve el crédito cuando no hay una sesión abierta; con la sesión
abierta se muestra el mensaje de error en su lugar.

### run-connected

```text
nauta run-connected [-u usuario] [-p contraseña] comando [argumentos...]
```

```sh
nauta run-connected -u plinkr rsync -av src/ host:dst/
```

El comando se ejecuta directamente, sin shell, así que no se expanden comodines
ni tuberías. `nauta` termina con el código de salida del comando, o con 127 si
no se pudo iniciar. La sesión se cierra aunque el comando falle o se
interrumpa. Es preferible usar la contraseña guardada antes que `-p`: los
argumentos de la línea de comandos son visibles para otros usuarios del sistema.

### users

```text
nauta users add usuario [contraseña]
nauta users set-default usuario
nauta users set-password usuario [contraseña]
nauta users remove usuario
nauta users list
```

Si no se indica la contraseña, se lee de la terminal sin eco. Si la entrada
estándar no es una terminal se lee una línea, de modo que se puede pasar por una
tubería. Al borrar el usuario predeterminado también se borra la marca, y los
comandos siguientes toman el primer usuario que quede.

### Historial de conexiones

| Opción | Descripción |
| --- | --- |
| `-lc`, `--list-conn` | Lista las conexiones del mes actual. |
| `-lm`, `--last-month` | Con `-lc`: lista las del mes anterior. |
| `-ac`, `--all-conn` | Con `-lc`: lista todas las conexiones. |
| `-rc`, `--resume-conn` | Muestra el total de horas por usuario y mes. |
| `-d`, `--debug` | Muestra información de depuración por la salida de error. |

```sh
nauta -lc
nauta -lc -lm
nauta -rc -lc -ac
```

`-lm` y `-ac` sin `-lc` son un error. `up --batch` y `run-connected` no
registran la conexión en el historial, como en NautaPy.

### proxy

```text
nauta proxy -t UNIDAD [-p PUERTO] [--host DIRECCIÓN] [-u USUARIO] [-m MÁX] [-l LOG] [--allow LISTA] [--deny LISTA]
```

| Opción | Por defecto | Descripción |
| --- | --- | --- |
| `-t`, `--time-unit` | obligatoria | Unidad mínima de facturación. Mismo formato que `up -t`. |
| `-p`, `--port` | `3128` | Puerto donde escucha. |
| `--host` | `127.0.0.1` | Dirección donde escucha. |
| `-u`, `--user` | usuario predeterminado | Cuenta con la que conectarse. |
| `-m`, `--max-conn` | sin límite | Máximo de conexiones simultáneas. |
| `-l`, `--log` | `-` (salida estándar) | Archivo de logs. |
| `--allow` | ninguna | Dominios permitidos, separados por comas. Incluye subdominios. |
| `--deny` | ninguna | Dominios bloqueados, separados por comas. Se evalúa antes que `--allow`. |

```sh
nauta proxy -t 120s
export http_proxy=http://127.0.0.1:3128
export https_proxy=http://127.0.0.1:3128
```

El proxy no autentica a quien se conecta. Con `--host 0.0.0.0`, cualquiera que
llegue al puerto navega con la cuenta de quien lo lanzó. Las sesiones que abre
el proxy quedan en el historial.

## Configuración

| Variable | Descripción |
| --- | --- |
| `NAUTAGO_HOME` | Carpeta de datos. Tiene prioridad sobre todo lo demás. |
| `XDG_DATA_HOME` | Si está definida, los datos van a `$XDG_DATA_HOME/nautago`. |

Carpeta de datos por defecto: `~/.local/share/nautago` en Unix y
`%USERPROFILE%\AppData\Roaming\nautago` en Windows. La carpeta se crea con
permisos `0700` y cada archivo con `0600`.

| Archivo | Contenido |
| --- | --- |
| `users.db` | Cuentas guardadas y usuario predeterminado (SQLite). |
| `connections.db` | Historial de conexiones (SQLite). |
| `credentials.key` | Clave con la que se cifran las contraseñas. |
| `nauta-session` | Tokens de la sesión abierta en el portal (JSON). |

Si se pierde `credentials.key`, las contraseñas guardadas no se pueden
recuperar. Los comandos que no las necesitan, como `nauta down`, siguen
funcionando. Hay que volver a escribir cada contraseña con
`nauta users set-password`, que es el único comando que crea una clave nueva en
esa situación.

## Cómo funciona

### Ciclo de vida de la sesión

`up` comprueba primero si hay internet. Si la red ya llega a internet, no abre
sesión; si el portal intercepta el sondeo, inicia sesión y escribe
`nauta-session` de forma atómica. Un archivo de sesión que el portal ya no
reconoce no bloquea un login nuevo, y solo se reemplaza cuando ese login sale
bien. Si falla, el archivo se conserva para que `nauta down` todavía pueda
cerrar una sesión que siga abierta en otra terminal.

`down` envía al portal los tokens guardados. Ante fallos de red, el cierre se
intenta hasta cinco veces. El archivo de sesión solo se borra cuando el portal
confirma el cierre: esos tokens son lo único que puede cerrar una sesión que sigue
facturando, así que un cierre fallido los conserva y `nauta down` se puede
volver a ejecutar. El historial se cierra en cualquier caso, porque el intento
ocurrió.

### VPN

OpenVPN y ProtonVPN se quedan con la tabla de rutas, y ni el portal ni el sondeo
de internet responden por la red de Nauta. Antes de cualquier petición al portal
(cierre, tiempo restante, crédito) NautaGo cierra el VPN y lo avisa.

Antes de iniciar sesión con un VPN conectado, `up`, `run-connected` y el proxy
miran el sondeo:

- Hay internet: lo da el VPN. No se abre sesión y se explica el motivo:
  `Hay una conexión activa: el internet está llegando por el VPN, no por la red
  de Nauta. Cierre el VPN para poder usar Nauta`. El VPN no se cierra, porque
  cortarle la conexión a quien está navegando sería peor que negarse.
- No llega nada: lo más probable es un túnel viejo que retiene las rutas. Se
  cierra el VPN y se intenta el login.

El sondeo prueba más de un destino, porque desde la salida de un VPN un sitio
cubano puede no responder, y eso no debe confundirse con una red muerta.

### Facturación del proxy

La primera petición abre una sesión y las simultáneas la comparten. Cuando
termina la última conexión, el proxy mantiene la sesión hasta el siguiente
múltiplo de `--time-unit` contado desde el login, porque esa fracción ya está
cobrada, y entonces la cierra. Las peticiones que llegan en ese intervalo
reutilizan la sesión. Una sesión que el proxy no abrió, por ejemplo una de
`nauta up` en otra terminal, nunca la cierra. Si el proxy se detiene antes del
límite, la sesión queda abierta y se cierra con `nauta down`.

## Migración desde NautaPy

En cada arranque, si existe `~/.local/share/nautapy` y NautaGo aún no tiene
datos, NautaGo importa los usuarios, el usuario predeterminado y el historial de
conexiones. Los archivos de NautaPy se abren en solo lectura y nunca se
modifican, así que NautaPy sigue funcionando.

- La importación se marca en la base de datos de NautaGo y ocurre una sola vez.
  Borrar la última cuenta no provoca una reimportación.
- Si NautaGo ya tiene datos, no se importa nada ni se deja marca: se avisa, y la
  importación se vuelve a intentar cuando los datos de NautaGo se muevan a otro
  sitio.
- Las filas con fechas ilegibles se descartan y se informa cuántas fueron.
- Las contraseñas se decodifican de base85 y se vuelven a cifrar con la clave
  local.
- Las marcas de tiempo guardadas en UTC se convierten a la hora local.
- Las conexiones hechas con NautaPy después de la importación no aparecen en
  NautaGo.

Una sesión abierta de NautaPy (`~/.local/share/nautapy/nauta-session`) se
traslada, no se copia, a la carpeta de NautaGo cuando este no tiene ninguna,
para que `nauta down` pueda cerrarla. Una sesión solo puede tener un
responsable de cerrarla; una copia que quedara atrás volvería a importarse
después de cada cierre. Un `nautapy up` que siga corriendo en otra terminal deja
de ver la sesión.

## Diferencias respecto de NautaPy

- Los comandos que fallan terminan con código `1`. NautaPy siempre terminaba con
  `0`.
- `run-connected` ejecuta el comando sin shell y devuelve su código de salida.
- OpenVPN se cierra con `sudo killall openvpn` (Unix) o `taskkill` (Windows) en
  lugar de un script con una ruta escrita a mano, y NautaGo espera a que el
  proceso termine.
- El VPN se cierra antes de cada petición al portal, no solo antes de cerrar
  sesión. Con un VPN arriba, el aviso final de tiempo restante de NautaPy
  fallaba.
- Con un VPN conectado, `up` distingue "internet a través del VPN" de "no llega
  nada" (ver la sección VPN).
- Un cierre fallido conserva el archivo de sesión.
- Un archivo de sesión ilegible no cuenta como sesión abierta.
- Un fallo al escribir el historial se avisa por `stderr` pero no cambia el
  código de salida.
- `nauta down` indica el usuario que está desconectando.
- Las contraseñas se cifran en lugar de codificarse en base85.
- El proxy es una implementación nueva. NautaPy solo lo describía en
  `PROXY_GUIDE.md`. Se añadieron el filtrado por dominio y la escucha en
  loopback por defecto.

## Desarrollo

```sh
gofmt -l .
go vet ./...
go test ./...
go test -race ./...
```

Las pruebas de protocolo usan `net/http/httptest` con páginas del portal
guardadas en `internal/nauta/testdata`: unas son capturas reales y otras se
escribieron siguiendo su estructura (ver su README). Ninguna prueba contacta con
el servicio de ETECSA.

Dependencias:

| Módulo | Para qué |
| --- | --- |
| `golang.org/x/net` | Analizador de HTML. El portal es una aplicación web, no una API. |
| `golang.org/x/term` | Leer contraseñas sin eco. |
| `modernc.org/sqlite` | SQLite en Go puro, compatible con las bases de datos de NautaPy. |

El driver de SQLite es la mayor parte del tamaño del binario: unos 18 MB con un
`go build` normal y 12 MB en los binarios de release, que quitan los símbolos
con `-s -w`.

Subir un tag publica un release: `.github/workflows/release.yml` ejecuta las
comprobaciones anteriores y después GoReleaser con `.goreleaser.yaml`. Los tags
con sufijo, como `v0.9.0-rc.1`, se publican como pre-release.

```sh
git tag -a v0.9.0 -m "v0.9.0"
git push origin v0.9.0
```

La versión sale solo del tag. `nauta --version` la toma de los ldflags del
release o, con `go install` y `go build`, de la versión del módulo que Go
registra en el binario.

## Estructura

```text
cmd/nauta/          punto de entrada: señales, almacén y CLI
internal/nauta/     protocolo del portal, archivo de sesión y ciclo de vida del cliente
internal/storage/   usuarios, credenciales, historial e importación de NautaPy
internal/cli/       análisis de argumentos, comandos y salida
internal/proxy/     proxy con sesión compartida y espera por facturación
internal/platform/  procesos y VPN específicos del sistema operativo
```

## Licencia

MIT. Ver [LICENSE.md](LICENSE.md).

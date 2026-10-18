# Fork de pilahito

Este repositorio es un fork de [TCNOco/TcNo-Acc-Switcher](https://github.com/TCNOco/TcNo-Acc-Switcher) (GPL-3.0, de TroubleChute / TCNOco). La licencia y los créditos originales se mantienen.

## Actualizaciones
Las copias instaladas desde este fork se actualizan **solo desde las releases de este fork** (`pilahito/TcNo-Acc-Switcher`), nunca desde las oficiales:

- `githubUpdaterConfig` apunta a `updatecheck.ReleaseRepository` (`internal/updatecheck/release_repo.go`).
- `updater-key.pub` es la clave pública del fork. Una release oficial va firmada con otra clave, así que el actualizador la rechazaría.
- Está desactivado el respaldo de la API de versiones de tcno.co, que solo conoce las versiones oficiales.

## Publicar una versión nueva
1. Fusiona los cambios en `master`.
2. Crea y sube una etiqueta con el formato `v<MAYOR>.<MENOR>.<PARCHE>-pilahito.<N>`:
   ```
   git tag v4.0.9-pilahito.2
   git push origin v4.0.9-pilahito.2
   ```
3. El workflow `Fork release (pilahito)` compila el `.exe` en Windows, lo firma para el actualizador con el secreto `UPDATER_KEY` y publica la release con `TcNo Account Switcher Installer.exe`, `TcNo-Acc-Switcher.7z`, `TcNo-Acc-Switcher.exe`, `TcNo-Acc-Switcher.exe.sig` y `SHA256SUMS`.
   - Si la etiqueta está en `master`, sale como release normal.
   - Si está en otra rama, sale como pre-release (para pruebas).

### Numeración
- `<PARCHE>` es el parche siguiente a la versión oficial en la que se basa el fork. Por ejemplo, con la base oficial 4.0.8 se usa `4.0.9-pilahito.N`.
- `N` sube en cada release del fork: `.1`, `.2`, …
- Cuando se fusione una versión oficial nueva (por ejemplo 4.0.9), se pasa a `4.0.10-pilahito.1`. Esta es mayor que cualquier `4.0.9-pilahito.N` según semver, así que el actualizador la acepta.
- Usa siempre un número mayor que el de la última release publicada. El actualizador solo instala versiones más nuevas.

## Clave de firma
La clave privada **no** está en el repositorio. Está en el secreto de Actions `UPDATER_KEY` y en una copia de seguridad fuera del repo. Si se pierde, hay que generar otra (`ssh-keygen -t ed25519`), cambiar `updater-key.pub` y el vector de `internal/updatecheck/public_key_test.go`. En ese caso, quien tenga una versión instalada tendrá que actualizar a mano una vez.

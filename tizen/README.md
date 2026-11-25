# CMediaStack on a Samsung television

A launcher ([ADR-0079](../docs/adr/0079-a-samsung-television-app.md)): the
first time it runs it asks for your server's address, then opens it. The
remote works in the server's own web interface, from v0.6.0.

It is not in Samsung's store, so it is installed from a computer.

## Once: developer mode and a certificate

1. **Developer mode.** On the television open **Apps** and enter **1 2 3 4 5**
   (the on-screen number pad, or the SmartThings app's remote). Turn
   Developer mode **On**, set **Host PC IP** to the computer's address, and
   restart the television by holding the power button.
2. **Tizen Studio** (the `web-ide` installer from
   <https://download.tizen.org/sdk/Installer/>), then from its package manager
   `package-manager-cli install --accept-license Certificate-Manager cert-add-on`.
   Both want administrator rights. If Certificate Manager does not open,
   start `tools/certificate-manager/eclipse.exe` instead.
3. **Connect:** `sdb connect <television's address>`.
4. **Certificate.** In Certificate Manager: **+**, **Samsung**, **TV**, a
   profile name (`CMediaStack` below), a new author certificate, sign in with
   a Samsung account, a new distributor certificate with the television's
   device ID in it (listed when it is connected).

## Build and install

Package a copy, so the signature files stay out of the tree:

```
cp -r tizen /tmp/cms-tv
tizen package -t wgt -s CMediaStack -- /tmp/cms-tv
tizen install -n /tmp/cms-tv/CMediaStack.wgt -s <address>:26101
tizen run -p CMediaStck.CMediaStack -s <address>:26101
```

To point it at another server, uninstall it and install it again.

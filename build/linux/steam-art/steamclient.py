#!/usr/bin/env python3
"""Small helpers run on the SteamOS box (python3 stdlib only).

  find               print "<grid dir> <appid>" for the Airwaves non-Steam shortcut
  eval <expression>  evaluate JS in Steam's SharedJSContext over CEF remote debugging
                     (needs Developer > CEF remote debugging, which Decky Loader turns on)
  apply <grid> <appid>
                     hand the art already copied into <grid> to the running Steam client so it
                     shows without a restart, and point the shortcut's icon at <appid>_icon.png

Going through Steam's own SteamClient API lets us set the shortcut icon while Steam runs,
without hand-editing shortcuts.vdf (which Steam would overwrite).
"""
import base64, glob, json, os, socket, struct, sys, urllib.request

NAME = "Airwaves"


def parse_vdf(d, i=0):
    out = {}
    while True:
        t = d[i]; i += 1
        if t == 8:
            return out, i
        j = d.index(b"\0", i); key = d[i:j].decode("utf-8", "replace"); i = j + 1
        if t == 0:
            out[key], i = parse_vdf(d, i)
        elif t == 1:
            j = d.index(b"\0", i); out[key] = d[i:j].decode("utf-8", "replace"); i = j + 1
        elif t == 2:
            out[key] = struct.unpack("<I", d[i:i + 4])[0]; i += 4
        else:
            raise ValueError(f"vdf type {t}")


def find():
    for path in glob.glob(os.path.expanduser("~/.local/share/Steam/userdata/*/config/shortcuts.vdf")):
        shortcuts = parse_vdf(open(path, "rb").read())[0].get("shortcuts", {})
        for s in shortcuts.values():
            if s.get("appname") == NAME:
                print(os.path.join(os.path.dirname(path), "grid"), s["appid"])
                return
    sys.exit(f"no {NAME} shortcut found")


def cef_eval(expr):
    targets = json.load(urllib.request.urlopen("http://127.0.0.1:8080/json", timeout=5))
    url = next(t["webSocketDebuggerUrl"] for t in targets if t["title"] == "SharedJSContext")
    hostport, path = url[len("ws://"):].split("/", 1)
    host, port = hostport.rsplit(":", 1)
    s = socket.create_connection((host, int(port)), timeout=20)
    key = base64.b64encode(os.urandom(16)).decode()
    s.sendall((f"GET /{path} HTTP/1.1\r\nHost: {hostport}\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
               f"Sec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n\r\n").encode())
    buf = b""
    while b"\r\n\r\n" not in buf:
        buf += s.recv(4096)
    head, buf = buf.split(b"\r\n\r\n", 1)
    if b" 101 " not in head.split(b"\r\n")[0]:
        sys.exit(head.decode())
    msg = json.dumps({"id": 1, "method": "Runtime.evaluate",
                      "params": {"expression": expr, "awaitPromise": True, "returnByValue": True}}).encode()
    n, mask = len(msg), os.urandom(4)
    frame = bytearray([0x81])
    if n < 126:
        frame.append(0x80 | n)
    elif n < 65536:
        frame += bytes([0x80 | 126]) + struct.pack(">H", n)
    else:
        frame += bytes([0x80 | 127]) + struct.pack(">Q", n)
    s.sendall(bytes(frame) + mask + bytes(b ^ mask[i % 4] for i, b in enumerate(msg)))

    def need(k):
        nonlocal buf
        while len(buf) < k:
            chunk = s.recv(65536)
            if not chunk:
                raise EOFError("CEF closed the socket")
            buf += chunk

    while True:
        need(2)
        op, n, off = buf[0] & 0x0F, buf[1] & 0x7F, 2
        if n == 126:
            need(4); n = struct.unpack(">H", buf[2:4])[0]; off = 4
        elif n == 127:
            need(10); n = struct.unpack(">Q", buf[2:10])[0]; off = 10
        need(off + n)
        payload, buf = buf[off:off + n], buf[off + n:]
        if op == 1:
            reply = json.loads(payload)
            if reply.get("id") == 1:
                result = reply.get("result", {})
                if "exceptionDetails" in result or "error" in reply:
                    sys.exit(json.dumps(reply))
                return result.get("result", {}).get("value")


# SetCustomArtworkForApp asset types, by grid file suffix.
ASSETS = {"p": 0, "_hero": 1, "_logo": 2, "": 3}


def apply(grid, appid):
    for suffix, kind in ASSETS.items():
        path = os.path.join(grid, f"{appid}{suffix}.png")
        if os.path.exists(path):
            data = base64.b64encode(open(path, "rb").read()).decode()
            cef_eval(f'SteamClient.Apps.SetCustomArtworkForApp({int(appid)}, "{data}", "png", {kind})')
            print(f"set {os.path.basename(path)}")
    icon = os.path.join(grid, f"{appid}_icon.png")
    if os.path.exists(icon):
        cef_eval(f"SteamClient.Apps.SetShortcutIcon({int(appid)}, {json.dumps(icon)})")
        print(f"set icon {icon}")


if __name__ == "__main__":
    if sys.argv[1:2] == ["find"]:
        find()
    elif sys.argv[1:2] == ["eval"]:
        print(json.dumps(cef_eval(sys.argv[2])))
    elif sys.argv[1:2] == ["apply"]:
        apply(sys.argv[2], sys.argv[3])
    else:
        sys.exit(__doc__)

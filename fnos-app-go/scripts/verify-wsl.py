import os, subprocess, time, urllib.request, socket, sys

D = "/tmp/goverify"
os.makedirs(D + "/var", exist_ok=True)
os.makedirs(D + "/web", exist_ok=True)
env = dict(os.environ)
env.update({
    "TRIM_PKGVAR": D + "/var",
    "GATEWAY_SOCKET": D + "/app.sock",
    "GATEWAY_PREFIX": "/app/scrcpy-remote-go",
    "GATEWAY_TARGET": "http://127.0.0.1:18012",
    "WEB_DIR": D + "/web",
})

srv = subprocess.Popen([D + "/srv", "--port", "18012", "--no-page", "--no-signal", "--no-qr"],
                       stdout=open(D + "/srv.log", "w"), stderr=subprocess.STDOUT, env=env)
time.sleep(2)
gw = subprocess.Popen([D + "/gw"], stdout=open(D + "/gw.log", "w"), stderr=subprocess.STDOUT, env=env)
time.sleep(2)

def probe(label, fn):
    try:
        print(label, "->", fn())
    except Exception as e:
        print(label, "FAILED:", e)

probe("server /api/devices", lambda: urllib.request.urlopen("http://127.0.0.1:18012/api/devices", timeout=3).read().decode())

# HTTP over unix socket via raw request
def unix_get(path):
    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    s.settimeout(3)
    s.connect(D + "/app.sock")
    s.sendall(("GET " + path + " HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n").encode())
    data = b""
    while True:
        c = s.recv(65536)
        if not c:
            break
        data += c
    s.close()
    return data.decode(errors="replace").split("\r\n")[0] + " | " + ("api" if b"[]" in data else ("html" if b"<!DOCTYPE" in data or b"<html" in data else data[:80].decode(errors='replace')))

probe("gateway /api/devices", lambda: unix_get("/app/scrcpy-remote-go/api/devices"))
probe("gateway page", lambda: unix_get("/app/scrcpy-remote-go/"))

gw.terminate(); srv.terminate()
time.sleep(1)
print("=== srv.log ==="); print(open(D + "/srv.log").read()[:500])
print("=== gw.log ==="); print(open(D + "/gw.log").read()[:500])

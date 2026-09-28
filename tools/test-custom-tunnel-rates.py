#!/usr/bin/env python3
"""Measure shared client limits against independent loopback socket observations."""

import argparse
import json
import pathlib
import socket
import subprocess
import tempfile
import threading
import time


def measure(binary, direction, rate, warmup, duration):
    stop = threading.Event()
    counter_lock = threading.Lock()
    received = 0
    errors = []
    connections = []
    threads = []
    payload = b"x" * 16384
    target = socket.socket()
    target.bind(("127.0.0.1", 0))
    target.listen()
    target.settimeout(0.1)
    with socket.socket() as reservation:
        reservation.bind(("127.0.0.1", 0))
        listen = reservation.getsockname()[1]

    def receive(conn, count):
        nonlocal received
        try:
            while not stop.is_set():
                try:
                    data = conn.recv(65536)
                except socket.timeout:
                    continue
                if not data:
                    if not stop.is_set():
                        errors.append("unexpected EOF")
                    return
                if count:
                    with counter_lock:
                        received += len(data)
        except OSError as exc:
            if not stop.is_set():
                errors.append(str(exc))

    def send(conn):
        try:
            while not stop.is_set():
                conn.sendall(payload)
        except OSError as exc:
            if not stop.is_set():
                errors.append(str(exc))

    def launch(fn, *args):
        thread = threading.Thread(target=fn, args=args, daemon=True)
        threads.append(thread)
        thread.start()

    def accept():
        while not stop.is_set():
            try:
                conn, _ = target.accept()
            except socket.timeout:
                continue
            except OSError:
                return
            conn.settimeout(2)
            connections.append(conn)
            if direction == "upload":
                launch(receive, conn, True)
            else:
                launch(send, conn)

    config = {
        "log": {"loglevel": "error"},
        "clientPolicy": {"policies": [{
            "clientId": "aggregate-measurement", "version": 1, "enabled": True,
            "multiplierMicros": 1000000, "burstBytes": 65536,
            "uploadBytesPerSecond": rate if direction == "upload" else 0,
            "downloadBytesPerSecond": rate if direction == "download" else 0,
        }]},
        "inbounds": [{"listen": "127.0.0.1", "port": listen, "protocol": "tunnel", "settings": {
            "allowedNetwork": "tcp", "rewriteAddress": "127.0.0.1",
            "rewritePort": target.getsockname()[1], "clientId": "aggregate-measurement",
        }}],
        "outbounds": [{"protocol": "freedom", "settings": {
            "finalRules": [{"action": "allow", "ip": ["127.0.0.1"]}],
        }}],
    }
    with tempfile.TemporaryDirectory(prefix="custom-xray-rates-") as directory:
        path = pathlib.Path(directory)
        state = str(path / "policy.db")
        subprocess.run([binary, "policy-init", "-file", state, "-instance", "rate-test-node"], check=True)
        config["clientPolicy"].update(stateFile=state, instanceId="rate-test-node")
        (path / "config.json").write_text(json.dumps(config))
        with (path / "core.log").open("w+") as log:
            process = subprocess.Popen([binary, "run", "-c", str(path / "config.json")], stdout=log, stderr=log)
            try:
                launch(accept)
                for _ in range(2):
                    deadline = time.monotonic() + 5
                    while True:
                        try:
                            conn = socket.create_connection(("127.0.0.1", listen), timeout=2)
                            break
                        except ConnectionRefusedError:
                            if process.poll() is not None or time.monotonic() > deadline:
                                log.seek(0)
                                raise AssertionError("core failed to listen: " + log.read())
                            time.sleep(0.02)
                    connections.append(conn)
                    if direction == "upload":
                        launch(send, conn)
                    else:
                        launch(receive, conn, True)
                started = time.monotonic()
                time.sleep(warmup)
                with counter_lock:
                    first = received
                measured_start = time.monotonic()
                time.sleep(duration)
                measured_end = time.monotonic()
                with counter_lock:
                    total = received
                assert process.poll() is None, "core exited during traffic"
                assert not errors, errors
                elapsed = measured_end - measured_start
                window = total - first
                if rate:
                    assert window <= rate * elapsed * 1.01 + 65536, ("aggregate rate exceeded", direction, rate, window, elapsed)
                    assert window >= rate * elapsed * 0.85, ("unhealthy low throughput", direction, rate, window, elapsed)
                    assert total <= rate * (measured_end - started) * 1.01 + 65536, ("startup burst exceeded", direction, rate, total)
                return {"direction": direction, "rateBytesPerSecond": rate, "connections": 2,
                        "warmupSeconds": warmup, "warmupBytes": first, "windowSeconds": elapsed,
                        "windowBytes": window, "measuredBytesPerSecond": window / elapsed,
                        "burstBytes": 65536, "corePid": process.pid}
            finally:
                stop.set()
                for conn in connections:
                    try:
                        conn.shutdown(socket.SHUT_RDWR)
                    except OSError:
                        pass
                    conn.close()
                target.close()
                process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
                for thread in threads:
                    thread.join(timeout=3)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", default="build/custom-xray")
    args = parser.parse_args()
    binary = str(pathlib.Path(args.binary).resolve())
    for direction in ("upload", "download"):
        baseline = measure(binary, direction, 0, 1, 2)
        print(json.dumps(baseline), flush=True)
        assert baseline["measuredBytesPerSecond"] > 4 * 1024 * 1024, "baseline too slow; rate validation inconclusive"
        for rate in (256 * 1024, 1024 * 1024):
            print(json.dumps(measure(binary, direction, rate, 2, 10)), flush=True)


if __name__ == "__main__":
    main()

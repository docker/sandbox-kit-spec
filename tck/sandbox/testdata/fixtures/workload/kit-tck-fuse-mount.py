#!/usr/bin/env python3
"""Mount FUSE as a non-root user through the image's setuid fusermount3.

Prints "yes" when the mount appears in the workload's mount namespace and
"no" when it does not. Exits nonzero with fusermount3's error when the
helper fails, so the suite's finding says why. No filesystem daemon serves
the mount: fusermount3 hands back the /dev/fuse descriptor, the probe
reads the workload entrypoint's mount table, and closing the descriptor
aborts the connection before the lazy unmount.
"""
import array
import os
import socket
import subprocess
import sys
import tempfile
import time

NOBODY = 65534
WORKLOAD_PID = "/var/tmp/kit-tck-workload-pid"


def receive_fd(sock):
    _, ancillary, _, _ = sock.recvmsg(1, socket.CMSG_SPACE(array.array("i").itemsize))
    for level, kind, data in ancillary:
        if level == socket.SOL_SOCKET and kind == socket.SCM_RIGHTS:
            return array.array("i", data)[0]
    raise RuntimeError("fusermount3 returned no descriptor")


def workload_pid():
    # Create may return before the entrypoint runs; it publishes the record
    # atomically, so a present file is a complete one.
    deadline = time.monotonic() + 5
    while True:
        try:
            with open(WORKLOAD_PID) as f:
                return f.read().strip()
        except FileNotFoundError:
            if time.monotonic() >= deadline:
                sys.exit(f"{WORKLOAD_PID} did not appear within 5s")
            time.sleep(0.05)


def mounted(path, pid):
    # The entrypoint's table, not this process's: a runtime that runs exec
    # in a private mount namespace would otherwise pass with a mount the
    # rest of the workload never sees.
    with open(f"/proc/{pid}/mountinfo") as f:
        table = f.read()
    for line in table.splitlines():
        fields = line.split()
        if fields[4] == path and fields[fields.index("-") + 1].split(".")[0] == "fuse":
            return True
    return False


def main():
    if os.getuid() == 0:
        # The duty is about non-root mounts, so root drops to nobody first.
        os.setgroups([])
        os.setgid(NOBODY)
        os.setuid(NOBODY)
    pid = workload_pid()
    mountpoint = tempfile.mkdtemp(prefix="kit-tck-fuse-")
    ours, theirs = socket.socketpair(socket.AF_UNIX, socket.SOCK_STREAM)
    try:
        helper = subprocess.run(
            ["fusermount3", "-o", "rw,fsname=kit-tck,subtype=kit-tck", "--", mountpoint],
            env=dict(os.environ, _FUSE_COMMFD=str(theirs.fileno())),
            pass_fds=[theirs.fileno()], capture_output=True, text=True)
        theirs.close()
        if helper.returncode != 0:
            # Nonzero, so the suite's failure carries the helper's error.
            sys.exit(f"fusermount3 exited {helper.returncode}: {helper.stderr.strip()}")
        device = receive_fd(ours)
        try:
            print("yes" if mounted(mountpoint, pid) else "no")
        finally:
            os.close(device)
            subprocess.run(["fusermount3", "-u", "-z", "--", mountpoint], capture_output=True)
    finally:
        ours.close()
        os.rmdir(mountpoint)


if __name__ == "__main__":
    main()

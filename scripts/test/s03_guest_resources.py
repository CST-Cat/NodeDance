"""Ownership checks for S03's temporary host NBD and boot-partition mount."""

from __future__ import annotations

import contextlib
import fcntl
import os
import pathlib
import re
import stat
from dataclasses import dataclass


class ResourceBusy(RuntimeError):
    """An external process or mount already owns the requested resource."""


class OwnershipLost(RuntimeError):
    """A resource no longer matches the identity captured by this run."""


@dataclass(frozen=True)
class MountIdentity:
    mount_id: int
    device: str
    target: str
    filesystem: str
    source: str


@dataclass(frozen=True)
class ProcessIdentity:
    pid: int
    start_time: str
    command: tuple[str, ...]
    tgid: int = 0


@dataclass(frozen=True)
class QemuNBDOwner:
    process: ProcessIdentity
    sysfs_thread_pid: int | None
    sysfs_thread_start_time: str | None

    @property
    def pid(self) -> int:
        return self.process.pid

    @property
    def tgid(self) -> int:
        return self.process.tgid

    @property
    def start_time(self) -> str:
        return self.process.start_time

    @property
    def command(self) -> tuple[str, ...]:
        return self.process.command


def acquire_exclusive_lock(path: pathlib.Path):
    """Return a held nonblocking lock; never unlink the lock file."""
    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor = os.open(path, os.O_CREAT | os.O_RDWR | getattr(os, "O_NOFOLLOW", 0) |
                         getattr(os, "O_CLOEXEC", 0), 0o600)
    if not stat.S_ISREG(os.fstat(descriptor).st_mode):
        os.close(descriptor)
        raise OwnershipLost(f"NBD lock path is not a regular file: {path}")
    os.fchmod(descriptor, 0o600)
    stream = os.fdopen(descriptor, "r+b", buffering=0)
    try:
        fcntl.flock(stream.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError as error:
        stream.close()
        raise ResourceBusy(f"exclusive lock is held: {path}") from error
    return stream


def _unescape_mount_field(value: str) -> str:
    return re.sub(r"\\([0-7]{3})", lambda match: chr(int(match.group(1), 8)), value)


def parse_mountinfo(contents: str) -> list[MountIdentity]:
    mounts = []
    for line in contents.splitlines():
        before, separator, after = line.partition(" - ")
        left, right = before.split(), after.split()
        if not separator or len(left) < 6 or len(right) < 2:
            raise ValueError(f"invalid mountinfo record: {line!r}")
        mounts.append(MountIdentity(
            mount_id=int(left[0]),
            device=left[2],
            target=_unescape_mount_field(left[4]),
            filesystem=right[0],
            source=_unescape_mount_field(right[1]),
        ))
    return mounts


def mount_identity_at(contents: str, target: pathlib.Path) -> MountIdentity | None:
    target_path = os.path.abspath(target)
    matches = [mount for mount in parse_mountinfo(contents) if mount.target == target_path]
    if len(matches) > 1:
        raise OwnershipLost(f"mountpoint has multiple active identities: {target_path}")
    return matches[0] if matches else None


def capture_mount_identity(contents: str, target: pathlib.Path, expected_device: str) -> MountIdentity:
    identity = mount_identity_at(contents, target)
    if identity is None:
        raise OwnershipLost(f"mount command did not create a mount at {target}")
    if identity.device != expected_device:
        raise OwnershipLost(
            f"mount at {target} has device {identity.device}, expected {expected_device}")
    return identity


def verify_mount_identity(contents: str, identity: MountIdentity) -> bool:
    current = mount_identity_at(contents, pathlib.Path(identity.target))
    return current == identity


def read_mountinfo(path: pathlib.Path = pathlib.Path("/proc/self/mountinfo")) -> str:
    return path.read_text(encoding="utf-8")


def _read_optional_integer(path: pathlib.Path, *, default: int | None = None) -> int | None:
    try:
        text = path.read_text(encoding="ascii").strip()
    except FileNotFoundError:
        return default
    try:
        return int(text)
    except ValueError as error:
        raise OwnershipLost(f"invalid integer in {path}: {text!r}") from error


def nbd_device_numbers(sysfs_dir: pathlib.Path) -> set[str]:
    numbers = set()
    for dev_file in [sysfs_dir / "dev", *sorted(sysfs_dir.glob("nbd*p*/dev"))]:
        if not dev_file.is_file():
            continue
        value = dev_file.read_text(encoding="ascii").strip()
        if not re.fullmatch(r"\d+:\d+", value):
            raise OwnershipLost(f"invalid block device identity in {dev_file}: {value!r}")
        numbers.add(value)
    if not numbers:
        raise OwnershipLost(f"no sysfs block-device identity found under {sysfs_dir}")
    return numbers


def assert_nbd_idle(
    sysfs_dir: pathlib.Path,
    mountinfo: str,
    swaps: str,
    *,
    proc_root: pathlib.Path = pathlib.Path("/proc"),
    device_path: str = "/dev/nbd0",
) -> None:
    """Fail closed if sysfs, mounts, swaps, or a qemu process show use."""
    if not sysfs_dir.is_dir():
        raise OwnershipLost(f"NBD sysfs directory is unavailable: {sysfs_dir}")
    pid_path = sysfs_dir / "pid"
    pid = _read_optional_integer(pid_path, default=None)
    if pid not in (None, 0):
        raise ResourceBusy(f"{device_path} is already owned by PID {pid}")
    size = _read_optional_integer(sysfs_dir / "size", default=None)
    if size is None or size != 0:
        raise ResourceBusy(f"{device_path} has non-idle sysfs size {size!r}")
    holders = list((sysfs_dir / "holders").iterdir())
    if holders:
        raise ResourceBusy(f"{device_path} has active holders: {', '.join(p.name for p in holders)}")

    assert_nbd_unmounted(sysfs_dir, mountinfo, swaps, device_path=device_path)
    for process in qemu_nbd_processes(proc_root, device_path):
        raise ResourceBusy(f"a qemu-nbd process already targets {device_path}: PID {process.pid}")


def assert_nbd_unmounted(
    sysfs_dir: pathlib.Path,
    mountinfo: str,
    swaps: str,
    *,
    device_path: str = "/dev/nbd0",
) -> None:
    device_numbers = nbd_device_numbers(sysfs_dir)
    for mount in parse_mountinfo(mountinfo):
        if mount.device in device_numbers:
            raise ResourceBusy(
                f"{device_path} partition {mount.device} is mounted at {mount.target}")
    for line in swaps.splitlines()[1:]:
        source = line.split()[0] if line.split() else ""
        if source == device_path or source.startswith(device_path + "p"):
            raise ResourceBusy(f"{device_path} partition is active swap: {source}")
        if source:
            try:
                source_stat = os.stat(source)
            except OSError:
                continue
            if stat.S_ISBLK(source_stat.st_mode):
                number = f"{os.major(source_stat.st_rdev)}:{os.minor(source_stat.st_rdev)}"
                if number in device_numbers:
                    raise ResourceBusy(f"{device_path} partition is active swap through alias {source}")


def process_identity(pid: int, proc_root: pathlib.Path = pathlib.Path("/proc")) -> ProcessIdentity:
    process_dir = proc_root / str(pid)
    try:
        command_data = (process_dir / "cmdline").read_bytes()
        stat_line = (process_dir / "stat").read_text(encoding="ascii")
        status = (process_dir / "status").read_text(encoding="ascii")
    except (OSError, ValueError) as error:
        raise OwnershipLost(f"cannot inspect process {pid}: {error}") from error
    command = tuple(part.decode(errors="replace") for part in command_data.split(b"\0") if part)
    closing = stat_line.rfind(")")
    if closing < 0:
        raise OwnershipLost(f"process {pid} has invalid /proc stat data")
    fields_after_comm = stat_line[closing + 1:].split()
    if len(fields_after_comm) <= 19:
        raise OwnershipLost(f"process {pid} has truncated /proc stat data")
    tgid = None
    for line in status.splitlines():
        if line.startswith("Tgid:"):
            try:
                tgid = int(line.split(":", 1)[1].strip())
            except ValueError as error:
                raise OwnershipLost(f"process {pid} has invalid Tgid status: {line!r}") from error
            break
    if tgid is None or tgid <= 0:
        raise OwnershipLost(f"process {pid} has no valid /proc status Tgid")
    return ProcessIdentity(pid=pid, start_time=fields_after_comm[19], command=command, tgid=tgid)


def _connect_device(command: tuple[str, ...]) -> str | None:
    for index, item in enumerate(command):
        if item.startswith("--connect="):
            return item.split("=", 1)[1]
        if item in {"--connect", "-c"} and index + 1 < len(command):
            return command[index + 1]
    return None


def qemu_nbd_processes(
    proc_root: pathlib.Path = pathlib.Path("/proc"),
    device_path: str = "/dev/nbd0",
) -> list[ProcessIdentity]:
    processes = []
    try:
        entries = list(proc_root.iterdir())
    except OSError as error:
        raise OwnershipLost(f"cannot enumerate processes under {proc_root}: {error}") from error
    for entry in entries:
        if not entry.name.isdigit():
            continue
        try:
            process = process_identity(int(entry.name), proc_root)
        except OwnershipLost as error:
            if not entry.exists():
                continue
            raise OwnershipLost(
                f"cannot prove whether process {entry.name} targets {device_path}: {error}") from error
        if (process.command and pathlib.Path(process.command[0]).name == "qemu-nbd" and
                _connect_device(process.command) == device_path):
            processes.append(process)
    return processes


def capture_qemu_nbd_owner(
    device_path: str,
    image_path: pathlib.Path,
    sysfs_dir: pathlib.Path,
    *,
    proc_root: pathlib.Path = pathlib.Path("/proc"),
    pid_file: pathlib.Path,
    processes_before_attach: tuple[ProcessIdentity, ...] | None = None,
) -> QemuNBDOwner:
    expected_image = str(image_path.resolve())
    matches = qemu_nbd_processes(proc_root, device_path)
    if not matches:
        raise OwnershipLost(
            f"no qemu-nbd process is visible for {device_path} and {expected_image}")
    launch_pid = read_private_pidfile(pid_file)
    owner = process_identity(launch_pid, proc_root)
    if owner.pid != owner.tgid:
        raise OwnershipLost(f"private PID file {pid_file} did not name a qemu-nbd process-group leader")
    if owner not in matches:
        raise OwnershipLost(
            f"private PID file {pid_file} names PID {owner.pid}, not a visible {device_path} qemu-nbd process")
    if (not owner.command or pathlib.Path(owner.command[0]).name != "qemu-nbd" or
            _connect_device(owner.command) != device_path or expected_image not in owner.command):
        raise OwnershipLost(f"private PID {launch_pid} does not match {device_path} and {expected_image}")
    if any(process.tgid != owner.tgid or process.command != owner.command for process in matches):
        raise OwnershipLost(f"competing qemu-nbd process group targets {device_path}")
    if processes_before_attach is not None and any(
            process.pid == owner.pid or process.tgid == owner.tgid for process in processes_before_attach):
        raise OwnershipLost(
            f"qemu-nbd PID/TGID {owner.pid} existed before this run attempted to attach {device_path}")
    size = _read_optional_integer(sysfs_dir / "size", default=None)
    if size is None or size <= 0:
        raise OwnershipLost(f"sysfs does not show an attached image on {device_path}: size={size!r}")
    pid_path = sysfs_dir / "pid"
    sysfs_pid = _read_optional_integer(pid_path, default=None)
    thread_start_time = None
    if sysfs_pid not in (None, 0):
        thread = process_identity(sysfs_pid, proc_root)
        if thread.tgid != owner.tgid or thread.command != owner.command:
            raise OwnershipLost(
                f"sysfs PID/TID {sysfs_pid} does not belong to current-run qemu-nbd TGID {owner.tgid}")
        thread_start_time = thread.start_time
    elif sysfs_pid == 0:
        raise OwnershipLost(f"sysfs PID is zero while {device_path} reports an attached image")
    return QemuNBDOwner(owner, sysfs_pid, thread_start_time)


def read_private_pidfile(path: pathlib.Path) -> int:
    descriptor = None
    try:
        descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0) |
                             getattr(os, "O_CLOEXEC", 0))
        file_stat = os.fstat(descriptor)
        with os.fdopen(descriptor, "r", encoding="ascii") as stream:
            descriptor = None
            contents = stream.read().strip()
    except OSError as error:
        raise OwnershipLost(f"cannot read current-run qemu-nbd PID file {path}: {error}") from error
    finally:
        if descriptor is not None:
            os.close(descriptor)
    if not stat.S_ISREG(file_stat.st_mode) or file_stat.st_nlink != 1:
        raise OwnershipLost(f"current-run qemu-nbd PID file is not a private regular file: {path}")
    if file_stat.st_uid != os.getuid() or stat.S_IMODE(file_stat.st_mode) & 0o077:
        raise OwnershipLost(f"current-run qemu-nbd PID file is not user-owned and private: {path}")
    try:
        pid = int(contents)
    except ValueError as error:
        raise OwnershipLost(f"current-run qemu-nbd PID file is invalid: {contents!r}") from error
    if pid <= 1:
        raise OwnershipLost(f"current-run qemu-nbd PID is invalid: {pid}")
    return pid


def verify_qemu_nbd_owner(
    owner: QemuNBDOwner,
    device_path: str,
    image_path: pathlib.Path,
    sysfs_dir: pathlib.Path,
    *,
    proc_root: pathlib.Path = pathlib.Path("/proc"),
    pid_file: pathlib.Path,
) -> None:
    if read_private_pidfile(pid_file) != owner.pid:
        raise OwnershipLost(f"current-run qemu-nbd PID file changed from {owner.pid}")
    try:
        current = process_identity(owner.pid, proc_root)
    except OwnershipLost as error:
        raise OwnershipLost(f"current-run qemu-nbd PID {owner.pid} disappeared: {error}") from error
    if current != owner.process:
        raise OwnershipLost(f"current-run qemu-nbd PID {owner.pid} changed identity")
    if not current.command or pathlib.Path(current.command[0]).name != "qemu-nbd":
        raise OwnershipLost(f"PID {owner.pid} is no longer qemu-nbd")
    if _connect_device(current.command) != device_path or str(image_path.resolve()) not in current.command:
        raise OwnershipLost(f"PID {owner.pid} no longer targets this NBD device and locked image")
    matches = qemu_nbd_processes(proc_root, device_path)
    if owner.process not in matches or any(
            process.tgid != owner.tgid or process.command != owner.command for process in matches):
        raise OwnershipLost(f"{device_path} qemu-nbd process ownership changed")
    pid_path = sysfs_dir / "pid"
    sysfs_pid = _read_optional_integer(pid_path, default=None)
    if sysfs_pid != owner.sysfs_thread_pid:
        raise OwnershipLost(
            f"sysfs NBD PID/TID changed from {owner.sysfs_thread_pid} to {sysfs_pid}")
    if sysfs_pid is not None:
        if sysfs_pid == 0:
            raise OwnershipLost(f"sysfs PID became zero while {device_path} remains attached")
        thread = process_identity(sysfs_pid, proc_root)
        if (thread.tgid != owner.tgid or thread.start_time != owner.sysfs_thread_start_time or
                thread.command != owner.command):
            raise OwnershipLost(
                f"sysfs PID/TID {sysfs_pid} no longer belongs to current-run qemu-nbd TGID {owner.tgid}")


def capture_new_mount_identity(
    before_mountinfo: str,
    after_mountinfo: str,
    target: pathlib.Path,
    expected_device: str,
) -> MountIdentity:
    """Capture only a mount that appeared at this run's previously empty target."""
    if mount_identity_at(before_mountinfo, target) is not None:
        raise ResourceBusy(f"mountpoint was occupied before this run mounted it: {target}")
    identity = capture_mount_identity(after_mountinfo, target, expected_device)
    previous_ids = {mount.mount_id for mount in parse_mountinfo(before_mountinfo)}
    if identity.mount_id in previous_ids:
        raise OwnershipLost(f"mount at {target} did not receive a new mount identity")
    return identity


def unmount_owned_mount(
    identity: MountIdentity,
    *,
    get_mountinfo,
    unmount,
) -> bool:
    """Unmount only while the exact identity captured by this run is still present."""
    target = pathlib.Path(identity.target)
    current = mount_identity_at(get_mountinfo(), target)
    if current is None:
        return False
    if current != identity:
        raise OwnershipLost(
            f"mountpoint identity changed; refusing umount: expected={identity}, current={current}")
    unmount(identity.target)
    if mount_identity_at(get_mountinfo(), target) is not None:
        raise OwnershipLost(f"verified mount remained after umount: {identity.target}")
    return True


def disconnect_owned_nbd(
    owner: QemuNBDOwner,
    device_path: str,
    image_path: pathlib.Path,
    sysfs_dir: pathlib.Path,
    *,
    mountinfo: str,
    swaps: str,
    disconnect,
    pid_file: pathlib.Path,
    proc_root: pathlib.Path = pathlib.Path("/proc"),
) -> None:
    """Run disconnect only after current PID ownership and an unmounted device are verified."""
    verify_qemu_nbd_owner(
        owner, device_path, image_path, sysfs_dir, proc_root=proc_root, pid_file=pid_file)
    assert_nbd_unmounted(sysfs_dir, mountinfo, swaps, device_path=device_path)
    disconnect()


@contextlib.contextmanager
def nbd_lock(path: pathlib.Path):
    stream = acquire_exclusive_lock(path)
    try:
        yield stream
    finally:
        stream.close()

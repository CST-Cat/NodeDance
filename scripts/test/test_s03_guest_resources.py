import ast
import pathlib
import re
import sys
import tempfile
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))

from s03_guest_resources import (  # noqa: E402
    MountIdentity,
    OwnershipLost,
    ProcessIdentity,
    QemuNBDOwner,
    ResourceBusy,
    assert_nbd_idle,
    capture_new_mount_identity,
    capture_mount_identity,
    capture_qemu_nbd_owner,
    disconnect_owned_nbd,
    nbd_lock,
    read_private_pidfile,
    unmount_owned_mount,
    verify_mount_identity,
    verify_qemu_nbd_owner,
)


def guest_initramfs_script():
    path = pathlib.Path(__file__).resolve().with_name("s03-guest.py")
    tree = ast.parse(path.read_text(encoding="utf-8"), filename=str(path))
    for node in tree.body:
        if isinstance(node, ast.Assign) and any(
                isinstance(target, ast.Name) and target.id == "INITRAMFS_SCRIPT"
                for target in node.targets):
            return ast.literal_eval(node.value)
    raise AssertionError("S03 guest initramfs script was not found")


def make_sysfs(root, *, pid="0", size="0", child="43:1"):
    device = root / "sys/block/nbd0"
    (device / "holders").mkdir(parents=True)
    (device / "dev").write_text("43:0\n")
    (device / "size").write_text(size + "\n")
    if pid is not None:
        (device / "pid").write_text(pid + "\n")
    if child is not None:
        (device / "nbd0p1").mkdir()
        (device / "nbd0p1/dev").write_text(child + "\n")
    return device


def make_process(proc_root, pid, command, start_time="99", tgid=None):
    process = proc_root / str(pid)
    process.mkdir(parents=True, exist_ok=True)
    (process / "cmdline").write_bytes(b"\0".join(part.encode() for part in command) + b"\0")
    (process / "status").write_text(f"Name:\tqemu-nbd\nTgid:\t{tgid or pid}\n")
    after_comm = ["S"] + [str(value) for value in range(4, 23)]
    after_comm[19] = start_time
    (process / "stat").write_text(f"{pid} (qemu-nbd) " + " ".join(after_comm) + "\n")
    return process


def make_pid_file(path, pid):
    path.write_text(f"{pid}\n")
    path.chmod(0o600)


class GuestResourceOwnershipTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self.temp.name)
        self.sysfs = make_sysfs(self.root)
        self.proc = self.root / "proc"
        self.proc.mkdir()
        self.mountinfo = ""
        self.swaps = "Filename\tType\tSize\tUsed\tPriority\n"

    def tearDown(self):
        self.temp.cleanup()

    def test_idle_device_requires_zero_owner_size_no_holders_or_mounts(self):
        assert_nbd_idle(self.sysfs, self.mountinfo, self.swaps,
                        proc_root=self.proc, device_path="/dev/nbd0")

    def test_missing_sysfs_pid_uses_size_mount_and_process_fallback(self):
        (self.sysfs / "pid").unlink()
        assert_nbd_idle(self.sysfs, self.mountinfo, self.swaps,
                        proc_root=self.proc, device_path="/dev/nbd0")
        (self.sysfs / "size").write_text("4096\n")
        with self.assertRaises(ResourceBusy):
            assert_nbd_idle(self.sysfs, self.mountinfo, self.swaps,
                            proc_root=self.proc, device_path="/dev/nbd0")
        (self.sysfs / "size").write_text("0\n")
        make_process(self.proc, 778, ["/usr/bin/qemu-nbd", "--connect=/dev/nbd0", "/tmp/active.img"])
        with self.assertRaises(ResourceBusy):
            assert_nbd_idle(self.sysfs, self.mountinfo, self.swaps,
                            proc_root=self.proc, device_path="/dev/nbd0")

    def test_rejects_sysfs_pid_and_nonzero_size_occupancy(self):
        (self.sysfs / "pid").write_text("456\n")
        with self.assertRaises(ResourceBusy):
            assert_nbd_idle(self.sysfs, self.mountinfo, self.swaps,
                            proc_root=self.proc, device_path="/dev/nbd0")
        (self.sysfs / "pid").write_text("0\n")
        (self.sysfs / "size").write_text("2048\n")
        with self.assertRaises(ResourceBusy):
            assert_nbd_idle(self.sysfs, self.mountinfo, self.swaps,
                            proc_root=self.proc, device_path="/dev/nbd0")

    def test_rejects_partition_mounted_or_used_as_swap(self):
        mountinfo = "10 1 43:1 / /tmp/s03-boot ro - ext4 /dev/nbd0p1 ro\n"
        with self.assertRaises(ResourceBusy):
            assert_nbd_idle(self.sysfs, mountinfo, self.swaps,
                            proc_root=self.proc, device_path="/dev/nbd0")
        swaps = self.swaps + "/dev/nbd0p1 partition 1024 0 -2\n"
        with self.assertRaises(ResourceBusy):
            assert_nbd_idle(self.sysfs, "", swaps,
                            proc_root=self.proc, device_path="/dev/nbd0")

    def test_rejects_qemu_process_when_nbd_sysfs_does_not_show_owner(self):
        make_process(self.proc, 777, ["/usr/bin/qemu-nbd", "--connect=/dev/nbd0", "/tmp/other.img"])
        with self.assertRaises(ResourceBusy):
            assert_nbd_idle(self.sysfs, self.mountinfo, self.swaps,
                            proc_root=self.proc, device_path="/dev/nbd0")

    def test_uninspectable_proc_entry_prevents_idle_claim(self):
        (self.proc / "779").mkdir()
        with self.assertRaises(OwnershipLost):
            assert_nbd_idle(self.sysfs, self.mountinfo, self.swaps,
                            proc_root=self.proc, device_path="/dev/nbd0")

    def test_mount_identity_detects_replacement_before_unmount(self):
        target = self.root / "private mount"
        target.mkdir()
        original = f"10 1 43:1 / {str(target).replace(' ', '\\040')} ro - ext4 /dev/nbd0p1 ro\n"
        identity = capture_mount_identity(original, target, "43:1")
        self.assertEqual(identity.mount_id, 10)
        self.assertTrue(verify_mount_identity(original, identity))
        replaced = original.replace("10 1", "11 1")
        self.assertFalse(verify_mount_identity(replaced, identity))
        with self.assertRaises(OwnershipLost):
            capture_mount_identity(original.replace("43:1", "8:1"), target, "43:1")

    def test_mount_identity_must_be_new_at_previously_empty_target(self):
        target = self.root / "run mount"
        before = "10 1 8:1 / / ro - ext4 /dev/root ro\n"
        after = before + f"11 1 43:1 / {str(target).replace(' ', '\\040')} ro - ext4 /dev/nbd0p1 ro\n"
        identity = capture_new_mount_identity(before, after, target, "43:1")
        self.assertEqual(identity.mount_id, 11)
        with self.assertRaises(ResourceBusy):
            capture_new_mount_identity(after, after, target, "43:1")
        reused_id = before + f"10 1 43:1 / {str(target).replace(' ', '\\040')} ro - ext4 /dev/nbd0p1 ro\n"
        with self.assertRaises(OwnershipLost):
            capture_new_mount_identity(before, reused_id, target, "43:1")

    def test_replacement_mount_is_never_unmounted(self):
        target = self.root / "run-mount"
        original = f"10 1 43:1 / {target} ro - ext4 /dev/nbd0p1 ro\n"
        replaced = f"11 1 43:1 / {target} ro - ext4 /dev/nbd0p1 ro\n"
        identity = capture_mount_identity(original, target, "43:1")
        calls = []
        with self.assertRaises(OwnershipLost):
            unmount_owned_mount(
                identity, get_mountinfo=lambda: replaced,
                unmount=lambda mount_target: calls.append(mount_target))
        self.assertEqual(calls, [])

    def test_owned_mount_is_unmounted_and_absent_mount_is_left_alone(self):
        target = self.root / "run-mount"
        original = f"10 1 43:1 / {target} ro - ext4 /dev/nbd0p1 ro\n"
        identity = capture_mount_identity(original, target, "43:1")
        states = [original, ""]
        calls = []
        self.assertTrue(unmount_owned_mount(
            identity, get_mountinfo=lambda: states.pop(0) if states else "",
            unmount=lambda mount_target: calls.append(mount_target)))
        self.assertEqual(calls, [str(target)])
        self.assertFalse(unmount_owned_mount(
            identity, get_mountinfo=lambda: "", unmount=lambda _: self.fail("unexpected umount")))

    def test_process_pid_start_time_and_command_must_remain_owned(self):
        image = self.root / "owned.qcow2"
        image.touch()
        self.sysfs.joinpath("pid").write_text("888\n")
        self.sysfs.joinpath("size").write_text("4096\n")
        command = ["/usr/bin/qemu-nbd", "--read-only", "--connect=/dev/nbd0", str(image)]
        make_process(self.proc, 888, command, start_time="12345")
        pid_file = self.root / "nbd-launch.pid"
        make_pid_file(pid_file, 888)
        owner = capture_qemu_nbd_owner(
            "/dev/nbd0", image, self.sysfs, proc_root=self.proc, pid_file=pid_file)
        self.assertEqual(owner.process, ProcessIdentity(888, "12345", tuple(command), 888))
        self.assertEqual(owner.sysfs_thread_pid, 888)
        self.assertEqual(owner.sysfs_thread_start_time, "12345")
        with self.assertRaises(OwnershipLost):
            capture_qemu_nbd_owner(
                "/dev/nbd0", image, self.sysfs, proc_root=self.proc,
                pid_file=pid_file,
                processes_before_attach=(owner.process,))
        verify_qemu_nbd_owner(
            owner, "/dev/nbd0", image, self.sysfs, proc_root=self.proc, pid_file=pid_file)
        self.sysfs.joinpath("pid").write_text("889\n")
        with self.assertRaises(OwnershipLost):
            verify_qemu_nbd_owner(
                owner, "/dev/nbd0", image, self.sysfs, proc_root=self.proc, pid_file=pid_file)
        self.sysfs.joinpath("pid").write_text("888\n")
        make_pid_file(pid_file, 889)
        with self.assertRaises(OwnershipLost):
            verify_qemu_nbd_owner(
                owner, "/dev/nbd0", image, self.sysfs, proc_root=self.proc, pid_file=pid_file)

    def test_sysfs_tid_must_belong_to_pidfile_tgid(self):
        image = self.root / "owned.qcow2"
        image.touch()
        command = ["/usr/bin/qemu-nbd", "--read-only", "--connect=/dev/nbd0", str(image)]
        self.sysfs.joinpath("pid").write_text("889\n")
        self.sysfs.joinpath("size").write_text("4096\n")
        make_process(self.proc, 888, command, start_time="12345", tgid=888)
        make_process(self.proc, 889, command, start_time="12346", tgid=888)
        pid_file = self.root / "nbd-launch.pid"
        make_pid_file(pid_file, 888)
        owner = capture_qemu_nbd_owner(
            "/dev/nbd0", image, self.sysfs, proc_root=self.proc, pid_file=pid_file,
            processes_before_attach=())
        self.assertEqual(owner.pid, 888)
        self.assertEqual(owner.tgid, 888)
        self.assertEqual(owner.sysfs_thread_pid, 889)
        self.assertEqual(owner.sysfs_thread_start_time, "12346")
        verify_qemu_nbd_owner(
            owner, "/dev/nbd0", image, self.sysfs, proc_root=self.proc, pid_file=pid_file)
        make_process(self.proc, 889, command, start_time="12347", tgid=888)
        with self.assertRaises(OwnershipLost):
            verify_qemu_nbd_owner(
                owner, "/dev/nbd0", image, self.sysfs, proc_root=self.proc, pid_file=pid_file)
        make_process(self.proc, 889, command, start_time="12345", tgid=889)
        with self.assertRaises(OwnershipLost):
            verify_qemu_nbd_owner(
                owner, "/dev/nbd0", image, self.sysfs, proc_root=self.proc, pid_file=pid_file)

        make_process(self.proc, 889, ["/usr/bin/sleep", "1"], start_time="12345", tgid=889)
        with self.assertRaises(OwnershipLost):
            verify_qemu_nbd_owner(
                owner, "/dev/nbd0", image, self.sysfs, proc_root=self.proc, pid_file=pid_file)

    def test_owner_is_verified_without_sysfs_pid_using_new_process_identity(self):
        image = self.root / "owned.qcow2"
        image.touch()
        command = ["/usr/bin/qemu-nbd", "--read-only", "--connect=/dev/nbd0", str(image)]
        self.sysfs.joinpath("pid").unlink()
        self.sysfs.joinpath("size").write_text("4096\n")
        make_process(self.proc, 888, command, start_time="12345")
        pid_file = self.root / "nbd-launch.pid"
        make_pid_file(pid_file, 888)
        owner = capture_qemu_nbd_owner(
            "/dev/nbd0", image, self.sysfs, proc_root=self.proc, pid_file=pid_file,
            processes_before_attach=())
        verify_qemu_nbd_owner(
            owner, "/dev/nbd0", image, self.sysfs, proc_root=self.proc, pid_file=pid_file)
        with self.assertRaises(OwnershipLost):
            capture_qemu_nbd_owner(
                "/dev/nbd0", image, self.sysfs, proc_root=self.proc,
                pid_file=pid_file,
                processes_before_attach=(owner.process,))

    def test_disconnect_callback_requires_live_owner_and_no_mounted_partition(self):
        image = self.root / "owned.qcow2"
        image.touch()
        command = ["/usr/bin/qemu-nbd", "--read-only", "--connect=/dev/nbd0", str(image)]
        self.sysfs.joinpath("pid").write_text("888\n")
        self.sysfs.joinpath("size").write_text("4096\n")
        make_process(self.proc, 888, command, start_time="12345")
        owner = QemuNBDOwner(ProcessIdentity(888, "12345", tuple(command), 888), 888, "12345")
        pid_file = self.root / "nbd-launch.pid"
        make_pid_file(pid_file, 888)
        calls = []
        disconnect_owned_nbd(
            owner, "/dev/nbd0", image, self.sysfs,
            mountinfo=self.mountinfo, swaps=self.swaps,
            disconnect=lambda: calls.append("disconnect"), proc_root=self.proc, pid_file=pid_file)
        self.assertEqual(calls, ["disconnect"])

        calls.clear()
        self.sysfs.joinpath("pid").write_text("889\n")
        with self.assertRaises(OwnershipLost):
            disconnect_owned_nbd(
                owner, "/dev/nbd0", image, self.sysfs,
                mountinfo=self.mountinfo, swaps=self.swaps,
                disconnect=lambda: calls.append("disconnect"), proc_root=self.proc, pid_file=pid_file)
        self.assertEqual(calls, [])

        self.sysfs.joinpath("pid").write_text("888\n")
        mounted = "10 1 43:1 / /tmp/other ro - ext4 /dev/nbd0p1 ro\n"
        with self.assertRaises(ResourceBusy):
            disconnect_owned_nbd(
                owner, "/dev/nbd0", image, self.sysfs,
                mountinfo=mounted, swaps=self.swaps,
                disconnect=lambda: calls.append("disconnect"), proc_root=self.proc, pid_file=pid_file)
        self.assertEqual(calls, [])
        self.sysfs.joinpath("pid").write_text("888\n")
        make_process(self.proc, 888, command, start_time="54321")
        with self.assertRaises(OwnershipLost):
            verify_qemu_nbd_owner(
                owner, "/dev/nbd0", image, self.sysfs, proc_root=self.proc, pid_file=pid_file)
        make_process(self.proc, 888, command, start_time="12345")
        make_process(self.proc, 888, ["/usr/bin/sleep", "999"], start_time="12345")
        with self.assertRaises(OwnershipLost):
            verify_qemu_nbd_owner(
                owner, "/dev/nbd0", image, self.sysfs, proc_root=self.proc, pid_file=pid_file)

    def test_nonblocking_global_lock_rejects_another_guest_extractor(self):
        lock_path = self.root / "locks/nbd0.lock"
        with nbd_lock(lock_path):
            with self.assertRaises(ResourceBusy):
                with nbd_lock(lock_path):
                    self.fail("a second guest extractor acquired the NBD lock")
        target = self.root / "other-file"
        target.write_text("not a lock")
        symlink = self.root / "locks/symlink-lock"
        symlink.symlink_to(target)
        with self.assertRaises(OSError):
            with nbd_lock(symlink):
                self.fail("a symlink lock path was followed")

    def test_private_pidfile_rejects_symlink_hardlink_and_public_mode(self):
        target = self.root / "target.pid"
        make_pid_file(target, 888)
        self.assertEqual(read_private_pidfile(target), 888)

        symlink = self.root / "symlink.pid"
        symlink.symlink_to(target)
        with self.assertRaises(OwnershipLost):
            read_private_pidfile(symlink)

        hardlink = self.root / "hardlink.pid"
        hardlink.hardlink_to(target)
        with self.assertRaises(OwnershipLost):
            read_private_pidfile(target)
        hardlink.unlink()

        target.chmod(0o644)
        with self.assertRaises(OwnershipLost):
            read_private_pidfile(target)


class GuestRebootShutdownFixtureTests(unittest.TestCase):
    def test_agent_is_stopped_and_reaped_before_both_root_unmounts(self):
        script = guest_initramfs_script()
        stop_function = re.search(r"(?ms)^stop_guest_agent\(\) \{.*?^\}", script)
        self.assertIsNotNone(stop_function, "guest Agent shutdown helper is missing")
        helper = stop_function.group(0)
        self.assertIn('kill -TERM "$agent_pid"', helper,
                      "guest shutdown must request a graceful Agent stop")
        self.assertIn('[ "$i" -lt 10 ]', helper,
                      "graceful Agent shutdown must be bounded")
        self.assertIn('kill -KILL "$agent_pid"', helper,
                      "guest shutdown must have a bounded forced-stop fallback")
        self.assertIn('wait "$agent_pid"', helper,
                      "guest shutdown must reap the Agent before unmount")

        unmounts = list(re.finditer(
            r'^\s*"\$BB" umount /mnt \|\| fatal ".*guest root filesystem.*"$',
            script, re.MULTILINE))
        self.assertEqual(len(unmounts), 2, "expected pre- and post-reboot root unmounts")
        for unmount in unmounts:
            prefix = script[:unmount.start()]
            stop = re.search(r"(?m)^\s*stop_guest_agent S03:AGENT_STOPPED_(?:BEFORE_REBOOT|AFTER_PROBE)$",
                             prefix)
            self.assertIsNotNone(stop, "root filesystem was unmounted before stopping the Agent")
            self.assertIn('"$BB" sync', prefix[stop.end():],
                          "guest disk must be synced after Agent journals close")
        self.assertEqual(len(re.findall(
            r"(?m)^\s*stop_guest_agent S03:AGENT_STOPPED_(?:BEFORE_REBOOT|AFTER_PROBE)$",
            script)), 2, "both guest shutdown paths must report their Agent stop")


if __name__ == "__main__":
    unittest.main()

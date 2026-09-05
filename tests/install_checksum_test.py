"""Run the POSIX release installer with isolated release and daemon fixtures."""

import hashlib
import io
import os
import pathlib
import platform
import subprocess
import sys
import tarfile
import tempfile

INSTALLER = pathlib.Path(__file__).resolve().parents[1] / "scripts/install.sh"


def run_case(case):
    with tempfile.TemporaryDirectory() as tmp:
        root = pathlib.Path(tmp)
        bins, fixture, dest = root / "tools", root / "release", root / "bin"
        for directory in (bins, fixture, dest):
            directory.mkdir()
        os_name = {"Darwin": "darwin", "Linux": "linux"}[platform.system()]
        arch = "arm64" if platform.machine() in ("arm64", "aarch64") else "amd64"
        asset = f"galley_1.2.3_{os_name}_{arch}.tar.gz"
        new = b"#!/bin/sh\n# verified release\nexit 0\n"
        with tarfile.open(fixture / asset, "w:gz") as archive:
            info = tarfile.TarInfo("galley")
            info.mode, info.size = 0o755, len(new)
            archive.addfile(info, io.BytesIO(new))
        digest = hashlib.sha256((fixture / asset).read_bytes()).hexdigest()
        line = f"{digest}  {asset}\n"
        checksums = {"valid": line, "mismatch": f"{'0' * 64}  {asset}\n",
                     "absent": f"{digest}  other.tar.gz\n", "duplicate": line + line,
                     "malformed": f"bad-hash  {asset}\n", "unavailable": line}[case]
        (fixture / "checksums.txt").write_text(checksums)
        curl = bins / "curl"
        curl.write_text(f"#!{sys.executable}\n" +
                        "import os,pathlib,sys,shutil\n" +
                        "url=next(x for x in sys.argv if x.startswith('https://'))\n" +
                        "name=url.rsplit('/',1)[1]\n" +
                        "if name == 'checksums.txt' and os.environ['INSTALL_CASE'] == 'unavailable': sys.exit(22)\n" +
                        "shutil.copyfile(pathlib.Path(os.environ['INSTALL_FIXTURE'])/name, sys.argv[sys.argv.index('-o')+1])\n")
        curl.chmod(0o755)
        (bins / "codesign").write_text("#!/bin/sh\nexit 0\n")
        (bins / "codesign").chmod(0o755)
        old = b'#!/bin/sh\ncase "$1 $2" in\n"daemon status") echo \'{"running":true,"verified":true}\';;\n"daemon stop") echo stopped >> "$INSTALL_STOP_MARKER";;\nesac\n'
        (dest / "galley").write_bytes(old)
        (dest / "galley").chmod(0o755)
        marker = root / "stopped"
        env = dict(os.environ, PATH=str(bins) + os.pathsep + os.environ["PATH"],
                   INSTALL_FIXTURE=str(fixture), INSTALL_CASE=case, INSTALL_STOP_MARKER=str(marker))
        result = subprocess.run(["sh", str(INSTALLER), "--version", "v1.2.3", "--bin-dir", str(dest)],
                                env=env, capture_output=True, text=True, timeout=20)
        if case == "valid":
            assert result.returncode == 0, result.stderr
            assert marker.exists(), "verified update did not stop the old daemon"
            assert (dest / "galley").read_bytes() == new
        else:
            assert result.returncode != 0, f"{case} unexpectedly installed"
            assert "checksum" in result.stderr.lower(), result.stderr
            assert not marker.exists(), "unverified update stopped the daemon"
            assert (dest / "galley").read_bytes() == old, "unverified update replaced the binary"
        print(f"install checksum {case}: passed")


if __name__ == "__main__":
    for case in ("mismatch", "unavailable", "absent", "duplicate", "malformed", "valid"):
        run_case(case)
